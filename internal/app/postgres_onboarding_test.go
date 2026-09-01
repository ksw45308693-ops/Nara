package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"g2b-monitor/internal/auth"
	appweb "g2b-monitor/internal/web"
)

type invitationDBStub struct {
	query string
	args  []any
	row   invitationRowStub
}

func (d *invitationDBStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	d.query, d.args = query, append([]any(nil), args...)
	return d.row
}

type invitationRowStub struct {
	values []any
	err    error
}

func (r invitationRowStub) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan width")
	}
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.values[i]))
	}
	return nil
}

func TestPostgresInvitationStoreUsesNarrowDefinerFunctions(t *testing.T) {
	expires := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)

	tenantDB := &invitationDBStub{row: invitationRowStub{values: []any{"tenant-1", "invite-1"}}}
	tenantStore := PostgresInvitationStore{DB: tenantDB}
	err := tenantStore.CreateTenantInvitation(context.Background(), TenantInvitationInput{
		ActorUserID: "actor-1", TenantName: "회사", ContactEmail: "contact@example.com",
		AdminName: "관리자", AdminEmail: "admin@example.com", Role: auth.TenantAdmin,
		TokenHash: hash, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tenantDB.query, "public.onboarding_create_tenant") || len(tenantDB.args) != 7 || tenantDB.args[5] != hash {
		t.Fatalf("tenant query=%q args=%#v", tenantDB.query, tenantDB.args)
	}

	memberDB := &invitationDBStub{row: invitationRowStub{values: []any{"invite-2"}}}
	memberStore := PostgresInvitationStore{DB: memberDB}
	err = memberStore.CreateMemberInvitation(context.Background(), MemberInvitationInput{
		ActorUserID: "actor-2", TenantID: "tenant-real", Name: "담당자", Email: "member@example.com",
		Role: auth.Member, TokenHash: hash, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(memberDB.query, "public.onboarding_invite_member") || len(memberDB.args) != 7 || memberDB.args[1] != "tenant-real" || memberDB.args[5] != hash {
		t.Fatalf("member query=%q args=%#v", memberDB.query, memberDB.args)
	}
}

func TestPostgresInvitationStoreLooksUpAndAcceptsByHashOnly(t *testing.T) {
	expires := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	hash := strings.Repeat("b", 64)
	lookupDB := &invitationDBStub{row: invitationRowStub{values: []any{"회사", "member@example.com", "담당자", "member", expires}}}
	store := PostgresInvitationStore{DB: lookupDB}
	record, err := store.InvitationByHash(context.Background(), hash)
	if err != nil || record.Role != auth.Member || record.ExpiresAt != expires {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	if !strings.Contains(lookupDB.query, "public.onboarding_invitation_lookup") || len(lookupDB.args) != 1 || lookupDB.args[0] != hash {
		t.Fatalf("lookup query=%q args=%#v", lookupDB.query, lookupDB.args)
	}

	acceptDB := &invitationDBStub{row: invitationRowStub{values: []any{"user-1", "tenant-1", "member@example.com", "member"}}}
	store.DB = acceptDB
	if err := store.AcceptInvitation(context.Background(), AcceptedInvitationInput{TokenHash: hash, DisplayName: "담당자", PasswordHash: "$2a$hash"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(acceptDB.query, "public.onboarding_accept_invitation") || !reflect.DeepEqual(acceptDB.args, []any{hash, "담당자", "$2a$hash"}) {
		t.Fatalf("accept query=%q args=%#v", acceptDB.query, acceptDB.args)
	}

	missing := PostgresInvitationStore{DB: &invitationDBStub{row: invitationRowStub{err: pgx.ErrNoRows}}}
	if _, err := missing.InvitationByHash(context.Background(), hash); !errors.Is(err, appweb.ErrInvitationUnavailable) {
		t.Fatalf("missing lookup error=%v", err)
	}
	if err := missing.AcceptInvitation(context.Background(), AcceptedInvitationInput{TokenHash: hash, DisplayName: "담당자", PasswordHash: "$2a$hash"}); !errors.Is(err, appweb.ErrInvitationUnavailable) {
		t.Fatalf("missing acceptance error=%v", err)
	}
}
