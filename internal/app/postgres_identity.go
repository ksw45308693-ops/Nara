package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"namo/internal/auth"
)

type PostgresRepository struct {
	Pool *pgxpool.Pool
}

func (r *PostgresRepository) Ping(ctx context.Context) error {
	if r == nil || r.Pool == nil {
		return errors.New("database pool is not configured")
	}
	return r.Pool.Ping(ctx)
}

func (r *PostgresRepository) AccountByEmail(ctx context.Context, email string) (LoginAccount, error) {
	if r == nil || r.Pool == nil {
		return LoginAccount{}, ErrUnauthenticated
	}
	var account LoginAccount
	var role string
	var tenantID *string
	err := r.Pool.QueryRow(ctx,
		`SELECT user_id, tenant_id::text, email, password_hash, role FROM public.auth_account_lookup($1)`,
		strings.TrimSpace(strings.ToLower(email)),
	).Scan(&account.UserID, &tenantID, &account.Email, &account.PasswordHash, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginAccount{}, ErrUnauthenticated
	}
	if err != nil {
		return LoginAccount{}, fmt.Errorf("lookup login account: %w", err)
	}
	if tenantID != nil {
		account.TenantID = *tenantID
	}
	account.Role = auth.Role(role)
	if !account.Role.Valid() {
		return LoginAccount{}, ErrUnauthenticated
	}
	return account, nil
}

func (r *PostgresRepository) SaveSession(ctx context.Context, session SessionRecord) error {
	if r == nil || r.Pool == nil || session.UserID == "" || session.TokenHash == "" || session.ExpiresAt.IsZero() {
		return errors.New("valid session record is required")
	}
	if _, err := r.Pool.Exec(ctx,
		`SELECT public.auth_session_create($1::uuid, $2, $3)`,
		session.UserID, session.TokenHash, session.ExpiresAt,
	); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SessionByHash(ctx context.Context, hash string) (SessionRecord, error) {
	if r == nil || r.Pool == nil || hash == "" {
		return SessionRecord{}, ErrUnauthenticated
	}
	var record SessionRecord
	var role string
	var tenantID *string
	err := r.Pool.QueryRow(ctx,
		`SELECT user_id, tenant_id::text, email, role, expires_at FROM public.auth_session_lookup($1)`, hash,
	).Scan(&record.UserID, &tenantID, &record.Email, &role, &record.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionRecord{}, ErrUnauthenticated
	}
	if err != nil {
		return SessionRecord{}, fmt.Errorf("lookup session: %w", err)
	}
	if tenantID != nil {
		record.TenantID = *tenantID
	}
	record.TokenHash = hash
	record.Role = auth.Role(role)
	if !record.Role.Valid() {
		return SessionRecord{}, ErrUnauthenticated
	}
	return record, nil
}

func (r *PostgresRepository) DeleteSession(ctx context.Context, hash string) error {
	if r == nil || r.Pool == nil || hash == "" {
		return nil
	}
	var deleted bool
	if err := r.Pool.QueryRow(ctx, `SELECT public.auth_session_delete($1)`, hash).Scan(&deleted); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
