package store

import (
	"os"
	"strings"
	"testing"
)

func TestSelfSignupSchemaKeepsPrivilegeAndRaceGuards(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0010_self_signup.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)

	dropOld := strings.Index(sql, "DROP CONSTRAINT %I")
	addNew := strings.Index(sql, "ADD CONSTRAINT users_role_tenant_scope")
	if dropOld < 0 || addNew < 0 || dropOld > addNew {
		t.Fatal("the platform-admin-only tenant constraint must be dropped before the wider one is added")
	}

	createStart := strings.Index(sql, "CREATE FUNCTION public.signup_create_account")
	if createStart < 0 {
		t.Fatal("signup function is missing")
	}
	create := sql[createStart:]
	lock := strings.Index(create, "pg_advisory_xact_lock")
	existing := strings.Index(create, "IF EXISTS (SELECT 1 FROM public.users WHERE lower(email) = v_email)")
	insert := strings.Index(create, "INSERT INTO public.users")
	if lock < 0 || existing < 0 || insert < 0 || lock > existing || existing > insert {
		t.Fatal("signup must lock the email, reject an existing account, and only then insert")
	}
	pending := strings.Index(create, "FROM public.invitations")
	if pending < 0 || pending > insert {
		t.Fatal("signup must reject a pending invitation before creating the account")
	}
	if strings.Contains(create[:insert+400], "'platform_admin'") || strings.Contains(create[:insert+400], "'tenant_admin'") {
		t.Fatal("signup must never grant an administrator role")
	}

	assignStart := strings.Index(sql, "CREATE FUNCTION public.signup_set_account_tenant")
	if assignStart < 0 {
		t.Fatal("assignment function is missing")
	}
	assign := sql[assignStart:]
	actorCheck := strings.Index(assign, "platform administrator role is required")
	update := strings.Index(assign, "UPDATE public.users SET tenant_id = p_tenant_id")
	memberOnly := strings.Index(assign, "WHERE id = p_user_id AND role = 'member'")
	if actorCheck < 0 || update < 0 || memberOnly < 0 || actorCheck > update || update > memberOnly {
		t.Fatal("assignment must verify the platform administrator before updating one member account")
	}

	for _, contract := range []string{
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"BYPASSRLS NOINHERIT",
		"GRANT SELECT, INSERT, UPDATE ON TABLE public.users TO namo_signup_definer",
		"ALTER FUNCTION public.signup_create_account(text, text) OWNER TO namo_signup_definer",
		"ALTER FUNCTION public.signup_member_accounts(uuid) OWNER TO namo_signup_definer",
		"ALTER FUNCTION public.signup_set_account_tenant(uuid, uuid, uuid) OWNER TO namo_signup_definer",
		"REVOKE ALL ON FUNCTION public.signup_member_accounts(uuid) FROM PUBLIC",
		"REVOKE ALL ON FUNCTION public.signup_set_account_tenant(uuid, uuid, uuid) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.signup_set_account_tenant(uuid, uuid, uuid) TO namo_runtime",
	} {
		if !strings.Contains(sql, contract) {
			t.Fatalf("self signup migration missing contract: %s", contract)
		}
	}
	if strings.Count(sql, "SECURITY DEFINER") != 3 {
		t.Fatalf("SECURITY DEFINER count = %d, want one per signup function", strings.Count(sql, "SECURITY DEFINER"))
	}
	for _, forbidden := range []string{
		"DELETE ON TABLE public.users",
		"GRANT ALL",
		"TO PUBLIC",
		"public.sessions",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("self signup migration contains forbidden grant: %s", forbidden)
		}
	}
}

func TestSignupQualificationSchemaKeepsGuardsAfterReplacement(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0011_signup_column_qualification.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)

	for _, function := range []string{
		"public.signup_create_account",
		"public.signup_member_accounts",
		"public.signup_set_account_tenant",
	} {
		if !strings.Contains(sql, "CREATE OR REPLACE FUNCTION "+function) {
			t.Fatalf("replacement is missing for %s", function)
		}
	}
	if strings.Count(sql, "SECURITY DEFINER") != 3 || strings.Count(sql, "SET search_path = pg_catalog") != 3 {
		t.Fatal("each replaced function must stay SECURITY DEFINER with a fixed search_path")
	}

	createStart := strings.Index(sql, "CREATE OR REPLACE FUNCTION public.signup_create_account")
	create := sql[createStart:strings.Index(sql, "CREATE OR REPLACE FUNCTION public.signup_member_accounts")]
	lock := strings.Index(create, "pg_advisory_xact_lock")
	existing := strings.Index(create, "WHERE lower(existing.email) = v_email")
	pending := strings.Index(create, "WHERE lower(pending.email) = v_email")
	insert := strings.Index(create, "INSERT INTO public.users")
	if lock < 0 || existing < 0 || pending < 0 || insert < 0 || lock > existing || existing > pending || pending > insert {
		t.Fatal("signup must lock the email, reject an account and a pending invitation, and only then insert")
	}
	if !strings.Contains(create, "VALUES (NULL, v_email, v_display_name, p_password_hash, 'member')") {
		t.Fatal("signup must still create a member without a tenant")
	}

	assign := sql[strings.Index(sql, "CREATE OR REPLACE FUNCTION public.signup_set_account_tenant"):]
	actor := strings.Index(assign, "platform administrator role is required")
	update := strings.Index(assign, "UPDATE public.users member SET tenant_id = p_tenant_id")
	memberOnly := strings.Index(assign, "WHERE member.id = p_user_id AND member.role = 'member'")
	if actor < 0 || update < 0 || memberOnly < 0 || actor > update || update > memberOnly {
		t.Fatal("assignment must verify the platform administrator before updating one member account")
	}
}
