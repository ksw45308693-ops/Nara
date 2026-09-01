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

type digestRunExecStub struct {
	query string
	args  []any
	err   error
}

func (s *digestRunExecStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	s.query = query
	s.args = append([]any(nil), args...)
	return pgconn.NewCommandTag("INSERT 0 1"), s.err
}

func TestInsertDigestRunPersistsTenantScopedDashboardFailure(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 3, 0, 0, time.UTC)
	stub := &digestRunExecStub{}
	record := DigestRunRecord{
		TenantID: "tenant-1", Status: "failed", StartedAt: now, FinishedAt: now.Add(time.Second),
		Sent: 1, Failed: 2, Skipped: 3, Err: errors.New("snapshot database failed"),
	}
	if err := insertDigestRun(context.Background(), stub, record); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.query, "INSERT INTO public.job_runs") || !strings.Contains(stub.query, "'digest'") || len(stub.args) != 5 {
		t.Fatalf("digest job SQL=%q args=%#v", stub.query, stub.args)
	}
	if stub.args[0] != "tenant-1" || stub.args[1] != "failed" || stub.args[2] != now || stub.args[3] != now.Add(time.Second) {
		t.Fatalf("tenant/status/time args=%#v", stub.args)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(stub.args[4].(string)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["sent"] != float64(1) || detail["failed"] != float64(2) || detail["skipped"] != float64(3) || detail["error"] != "snapshot database failed" {
		t.Fatalf("digest detail=%#v", detail)
	}
}

func TestBoundedDigestRunErrorKeepsValidUTF8(t *testing.T) {
	message := strings.Repeat("가", 1000)
	bounded := boundedDigestRunError(errors.New(message))
	if len(bounded) > 2048 || !utf8.ValidString(bounded) || !strings.HasPrefix(message, bounded) {
		t.Fatalf("bounded digest error bytes=%d valid=%t", len(bounded), utf8.ValidString(bounded))
	}
}
