// Package store contains PostgreSQL persistence primitives.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Migration struct {
	Version int
	SQL     string
}

// SQLExecutor is satisfied directly by pgx connections, pools, and
// transactions. It keeps migration tests independent of a live PostgreSQL.
type SQLExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type MigrationTx interface {
	SQLExecutor
	Commit(context.Context) error
	Rollback(context.Context) error
}

type MigrationBeginner interface {
	Begin(context.Context) (MigrationTx, error)
}

func migrationChecksum(sql string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(sql)))
}

const migrationAdvisoryLock int64 = 677266708933247672

func ApplyMigrations(ctx context.Context, db MigrationBeginner, migrations []Migration) error {
	ordered := append([]Migration(nil), migrations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Version < ordered[j].Version })
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].Version == ordered[i].Version {
			return fmt.Errorf("duplicate migration version %d", ordered[i].Version)
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, migrationAdvisoryLock); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (version integer PRIMARY KEY, checksum text, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE public.schema_migrations ADD COLUMN IF NOT EXISTS checksum text`); err != nil {
		return fmt.Errorf("upgrade migration ledger: %w", err)
	}
	versions := make([]int32, len(ordered))
	for i, migration := range ordered {
		versions[i] = int32(migration.Version)
	}
	var unknownVersions string
	if err := tx.QueryRow(ctx, `SELECT coalesce(string_agg(version::text, ',' ORDER BY version), '') FROM public.schema_migrations WHERE NOT (version = ANY($1::integer[]))`, versions).Scan(&unknownVersions); err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	if unknownVersions != "" {
		return fmt.Errorf("unknown migration versions: %s", unknownVersions)
	}
	for _, m := range ordered {
		var version int
		var recordedChecksum string
		checksum := migrationChecksum(m.SQL)
		err := tx.QueryRow(ctx, `SELECT version, coalesce(checksum, '') FROM public.schema_migrations WHERE version = $1`, m.Version).Scan(&version, &recordedChecksum)
		if err == nil {
			if recordedChecksum == "" {
				if _, err := tx.Exec(ctx, `UPDATE public.schema_migrations SET checksum = $2 WHERE version = $1 AND checksum IS NULL`, m.Version, checksum); err != nil {
					return fmt.Errorf("backfill migration %d checksum: %w", m.Version, err)
				}
				continue
			}
			if recordedChecksum != checksum {
				return fmt.Errorf("migration %d checksum mismatch", m.Version)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.schema_migrations (version, checksum) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING`, m.Version, checksum); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	committed = true
	return nil
}
