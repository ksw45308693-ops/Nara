package store

import (
	"os"
	"strings"
	"testing"
)

func TestInvitationRaceUpgradeRedefinesAllMutatingFunctions(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0005_invitation_race_upgrade.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if strings.Count(sql, "CREATE OR REPLACE FUNCTION public.onboarding_") != 3 {
		t.Fatal("upgrade must replace the three mutating onboarding functions")
	}
	if strings.Count(sql, "pg_advisory_xact_lock") != 6 {
		t.Fatal("each mutating onboarding function must take tenant then email locks")
	}
	if strings.Count(sql, "email already belongs to an account") != 3 {
		t.Fatal("each mutating onboarding function must recheck user existence after locking")
	}
	for _, contract := range []string{
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
		"CREATE OR REPLACE FUNCTION public.onboarding_accept_invitation",
		"hashtextextended('namo-tenant-onboarding:' ||",
		"role = 'tenant_admin' AND accepted_at IS NULL",
		"lower(email) <> v_invitee_email",
		"ALTER FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer",
		"ALTER FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer",
		"ALTER FUNCTION public.onboarding_accept_invitation(text, text, text) OWNER TO namo_onboarding_definer",
		"REVOKE ALL ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) FROM PUBLIC",
		"REVOKE ALL ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) FROM PUBLIC",
		"REVOKE ALL ON FUNCTION public.onboarding_accept_invitation(text, text, text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) TO namo_runtime",
		"GRANT EXECUTE ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) TO namo_runtime",
		"GRANT EXECUTE ON FUNCTION public.onboarding_accept_invitation(text, text, text) TO namo_runtime",
	} {
		if !strings.Contains(sql, contract) {
			t.Fatalf("invitation race upgrade missing contract: %s", contract)
		}
	}
	if strings.Contains(sql, "raw_token") || strings.Contains(sql, "password text") {
		t.Fatal("upgrade must never persist a raw token or plaintext password")
	}

	functionStarts := []string{
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
		"CREATE OR REPLACE FUNCTION public.onboarding_accept_invitation",
	}
	for index, marker := range functionStarts {
		start := strings.Index(sql, marker)
		end := len(sql)
		if index+1 < len(functionStarts) {
			end = strings.Index(sql, functionStarts[index+1])
		}
		if start < 0 || end <= start {
			t.Fatalf("cannot isolate function contract: %s", marker)
		}
		definition := sql[start:end]
		tenantLock := strings.Index(definition, "hashtextextended('namo-tenant-onboarding:' ||")
		emailLock := strings.Index(definition, "hashtextextended('namo-invitation:' ||")
		if tenantLock < 0 || emailLock < 0 || tenantLock > emailLock {
			t.Fatalf("%s must lock tenant before email", marker)
		}
		if marker == "CREATE OR REPLACE FUNCTION public.onboarding_accept_invitation" {
			lookup := strings.Index(definition, "SELECT i.tenant_id, lower(i.email) INTO v_tenant_id, v_email")
			if lookup < 0 || lookup > tenantLock {
				t.Fatal("acceptance must learn the invitation tenant before taking tenant then email locks")
			}
		}
	}
	createStart := strings.Index(sql, functionStarts[0])
	createEnd := strings.Index(sql, functionStarts[1])
	createDefinition := sql[createStart:createEnd]
	if strings.Contains(createDefinition, "FOR UPDATE") {
		t.Fatal("tenant resolution must not take a row lock before the tenant advisory lock")
	}
	revokeOldAdministrator := strings.Index(createDefinition, "UPDATE public.invitations\n    SET accepted_at = clock_timestamp()")
	insertReplacement := strings.Index(createDefinition, "INSERT INTO public.invitations")
	if revokeOldAdministrator < 0 || insertReplacement < 0 || revokeOldAdministrator > insertReplacement {
		t.Fatal("old initial-administrator bearers must be consumed before inserting the replacement")
	}
}
