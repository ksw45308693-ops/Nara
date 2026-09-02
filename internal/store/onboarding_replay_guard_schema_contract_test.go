package store

import (
	"os"
	"strings"
	"testing"
)

func TestInvitationReplayGuardRejectsLivePendingWithoutReplacement(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0009_invitation_replay_guard.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(body), "\r\n", "\n")
	starts := []string{
		"CREATE OR REPLACE FUNCTION public.onboarding_create_tenant",
		"CREATE OR REPLACE FUNCTION public.onboarding_invite_member",
	}
	if strings.Count(sql, "CREATE OR REPLACE FUNCTION public.onboarding_") != len(starts) || strings.Contains(sql, "DO UPDATE SET") {
		t.Fatal("replay guard must replace only creation functions and must not update pending invitations")
	}
	for index, marker := range starts {
		start := strings.Index(sql, marker)
		end := len(sql)
		if index+1 < len(starts) {
			end = strings.Index(sql, starts[index+1])
		}
		if start < 0 || end <= start {
			t.Fatalf("cannot isolate function: %s", marker)
		}
		definition := sql[start:end]
		tenantLock := strings.Index(definition, "hashtextextended('namo-tenant-onboarding:' ||")
		emailLock := strings.Index(definition, "hashtextextended('namo-invitation:' ||")
		closeExpired := strings.Index(definition, "AND expires_at <= clock_timestamp()")
		pendingCheck := strings.Index(definition, "invitation already pending")
		insertInvitation := strings.Index(definition, "INSERT INTO public.invitations")
		if tenantLock < 0 || emailLock < tenantLock || closeExpired < emailLock || pendingCheck < closeExpired || insertInvitation < pendingCheck {
			t.Fatalf("%s order must be tenant lock, email lock, expiry close, pending check, insert", marker)
		}
		tenantPredicate := "tenant_id = v_tenant_id"
		if index == 1 {
			tenantPredicate = "tenant_id = p_tenant_id"
		}
		for _, contract := range []string{
			tenantPredicate,
			"accepted_at IS NULL",
			"expires_at > clock_timestamp()",
			"RAISE EXCEPTION 'invitation already pending' USING ERRCODE = 'P0001'",
			"SECURITY DEFINER",
			"SET search_path = pg_catalog",
		} {
			if !strings.Contains(definition, contract) {
				t.Fatalf("%s missing contract: %s", marker, contract)
			}
		}
	}
	for _, contract := range []string{
		"ALTER FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer",
		"ALTER FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer",
		"REVOKE ALL ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) FROM PUBLIC",
		"REVOKE ALL ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) TO namo_runtime",
		"GRANT EXECUTE ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) TO namo_runtime",
	} {
		if !strings.Contains(sql, contract) {
			t.Fatalf("replay guard migration missing contract: %s", contract)
		}
	}
	if strings.Contains(sql, "raw_token") || strings.Contains(sql, "password text") {
		t.Fatal("replay guard must not persist bearer values or plaintext passwords")
	}
}
