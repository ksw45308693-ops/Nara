package store

import (
	"os"
	"strings"
	"testing"
)

func TestExpiredInvitationUpgradeClosesRowsInsideEmailLock(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0006_expired_invitation_upgrade.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	functionStarts := []string{
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
	}
	if strings.Count(sql, "CREATE OR REPLACE FUNCTION public.onboarding_") != len(functionStarts) || strings.Contains(sql, "CREATE OR REPLACE FUNCTION public.onboarding_accept_invitation") {
		t.Fatal("upgrade must replace only invitation creation functions")
	}
	if strings.Count(sql, "pg_advisory_xact_lock") != 4 || strings.Count(sql, "AND expires_at <= clock_timestamp()") != 2 {
		t.Fatal("each creation function must take tenant/email locks and close expired rows")
	}
	for index, marker := range functionStarts {
		start := strings.Index(sql, marker)
		end := len(sql)
		if index+1 < len(functionStarts) {
			end = strings.Index(sql, functionStarts[index+1])
		}
		if start < 0 || end <= start {
			t.Fatalf("cannot isolate function: %s", marker)
		}
		definition := sql[start:end]
		tenantLock := strings.Index(definition, "hashtextextended('g2b-tenant-onboarding:' ||")
		emailLock := strings.Index(definition, "hashtextextended('g2b-invitation:' ||")
		closeExpired := strings.Index(definition, "AND expires_at <= clock_timestamp()")
		accountCheck := strings.Index(definition, "email already belongs to an account")
		insertInvitation := strings.Index(definition, "INSERT INTO public.invitations")
		if tenantLock < 0 || emailLock < tenantLock || closeExpired < emailLock || accountCheck < closeExpired || insertInvitation < accountCheck {
			t.Fatalf("%s order must be tenant lock, email lock, expiry close, account check, insert", marker)
		}
		cleanupStart := strings.Index(definition, "UPDATE public.invitations")
		if cleanupStart < 0 {
			t.Fatalf("%s expired cleanup is missing", marker)
		}
		cleanupEnd := strings.Index(definition[cleanupStart:], ";")
		if cleanupEnd < 0 {
			t.Fatalf("%s expired cleanup is incomplete", marker)
		}
		cleanup := definition[cleanupStart : cleanupStart+cleanupEnd]
		expectedEmail := "lower(email) = v_email"
		if index == 0 {
			expectedEmail = "lower(email) = v_invitee_email"
		}
		if !strings.Contains(cleanup, expectedEmail) || strings.Contains(cleanup, "tenant_id") {
			t.Fatalf("%s cleanup must release the normalized email across tenants: %s", marker, cleanup)
		}
		for _, contract := range []string{
			"SECURITY DEFINER",
			"SET search_path = pg_catalog",
			"UPDATE public.invitations",
			"SET accepted_at = clock_timestamp()",
			"lower(email) = ",
			"accepted_at IS NULL",
			"expires_at <= clock_timestamp()",
		} {
			if !strings.Contains(definition, contract) {
				t.Fatalf("%s missing contract: %s", marker, contract)
			}
		}
	}
	for _, contract := range []string{
		"ALTER FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) OWNER TO g2b_onboarding_definer",
		"ALTER FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) OWNER TO g2b_onboarding_definer",
		"REVOKE ALL ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) FROM PUBLIC",
		"REVOKE ALL ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) TO g2b_runtime",
		"GRANT EXECUTE ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) TO g2b_runtime",
		"The current schema uses accepted_at as a general closed marker",
	} {
		if !strings.Contains(sql, contract) {
			t.Fatalf("expired invitation upgrade missing contract: %s", contract)
		}
	}
	if strings.Contains(sql, "raw_token") || strings.Contains(sql, "password text") {
		t.Fatal("upgrade must not persist invitation bearer values or plaintext passwords")
	}
}
