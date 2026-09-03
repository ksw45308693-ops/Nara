package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func signupPageHandler(t *testing.T, actions Actions, requestContext RequestContext) (http.Handler, *staticBackend) {
	t.Helper()
	backend := &staticBackend{data: AppData{
		Tenants:       []TenantView{{Name: "실제 테넌트", Members: 1, LastDigest: "오늘", State: "정상"}},
		Accounts:      []AccountView{{UserID: "user-pending", Email: "newcomer@example.com", Created: "2026.09.03 09:12"}, {UserID: "user-assigned", Email: "member@example.com", TenantName: "실제 테넌트", Created: "2026.08.28 14:03", Assigned: true}},
		TenantOptions: []TenantOption{{ID: "tenant-real", Name: "실제 테넌트"}},
		Admin:         AdminView{Healthy: true, LastCollected: "오늘 05:40"},
	}}
	handler, err := NewHandlerWithOptions(Options{
		Backend: backend,
		Actions: actions,
		MapContext: func(*http.Request) (RequestContext, error) {
			return requestContext, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, backend
}

func TestSignupPageOffersEmailAndPasswordOnly(t *testing.T) {
	handler, backend := signupPageHandler(t, &recordingActions{}, RequestContext{CSRFToken: "token-123"})

	response := serveHandler(t, handler, http.MethodGet, "/signup", "")

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if backend.calls != 0 {
		t.Fatalf("anonymous signup page loaded tenant data %d times", backend.calls)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	body := response.Body.String()
	for _, want := range []string{
		`method="post" action="/signup"`,
		`name="_csrf" value="token-123"`,
		`name="email"`,
		`name="password"`,
		`name="password_confirm"`,
		`href="/login"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("signup page missing %q", want)
		}
	}
	for _, forbidden := range []string{`name="display_name"`, `name="tenant_name"`, `name="role"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("signup page collects more than email and password: %q", forbidden)
		}
	}
}

func TestLoginPageLinksToSignup(t *testing.T) {
	handler, _ := signupPageHandler(t, &recordingActions{}, RequestContext{CSRFToken: "token-123"})

	response := serveHandler(t, handler, http.MethodGet, "/login", "")

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `href="/signup"`) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestAccountWithoutTenantSeesWaitingScreen(t *testing.T) {
	member := RequestContext{UserID: "user-pending", UserName: "newcomer@example.com", Email: "newcomer@example.com", Role: "member", CSRFToken: "token-123"}
	handler, backend := signupPageHandler(t, &recordingActions{}, member)

	dashboard := serveHandler(t, handler, http.MethodGet, "/dashboard", "")

	if dashboard.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", dashboard.Code, dashboard.Body.String())
	}
	if backend.calls != 0 {
		t.Fatalf("waiting account loaded tenant data %d times", backend.calls)
	}
	body := dashboard.Body.String()
	if !strings.Contains(body, "회사 배정 대기") || !strings.Contains(body, `action="/logout"`) {
		t.Fatalf("waiting screen body=%q", body)
	}
	if strings.Contains(body, `href="/notices"`) {
		t.Fatal("waiting screen exposes tenant navigation")
	}
}

func TestAccountWithoutTenantCannotSubmitTenantCommands(t *testing.T) {
	member := RequestContext{UserID: "user-pending", Email: "newcomer@example.com", Role: "member", CSRFToken: "token-123"}
	actions := &recordingActions{}
	handler, _ := signupPageHandler(t, actions, member)

	response := serveHandler(t, handler, http.MethodPost, "/filters", url.Values{"_csrf": {"token-123"}, "name": {"필터"}}.Encode())

	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", response.Code)
	}
	if actions.saveFilterCalls != 0 {
		t.Fatalf("filter command ran %d times for an unassigned account", actions.saveFilterCalls)
	}
}

func TestAdminPageAssignsAndRevokesMemberTenant(t *testing.T) {
	admin := RequestContext{UserID: "user-admin", UserName: "관리자", Role: "platform_admin", CSRFToken: "token-123"}
	actions := &recordingActions{}
	handler, _ := signupPageHandler(t, actions, admin)

	page := serveHandler(t, handler, http.MethodGet, "/admin", "")
	if page.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", page.Code, page.Body.String())
	}
	for _, want := range []string{
		`action="/admin/accounts"`,
		`name="user_id" value="user-pending"`,
		`<option value="tenant-real">실제 테넌트</option>`,
		"미배정",
		`name="mode" value="revoke"`,
	} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("admin page missing %q", want)
		}
	}

	assign := serveHandler(t, handler, http.MethodPost, "/admin/accounts",
		url.Values{"_csrf": {"token-123"}, "user_id": {"user-pending"}, "tenant_id": {"tenant-real"}}.Encode())
	if assign.Code != http.StatusSeeOther || assign.Header().Get("Location") != "/admin?result=account-assigned" {
		t.Fatalf("assign status=%d location=%q", assign.Code, assign.Header().Get("Location"))
	}
	if actions.lastAssign != (AssignAccountCommand{UserID: "user-pending", TenantID: "tenant-real"}) {
		t.Fatalf("assign command = %+v", actions.lastAssign)
	}

	revoke := serveHandler(t, handler, http.MethodPost, "/admin/accounts",
		url.Values{"_csrf": {"token-123"}, "user_id": {"user-assigned"}, "tenant_id": {"tenant-real"}, "mode": {"revoke"}}.Encode())
	if revoke.Code != http.StatusSeeOther || revoke.Header().Get("Location") != "/admin?result=account-revoked" {
		t.Fatalf("revoke status=%d location=%q", revoke.Code, revoke.Header().Get("Location"))
	}
	if actions.lastAssign != (AssignAccountCommand{UserID: "user-assigned"}) {
		t.Fatalf("revoke command = %+v", actions.lastAssign)
	}
	if actions.assignCalls != 2 {
		t.Fatalf("assignCalls=%d", actions.assignCalls)
	}
}

func TestAccountAssignmentRejectsNonPlatformAdminAndBadRequest(t *testing.T) {
	actions := &recordingActions{}
	tenantAdmin, _ := signupPageHandler(t, actions, RequestContext{UserID: "user-admin", TenantID: "tenant-real", Role: "tenant_admin", CSRFToken: "token-123"})

	forbidden := serveHandler(t, tenantAdmin, http.MethodPost, "/admin/accounts",
		url.Values{"_csrf": {"token-123"}, "user_id": {"user-pending"}, "tenant_id": {"tenant-real"}}.Encode())
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("tenant admin status=%d, want 403", forbidden.Code)
	}

	admin, _ := signupPageHandler(t, actions, RequestContext{UserID: "user-admin", Role: "platform_admin", CSRFToken: "token-123"})
	missingCSRF := serveHandler(t, admin, http.MethodPost, "/admin/accounts",
		url.Values{"user_id": {"user-pending"}, "tenant_id": {"tenant-real"}}.Encode())
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d, want 403", missingCSRF.Code)
	}
	missingUser := serveHandler(t, admin, http.MethodPost, "/admin/accounts",
		url.Values{"_csrf": {"token-123"}, "tenant_id": {"tenant-real"}}.Encode())
	if missingUser.Code != http.StatusBadRequest {
		t.Fatalf("missing account status=%d, want 400", missingUser.Code)
	}
	if actions.assignCalls != 0 {
		t.Fatalf("assignCalls=%d for rejected requests", actions.assignCalls)
	}
}
