package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingOnboarding struct {
	tenantCalls, memberCalls, lookupCalls, acceptCalls int
	tenantContext, memberContext                       RequestContext
	tenantCommand                                      TenantInviteCommand
	memberCommand                                      MemberInviteCommand
	acceptCommand                                      AcceptInviteCommand
	invitation                                         InvitationView
	result                                             InvitationResult
	err                                                error
}

func (o *recordingOnboarding) InviteTenant(_ context.Context, requestContext RequestContext, command TenantInviteCommand) (InvitationResult, error) {
	o.tenantCalls++
	o.tenantContext, o.tenantCommand = requestContext, command
	return o.result, o.err
}

func (o *recordingOnboarding) InviteMember(_ context.Context, requestContext RequestContext, command MemberInviteCommand) (InvitationResult, error) {
	o.memberCalls++
	o.memberContext, o.memberCommand = requestContext, command
	return o.result, o.err
}

func (o *recordingOnboarding) Invitation(context.Context, string) (InvitationView, error) {
	o.lookupCalls++
	return o.invitation, o.err
}

func (o *recordingOnboarding) AcceptInvitation(_ context.Context, command AcceptInviteCommand) error {
	o.acceptCalls++
	o.acceptCommand = command
	return o.err
}

func TestInviteTenantRequiresRoleAndCSRF(t *testing.T) {
	form := "_csrf=token&tenant_name=새회사&contact_email=contact%40example.com&admin_name=김관리&admin_email=admin%40example.com"
	link := "https://monitor.example/accept-invite#token=" + strings.Repeat("t", 43)

	allowed := &recordingOnboarding{result: InvitationResult{URL: link, ExpiresAt: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}}
	response := serveHandler(t, onboardingHandler(t, RequestContext{UserID: "platform-1", Role: "platform_admin", CSRFToken: "token"}, allowed), http.MethodPost, "/admin/tenants", form)
	if response.Code != http.StatusOK || response.Header().Get("Location") != "" || allowed.tenantCalls != 1 {
		t.Fatalf("allowed status=%d location=%q calls=%d body=%q", response.Code, response.Header().Get("Location"), allowed.tenantCalls, response.Body.String())
	}
	assertInvitationLinkResponse(t, response, link)
	if allowed.tenantCommand.AdminEmail != "admin@example.com" || allowed.tenantContext.UserID != "platform-1" {
		t.Fatalf("tenant invite=%#v context=%#v", allowed.tenantCommand, allowed.tenantContext)
	}

	for _, test := range []struct {
		name    string
		context RequestContext
		form    string
		want    int
	}{
		{"wrong role", RequestContext{UserID: "member", TenantID: "tenant-1", Role: "member", CSRFToken: "token"}, form, http.StatusForbidden},
		{"bad csrf", RequestContext{UserID: "platform-1", Role: "platform_admin", CSRFToken: "token"}, strings.Replace(form, "_csrf=token", "_csrf=wrong", 1), http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			onboarding := &recordingOnboarding{}
			got := serveHandler(t, onboardingHandler(t, test.context, onboarding), http.MethodPost, "/admin/tenants", test.form)
			if got.Code != test.want || onboarding.tenantCalls != 0 {
				t.Fatalf("status=%d calls=%d", got.Code, onboarding.tenantCalls)
			}
		})
	}
}

func TestInviteMemberCannotChooseAnotherTenant(t *testing.T) {
	link := "https://monitor.example/accept-invite#token=" + strings.Repeat("m", 43)
	onboarding := &recordingOnboarding{result: InvitationResult{URL: link, ExpiresAt: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}}
	requestContext := RequestContext{UserID: "admin-1", TenantID: "tenant-real", TenantName: "실제 회사", Role: "tenant_admin", CSRFToken: "token"}
	form := "_csrf=token&tenant_id=tenant-other&name=새담당&email=new%40example.com&role=tenant_admin"
	response := serveHandler(t, onboardingHandler(t, requestContext, onboarding), http.MethodPost, "/settings/invitations", form)
	if response.Code != http.StatusOK || response.Header().Get("Location") != "" || onboarding.memberCalls != 1 {
		t.Fatalf("status=%d location=%q calls=%d", response.Code, response.Header().Get("Location"), onboarding.memberCalls)
	}
	assertInvitationLinkResponse(t, response, link)
	if onboarding.memberContext.TenantID != "tenant-real" || onboarding.memberCommand.Role != "tenant_admin" {
		t.Fatalf("context=%#v command=%#v", onboarding.memberContext, onboarding.memberCommand)
	}

	for _, role := range []string{"member", "platform_admin"} {
		restricted := &recordingOnboarding{}
		ctx := RequestContext{UserID: "user", TenantID: "tenant-real", Role: role, CSRFToken: "token"}
		got := serveHandler(t, onboardingHandler(t, ctx, restricted), http.MethodPost, "/settings/invitations", form)
		if got.Code != http.StatusForbidden || restricted.memberCalls != 0 {
			t.Fatalf("role=%s status=%d calls=%d", role, got.Code, restricted.memberCalls)
		}
	}
}

func TestInviteLinkIsOnlyRenderedForSuccessfulPost(t *testing.T) {
	link := "https://monitor.example/accept-invite#token=" + strings.Repeat("s", 43)
	onboarding := &recordingOnboarding{result: InvitationResult{URL: link, ExpiresAt: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}}
	handler := onboardingHandler(t, RequestContext{UserID: "admin-1", TenantID: "tenant-1", TenantName: "회사", Role: "tenant_admin", CSRFToken: "token"}, onboarding)

	post := serveHandler(t, handler, http.MethodPost, "/settings/invitations", "_csrf=token&name=담당자&email=user%40example.com&role=member")
	assertInvitationLinkResponse(t, post, link)
	onboarding.err = ErrInvitationPending
	replayed := serveHandler(t, handler, http.MethodPost, "/settings/invitations", "_csrf=token&name=담당자&email=user%40example.com&role=member")
	if replayed.Code != http.StatusConflict || strings.Contains(replayed.Body.String(), "#token=") || strings.Contains(replayed.Body.String(), strings.Repeat("s", 43)) {
		t.Fatalf("replay status=%d body=%q", replayed.Code, replayed.Body.String())
	}
	if replayed.Header().Get("Cache-Control") != "no-store" || replayed.Header().Get("Referrer-Policy") != "no-referrer" || replayed.Header().Get("Set-Cookie") != "" || replayed.Header().Get("Location") != "" {
		t.Fatalf("replay sensitive headers=%v", replayed.Header())
	}
	get := serveHandler(t, handler, http.MethodGet, "/settings", "")
	if strings.Contains(get.Body.String(), link) || strings.Contains(get.Body.String(), "#token=") {
		t.Fatalf("invitation link leaked into a later GET: %s", get.Body.String())
	}
}

func TestPublicInvitationPageAndAcceptance(t *testing.T) {
	token := strings.Repeat("a", 43)
	onboarding := &recordingOnboarding{invitation: InvitationView{
		TenantName: "초대 회사", Email: "member@example.com", DisplayName: "김담당", Role: "member",
		ExpiresAt: time.Date(2026, 9, 3, 7, 0, 0, 0, time.FixedZone("KST", 9*60*60)),
	}}
	handler := onboardingHandler(t, RequestContext{CSRFToken: "anonymous-token"}, onboarding)
	bootstrap := serveHandler(t, handler, http.MethodGet, "/accept-invite", "")
	if bootstrap.Code != http.StatusOK || !strings.Contains(bootstrap.Body.String(), `data-invite-token-form`) || !strings.Contains(bootstrap.Body.String(), `/assets/app.js?v=2`) || onboarding.lookupCalls != 0 {
		t.Fatalf("bootstrap status=%d lookup=%d body=%q", bootstrap.Code, onboarding.lookupCalls, bootstrap.Body.String())
	}
	page := serveHandler(t, handler, http.MethodPost, "/accept-invite", "_csrf=anonymous-token&mode=inspect&token="+token)
	if page.Header().Get("Referrer-Policy") != "no-referrer" || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("sensitive invitation headers = %v", page.Header())
	}
	for _, want := range []string{"초대 회사", "member@example.com", "김담당", `name="token" value="` + token + `"`, `name="_csrf" value="anonymous-token"`} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), want) {
			t.Fatalf("page status=%d missing %q: %s", page.Code, want, page.Body.String())
		}
	}

	form := "_csrf=anonymous-token&mode=accept&token=" + token + "&display_name=새담당&password=correct-horse-123&password_confirm=correct-horse-123"
	accepted := serveHandler(t, handler, http.MethodPost, "/accept-invite", form)
	if accepted.Code != http.StatusSeeOther || accepted.Header().Get("Location") != "/login?accepted=1" || onboarding.acceptCalls != 1 {
		t.Fatalf("accepted status=%d location=%q calls=%d", accepted.Code, accepted.Header().Get("Location"), onboarding.acceptCalls)
	}
	if onboarding.acceptCommand.Token != token || onboarding.acceptCommand.Password != "correct-horse-123" {
		t.Fatalf("accept command=%#v", onboarding.acceptCommand)
	}
}

func TestInvitationExpiryRendersInSeoulWhenHostAndRecordAreUTC(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = originalLocal })

	token := strings.Repeat("a", 43)
	onboarding := &recordingOnboarding{invitation: InvitationView{
		TenantName: "초대 회사", Email: "member@example.com", DisplayName: "김담당", Role: "member",
		ExpiresAt: time.Date(2026, 9, 2, 22, 0, 0, 0, time.UTC),
	}}
	handler := onboardingHandler(t, RequestContext{CSRFToken: "anonymous-token"}, onboarding)
	page := serveHandler(t, handler, http.MethodPost, "/accept-invite", "_csrf=anonymous-token&mode=inspect&token="+token)

	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "2026.09.03 07:00") {
		t.Fatalf("UTC-host invitation expiry status=%d body=%q", page.Code, page.Body.String())
	}
}

func TestUnavailableAndInvalidInvitationAcceptanceFailClosed(t *testing.T) {
	token := strings.Repeat("a", 43)
	unavailable := &recordingOnboarding{err: ErrInvitationUnavailable}
	handler := onboardingHandler(t, RequestContext{CSRFToken: "token"}, unavailable)
	if got := serveHandler(t, handler, http.MethodPost, "/accept-invite", "_csrf=token&mode=inspect&token="+token); got.Code != http.StatusGone {
		t.Fatalf("unavailable inspection status=%d", got.Code)
	}
	temporary := &recordingOnboarding{err: errors.New("database unavailable")}
	handler = onboardingHandler(t, RequestContext{CSRFToken: "token"}, temporary)
	if got := serveHandler(t, handler, http.MethodPost, "/accept-invite", "_csrf=token&mode=inspect&token="+token); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("temporary inspection failure status=%d", got.Code)
	}

	valid := &recordingOnboarding{invitation: InvitationView{Email: "member@example.com"}}
	handler = onboardingHandler(t, RequestContext{CSRFToken: "token"}, valid)
	for _, form := range []string{
		"_csrf=wrong&token=" + token + "&display_name=담당자&password=correct-horse-123&password_confirm=correct-horse-123",
		"_csrf=token&token=" + token + "&display_name=담당자&password=correct-horse-123&password_confirm=different-pass",
		"_csrf=token&token=" + token + "&display_name=담당자&password=short&password_confirm=short",
	} {
		got := serveHandler(t, handler, http.MethodPost, "/accept-invite", form)
		if got.Code != http.StatusForbidden && got.Code != http.StatusBadRequest {
			t.Fatalf("invalid acceptance status=%d body=%q", got.Code, got.Body.String())
		}
	}
	if valid.acceptCalls != 0 {
		t.Fatalf("invalid forms called accept %d times", valid.acceptCalls)
	}
}

func onboardingHandler(t *testing.T, requestContext RequestContext, onboarding Onboarding) http.Handler {
	t.Helper()
	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{}, Actions: &recordingActions{}, Onboarding: onboarding,
		MapContext: func(*http.Request) (RequestContext, error) { return requestContext, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestInviteMailFailureExplainsSafeReinvite(t *testing.T) {
	onboarding := &recordingOnboarding{err: ErrInvitationMailDelivery}
	ctx := RequestContext{UserID: "admin", TenantID: "tenant-1", Role: "tenant_admin", CSRFToken: "token"}
	response := serveHandler(t, onboardingHandler(t, ctx, onboarding), http.MethodPost, "/settings/invitations", "_csrf=token&name=담당자&email=user%40example.com&role=member")
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "같은 이메일로 다시 초대") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestInvitationBootstrapMovesFragmentTokenIntoPostBody(t *testing.T) {
	body := serve(t, http.MethodGet, "/assets/app.js").Body.String()
	for _, want := range []string{"data-invite-token-form", "window.location.hash", "history.replaceState", "requestSubmit"} {
		if !strings.Contains(body, want) {
			t.Fatalf("invitation bootstrap script missing %q", want)
		}
	}
	for _, want := range []string{"data-copy-invitation", "navigator.clipboard.writeText", ".select()"} {
		if !strings.Contains(body, want) {
			t.Fatalf("invitation copy fallback script missing %q", want)
		}
	}
}

func assertInvitationLinkResponse(t *testing.T, response *httptest.ResponseRecorder, link string) {
	t.Helper()
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("status=%d sensitive headers=%v body=%q", response.Code, response.Header(), body)
	}
	if strings.Count(body, link) != 1 || !strings.Contains(body, "readonly") || !strings.Contains(body, "data-copy-invitation") {
		t.Fatalf("one-time readonly invitation link missing or repeated: %s", body)
	}
	if response.Header().Get("Location") != "" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invitation escaped through redirect or cookie: %v", response.Header())
	}
	rawToken := strings.TrimPrefix(link, "https://monitor.example/accept-invite#token=")
	for name, values := range response.Header() {
		if strings.Contains(strings.Join(values, "\n"), rawToken) {
			t.Fatalf("raw invitation token leaked through %s header", name)
		}
	}
}
