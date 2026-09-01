package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingMigrator struct {
	tx    *recordingMigrationTx
	began bool
}

func (r *recordingMigrator) Begin(context.Context) (MigrationTx, error) {
	r.began = true
	return r.tx, nil
}

type recordingMigrationTx struct {
	statements      []string
	applied         map[int]bool
	checksums       map[int]string
	unknownVersions string
	errs            map[string]error
	committed       bool
	rolledBack      bool
}

func (r *recordingMigrationTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.statements = append(r.statements, sql)
	return pgconn.NewCommandTag("INSERT 0 1"), r.errs[sql]
}

func (r *recordingMigrationTx) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	if _, ok := args[0].([]int32); ok {
		return migrationVersionsRow(r.unknownVersions)
	}
	if len(args) == 1 && r.applied != nil && r.applied[args[0].(int)] {
		return migrationRow{version: args[0].(int), checksum: r.checksums[args[0].(int)]}
	}
	return migrationRow{err: pgx.ErrNoRows}
}

func (r *recordingMigrationTx) Commit(context.Context) error   { r.committed = true; return nil }
func (r *recordingMigrationTx) Rollback(context.Context) error { r.rolledBack = true; return nil }

type migrationRow struct {
	version  int
	checksum string
	err      error
}

type migrationVersionsRow string

func (r migrationVersionsRow) Scan(dest ...any) error {
	*dest[0].(*string) = string(r)
	return nil
}

func (r migrationRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		*dest[0].(*int) = r.version
	}
	if len(dest) > 1 {
		*dest[1].(*string) = r.checksum
	}
	return nil
}

func TestApplyMigrationsUsesTransactionLockAndPreservesInputOrder(t *testing.T) {
	tx := &recordingMigrationTx{}
	db := &recordingMigrator{tx: tx}
	migrations := []Migration{{Version: 2, SQL: "two"}, {Version: 1, SQL: "one"}}
	if err := ApplyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	if migrations[0].Version != 2 {
		t.Fatal("caller migration slice was reordered")
	}
	if !db.began || !tx.committed || tx.rolledBack {
		t.Fatalf("began=%t committed=%t rolledback=%t", db.began, tx.committed, tx.rolledBack)
	}
	if len(tx.statements) != 7 || tx.statements[0] != "SELECT pg_catalog.pg_advisory_xact_lock($1)" || tx.statements[3] != "one" || tx.statements[5] != "two" {
		t.Fatalf("statements = %#v", tx.statements)
	}
}

func TestApplyMigrationsSkipsAlreadyRecordedMigration(t *testing.T) {
	tx := &recordingMigrationTx{applied: map[int]bool{1: true}, checksums: map[int]string{1: migrationChecksum("one")}}
	if err := ApplyMigrations(context.Background(), &recordingMigrator{tx: tx}, []Migration{{Version: 2, SQL: "two"}, {Version: 1, SQL: "one"}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.statements) != 5 || tx.statements[3] != "two" {
		t.Fatalf("statements = %#v", tx.statements)
	}
}

func TestApplyMigrationsRejectsDuplicateVersionsBeforeBeginning(t *testing.T) {
	db := &recordingMigrator{tx: &recordingMigrationTx{}}
	migrations := []Migration{{Version: 1, SQL: "one"}, {Version: 1, SQL: "other"}}
	if err := ApplyMigrations(context.Background(), db, migrations); err == nil {
		t.Fatal("duplicate versions were accepted")
	}
	if db.began || migrations[0].SQL != "one" {
		t.Fatal("duplicate check changed external state")
	}
}

func TestApplyMigrationsRejectsUnknownLedgerVersionsBeforeApplying(t *testing.T) {
	tx := &recordingMigrationTx{unknownVersions: "2,99"}
	err := ApplyMigrations(context.Background(), &recordingMigrator{tx: tx}, []Migration{{Version: 1, SQL: "one"}, {Version: 3, SQL: "three"}})
	if err == nil || !strings.Contains(err.Error(), "unknown migration versions: 2,99") {
		t.Fatalf("err = %v", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("committed=%t rolledback=%t", tx.committed, tx.rolledBack)
	}
	for _, statement := range tx.statements {
		if statement == "one" || statement == "three" {
			t.Fatalf("migration applied before rejecting unknown ledger version: %#v", tx.statements)
		}
	}
}

func TestApplyMigrationsRollsBackOnStatementFailure(t *testing.T) {
	tx := &recordingMigrationTx{errs: map[string]error{"one": errors.New("broken SQL")}}
	err := ApplyMigrations(context.Background(), &recordingMigrator{tx: tx}, []Migration{{Version: 1, SQL: "one"}})
	if err == nil || tx.committed || !tx.rolledBack {
		t.Fatalf("err=%v committed=%t rolledback=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestApplyMigrationsRejectsChangedAppliedSQL(t *testing.T) {
	tx := &recordingMigrationTx{
		applied:   map[int]bool{1: true},
		checksums: map[int]string{1: migrationChecksum("original")},
	}
	err := ApplyMigrations(context.Background(), &recordingMigrator{tx: tx}, []Migration{{Version: 1, SQL: "changed"}})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("committed=%t rolledback=%t", tx.committed, tx.rolledBack)
	}
}

func TestApplyMigrationsBackfillsLegacyChecksum(t *testing.T) {
	tx := &recordingMigrationTx{applied: map[int]bool{1: true}}
	if err := ApplyMigrations(context.Background(), &recordingMigrator{tx: tx}, []Migration{{Version: 1, SQL: "one"}}); err != nil {
		t.Fatal(err)
	}
	want := migrationChecksum("one")
	found := false
	for _, statement := range tx.statements {
		if strings.Contains(statement, "UPDATE public.schema_migrations SET checksum") {
			found = true
		}
	}
	if !found || want == "" {
		t.Fatalf("legacy checksum was not backfilled: %#v", tx.statements)
	}
}
