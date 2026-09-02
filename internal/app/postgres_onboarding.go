package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"namo/internal/auth"
	appweb "namo/internal/web"
)

type invitationDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PostgresInvitationStore struct{ DB invitationDB }

func (s PostgresInvitationStore) CreateTenantInvitation(ctx context.Context, input TenantInvitationInput) error {
	if s.DB == nil || input.Role != auth.TenantAdmin || input.ActorUserID == "" || len(input.TokenHash) != 64 || input.ExpiresAt.IsZero() {
		return errors.New("valid tenant invitation is required")
	}
	var tenantID, invitationID string
	err := s.DB.QueryRow(ctx, `SELECT tenant_id::text, invitation_id::text
FROM public.onboarding_create_tenant($1::uuid, $2, $3, $4, $5, $6, $7)`,
		input.ActorUserID, input.TenantName, input.ContactEmail, input.AdminEmail,
		input.AdminName, input.TokenHash, input.ExpiresAt,
	).Scan(&tenantID, &invitationID)
	if err != nil {
		return fmt.Errorf("persist tenant invitation: %w", err)
	}
	return nil
}

func (s PostgresInvitationStore) CreateMemberInvitation(ctx context.Context, input MemberInvitationInput) error {
	if s.DB == nil || (input.Role != auth.TenantAdmin && input.Role != auth.Member) || input.ActorUserID == "" || input.TenantID == "" || len(input.TokenHash) != 64 || input.ExpiresAt.IsZero() {
		return errors.New("valid member invitation is required")
	}
	var invitationID string
	err := s.DB.QueryRow(ctx, `SELECT public.onboarding_invite_member($1::uuid, $2::uuid, $3, $4, $5, $6, $7)::text`,
		input.ActorUserID, input.TenantID, input.Email, input.Name, string(input.Role), input.TokenHash, input.ExpiresAt,
	).Scan(&invitationID)
	if err != nil {
		return fmt.Errorf("persist member invitation: %w", err)
	}
	return nil
}

func (s PostgresInvitationStore) InvitationByHash(ctx context.Context, hash string) (InvitationRecord, error) {
	if s.DB == nil || len(hash) != 64 {
		return InvitationRecord{}, appweb.ErrInvitationUnavailable
	}
	var record InvitationRecord
	var role string
	err := s.DB.QueryRow(ctx, `SELECT tenant_name, email, display_name, role, expires_at
FROM public.onboarding_invitation_lookup($1)`, hash).Scan(
		&record.TenantName, &record.Email, &record.DisplayName, &role, &record.ExpiresAt,
	)
	if invitationUnavailable(err) {
		return InvitationRecord{}, appweb.ErrInvitationUnavailable
	}
	if err != nil {
		return InvitationRecord{}, fmt.Errorf("lookup invitation: %w", err)
	}
	record.Role = auth.Role(role)
	if record.Role != auth.Member && record.Role != auth.TenantAdmin {
		return InvitationRecord{}, appweb.ErrInvitationUnavailable
	}
	return record, nil
}

func (s PostgresInvitationStore) AcceptInvitation(ctx context.Context, input AcceptedInvitationInput) error {
	if s.DB == nil || len(input.TokenHash) != 64 || strings.TrimSpace(input.DisplayName) == "" || strings.TrimSpace(input.PasswordHash) == "" {
		return appweb.ErrInvitationUnavailable
	}
	var userID, tenantID, email, role string
	err := s.DB.QueryRow(ctx, `SELECT user_id::text, tenant_id::text, email, role
FROM public.onboarding_accept_invitation($1, $2, $3)`, input.TokenHash, input.DisplayName, input.PasswordHash).Scan(
		&userID, &tenantID, &email, &role,
	)
	if invitationUnavailable(err) {
		return appweb.ErrInvitationUnavailable
	}
	if err != nil {
		return fmt.Errorf("accept invitation: %w", err)
	}
	return nil
}

func invitationUnavailable(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "P0002"
}
