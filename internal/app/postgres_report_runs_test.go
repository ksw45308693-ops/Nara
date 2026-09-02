package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

type reportRunExecFake struct {
	query string
	args  []any
}

func (f *reportRunExecFake) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	f.query = query
	f.args = append([]any(nil), args...)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func TestReportRunInsertPersistsReportKindAndTenantResult(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 5, 0, 0, time.UTC)
	fake := &reportRunExecFake{}
	record := ReportRunRecord{
		TenantID: "tenant-1", Status: "failed", StartedAt: now, FinishedAt: now.Add(time.Second),
		Generated: 1, Failed: 2, Skipped: 3, Err: errors.New("disk unavailable"),
	}
	if err := insertReportRun(context.Background(), fake, record); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.query, "INSERT INTO public.job_runs") || !strings.Contains(fake.query, "'report'") || len(fake.args) != 5 {
		t.Fatalf("query=%q args=%#v", fake.query, fake.args)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(fake.args[4].(string)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["generated"] != float64(1) || detail["failed"] != float64(2) || detail["skipped"] != float64(3) || detail["error"] != "disk unavailable" {
		t.Fatalf("detail=%#v", detail)
	}
}

func TestReportRunBoundedErrorKeepsValidUTF8(t *testing.T) {
	message := strings.Repeat("가", 1000)
	bounded := boundedReportRunError(errors.New(message))
	if len(bounded) > 2048 || !utf8.ValidString(bounded) || !strings.HasPrefix(message, bounded) {
		t.Fatalf("bytes=%d valid=%t", len(bounded), utf8.ValidString(bounded))
	}
}
