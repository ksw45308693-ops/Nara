package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"namo/internal/matcher"
	"namo/internal/model"
)

type filterRowsStub struct {
	pgx.Rows
	filters []StoredFilter
	index   int
}

func (r *filterRowsStub) Close()     {}
func (r *filterRowsStub) Err() error { return nil }
func (r *filterRowsStub) Next() bool { return r.index < len(r.filters) }
func (r *filterRowsStub) Scan(dest ...any) error {
	filter := r.filters[r.index]
	r.index++
	raw, _ := json.Marshal(filter.Rule)
	*(dest[0].(*string)) = filter.ID
	*(dest[1].(*[]byte)) = raw
	*(dest[2].(*time.Time)) = filter.Revision
	return nil
}

type filterQueryStub struct {
	query string
	args  []any
	rows  *filterRowsStub
}

func (s *filterQueryStub) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	s.query, s.args = query, args
	return s.rows, nil
}

func TestEnabledFilterRowsCarryUpdatedAtRevisionAndTenantScope(t *testing.T) {
	revision := time.Date(2026, 9, 1, 6, 55, 0, 123000, time.UTC)
	stub := &filterQueryStub{rows: &filterRowsStub{filters: []StoredFilter{{ID: "filter-1", Revision: revision, Rule: matcher.Rule{IncludeAny: []string{"감사"}}}}}}
	filters, err := loadEnabledFilters(context.Background(), stub, "tenant-a")
	if err != nil || len(filters) != 1 {
		t.Fatalf("filters=%+v err=%v", filters, err)
	}
	if filters[0].TenantID != "tenant-a" || !filters[0].Revision.Equal(revision) {
		t.Fatalf("filter revision/tenant=%+v", filters[0])
	}
	for _, want := range []string{"updated_at", "tenant_id=$1::uuid", "enabled"} {
		if !strings.Contains(stub.query, want) {
			t.Fatalf("filter query missing %q: %s", want, stub.query)
		}
	}
}

type revisionMatchStoreStub struct {
	query        string
	queryArgs    []any
	exec         string
	execArgs     []any
	inserted     bool
	queryErr     error
	rowsAffected int64
}

func (s *revisionMatchStoreStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	s.query, s.queryArgs = query, args
	return budgetRowStub{scan: func(dest ...any) error {
		if s.queryErr != nil {
			return s.queryErr
		}
		*(dest[0].(*bool)) = s.inserted
		return nil
	}}
}

func (s *revisionMatchStoreStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	s.exec, s.execArgs = query, args
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", s.rowsAffected)), nil
}

func TestMatchWritesRequireCurrentEnabledFilterRevision(t *testing.T) {
	revision := time.Date(2026, 9, 1, 6, 59, 0, 0, time.UTC)
	match := StoredMatch{TenantID: "tenant-a", FilterID: "filter-1", NoticeID: "notice-1", FilterRevision: revision}

	staleUpsert := &revisionMatchStoreStub{queryErr: pgx.ErrNoRows, rowsAffected: 0}
	staleResult, err := upsertMatchRevision(context.Background(), staleUpsert, match, []byte(`{"reasons":[]}`))
	if err != nil || staleResult.Applied || staleResult.Created {
		t.Fatalf("stale upsert=%+v err=%v", staleResult, err)
	}
	currentUpsert := &revisionMatchStoreStub{queryErr: pgx.ErrNoRows, rowsAffected: 1}
	currentResult, err := upsertMatchRevision(context.Background(), currentUpsert, match, []byte(`{"reasons":[]}`))
	if err != nil || !currentResult.Applied || currentResult.Created {
		t.Fatalf("current update=%+v err=%v", currentResult, err)
	}
	createdUpsert := &revisionMatchStoreStub{inserted: true}
	createdResult, err := upsertMatchRevision(context.Background(), createdUpsert, match, []byte(`{"reasons":[]}`))
	if err != nil || !createdResult.Applied || !createdResult.Created {
		t.Fatalf("current insert=%+v err=%v", createdResult, err)
	}

	staleDelete := &revisionMatchStoreStub{rowsAffected: 0}
	deleted, err := deleteMatchRevision(context.Background(), staleDelete, match)
	if err != nil || deleted {
		t.Fatalf("stale delete=%t err=%v", deleted, err)
	}
	currentDelete := &revisionMatchStoreStub{rowsAffected: 1}
	deleted, err = deleteMatchRevision(context.Background(), currentDelete, match)
	if err != nil || !deleted {
		t.Fatalf("current delete=%t err=%v", deleted, err)
	}

	for _, query := range []string{staleUpsert.query, staleUpsert.exec} {
		for _, want := range []string{"public.filters", "tenant_id=$1::uuid", "updated_at=$5", "enabled", "FOR UPDATE"} {
			if !strings.Contains(query, want) {
				t.Fatalf("revision write query missing %q: %s", want, query)
			}
		}
	}
	for _, args := range [][]any{staleUpsert.queryArgs, staleUpsert.execArgs} {
		if args[0] != "tenant-a" || args[1] != "filter-1" || args[4] != revision {
			t.Fatalf("revision write lost tenant/filter/revision: %#v", args)
		}
	}
	for _, want := range []string{"public.filters", "tenant_id=$1::uuid", "updated_at=$4", "enabled", "FOR UPDATE"} {
		if !strings.Contains(staleDelete.exec, want) {
			t.Fatalf("revision delete query missing %q: %s", want, staleDelete.exec)
		}
	}
	if staleDelete.execArgs[0] != "tenant-a" || staleDelete.execArgs[1] != "filter-1" || staleDelete.execArgs[3] != revision {
		t.Fatalf("revision delete lost tenant/filter/revision: %#v", staleDelete.execArgs)
	}
}

func TestNoticeHashesRequireValidSourceIdentityAndRevision(t *testing.T) {
	valid := model.Notice{Category: model.CategoryGoods, BidNumber: "N-1", BidSequence: "00", Title: "샘플"}
	identity, revision, err := noticeHashes(valid)
	if err != nil || len(identity) != 32 || len(revision) != 32 {
		t.Fatalf("identity=%x revision=%x err=%v", identity, revision, err)
	}
	if _, _, err := noticeHashes(model.Notice{}); err == nil {
		t.Fatal("invalid source notice was accepted for persistence")
	}
}

func TestMergeStoredRegionPreservesOnlyWhenBaseNoticeIsUnchanged(t *testing.T) {
	stored := model.Notice{Category: model.CategoryGoods, BidNumber: "N-1", BidSequence: "00", Title: "샘플", Region: "서울"}
	payload, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	incoming := stored
	incoming.Region = ""
	merged, complete, err := mergeStoredRegion(incoming, payload, true)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Region != "서울" || !complete || merged.Revision() != stored.Revision() {
		t.Fatalf("merged=%+v revision=%q want=%q", merged, merged.Revision(), stored.Revision())
	}

	incoming.Title = "변경된 샘플"
	merged, complete, err = mergeStoredRegion(incoming, payload, true)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Region != "" || complete {
		t.Fatalf("changed notice retained stale region: merged=%+v complete=%t", merged, complete)
	}
}

type budgetRowStub struct{ scan func(...any) error }

func (r budgetRowStub) Scan(dest ...any) error { return r.scan(dest...) }

type budgetQueryStub struct {
	queries []string
	args    [][]any
	err     error
}

func (s *budgetQueryStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	s.queries = append(s.queries, query)
	s.args = append(s.args, args)
	return budgetRowStub{scan: func(dest ...any) error {
		if s.err != nil {
			return s.err
		}
		*(dest[0].(*int)) = 1
		return nil
	}}
}

func TestPostgresDailyCallBudgetUsesKoreanDayAndDefaultCap(t *testing.T) {
	query := &budgetQueryStub{}
	budget := PostgresDailyCallBudget{DB: query, Now: func() time.Time {
		return time.Date(2026, 8, 31, 15, 30, 0, 0, time.UTC)
	}}
	if err := budget.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(query.queries) != 1 || !strings.Contains(query.queries[0], "public.api_daily_usage") {
		t.Fatalf("queries=%+v", query.queries)
	}
	if got := query.args[0][0]; got != "2026-09-01" {
		t.Fatalf("usage day=%v", got)
	}
	if got := query.args[0][1]; got != 900 {
		t.Fatalf("limit=%v", got)
	}
}

func TestPostgresDailyCallBudgetReturnsStableExhaustionError(t *testing.T) {
	query := &budgetQueryStub{err: pgx.ErrNoRows}
	err := (PostgresDailyCallBudget{DB: query}).Take(context.Background())
	if !errors.Is(err, ErrDailyAPICallBudget) {
		t.Fatalf("error=%v", err)
	}
}
