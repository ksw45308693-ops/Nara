package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type QueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// VerifyMigrationRole requires the same PostgreSQL authority that the schema
// migrations need to create and harden BYPASSRLS definer roles and to replace
// functions already owned by those NOLOGIN roles. CREATEROLE alone is not
// sufficient for that privilege boundary.
func VerifyMigrationRole(ctx context.Context, db QueryRower) error {
	if db == nil {
		return errors.New("PostgreSQL superuser migration database is required")
	}
	const query = `SELECT current_user, r.rolsuper
FROM pg_catalog.pg_roles r
WHERE r.rolname = current_user`
	var current string
	var superuser bool
	if err := db.QueryRow(ctx, query).Scan(&current, &superuser); err != nil {
		return fmt.Errorf("verify PostgreSQL superuser migration role: %w", err)
	}
	if !superuser {
		return fmt.Errorf("database role %q is not a PostgreSQL superuser required for migrations", current)
	}
	return nil
}

// VerifyRuntimeRole prevents serve from starting with migration-owner or RLS-
// bypass credentials, even if configuration strings were accidentally swapped.
func VerifyRuntimeRole(ctx context.Context, db QueryRower) error {
	if db == nil {
		return errors.New("runtime database is required")
	}
	const query = `SELECT current_user,
       NOT (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication) AS safe,
       pg_has_role(current_user, 'g2b_runtime', 'usage') AS runtime_usage
FROM pg_catalog.pg_roles r
WHERE r.rolname = current_user`
	var current string
	var safe, usage bool
	if err := db.QueryRow(ctx, query).Scan(&current, &safe, &usage); err != nil {
		return fmt.Errorf("verify runtime database role: %w", err)
	}
	if !safe || !usage {
		return fmt.Errorf("database role %q is not a safe NOBYPASSRLS g2b_runtime user", current)
	}
	return nil
}
