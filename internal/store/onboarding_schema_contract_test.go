package store

import (
	"os"
	"strings"
	"testing"
)

func TestInvitationOnboardingSchemaEnforcesPrivilegeAndSingleUse(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0003_invitation_onboarding.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	backfill := strings.Index(sql, "UPDATE public.invitations")
	constraint := strings.Index(sql, "ADD CONSTRAINT invitations_display_name_required")
	if backfill < 0 || constraint < 0 || backfill > constraint {
		t.Fatal("existing invitation names must be backfilled before the required-name constraint")
	}
	if strings.Count(sql, "pg_advisory_xact_lock") != 3 {
		t.Fatal("tenant invite, member invite, and acceptance must share the email lock")
	}
	acceptStart := strings.Index(sql, "CREATE FUNCTION public.onboarding_accept_invitation")
	if acceptStart < 0 {
		t.Fatal("acceptance function is missing")
	}
	acceptLock := strings.Index(sql[acceptStart:], "pg_advisory_xact_lock")
	acceptRowLock := strings.Index(sql[acceptStart:], "FOR UPDATE")
	if acceptLock < 0 || acceptRowLock < 0 || acceptLock > acceptRowLock {
		t.Fatal("acceptance must acquire the email advisory lock before the invitation row lock")
	}
	for _, contract := range []string{
		"ADD COLUMN display_name",
		"CREATE ROLE namo_onboarding_definer NOLOGIN",
		"BYPASSRLS NOINHERIT",
		"CREATE FUNCTION public.onboarding_create_tenant",
		"CREATE FUNCTION public.onboarding_invite_member",
		"CREATE FUNCTION public.onboarding_invitation_lookup",
		"CREATE FUNCTION public.onboarding_accept_invitation",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"role = 'platform_admin'",
		"role = 'tenant_admin'",
		"FOR UPDATE",
		"accepted_at IS NULL",
		"expires_at > clock_timestamp()",
		"pg_advisory_xact_lock",
		"hashtextextended('namo-invitation:' ||",
		"UPDATE public.invitations SET accepted_at = clock_timestamp()",
		"REVOKE INSERT, UPDATE, DELETE ON TABLE public.invitations FROM namo_runtime",
		"REVOKE ALL ON FUNCTION public.onboarding_accept_invitation(text, text, text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.onboarding_accept_invitation(text, text, text) TO namo_runtime",
	} {
		if !strings.Contains(sql, contract) {
			t.Fatalf("onboarding migration missing contract: %s", contract)
		}
	}
	if strings.Contains(sql, "raw_token") || strings.Contains(sql, "password text") {
		t.Fatal("onboarding schema must never persist a raw token or plaintext password")
	}
}

func TestInvitationOnboardingSchemaMakesReinviteRecoverable(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0003_invitation_onboarding.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, contract := range []string{
		"DROP CONSTRAINT IF EXISTS invitations_tenant_id_email_key",
		"SET email = lower(btrim(email))",
		"row_number() OVER (PARTITION BY lower(email)",
		"SET accepted_at = clock_timestamp()",
		"invitations_tenant_pending_email_unique",
		"ON CONFLICT (tenant_id, (lower(email))) WHERE accepted_at IS NULL",
		"token_hash = EXCLUDED.token_hash",
		"expires_at = EXCLUDED.expires_at",
		"accepted_at = NULL",
		"INSERT INTO public.schedules",
		"'기본 알림', 7, 0, 'Asia/Seoul'",
	} {
		if !strings.Contains(sql, contract) {
			t.Fatalf("reinvite/default schedule contract missing: %s", contract)
		}
	}
}
