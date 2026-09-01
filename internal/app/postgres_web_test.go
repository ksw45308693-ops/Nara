package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"g2b-monitor/internal/matcher"
	"g2b-monitor/internal/model"
	appweb "g2b-monitor/internal/web"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type toggleExecCall struct {
	query string
	args  []any
}

type toggleExecStub struct {
	calls []toggleExecCall
	rows  []int64
}

type saveFilterRow struct {
	id                    string
	rulesChanged, enabled bool
	err                   error
}

func (r saveFilterRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.id
	if len(dest) > 1 {
		*(dest[1].(*bool)) = r.rulesChanged
		*(dest[2].(*bool)) = r.enabled
	}
	return nil
}

type saveFilterStub struct {
	toggleExecStub
	rowCalls []toggleExecCall
	rows     []saveFilterRow
}

func (s *saveFilterStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	s.rowCalls = append(s.rowCalls, toggleExecCall{query: query, args: args})
	if len(s.rows) == 0 {
		return saveFilterRow{err: pgx.ErrNoRows}
	}
	row := s.rows[0]
	s.rows = s.rows[1:]
	return row
}

func TestSaveFilterRuleEditDeletesOnlyItsTenantMatches(t *testing.T) {
	stub := &saveFilterStub{rows: []saveFilterRow{{err: pgx.ErrNoRows}, {id: "filter-1", rulesChanged: true, enabled: true}}}
	err := saveFilter(context.Background(), stub, "tenant-a", "감사", []byte(`{"IncludeAny":["신규"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.rowCalls) != 2 || !strings.Contains(stub.rowCalls[0].query, "ON CONFLICT (tenant_id, name) DO NOTHING") || !strings.Contains(stub.rowCalls[1].query, "rules IS DISTINCT FROM $3::jsonb") || !strings.Contains(stub.rowCalls[1].query, "FOR UPDATE") {
		t.Fatalf("insert/lock queries=%#v", stub.rowCalls)
	}
	for _, call := range stub.rowCalls {
		if !strings.Contains(call.query, "tenant_id") || call.args[0] != "tenant-a" {
			t.Fatalf("cross-tenant unsafe insert/lock=%#v", call)
		}
	}
	if len(stub.calls) != 2 || !strings.Contains(stub.calls[0].query, "UPDATE public.filters") || !strings.Contains(stub.calls[1].query, "DELETE FROM public.matches") {
		t.Fatalf("rule edit writes=%#v", stub.calls)
	}
	for _, call := range stub.calls {
		if !strings.Contains(call.query, "tenant_id=$1::uuid") || call.args[0] != "tenant-a" || call.args[1] != "filter-1" {
			t.Fatalf("cross-tenant unsafe write=%#v", call)
		}
	}
}

func TestSaveFilterNewAndIdenticalRulesPreserveMatches(t *testing.T) {
	tests := []struct {
		name      string
		rows      []saveFilterRow
		wantExecs int
	}{
		{name: "new", rows: []saveFilterRow{{id: "new-filter"}}, wantExecs: 0},
		{name: "identical", rows: []saveFilterRow{{err: pgx.ErrNoRows}, {id: "filter-1", enabled: true}}, wantExecs: 0},
		{name: "reenable", rows: []saveFilterRow{{err: pgx.ErrNoRows}, {id: "filter-1", enabled: false}}, wantExecs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &saveFilterStub{rows: tt.rows}
			if err := saveFilter(context.Background(), stub, "tenant-a", "감사", []byte(`{"IncludeAny":["감사"]}`)); err != nil {
				t.Fatal(err)
			}
			if len(stub.calls) != tt.wantExecs {
				t.Fatalf("writes=%#v, want %d", stub.calls, tt.wantExecs)
			}
			for _, call := range stub.calls {
				if strings.Contains(call.query, "DELETE FROM public.matches") {
					t.Fatalf("%s save deleted matches: %s", tt.name, call.query)
				}
			}
		})
	}
}

func (s *toggleExecStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	s.calls = append(s.calls, toggleExecCall{query: query, args: args})
	rows := int64(1)
	if len(s.rows) > 0 {
		rows, s.rows = s.rows[0], s.rows[1:]
	}
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", rows)), nil
}

func TestToggleFilterDisableDeletesOnlySameTenantMatches(t *testing.T) {
	stub := &toggleExecStub{rows: []int64{1, 3}}
	err := toggleFilter(context.Background(), stub, "tenant-a", appweb.ToggleFilterCommand{FilterID: "filter-1", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("calls=%#v", stub.calls)
	}
	if !strings.Contains(stub.calls[0].query, "UPDATE public.filters") || !strings.Contains(stub.calls[0].query, "tenant_id=$1::uuid AND id=$2::uuid") {
		t.Fatalf("tenant-scoped toggle query=%s", stub.calls[0].query)
	}
	if !strings.Contains(stub.calls[1].query, "DELETE FROM public.matches") || !strings.Contains(stub.calls[1].query, "tenant_id=$1::uuid AND filter_id=$2::uuid") {
		t.Fatalf("tenant-scoped match deletion query=%s", stub.calls[1].query)
	}
	for _, call := range stub.calls {
		if call.args[0] != "tenant-a" || call.args[1] != "filter-1" {
			t.Fatalf("cross-tenant unsafe args=%#v", call.args)
		}
	}
}

func TestToggleFilterEnablePreservesMatchesUntilCollectorRematches(t *testing.T) {
	stub := &toggleExecStub{rows: []int64{1}}
	if err := toggleFilter(context.Background(), stub, "tenant-a", appweb.ToggleFilterCommand{FilterID: "filter-1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("enable unexpectedly deleted matches: %#v", stub.calls)
	}
}

func TestTenantNoticeQueryRequiresEnabledFilter(t *testing.T) {
	for _, want := range []string{"JOIN public.filters f", "f.tenant_id=m.tenant_id", "f.id=m.filter_id", "f.enabled"} {
		if !strings.Contains(tenantNoticesSQL, want) {
			t.Fatalf("tenant notice query missing %q: %s", want, tenantNoticesSQL)
		}
	}
}

type failureCountQueryStub struct {
	query string
	args  []any
}

func (s *failureCountQueryStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	s.query = query
	s.args = args
	return failureCountRow(4)
}

type failureCountRow int

func (r failureCountRow) Scan(dest ...any) error {
	*(dest[0].(*int)) = int(r)
	return nil
}

func TestLoadTenantFailureCountIncludesJobsAndDeliveries(t *testing.T) {
	stub := &failureCountQueryStub{}
	count, err := loadTenantFailureCount(context.Background(), stub, "tenant-1")
	if err != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	for _, want := range []string{"public.job_runs", "public.deliveries", "status='failed'", "interval '24 hours'"} {
		if !strings.Contains(stub.query, want) {
			t.Fatalf("failure query missing %q: %s", want, stub.query)
		}
	}
	if !strings.Contains(stub.query, "claimed_at >= now()-interval '24 hours'") || strings.Contains(stub.query, "due_at >= now()-interval '24 hours'") {
		t.Fatalf("delivery failures must use observed claim time, not old due time: %s", stub.query)
	}
	if !strings.Contains(stub.query, "position($2 in COALESCE(last_error,'')) = 0") || len(stub.args) != 2 || stub.args[1] != expiredDigestTerminalReason {
		t.Fatalf("expected expired-delivery cancellation exclusion: query=%s args=%#v", stub.query, stub.args)
	}
}

func TestApplyTenantFailuresMarksPlatformHealthForDigestJobErrors(t *testing.T) {
	data := appweb.AppData{Admin: appweb.AdminView{Healthy: true}}
	view := appweb.TenantView{State: "정상"}
	applyTenantFailures(&data, &view, 2)
	if data.Admin.Healthy || data.Admin.FailedJobs != 2 || view.State != "점검" {
		t.Fatalf("admin=%+v tenant=%+v", data.Admin, view)
	}
}

func TestWebServiceAllowsOnlyKnownAnonymousPagesWithoutPrincipal(t *testing.T) {
	service := &WebService{}
	for _, path := range []string{"/login", "/accept-invite"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if _, err := service.MapRequest(request); err != nil {
			t.Fatalf("%s anonymous mapping error = %v", path, err)
		}
	}
	if _, err := service.MapRequest(httptest.NewRequest(http.MethodGet, "/dashboard", nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("protected anonymous mapping error = %v", err)
	}
}

func TestFilterRuleFromWebCommandSupportsAnyAndAll(t *testing.T) {
	amount := int64(50_000_000)
	command := appweb.FilterCommand{
		IncludeKeywords: "회계, 감사", IncludeMode: "all", ExcludeKeywords: "상주, 파견",
		Category: "용역", Region: "서울", MinimumAmount: &amount, DeadlineDays: 7, Agency: "공공기관",
	}
	rule := filterRuleFromWebCommand(command)
	if !reflect.DeepEqual(rule.IncludeAll, []string{"회계", "감사"}) || len(rule.IncludeAny) != 0 {
		t.Fatalf("include rule = %+v", rule)
	}
	if !reflect.DeepEqual(rule.Exclude, []string{"상주", "파견"}) ||
		!reflect.DeepEqual(rule.Categories, []model.Category{model.CategoryService}) ||
		!reflect.DeepEqual(rule.Regions, []string{"서울"}) ||
		!reflect.DeepEqual(rule.Agencies, []string{"공공기관"}) ||
		rule.MinAmount == nil || *rule.MinAmount != amount || rule.DeadlineWithinDays == nil || *rule.DeadlineWithinDays != 7 {
		t.Fatalf("rule = %+v", rule)
	}

	command.IncludeMode = "any"
	rule = filterRuleFromWebCommand(command)
	if !reflect.DeepEqual(rule.IncludeAny, []string{"회계", "감사"}) || len(rule.IncludeAll) != 0 {
		t.Fatalf("ANY rule = %+v", rule)
	}
}

func TestWebLabelsExposeMatchReasonAndKoreanCategory(t *testing.T) {
	detail := matcher.Detail{Code: matcher.ReasonRegion, RuleValue: "서울", NoticeValue: "서울특별시"}
	if got := reasonText(detail); got != "지역 ‘서울’ 일치 (공고: 서울특별시)" {
		t.Fatalf("reason = %q", got)
	}
	if got := categoryLabel(model.CategoryConstruction); got != "공사" {
		t.Fatalf("category = %q", got)
	}
	when := time.Date(2026, 9, 1, 7, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	if got := formatKoreanTime(when); got != "2026.09.01 07:00" {
		t.Fatalf("time = %q", got)
	}
}

func TestNextDeliveryUsesSelectedSeoulWeekday(t *testing.T) {
	seoul := time.FixedZone("KST", 9*60*60)
	fridayAfterRun := time.Date(2026, 9, 4, 8, 0, 0, 0, seoul)
	next, ok := nextDeliveryAt(fridayAfterRun, 7, 0, []int{1, 3, 5})
	if !ok || next.Weekday() != time.Monday || next.Hour() != 7 || next.Minute() != 0 {
		t.Fatalf("next delivery = %s ok=%t", next, ok)
	}
}
