package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type queryRowStub struct {
	sql               string
	safe, member      bool
	current, roleName string
	err               error
}

func (q *queryRowStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	q.sql = sql
	return scanRowStub{values: []any{q.current, q.safe, q.member}, err: q.err}
}

type scanRowStub struct {
	values []any
	err    error
}

type migrationRoleRowStub struct {
	current   string
	superuser bool
	err       error
	sql       string
}

func (q *migrationRoleRowStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	q.sql = sql
	return migrationRoleScanStub{current: q.current, superuser: q.superuser, err: q.err}
}

type migrationRoleScanStub struct {
	current   string
	superuser bool
	err       error
}

func (r migrationRoleScanStub) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.current
	*(dest[1].(*bool)) = r.superuser
	return nil
}

func (r scanRowStub) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.values[0].(string)
	*(dest[1].(*bool)) = r.values[1].(bool)
	*(dest[2].(*bool)) = r.values[2].(bool)
	return nil
}

func TestVerifyRuntimeRoleFailsClosedOnUnsafeAttributes(t *testing.T) {
	safe := &queryRowStub{current: "g2b_app", safe: true, member: true}
	if err := VerifyRuntimeRole(context.Background(), safe); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"rolsuper", "rolbypassrls", "rolcreaterole", "rolcreatedb", "rolreplication", "pg_has_role"} {
		if !strings.Contains(safe.sql, marker) {
			t.Errorf("role verification SQL missing %q: %s", marker, safe.sql)
		}
	}
	if !strings.Contains(safe.sql, "'usage'") {
		t.Fatalf("role verification must require inherited runtime usage: %s", safe.sql)
	}
	for name, row := range map[string]*queryRowStub{
		"unsafe":      {current: "g2b_owner", safe: false, member: true},
		"not-member":  {current: "g2b_app", safe: true, member: false},
		"query-error": {err: errors.New("database unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyRuntimeRole(context.Background(), row); err == nil {
				t.Fatal("unsafe or unverifiable runtime role was accepted")
			}
		})
	}
}

func TestVerifyMigrationRoleRequiresSuperuser(t *testing.T) {
	superuser := &migrationRoleRowStub{current: "postgres", superuser: true}
	if err := VerifyMigrationRole(context.Background(), superuser); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"current_user", "rolsuper", "pg_catalog.pg_roles"} {
		if !strings.Contains(superuser.sql, marker) {
			t.Errorf("migration role verification SQL missing %q: %s", marker, superuser.sql)
		}
	}

	for name, row := range map[string]*migrationRoleRowStub{
		"createrole-only": {current: "g2b_owner", superuser: false},
		"query-error":     {err: errors.New("database unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			err := VerifyMigrationRole(context.Background(), row)
			if err == nil || !strings.Contains(err.Error(), "PostgreSQL superuser") {
				t.Fatalf("unsafe or unverifiable migration role error=%v", err)
			}
		})
	}
}
