package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPagesRenderExpectedLandmarks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path       string
		wantTitle  string
		wantMarker string
	}{
		{"/login", "로그인", `<main class="login-main"`},
		{"/dashboard", "입찰공고 모니터링", `aria-label="모니터링 자동화 흐름"`},
		{"/notices", "공고 목록", `aria-current="page"`},
		{"/notices/2026-sample-001", "공고 상세", `aria-label="선정 사유"`},
		{"/filters", "필터 관리", `name="include_keywords"`},
		{"/notifications", "알림 설정", `name="delivery_time"`},
		{"/settings", "환경 설정", `name="tenant_name"`},
		{"/admin", "플랫폼 관리", `aria-label="수집 상태"`},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			response := serve(t, http.MethodGet, tt.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			body := response.Body.String()
			for _, want := range []string{"<!doctype html>", `lang="ko"`, "<title>" + tt.wantTitle, tt.wantMarker} {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q", want)
				}
			}
		})
	}
}

func TestDashboardRendersApprovedProcessFlow(t *testing.T) {
	t.Parallel()

	response := serve(t, http.MethodGet, "/dashboard")
	body := response.Body.String()
	for _, want := range []string{
		"나라장터 공고", "키워드 필터", "신규 공고 요약", "메일 송부",
		"매일 07:00 자동 실행", "준비 중", "공고 → 양식 자동 추천", "관련 업무이력 자동 리스트업",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if !strings.Contains(body, `<ol class="process-flow" aria-label="모니터링 자동화 흐름">`) {
		t.Error("process flow is not exposed as an ordered list")
	}
	last := -1
	for _, stage := range []string{"나라장터 공고", "키워드 필터", "신규 공고 요약", "메일 송부"} {
		index := strings.Index(body, stage)
		if index <= last {
			t.Fatalf("process stage %q is out of order", stage)
		}
		last = index
	}
}

func TestMobileDrawerControlComesBeforeNavigation(t *testing.T) {
	t.Parallel()

	body := serve(t, http.MethodGet, "/dashboard").Body.String()
	button := strings.Index(body, `data-nav-toggle`)
	nav := strings.Index(body, `id="primary-nav"`)
	if button < 0 || nav < 0 || button >= nav {
		t.Fatalf("mobile menu button index = %d, navigation index = %d; button must come first", button, nav)
	}
	for _, want := range []string{`aria-expanded="false"`, `aria-controls="primary-nav"`, `data-drawer`} {
		if !strings.Contains(body, want) {
			t.Errorf("drawer markup missing %q", want)
		}
	}
}

func TestNoticeStatesExplainRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{"/notices?state=empty", "필터를 조정하거나 수집 상태를 확인해 주세요."},
		{"/notices?state=error", "공고를 불러오지 못했습니다."},
		{"/notices?state=loading", `aria-busy="true"`},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if body := serve(t, http.MethodGet, tt.path).Body.String(); !strings.Contains(body, tt.want) {
				t.Errorf("body missing %q", tt.want)
			}
		})
	}
}

func TestEmbeddedAssetsAreServed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path        string
		contentType string
		marker      string
	}{
		{"/assets/app.css", "text/css", "--navy"},
		{"/assets/app.js", "text/javascript", "data-nav-toggle"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			response := serve(t, http.MethodGet, tt.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Content-Type"); !strings.Contains(got, tt.contentType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tt.contentType)
			}
			if body := response.Body.String(); !strings.Contains(body, tt.marker) {
				t.Errorf("asset missing %q", tt.marker)
			}
		})
	}
}

func TestUnknownRouteAndUnsupportedMethod(t *testing.T) {
	t.Parallel()

	if got := serve(t, http.MethodGet, "/missing").Code; got != http.StatusNotFound {
		t.Errorf("unknown route status = %d, want %d", got, http.StatusNotFound)
	}
	if got := serve(t, http.MethodDelete, "/dashboard").Code; got != http.StatusMethodNotAllowed {
		t.Errorf("DELETE status = %d, want %d", got, http.StatusMethodNotAllowed)
	}
}

func TestMethodNotAllowedReportsRouteMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		path   string
		allow  string
	}{
		{http.MethodPost, "/dashboard", "GET, HEAD"},
		{http.MethodDelete, "/login", "GET, HEAD"},
		{http.MethodGet, "/filters/toggle", "POST"},
		{http.MethodPatch, "/assets/app.css", "GET, HEAD"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()
			response := serve(t, tt.method, tt.path)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
			}
			if got := response.Header().Get("Allow"); got != tt.allow {
				t.Errorf("Allow = %q, want %q", got, tt.allow)
			}
		})
	}
}

func TestNoticesFilterSampleRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		query   string
		present string
		absent  string
	}{
		{"q=" + url.QueryEscape("정보시스템"), "2026-sample-002", "2026-sample-001"},
		{"category=" + url.QueryEscape("물품"), "2026-sample-003", "2026-sample-002"},
		{"region=" + url.QueryEscape("서울"), "2026-sample-002", "2026-sample-003"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			body := serve(t, http.MethodGet, "/notices?"+tt.query).Body.String()
			if !strings.Contains(body, tt.present) {
				t.Errorf("filtered notices missing %q", tt.present)
			}
			if strings.Contains(body, tt.absent) {
				t.Errorf("filtered notices unexpectedly contain %q", tt.absent)
			}
		})
	}
}

func TestNoticeSearchValueIsEscaped(t *testing.T) {
	t.Parallel()

	body := serve(t, http.MethodGet, "/notices?q=%3Cscript%3Ealert(1)%3C%2Fscript%3E").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("search query rendered as executable markup")
	}
	if !strings.Contains(body, `value="&lt;script&gt;alert(1)&lt;/script&gt;"`) {
		t.Error("escaped search value is not preserved in the control")
	}
}

func TestInvalidNoticeIDsReturnNotFound(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/notices/unknown", "/notices/2026-sample-001/extra", "/notices//"} {
		if got := serve(t, http.MethodGet, path).Code; got != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, got, http.StatusNotFound)
		}
	}
	body := serve(t, http.MethodGet, "/notices/2026-sample-002").Body.String()
	if !strings.Contains(body, "샘플: 정보시스템 운영 지원") {
		t.Error("valid notice ID did not render the matching notice")
	}
}

func TestInjectedFilterToggleCallsAction(t *testing.T) {
	t.Parallel()

	actions := &recordingActions{}
	handler := productionHandler(t, actions)
	request := httptest.NewRequest(http.MethodPost, "/filters/toggle", strings.NewReader("_csrf=token-123&filter=3&enabled=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if got := response.Header().Get("Location"); got != "/filters?saved=1" {
		t.Fatalf("Location = %q", got)
	}
	if actions.toggleCalls != 1 || actions.lastToggle.FilterID != "3" || !actions.lastToggle.Enabled {
		t.Errorf("toggle action = %#v, calls = %d", actions.lastToggle, actions.toggleCalls)
	}
}

func TestSettingsTableKeepsMobileLabels(t *testing.T) {
	t.Parallel()

	body := serve(t, http.MethodGet, "/settings").Body.String()
	for _, want := range []string{`<th scope="col">이름</th>`, `data-label="이름"`, `data-label="이메일"`, `data-label="역할"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings table missing %q", want)
		}
	}
}

func TestUnavailableControlsExplainWhyTheyAreDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		ids  []string
	}{
		{"/notices/2026-sample-001", []string{"detail-original-note"}},
		{"/notifications", []string{"recipient-integration-note"}},
		{"/settings", []string{"member-integration-note", "session-note"}},
		{"/admin", []string{"admin-integration-note"}},
	}
	for _, tt := range tests {
		body := serve(t, http.MethodGet, tt.path).Body.String()
		for _, id := range tt.ids {
			if !strings.Contains(body, `aria-describedby="`+id+`"`) || !strings.Contains(body, `id="`+id+`"`) {
				t.Errorf("%s does not connect disabled control to %q", tt.path, id)
			}
		}
	}
}

func TestViewHelpersHandleEmptyAndUnicodeValues(t *testing.T) {
	t.Parallel()

	if got := firstReason(nil); got != "" {
		t.Errorf("firstReason(nil) = %q, want empty", got)
	}
	if got := initial("김담당"); got != "김" {
		t.Errorf("initial(김담당) = %q, want 김", got)
	}
	if got := initial(""); got != "" {
		t.Errorf("initial(empty) = %q, want empty", got)
	}
}

func serve(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)
	return response
}

func TestLoginRedirectsToDashboard(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("email=demo%40example.com&password=demo"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)

	result := response.Result()
	defer result.Body.Close()
	_, _ = io.Copy(io.Discard, result.Body)
	if result.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", result.StatusCode, http.StatusNotImplemented)
	}
	if body := response.Body.String(); !strings.Contains(body, "읽기 전용 데모") {
		t.Errorf("body missing read-only explanation: %q", body)
	}
}

func TestDemoMutationDoesNotClaimPersistence(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/filters", strings.NewReader("name=새+필터"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Errorf("demo mutation redirected to fake success %q", location)
	}
	if body := serve(t, http.MethodGet, "/filters").Body.String(); !strings.Contains(body, `disabled aria-describedby="demo-readonly-note"`) {
		t.Error("demo save controls are not visibly disabled")
	}
}

func TestInjectedContextAndBackendMapToPages(t *testing.T) {
	t.Parallel()

	handler := productionHandler(t, &recordingActions{})
	tests := []struct {
		path string
		want []string
	}{
		{"/dashboard", []string{"실사용자", "실테넌트", "오늘 05:55", "9건"}},
		{"/notices", []string{"실제 연동 공고", "실제 기관"}},
		{"/filters", []string{"실제 필터"}},
		{"/notifications", []string{"real@example.com", `value="08:15"`, "Asia/Seoul", "9건", `value="1" checked`, `value="5" checked`}},
		{"/settings", []string{"실제 구성원", `value="contact@real.example"`}},
		{"/admin", []string{"실제 테넌트", "오늘 05:40", "2,468건", "3건"}},
	}
	for _, tt := range tests {
		response := serveHandler(t, handler, http.MethodGet, tt.path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", tt.path, response.Code)
		}
		for _, want := range tt.want {
			if !strings.Contains(response.Body.String(), want) {
				t.Errorf("GET %s missing %q", tt.path, want)
			}
		}
		if strings.Contains(response.Body.String(), "샘플 화면") || strings.Contains(response.Body.String(), "샘플 데이터") {
			t.Errorf("GET %s labels production data as sample", tt.path)
		}
	}
}

func TestProductionNoticeUsesSafeSourceURL(t *testing.T) {
	t.Parallel()

	body := serveHandler(t, productionHandler(t, &recordingActions{}), http.MethodGet, "/notices/real-001", "").Body.String()
	for _, want := range []string{`href="https://www.g2b.go.kr/sample/real-001"`, `rel="noopener noreferrer"`, "나라장터 원문 보기"} {
		if !strings.Contains(body, want) {
			t.Errorf("notice detail missing %q", want)
		}
	}
}

func TestProductionMutationFormsContainMappedCSRF(t *testing.T) {
	t.Parallel()

	handler := productionHandler(t, &recordingActions{})
	for _, path := range []string{"/filters", "/notifications", "/settings"} {
		body := serveHandler(t, handler, http.MethodGet, path, "").Body.String()
		if !strings.Contains(body, `type="hidden" name="_csrf" value="token-123"`) {
			t.Errorf("GET %s missing mapped CSRF field", path)
		}
	}
}

func TestSaveFilterValidatesCSRFAndCommandBeforeAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		form      string
		wantCode  int
		wantCalls int
	}{
		{"valid", "_csrf=token-123&name=감사+용역&include_keywords=감사&include_mode=all&deadline_days=3", http.StatusSeeOther, 1},
		{"busan and gyeongnam", "_csrf=token-123&name=부산+공사&deadline_days=3&region=부산%2C+경남", http.StatusSeeOther, 1},
		{"region too long", "_csrf=token-123&name=지역&deadline_days=3&region=" + strings.Repeat("가", 129), http.StatusBadRequest, 0},
		{"default all", "_csrf=token-123&name=감사+용역&include_keywords=감사&deadline_days=3&category=&region=&min_amount=", http.StatusSeeOther, 1},
		{"bad csrf", "_csrf=wrong&name=감사+용역&deadline_days=3", http.StatusForbidden, 0},
		{"missing name", "_csrf=token-123&deadline_days=3", http.StatusBadRequest, 0},
		{"invalid deadline", "_csrf=token-123&name=감사+용역&deadline_days=-1", http.StatusBadRequest, 0},
		{"invalid category", "_csrf=token-123&name=감사+용역&deadline_days=3&category=임의값", http.StatusBadRequest, 0},
		{"invalid include mode", "_csrf=token-123&name=감사+용역&deadline_days=3&include_mode=임의값", http.StatusBadRequest, 0},
		{"malformed amount", "_csrf=token-123&name=감사+용역&deadline_days=3&min_amount=만원", http.StatusBadRequest, 0},
		{"negative amount", "_csrf=token-123&name=감사+용역&deadline_days=3&min_amount=-1", http.StatusBadRequest, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := &recordingActions{}
			handler := productionHandler(t, actions)
			response := serveHandler(t, handler, http.MethodPost, "/filters", tt.form)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantCode, response.Body.String())
			}
			if actions.saveFilterCalls != tt.wantCalls {
				t.Errorf("SaveFilter calls = %d, want %d", actions.saveFilterCalls, tt.wantCalls)
			}
			if tt.name == "default all" && (actions.lastFilter.Category != "" || actions.lastFilter.Region != "" || actions.lastFilter.MinimumAmount != nil) {
				t.Errorf("default all command = %#v", actions.lastFilter)
			}
			if tt.name == "valid" && actions.lastFilter.IncludeMode != "all" {
				t.Errorf("include mode = %q, want all", actions.lastFilter.IncludeMode)
			}
		})
	}
}

func TestFilterAllOptionsHaveEmptyValues(t *testing.T) {
	t.Parallel()

	body := serve(t, http.MethodGet, "/filters").Body.String()
	if strings.Count(body, `<option value="">전체</option>`) != 1 || !strings.Contains(body, `name="region" maxlength="128"`) {
		t.Errorf("filter category default or free-text region input is missing")
	}
}

func TestNoticeRegionSearchAcceptsCommaSeparatedFreeText(t *testing.T) {
	notices := []noticeView{
		{ID: "busan", Region: "부산광역시"},
		{ID: "seoul", Region: "서울특별시"},
	}
	got := filterNotices(notices, "", "", "경남, 부산")
	if len(got) != 1 || got[0].ID != "busan" {
		t.Fatalf("filtered notices=%+v", got)
	}
}

func TestInjectedRoleRestrictsPlatformAdmin(t *testing.T) {
	t.Parallel()

	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{data: AppData{}},
		Actions: &recordingActions{},
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{UserName: "담당자", TenantName: "일반 테넌트", TenantID: "tenant-1", Role: "member", CSRFToken: "token-123"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if body := serveHandler(t, handler, http.MethodGet, "/dashboard", "").Body.String(); strings.Contains(body, `href="/admin"`) {
		t.Error("member dashboard exposes platform admin navigation")
	}
	if got := serveHandler(t, handler, http.MethodGet, "/admin", "").Code; got != http.StatusForbidden {
		t.Errorf("member GET /admin status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestAuthenticatedShellIncludesLogoutForm(t *testing.T) {
	t.Parallel()

	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{data: AppData{}},
		Actions: &recordingActions{},
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{UserName: "담당자", TenantName: "테넌트", TenantID: "tenant-1", Role: "member", CSRFToken: "logout-token"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := serveHandler(t, handler, http.MethodGet, "/dashboard", "").Body.String()
	for _, want := range []string{
		`<form class="logout-form" method="post" action="/logout">`,
		`<input type="hidden" name="_csrf" value="logout-token">`,
		`>로그아웃</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("authenticated shell missing %q", want)
		}
	}
}

func TestNonPlatformAdminRejectedBeforeBackendLoad(t *testing.T) {
	t.Parallel()

	backend := &staticBackend{data: AppData{}}
	handler, err := NewHandlerWithOptions(Options{
		Backend: backend,
		Actions: &recordingActions{},
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{Role: "tenant_admin", TenantID: "tenant-1", CSRFToken: "token"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := serveHandler(t, handler, http.MethodGet, "/admin", "").Code; got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
	}
	if backend.calls != 0 {
		t.Errorf("Backend.Load calls = %d, want 0", backend.calls)
	}
}

func TestTenantMutationCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		role      string
		tenantID  string
		wantCode  int
		wantCalls int
	}{
		{"member", "member", "tenant-1", http.StatusForbidden, 0},
		{"tenant admin", "tenant_admin", "tenant-1", http.StatusSeeOther, 1},
		{"platform without tenant", "platform_admin", "", http.StatusForbidden, 0},
		{"platform with tenant", "platform_admin", "tenant-1", http.StatusSeeOther, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := &recordingActions{}
			handler, err := NewHandlerWithOptions(Options{
				Backend: &staticBackend{data: AppData{}},
				Actions: actions,
				MapContext: func(*http.Request) (RequestContext, error) {
					return RequestContext{Role: tt.role, TenantID: tt.tenantID, CSRFToken: "token"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := serveHandler(t, handler, http.MethodPost, "/filters", "_csrf=token&name=필터&deadline_days=3")
			if response.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", response.Code, tt.wantCode)
			}
			if actions.saveFilterCalls != tt.wantCalls {
				t.Errorf("SaveFilter calls = %d, want %d", actions.saveFilterCalls, tt.wantCalls)
			}
		})
	}
}

func TestMemberPagesRenderReadOnlyControls(t *testing.T) {
	t.Parallel()

	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{data: AppData{Filters: []FilterView{{ID: "f", Name: "필터"}}}},
		Actions: &recordingActions{},
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{Role: "member", TenantID: "tenant-1", CSRFToken: "token"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/filters", "/notifications", "/settings"} {
		body := serveHandler(t, handler, http.MethodGet, path, "").Body.String()
		if !strings.Contains(body, "읽기 전용") || !strings.Contains(body, "disabled") {
			t.Errorf("GET %s does not render read-only controls", path)
		}
	}
}

func TestProductionLoginFormIsEnabledForOuterAuth(t *testing.T) {
	t.Parallel()

	body := serveHandler(t, productionHandler(t, &recordingActions{}), http.MethodGet, "/login", "").Body.String()
	if !strings.Contains(body, `<form method="post" action="/login"`) {
		t.Error("production login form does not target outer auth middleware")
	}
	if strings.Contains(body, `id="email" name="email" type="email" autocomplete="username" placeholder="name@company.com" disabled`) {
		t.Error("production login email remains disabled")
	}
	if !strings.Contains(body, `type="hidden" name="_csrf" value="token-123"`) {
		t.Error("production login form missing CSRF field")
	}
}

func TestProductionLoginDoesNotLoadAuthenticatedBackend(t *testing.T) {
	t.Parallel()
	backend := &staticBackend{err: errors.New("authentication required")}
	handler, err := NewHandlerWithOptions(Options{
		Backend: backend,
		Actions: &recordingActions{},
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{CSRFToken: "login-token"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := serveHandler(t, handler, http.MethodGet, "/login", "")
	if response.Code != http.StatusOK || backend.calls != 0 {
		t.Fatalf("status=%d backend calls=%d body=%s", response.Code, backend.calls, response.Body.String())
	}
}

func TestNewHandlerWithOptionsRejectsMissingProductionDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewHandlerWithOptions(Options{}); err == nil {
		t.Fatal("NewHandlerWithOptions accepted empty production dependencies")
	}
}

func TestNotificationAndSettingsActionsValidateMapAndReportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		form        string
		actionErr   error
		wantCode    int
		wantNotify  int
		wantSetting int
	}{
		{"notification valid", "/notifications", "_csrf=token-123&delivery_time=08%3A15&timezone=Asia%2FSeoul&delivery_days=1&delivery_days=5", nil, http.StatusSeeOther, 1, 0},
		{"notification invalid time", "/notifications", "_csrf=token-123&delivery_time=25%3A99&timezone=Asia%2FSeoul&delivery_days=1", nil, http.StatusBadRequest, 0, 0},
		{"notification action error", "/notifications", "_csrf=token-123&delivery_time=08%3A15&timezone=Asia%2FSeoul&delivery_days=1", errors.New("save failed"), http.StatusInternalServerError, 1, 0},
		{"settings valid", "/settings", "_csrf=token-123&tenant_name=실제+테넌트&contact_email=contact%40real.example", nil, http.StatusSeeOther, 0, 1},
		{"settings invalid email", "/settings", "_csrf=token-123&tenant_name=실제+테넌트&contact_email=bad", nil, http.StatusBadRequest, 0, 0},
		{"settings action error", "/settings", "_csrf=token-123&tenant_name=실제+테넌트&contact_email=contact%40real.example", errors.New("save failed"), http.StatusInternalServerError, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := &recordingActions{err: tt.actionErr}
			response := serveHandler(t, productionHandler(t, actions), http.MethodPost, tt.path, tt.form)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantCode, response.Body.String())
			}
			if actions.notificationCalls != tt.wantNotify || actions.settingsCalls != tt.wantSetting {
				t.Errorf("notification calls=%d settings calls=%d", actions.notificationCalls, actions.settingsCalls)
			}
			if tt.wantNotify == 1 && (actions.lastNotification.DeliveryTime != "08:15" || actions.lastNotification.Timezone != "Asia/Seoul" || len(actions.lastNotification.DeliveryDays) == 0) {
				t.Errorf("notification command = %#v", actions.lastNotification)
			}
			if tt.wantSetting == 1 && actions.lastSettings.ContactEmail != "contact@real.example" {
				t.Errorf("settings command = %#v", actions.lastSettings)
			}
		})
	}
}

func TestNotificationWeekdaysRequireOneValidDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		days      string
		wantCode  int
		wantCalls int
	}{
		{"none", "", http.StatusBadRequest, 0},
		{"below range", "&delivery_days=-1", http.StatusBadRequest, 0},
		{"above range", "&delivery_days=7", http.StatusBadRequest, 0},
		{"valid", "&delivery_days=0&delivery_days=6", http.StatusSeeOther, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := &recordingActions{}
			form := "_csrf=token-123&delivery_time=08%3A15&timezone=Asia%2FSeoul" + tt.days
			response := serveHandler(t, productionHandler(t, actions), http.MethodPost, "/notifications", form)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantCode)
			}
			if actions.notificationCalls != tt.wantCalls {
				t.Errorf("SaveNotification calls = %d, want %d", actions.notificationCalls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && (len(actions.lastNotification.DeliveryDays) != 2 || actions.lastNotification.DeliveryDays[0] != 0 || actions.lastNotification.DeliveryDays[1] != 6) {
				t.Errorf("DeliveryDays = %#v", actions.lastNotification.DeliveryDays)
			}
		})
	}
}

func TestRecipientAddValidatesAndCallsAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		form      string
		actionErr error
		wantCode  int
		wantCalls int
	}{
		{"valid", "_csrf=token-123&name=새+담당자&email=new%40real.example", nil, http.StatusSeeOther, 1},
		{"normalizes case and whitespace", "_csrf=token-123&name=새+담당자&email=++New%40REAL.Example++", nil, http.StatusSeeOther, 1},
		{"missing name", "_csrf=token-123&email=new%40real.example", nil, http.StatusBadRequest, 0},
		{"invalid email", "_csrf=token-123&name=새+담당자&email=bad", nil, http.StatusBadRequest, 0},
		{"bad csrf", "_csrf=wrong&name=새+담당자&email=new%40real.example", nil, http.StatusForbidden, 0},
		{"action error", "_csrf=token-123&name=새+담당자&email=new%40real.example", errors.New("add failed"), http.StatusInternalServerError, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := &recordingActions{err: tt.actionErr}
			response := serveHandler(t, productionHandler(t, actions), http.MethodPost, "/notifications/recipients", tt.form)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantCode, response.Body.String())
			}
			if actions.recipientCalls != tt.wantCalls {
				t.Errorf("AddRecipient calls = %d, want %d", actions.recipientCalls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && actions.lastRecipient.Email != "new@real.example" {
				t.Errorf("recipient command = %#v", actions.lastRecipient)
			}
		})
	}
}

func TestRecipientAddRespectsDemoAndMemberPermissions(t *testing.T) {
	t.Parallel()

	form := "_csrf=token&name=담당자&email=user%40example.com"
	if got := serveHandler(t, NewHandler(), http.MethodPost, "/notifications/recipients", form).Code; got != http.StatusNotImplemented {
		t.Errorf("demo status = %d, want %d", got, http.StatusNotImplemented)
	}
	actions := &recordingActions{}
	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{}, Actions: actions,
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{Role: "member", TenantID: "tenant-1", CSRFToken: "token"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := serveHandler(t, handler, http.MethodPost, "/notifications/recipients", form).Code; got != http.StatusForbidden {
		t.Errorf("member status = %d, want %d", got, http.StatusForbidden)
	}
	if actions.recipientCalls != 0 {
		t.Errorf("member AddRecipient calls = %d", actions.recipientCalls)
	}
}

func TestPlatformActionsValidateCSRFPermissionsAndShowResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		csrf        string
		actionErr   error
		wantCode    int
		wantCollect int
		wantMail    int
		wantResult  string
	}{
		{"collect", "/admin/collect", "token-123", nil, http.StatusSeeOther, 1, 0, "수집 작업을 시작했습니다."},
		{"test mail", "/admin/test-mail", "token-123", nil, http.StatusSeeOther, 0, 1, "테스트 메일을 발송했습니다."},
		{"bad csrf", "/admin/collect", "wrong", nil, http.StatusForbidden, 0, 0, ""},
		{"action error", "/admin/collect", "token-123", errors.New("run failed"), http.StatusInternalServerError, 1, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := &recordingActions{err: tt.actionErr}
			handler := productionHandler(t, actions)
			response := serveHandler(t, handler, http.MethodPost, tt.path, "_csrf="+tt.csrf)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantCode)
			}
			if actions.collectCalls != tt.wantCollect || actions.testMailCalls != tt.wantMail {
				t.Errorf("collect=%d test-mail=%d", actions.collectCalls, actions.testMailCalls)
			}
			if tt.wantResult != "" {
				location := response.Header().Get("Location")
				body := serveHandler(t, handler, http.MethodGet, location, "").Body.String()
				if !strings.Contains(body, tt.wantResult) {
					t.Errorf("result page missing %q", tt.wantResult)
				}
			}
		})
	}
}

func TestPlatformActionsDisabledForDemoAndForbiddenForMember(t *testing.T) {
	t.Parallel()

	if got := serveHandler(t, NewHandler(), http.MethodPost, "/admin/collect", "_csrf=x").Code; got != http.StatusNotImplemented {
		t.Errorf("demo status = %d, want %d", got, http.StatusNotImplemented)
	}
	actions := &recordingActions{}
	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{}, Actions: actions,
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{Role: "member", TenantID: "tenant-1", CSRFToken: "token"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := serveHandler(t, handler, http.MethodPost, "/admin/test-mail", "_csrf=token").Code; got != http.StatusForbidden {
		t.Errorf("member status = %d, want %d", got, http.StatusForbidden)
	}
	if actions.collectCalls != 0 || actions.testMailCalls != 0 {
		t.Error("forbidden platform action was called")
	}
}

type staticBackend struct {
	data     AppData
	err      error
	calls    int
	lastPage PageRequest
}

func (b *staticBackend) Load(_ context.Context, _ RequestContext, page PageRequest) (AppData, error) {
	b.calls++
	b.lastPage = page
	return b.data, b.err
}

type recordingActions struct {
	saveFilterCalls   int
	toggleCalls       int
	lastFilter        FilterCommand
	lastToggle        ToggleFilterCommand
	notificationCalls int
	settingsCalls     int
	lastNotification  NotificationCommand
	lastSettings      SettingsCommand
	recipientCalls    int
	lastRecipient     RecipientCommand
	collectCalls      int
	testMailCalls     int
	err               error
}

func (a *recordingActions) SaveFilter(_ context.Context, _ RequestContext, command FilterCommand) error {
	a.saveFilterCalls++
	a.lastFilter = command
	return a.err
}

func (a *recordingActions) ToggleFilter(_ context.Context, _ RequestContext, command ToggleFilterCommand) error {
	a.toggleCalls++
	a.lastToggle = command
	return a.err
}

func (a *recordingActions) SaveNotification(_ context.Context, _ RequestContext, command NotificationCommand) error {
	a.notificationCalls++
	a.lastNotification = command
	return a.err
}

func (a *recordingActions) SaveSettings(_ context.Context, _ RequestContext, command SettingsCommand) error {
	a.settingsCalls++
	a.lastSettings = command
	return a.err
}

func (a *recordingActions) AddRecipient(_ context.Context, _ RequestContext, command RecipientCommand) error {
	a.recipientCalls++
	a.lastRecipient = command
	return a.err
}

func (a *recordingActions) RunCollection(context.Context, RequestContext) error {
	a.collectCalls++
	return a.err
}

func (a *recordingActions) SendTestMail(context.Context, RequestContext) error {
	a.testMailCalls++
	return a.err
}

func productionHandler(t *testing.T, actions Actions) http.Handler {
	t.Helper()
	handler, err := NewHandlerWithOptions(Options{
		Backend: &staticBackend{data: AppData{
			Dashboard:    DashboardView{LastCollected: "오늘 05:55", NewNotices: 18, Matches: 9, RunTime: "06:30", Healthy: true},
			Notices:      []NoticeView{{ID: "real-001", Title: "실제 연동 공고", Category: "용역", Agency: "실제 기관", Region: "서울", Amount: "1원", Deadline: "2026.09.10", SourceURL: "https://www.g2b.go.kr/sample/real-001", Reasons: []string{"실제 조건"}}},
			Filters:      []FilterView{{ID: "real-filter", Name: "실제 필터", Summary: "실제 조건", Matches: 1, Enabled: true}},
			Recipients:   []RecipientView{{Name: "실제 수신자", Email: "real@example.com", State: "수신"}},
			Members:      []MemberView{{Name: "실제 구성원", Email: "member@example.com", Role: "담당자"}},
			Tenants:      []TenantView{{Name: "실제 테넌트", Members: 1, LastDigest: "오늘", State: "정상"}},
			DeliveryTime: "08:15",
			DeliveryDays: []int{1, 2, 3, 4, 5},
			Timezone:     "Asia/Seoul",
			ContactEmail: "contact@real.example",
			Admin:        AdminView{Healthy: true, LastCollected: "오늘 05:40", CollectedCount: 2468, FailedJobs: 3},
			Demo:         false,
		}},
		Actions: actions,
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{UserName: "실사용자", TenantName: "실테넌트", TenantID: "tenant-real", Role: "platform_admin", CSRFToken: "token-123"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveHandler(t *testing.T, handler http.Handler, method, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(form))
	if form != "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
