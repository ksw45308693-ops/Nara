package store

import (
	"os"
	"strings"
	"testing"
)

func TestInitialSchemaKeepsTenantReferencesInTenant(t *testing.T) {
	sql, err := os.ReadFile("../../migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(sql)
	for _, contract := range []string{
		"FOREIGN KEY (tenant_id, filter_id) REFERENCES filters (tenant_id, id)",
		"FOREIGN KEY (tenant_id, schedule_id) REFERENCES schedules (tenant_id, id)",
		"FOREIGN KEY (tenant_id, recipient_id) REFERENCES recipients (tenant_id, id)",
		"ALTER TABLE filters ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE deliveries ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE filters FORCE ROW LEVEL SECURITY",
		"ALTER TABLE deliveries FORCE ROW LEVEL SECURITY",
		"CHECK (status IN ('pending', 'sending', 'sent', 'failed'))",
		"ALTER TABLE tenants ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE tenants FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenants_tenant_isolation",
		"CREATE FUNCTION auth_session_lookup(p_token_hash text)",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"REVOKE ALL ON FUNCTION auth_session_lookup(text) FROM PUBLIC",
		"CREATE ROLE namo_runtime NOLOGIN NOBYPASSRLS",
		"ALTER ROLE namo_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT",
		"GRANT EXECUTE ON FUNCTION auth_session_lookup(text) TO namo_runtime",
		"ALTER ROLE namo_auth_definer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS NOINHERIT",
		"ALTER FUNCTION auth_session_lookup(text) OWNER TO namo_auth_definer",
		"CREATE FUNCTION auth_account_lookup(p_email text)",
		"ALTER FUNCTION auth_account_lookup(text) OWNER TO namo_auth_definer",
		"GRANT SELECT (id, tenant_id, email, password_hash, role) ON TABLE public.users TO namo_auth_definer",
		"GRANT SELECT (user_id, token_hash, expires_at) ON TABLE public.sessions TO namo_auth_definer",
		"GRANT USAGE ON SCHEMA public TO namo_runtime",
		"FROM pg_catalog.pg_auth_members",
		"REVOKE %I FROM namo_runtime",
		"REVOKE %I FROM namo_auth_definer",
		"CREATE FUNCTION auth_session_create(p_user_id uuid, p_token_hash text, p_expires_at timestamptz)",
		"CREATE FUNCTION auth_session_delete(p_token_hash text)",
		"INSERT INTO public.sessions (user_id, token_hash, expires_at)",
		"p_expires_at > now() + interval '90 days'",
		"p_expires_at IS NULL OR p_expires_at <= now()",
		"GRANT INSERT (user_id, token_hash, expires_at), DELETE ON TABLE public.sessions TO namo_auth_definer",
		"REVOKE ALL ON FUNCTION auth_session_create(uuid, text, timestamptz) FROM PUBLIC",
		"REVOKE ALL ON FUNCTION auth_session_delete(text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION auth_session_create(uuid, text, timestamptz) TO namo_runtime",
		"GRANT EXECUTE ON FUNCTION auth_session_delete(text) TO namo_runtime",
	} {
		if !strings.Contains(text, contract) {
			t.Fatalf("missing SQL contract: %s", contract)
		}
	}
}

func TestInitialSchemaLeavesSessionTenantAndRLSOutOfAuthenticationLookup(t *testing.T) {
	sql, err := os.ReadFile("../../migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(sql)
	start := strings.Index(text, "CREATE TABLE sessions")
	end := strings.Index(text[start:], "CREATE TABLE notices")
	if start < 0 || end < 0 {
		t.Fatal("sessions definition is missing")
	}
	if strings.Contains(text[start:start+end], "tenant_id") {
		t.Fatal("sessions must derive tenancy through users only")
	}
	if strings.Contains(text, "ALTER TABLE sessions") || strings.Contains(text, "sessions_tenant_isolation") {
		t.Fatal("RLS must not block privileged authentication lookup")
	}
}
