package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestAllReturnsOrderedOperationalMigrations(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[0].Version != 1 || migrations[1].Version != 2 || migrations[2].Version != 3 || migrations[3].Version != 4 || migrations[4].Version != 5 || migrations[5].Version != 6 || migrations[6].Version != 7 || migrations[7].Version != 8 || migrations[8].Version != 9 {
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
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
		"invitation already pending",
		"expires_at <= clock_timestamp()",
		"OWNER TO namo_onboarding_definer",
		"GRANT EXECUTE ON FUNCTION public.onboarding_invite_member",
	} {
		if !strings.Contains(migrations[8].SQL, contract) {
			t.Fatalf("invitation replay guard migration missing contract: %s", contract)
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
	for _, contract := range []string{
		"CREATE TABLE public.reports",
		"id uuid PRIMARY KEY DEFAULT gen_random_uuid()",
		"tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE",
		"schedule_id uuid",
		"due_at timestamptz NOT NULL",
		"window_start_at timestamptz",
		"window_end_at timestamptz NOT NULL",
		"relative_path text NOT NULL DEFAULT ''",
		"sha256 text NOT NULL DEFAULT ''",
		"notice_count integer NOT NULL DEFAULT 0 CHECK (notice_count >= 0)",
		"last_error text",
		"claim_token uuid",
		"claimed_at timestamptz",
		"generated_at timestamptz",
		"created_at timestamptz NOT NULL DEFAULT now()",
		"UNIQUE (tenant_id, id)",
		"window_start_at IS NULL OR window_start_at <= window_end_at",
		"trigger IN ('scheduled', 'manual')",
		"status IN ('generating', 'generated', 'failed')",
		"attempts BETWEEN 0 AND 3",
		"(trigger = 'scheduled' AND schedule_id IS NOT NULL) OR (trigger = 'manual' AND schedule_id IS NULL)",
		"FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at)",
		"REFERENCES public.digest_windows (tenant_id, schedule_id, due_at, window_end_at)",
		"CREATE UNIQUE INDEX reports_scheduled_due_unique",
		"WHERE trigger = 'scheduled'",
		"CREATE INDEX reports_tenant_status_due_idx",
		"CREATE TABLE public.report_items",
		"report_id uuid NOT NULL",
		"tenant_id uuid NOT NULL",
		"ordinal integer NOT NULL CHECK (ordinal > 0)",
		"match_id uuid NOT NULL",
		"notice_id uuid NOT NULL",
		"title text NOT NULL",
		"category IN ('construction', 'service', 'goods', 'foreign')",
		"agency text NOT NULL",
		"region text NOT NULL",
		"amount bigint NOT NULL DEFAULT 0 CHECK (amount >= 0)",
		"deadline_at timestamptz NOT NULL",
		"source_url text NOT NULL",
		"rule_name text NOT NULL",
		"reasons jsonb NOT NULL",
		"FOREIGN KEY (tenant_id, report_id)",
		"REFERENCES public.reports (tenant_id, id) ON DELETE CASCADE",
		"UNIQUE (tenant_id, report_id, ordinal)",
		"UNIQUE (tenant_id, report_id, match_id)",
		"CREATE INDEX report_items_tenant_report_ordinal_idx",
		"ALTER TABLE public.reports ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE public.reports FORCE ROW LEVEL SECURITY",
		"ALTER TABLE public.report_items ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE public.report_items FORCE ROW LEVEL SECURITY",
		"CREATE POLICY reports_tenant_isolation",
		"CREATE POLICY report_items_tenant_isolation",
		"current_setting('app.tenant_id', true)::uuid",
		"GRANT SELECT, INSERT, UPDATE ON TABLE public.reports TO namo_runtime",
		"GRANT SELECT, INSERT ON TABLE public.report_items TO namo_runtime",
	} {
		if !strings.Contains(migrations[6].SQL, contract) {
			t.Fatalf("report delivery migration missing contract: %s", contract)
		}
	}
	originalReportDeliveryChecksum := sha256.Sum256([]byte(migrations[6].SQL))
	if got := hex.EncodeToString(originalReportDeliveryChecksum[:]); got != "5234cebcbf23e59dfddb87207a6a24d61560b588fd5a378044e55379fcff99ab" {
		t.Fatalf("report delivery migration checksum=%s", got)
	}
	for _, contract := range []string{
		"ALTER TABLE public.reports ADD COLUMN tenant_name text",
		"ALTER TABLE public.reports ADD COLUMN schedule_name text",
		"UPDATE public.reports r",
		"ALTER COLUMN tenant_name SET NOT NULL",
		"ALTER COLUMN schedule_name SET NOT NULL",
		"CHECK (btrim(tenant_name) <> '')",
		"CHECK (btrim(schedule_name) <> '')",
		"GRANT SELECT, INSERT, UPDATE ON TABLE public.reports TO namo_runtime",
	} {
		if !strings.Contains(migrations[7].SQL, contract) {
			t.Fatalf("report name snapshot migration missing contract: %s", contract)
		}
	}
	for _, forbidden := range []string{
		"REFERENCES public.matches",
		"REFERENCES public.notices",
		"GRANT SELECT, INSERT, UPDATE ON TABLE public.report_items",
		"GRANT SELECT, INSERT, DELETE ON TABLE public.report_items",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.reports",
	} {
		if strings.Contains(migrations[6].SQL, forbidden) {
			t.Fatalf("report delivery migration contains forbidden contract: %s", forbidden)
		}
	}
	for _, forbidden := range []string{"tenant_name", "schedule_name"} {
		if strings.Contains(migrations[6].SQL, forbidden) {
			t.Fatalf("versioned report delivery migration changed: %s", forbidden)
		}
	}
}
