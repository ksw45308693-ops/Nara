package migrations

import (
	"strings"
	"testing"
)

func TestAllReturnsOrderedOperationalMigrations(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 6 || migrations[0].Version != 1 || migrations[1].Version != 2 || migrations[2].Version != 3 || migrations[3].Version != 4 || migrations[4].Version != 5 || migrations[5].Version != 6 {
		t.Fatalf("migrations = %+v", migrations)
	}
	for _, migration := range migrations {
		if strings.TrimSpace(migration.SQL) == "" {
			t.Fatalf("migration %d is empty", migration.Version)
		}
	}
	if !strings.Contains(migrations[1].SQL, "notice_revisions") || !strings.Contains(migrations[1].SQL, "runtime_tenant_catalog") {
		t.Fatal("operational migration is incomplete")
	}
	for _, contract := range []string{
		"ALTER TABLE public.deliveries ADD COLUMN claimed_at",
		"CREATE TABLE public.digest_windows",
		"CREATE TABLE public.digest_window_items",
		"CREATE TABLE public.digest_window_recipients",
		"ALTER TABLE public.digest_windows ENABLE ROW LEVEL SECURITY",
		"CREATE POLICY digest_windows_tenant_isolation",
		"CREATE UNIQUE INDEX digest_windows_one_pending_per_schedule",
		"status IN ('pending', 'completed', 'failed')",
		"CREATE POLICY digest_window_items_tenant_isolation",
		"CREATE POLICY digest_window_recipients_tenant_isolation",
		"ADD COLUMN window_end_at",
		"ADD COLUMN claim_token",
		"CHECK (window_end_at >= due_at)",
		"UNIQUE (tenant_id, schedule_id, recipient_id, due_at)",
		"SELECT d.tenant_id, d.schedule_id, d.due_at",
		"FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at)",
		"FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at, recipient_id)",
		"DROP CONSTRAINT deliveries_recipient_tenant_fk",
		"GRANT SELECT (id, name, contact_email, created_at) ON TABLE public.tenants TO namo_catalog_definer",
	} {
		if !strings.Contains(migrations[1].SQL, contract) {
			t.Fatalf("operational migration missing contract: %s", contract)
		}
	}
	for _, contract := range []string{
		"ADD COLUMN region_lookup_complete",
		"CREATE TABLE public.api_daily_usage",
		"recipients_tenant_lower_email_unique",
		"CREATE TEMP TABLE recipient_merge",
		"GRANT SELECT, INSERT, UPDATE ON TABLE public.api_daily_usage TO namo_runtime",
	} {
		if !strings.Contains(migrations[3].SQL, contract) {
			t.Fatalf("release hardening migration missing contract: %s", contract)
		}
	}
	for _, contract := range []string{
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
		"CREATE OR REPLACE FUNCTION public.onboarding_accept_invitation",
		"pg_advisory_xact_lock",
		"hashtextextended('namo-tenant-onboarding:' ||",
		"IF EXISTS (SELECT 1 FROM public.users WHERE lower(email) = v_email)",
		"role = 'tenant_admin'",
		"lower(email) <> v_invitee_email",
		"OWNER TO namo_onboarding_definer",
		"REVOKE ALL ON FUNCTION public.onboarding_accept_invitation(text, text, text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.onboarding_accept_invitation(text, text, text) TO namo_runtime",
	} {
		if !strings.Contains(migrations[4].SQL, contract) {
			t.Fatalf("invitation race upgrade missing contract: %s", contract)
		}
	}
	for _, contract := range []string{
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
		"hashtextextended('namo-tenant-onboarding:' ||",
		"hashtextextended('namo-invitation:' ||",
		"SET accepted_at = clock_timestamp()",
		"expires_at <= clock_timestamp()",
		"OWNER TO namo_onboarding_definer",
		"REVOKE ALL ON FUNCTION public.onboarding_invite_member",
		"GRANT EXECUTE ON FUNCTION public.onboarding_invite_member",
	} {
		if !strings.Contains(migrations[5].SQL, contract) {
			t.Fatalf("expired invitation upgrade missing contract: %s", contract)
		}
	}
}
