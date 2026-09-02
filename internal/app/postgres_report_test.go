package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type reportRowFunc func(...any) error

func (f reportRowFunc) Scan(dest ...any) error { return f(dest...) }

type reportRowsStub struct {
	pgx.Rows
	rows  [][]any
	index int
}

func (r *reportRowsStub) Close()     {}
func (r *reportRowsStub) Err() error { return nil }
func (r *reportRowsStub) Next() bool { return r.index < len(r.rows) }
func (r *reportRowsStub) Scan(dest ...any) error {
	row := r.rows[r.index]
	r.index++
	for index := range dest {
		switch target := dest[index].(type) {
		case *string:
			*target = row[index].(string)
		case *int:
			*target = row[index].(int)
		case *int64:
			*target = row[index].(int64)
		case *time.Time:
			*target = row[index].(time.Time)
		case **time.Time:
			if row[index] == nil {
				*target = nil
			} else {
				value := row[index].(time.Time)
				*target = &value
			}
		case *[]int16:
			*target = row[index].([]int16)
		case *[]byte:
			*target = row[index].([]byte)
		default:
			return errors.New("unsupported report row destination")
		}
	}
	return nil
}

type reportStoreStub struct {
	rowQueries []string
	rowArgs    [][]any
	rowResults []reportRowFunc
	queries    []string
	queryArgs  [][]any
	queryRows  []*reportRowsStub
	execs      []string
	execArgs   [][]any
	execRows   []int64
}

func (s *reportStoreStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	s.rowQueries = append(s.rowQueries, query)
	s.rowArgs = append(s.rowArgs, args)
	if len(s.rowResults) == 0 {
		return reportRowFunc(func(...any) error { return pgx.ErrNoRows })
	}
	result := s.rowResults[0]
	s.rowResults = s.rowResults[1:]
	return result
}

func (s *reportStoreStub) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	s.queries = append(s.queries, query)
	s.queryArgs = append(s.queryArgs, args)
	if len(s.queryRows) == 0 {
		return &reportRowsStub{}, nil
	}
	rows := s.queryRows[0]
	s.queryRows = s.queryRows[1:]
	return rows, nil
}

func (s *reportStoreStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	s.execs = append(s.execs, query)
	s.execArgs = append(s.execArgs, args)
	rows := int64(1)
	if len(s.execRows) > 0 {
		rows, s.execRows = s.execRows[0], s.execRows[1:]
	}
	return pgconn.NewCommandTag("UPDATE " + string(rune('0'+rows))), nil
}

func TestPostgresReportScheduledClaimUsesSnapshotLeaseAndFencing(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-3 * time.Hour)
	stub := &reportStoreStub{
		rowResults: []reportRowFunc{
			func(dest ...any) error { *(dest[0].(*bool)) = true; return nil },
			workRow("report-1", "tenant-1", "테넌트", "schedule-1", "매일", "scheduled", "reports/tenant-1/report-1.html", "token-1", dueAt, nil, now, 1),
		},
		queryRows: []*reportRowsStub{{rows: [][]any{{"notice-1", "공고", "goods", "기관", "서울", int64(100), now.Add(time.Hour), "https://example.test/1", "필터", []byte(`{"reasons":["region"]}`)}}}},
	}
	work, created, err := claimScheduledReport(context.Background(), stub, "tenant-1", "테넌트", reportSchedule{ID: "schedule-1", Name: "매일"}, dueAt, now, now)
	if err != nil || !created || work.ReportID != "report-1" || len(work.Notices) != 1 {
		t.Fatalf("work=%+v created=%t err=%v", work, created, err)
	}
	claimSQL := stub.rowQueries[1]
	for _, want := range []string{"public.reports", "tenant_name", "schedule_name", "trigger", "'scheduled'", "status = 'generating'", "attempts < 3", "interval '15 minutes'", "claim_token", "gen_random_uuid()", "reports/", "ON CONFLICT"} {
		if !strings.Contains(claimSQL, want) {
			t.Fatalf("scheduled claim SQL missing %q: %s", want, claimSQL)
		}
	}
	if len(stub.execs) != 1 || !strings.Contains(stub.execs[0], "public.report_items") || !strings.Contains(stub.execs[0], "public.digest_window_items") || !strings.Contains(stub.execs[0], "ON CONFLICT") {
		t.Fatalf("snapshot SQL=%q", stub.execs)
	}
}

func TestPostgresReportSchedulesRecoverCompletedDigestWindowWithoutReport(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-3 * time.Hour)
	stub := &reportStoreStub{
		queryRows: []*reportRowsStub{{rows: [][]any{{"schedule-1", "매일", 7, 0, "Asia/Seoul", []int16{1, 2, 3}, nil}}}},
		rowResults: []reportRowFunc{func(dest ...any) error {
			*(dest[0].(*time.Time)) = dueAt
			*(dest[1].(*time.Time)) = now
			return nil
		}},
	}
	schedules, err := loadReportSchedules(context.Background(), stub, "tenant-1")
	if err != nil || len(schedules) != 1 || !schedules[0].PendingDue.Equal(dueAt) || !schedules[0].PendingWindowEnd.Equal(now) {
		t.Fatalf("schedules=%+v err=%v", schedules, err)
	}
	if len(stub.rowQueries) != 1 {
		t.Fatalf("window queries=%q", stub.rowQueries)
	}
	query := stub.rowQueries[0]
	for _, want := range []string{"w.status = 'completed'", "public.digest_window_items", "public.reports", "r.id IS NULL", "r.attempts < 3", "interval '15 minutes'"} {
		if !strings.Contains(query, want) {
			t.Fatalf("completed report recovery query missing %q: %s", want, query)
		}
	}
}

func TestPostgresReportEmptyScheduledWindowCompletesWithoutReport(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	stub := &reportStoreStub{rowResults: []reportRowFunc{func(dest ...any) error { *(dest[0].(*bool)) = false; return nil }}}
	work, created, err := claimScheduledReport(context.Background(), stub, "tenant-1", "테넌트", reportSchedule{ID: "schedule-1", Name: "매일"}, now.Add(-time.Hour), now, now)
	if err != nil || created || work.ReportID != "" {
		t.Fatalf("work=%+v created=%t err=%v", work, created, err)
	}
	if len(stub.rowQueries) != 1 || len(stub.execs) != 1 || strings.Contains(stub.execs[0], "INSERT INTO public.reports") {
		t.Fatalf("row queries=%q execs=%q", stub.rowQueries, stub.execs)
	}
	for _, want := range []string{"UPDATE public.digest_windows", "status = 'completed'", "UPDATE public.schedules", "last_success_at"} {
		if !strings.Contains(stub.execs[0], want) {
			t.Fatalf("empty completion SQL missing %q: %s", want, stub.execs[0])
		}
	}
}

func TestPostgresReportManualClaimSnapshotsOpenMatchesWithoutScheduleAdvance(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	stub := &reportStoreStub{
		rowResults: []reportRowFunc{
			func(dest ...any) error { *(dest[0].(*string)) = "테넌트"; return nil },
			func(dest ...any) error { *(dest[0].(*bool)) = true; return nil },
			workRow("report-2", "tenant-1", "테넌트", "", "수동", "manual", "reports/tenant-1/report-2.html", "token-2", now, nil, now, 1),
		},
		queryRows: []*reportRowsStub{{}},
	}
	work, created, err := claimManualReport(context.Background(), stub, "tenant-1", now)
	if err != nil || !created || work.Trigger != "manual" {
		t.Fatalf("work=%+v created=%t err=%v", work, created, err)
	}
	joined := strings.Join(append(append([]string{}, stub.rowQueries...), stub.execs...), "\n")
	for _, want := range []string{"tenant_name", "schedule_name", "public.matches", "JOIN public.filters", "f.enabled", "public.report_items", "deadline_at"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("manual snapshot SQL missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "UPDATE public.schedules") {
		t.Fatalf("manual claim advanced a schedule: %s", joined)
	}
}

func TestPostgresReportReclaimAndRetryKeepSnapshotWithFreshFence(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		retry bool
		want  []string
	}{
		{"automatic reclaim", false, []string{"interval '15 minutes'", "attempts < 3", "attempts = attempts + 1", "claim_token = pg_catalog.gen_random_uuid()"}},
		{"operator retry", true, []string{"status = 'failed'", "attempts = 1", "claim_token = pg_catalog.gen_random_uuid()"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &reportStoreStub{rowResults: []reportRowFunc{workRow("report-1", "tenant-1", "테넌트", "", "수동", "manual", "reports/tenant-1/report-1.html", "new-token", now, nil, now, 1)}, queryRows: []*reportRowsStub{{}}}
			var ok bool
			var err error
			if test.retry {
				_, ok, err = retryReport(context.Background(), stub, "tenant-1", "report-1", now)
			} else {
				_, ok, err = reclaimReport(context.Background(), stub, "tenant-1", "report-1")
			}
			if err != nil || !ok {
				t.Fatalf("ok=%t err=%v", ok, err)
			}
			for _, want := range test.want {
				if !strings.Contains(stub.rowQueries[0], want) {
					t.Fatalf("claim SQL missing %q: %s", want, stub.rowQueries[0])
				}
			}
			if !strings.Contains(stub.rowQueries[0], "c.tenant_name") || !strings.Contains(stub.rowQueries[0], "c.schedule_name") || strings.Contains(stub.rowQueries[0], "JOIN public.tenants") || strings.Contains(stub.rowQueries[0], "JOIN public.schedules") {
				t.Fatalf("claim reloaded mutable display names: %s", stub.rowQueries[0])
			}
			if len(stub.execs) != 0 {
				t.Fatalf("existing snapshot was rewritten: %q", stub.execs)
			}
		})
	}
}

func TestPostgresReportFinalizeFencesSuccessAndFailure(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	work := ReportWork{ReportID: "report-1", TenantID: "tenant-1", ScheduleID: "schedule-1", Trigger: "scheduled", RelativePath: "reports/tenant-1/report-1.html", ClaimToken: "token-1", DueAt: now.Add(-time.Hour), WindowEnd: now, Attempts: 2}
	artifact := ReportArtifact{RelativePath: work.RelativePath, SHA256: strings.Repeat("a", 64), NoticeCount: 1}

	success := &reportStoreStub{}
	if err := finalizeReport(context.Background(), success, work, artifact, now); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(success.execs, "\n")
	for _, want := range []string{"claim_token = $", "attempts = $", "status = 'generating'", "status = 'generated'", "UPDATE public.digest_windows", "UPDATE public.schedules", "last_success_at"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("success finalize missing %q: %s", want, joined)
		}
	}

	failure := &reportStoreStub{}
	if err := finalizeReportFailure(context.Background(), failure, work, errors.New("render failed")); err != nil {
		t.Fatal(err)
	}
	if len(failure.execs) != 1 || !strings.Contains(failure.execs[0], "claim_token = $") || !strings.Contains(failure.execs[0], "attempts = $") || !strings.Contains(failure.execs[0], "status = 'failed'") {
		t.Fatalf("failure finalize SQL=%q", failure.execs)
	}
}

func TestPostgresReportFinalizeAcceptsTheSameWindowAlreadyCompletedByDigest(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	work := ReportWork{ReportID: "report-1", TenantID: "tenant-1", ScheduleID: "schedule-1", Trigger: "scheduled", RelativePath: "reports/tenant-1/report-1.html", ClaimToken: "token-1", DueAt: now.Add(-time.Hour), WindowEnd: now, Attempts: 1}
	artifact := ReportArtifact{RelativePath: work.RelativePath, SHA256: strings.Repeat("a", 64), NoticeCount: 1}
	stub := &reportStoreStub{execRows: []int64{1, 1}}
	if err := finalizeReport(context.Background(), stub, work, artifact, now); err != nil {
		t.Fatal(err)
	}
	if len(stub.execs) != 2 {
		t.Fatalf("finalize queries=%q", stub.execs)
	}
	completion := stub.execs[1]
	for _, want := range []string{"target_window", "FOR UPDATE", "target.status = 'completed'", "eligible_window"} {
		if !strings.Contains(completion, want) {
			t.Fatalf("idempotent completion query missing %q: %s", want, completion)
		}
	}
}

func TestPostgresReportSnapshotCombinesRulesPerNotice(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	stub := &reportStoreStub{queryRows: []*reportRowsStub{{rows: [][]any{
		{"notice-1", "공고", "service", "기관", "서울", int64(100), now, "https://example.test/1", "지역 필터", []byte(`{"details":[{"Code":"region","RuleValue":"서울"}]}`)},
		{"notice-1", "공고", "service", "기관", "서울", int64(100), now, "https://example.test/1", "금액 필터", []byte(`{"reasons":["min_amount"]}`)},
	}}}}
	work := ReportWork{ReportID: "report-1", TenantID: "tenant-1"}
	if err := loadReportNotices(context.Background(), stub, &work); err != nil {
		t.Fatal(err)
	}
	if len(work.Notices) != 1 || len(work.Notices[0].Matches) != 2 || work.Notices[0].Matches[0].RuleName != "지역 필터" || len(work.Notices[0].Matches[0].Reasons) != 1 {
		t.Fatalf("notices=%+v", work.Notices)
	}
}

func workRow(reportID, tenantID, tenantName, scheduleID, scheduleName, trigger, path, token string, dueAt time.Time, windowStart *time.Time, windowEnd time.Time, attempts int) reportRowFunc {
	return func(dest ...any) error {
		*(dest[0].(*string)) = reportID
		*(dest[1].(*string)) = tenantID
		*(dest[2].(*string)) = tenantName
		*(dest[3].(*string)) = scheduleID
		*(dest[4].(*string)) = scheduleName
		*(dest[5].(*string)) = trigger
		*(dest[6].(*string)) = path
		*(dest[7].(*string)) = token
		*(dest[8].(*time.Time)) = dueAt
		*(dest[9].(**time.Time)) = windowStart
		*(dest[10].(*time.Time)) = windowEnd
		*(dest[11].(*int)) = attempts
		return nil
	}
}
