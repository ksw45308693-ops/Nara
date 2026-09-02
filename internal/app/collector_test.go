package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"namo/internal/matcher"
	"namo/internal/model"
)

type sourceStub struct {
	calls       []fetchCall
	notices     map[model.Category][]model.Notice
	warning     SourceWarning
	err         error
	region      string
	regionErr   error
	regionCalls int
}

type fetchCall struct {
	category   model.Category
	start, end time.Time
}

func (s *sourceStub) Fetch(_ context.Context, category model.Category, start, end time.Time) (FetchResult, error) {
	s.calls = append(s.calls, fetchCall{category: category, start: start, end: end})
	if s.err != nil {
		return FetchResult{}, s.err
	}
	result := FetchResult{Notices: s.notices[category]}
	if s.warning.Code != "" && len(s.calls) == 1 {
		result.Warnings = []SourceWarning{s.warning}
	}
	return result, nil
}

func (s *sourceStub) LookupRegion(_ context.Context, _, _ string) (string, error) {
	s.regionCalls++
	return s.region, s.regionErr
}

type matcherStub struct{}

func (matcherStub) Match(_ time.Time, notice model.Notice, rule matcher.Rule) matcher.Result {
	return matcher.MatchAt(time.Date(2026, 9, 1, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60)), notice, rule)
}

type collectorRepoStub struct {
	last            time.Time
	stored          []model.Notice
	warnings        []SourceWarning
	filters         []StoredFilter
	matches         []StoredMatch
	finishedAt      time.Time
	finishedErr     error
	unchanged       bool
	existingMatches bool
	deleted         []StoredMatch
	active          []ActiveNotice
	regionComplete  bool
	markedComplete  []string
}

func (r *collectorRepoStub) LastSuccessfulCollection(context.Context) (time.Time, error) {
	return r.last, nil
}

func (r *collectorRepoStub) StoreNotice(_ context.Context, notice model.Notice) (StoredNotice, error) {
	r.stored = append(r.stored, notice)
	return StoredNotice{ID: notice.Identity(), Changed: !r.unchanged, Region: notice.Region, RegionLookupComplete: r.regionComplete || notice.Region != ""}, nil
}

func (r *collectorRepoStub) StoreWarning(_ context.Context, warning SourceWarning) error {
	r.warnings = append(r.warnings, warning)
	return nil
}

func TestCollectorEnrichesRegionOnlyForChangedNotices(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	notice := model.Notice{
		Category: model.CategoryService, BidNumber: "2026-2", BidSequence: "00",
		Title: "서울 회계감사 용역", Deadline: now.Add(48 * time.Hour),
	}
	source := &sourceStub{
		notices: map[model.Category][]model.Notice{model.CategoryService: {notice}},
		region:  "서울",
	}
	repo := &collectorRepoStub{filters: []StoredFilter{{
		ID: "filter-region", TenantID: "tenant-1", Rule: matcher.Rule{Regions: []string{"서울"}},
	}}}
	collector := Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}

	result, err := collector.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if source.regionCalls != 1 {
		t.Fatalf("region calls = %d, want one", source.regionCalls)
	}
	if len(repo.stored) != 2 || repo.stored[1].Region != "서울" {
		t.Fatalf("stored notices = %+v, want enriched revision", repo.stored)
	}
	if result.Changed != 1 || result.Matched != 1 || len(repo.matches) != 1 {
		t.Fatalf("result=%+v matches=%+v", result, repo.matches)
	}

	unchangedSource := &sourceStub{notices: map[model.Category][]model.Notice{model.CategoryService: {notice}}, region: "서울"}
	unchangedRepo := &collectorRepoStub{unchanged: true, regionComplete: true}
	collector.Source, collector.Repository = unchangedSource, unchangedRepo
	if _, err := collector.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unchangedSource.regionCalls != 0 {
		t.Fatalf("unchanged notice consumed %d region lookups", unchangedSource.regionCalls)
	}
}

func (r *collectorRepoStub) ActiveNotices(_ context.Context, _ time.Time) ([]ActiveNotice, error) {
	if r.active != nil {
		return r.active, nil
	}
	latest := make(map[string]model.Notice)
	var order []string
	for _, notice := range r.stored {
		id := notice.Identity()
		if _, exists := latest[id]; !exists {
			order = append(order, id)
		}
		latest[id] = notice
	}
	result := make([]ActiveNotice, 0, len(order))
	for _, id := range order {
		notice := latest[id]
		result = append(result, ActiveNotice{ID: id, Notice: notice, RegionLookupComplete: r.regionComplete || notice.Region != ""})
	}
	return result, nil
}

func (r *collectorRepoStub) MarkRegionLookupComplete(_ context.Context, noticeID string) error {
	r.markedComplete = append(r.markedComplete, noticeID)
	r.regionComplete = true
	return nil
}

func (r *collectorRepoStub) EnabledFilters(context.Context) ([]StoredFilter, error) {
	return r.filters, nil
}

func (r *collectorRepoStub) UpsertMatch(_ context.Context, match StoredMatch) (bool, error) {
	r.matches = append(r.matches, match)
	return !r.existingMatches, nil
}

func (r *collectorRepoStub) DeleteMatch(_ context.Context, match StoredMatch) error {
	r.deleted = append(r.deleted, match)
	return nil
}

func TestCollectorReevaluatesPersistedNoticesWithoutDuplicatingMatches(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	notice := model.Notice{Category: model.CategoryService, BidNumber: "N-3", BidSequence: "00", Title: "회계감사"}
	source := &sourceStub{notices: map[model.Category][]model.Notice{model.CategoryService: {notice}}}
	repo := &collectorRepoStub{
		unchanged: true, existingMatches: true,
		filters: []StoredFilter{{ID: "filter", TenantID: "tenant", Rule: matcher.Rule{IncludeAny: []string{"회계"}}}},
	}
	result, err := (Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.matches) != 1 || result.Matched != 0 || result.Changed != 0 {
		t.Fatalf("result=%+v matches=%+v", result, repo.matches)
	}

	repo.filters[0].Rule = matcher.Rule{IncludeAny: []string{"전혀없는조건"}}
	if _, err := (Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0].NoticeID == "" {
		t.Fatalf("deleted matches = %+v", repo.deleted)
	}
}

func (r *collectorRepoStub) FinishCollection(_ context.Context, at time.Time, _ CollectionResult, runErr error) error {
	r.finishedAt, r.finishedErr = at, runErr
	return nil
}

func TestCollectorInitialWindowFetchesAllCategoriesAndMatches(t *testing.T) {
	loc := time.FixedZone("KST", 9*60*60)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, loc)
	notice := model.Notice{
		Category: model.CategoryService, BidNumber: "2026-1", BidSequence: "00",
		Title: "회계감사 용역", Agency: "샘플 기관", Deadline: now.Add(48 * time.Hour),
	}
	source := &sourceStub{
		notices: map[model.Category][]model.Notice{model.CategoryService: {notice}},
		warning: SourceWarning{Category: model.CategoryService, Page: 1, Item: 2, Field: "region", Code: "unavailable"},
	}
	repo := &collectorRepoStub{filters: []StoredFilter{{ID: "filter-1", TenantID: "tenant-1", Rule: matcher.Rule{IncludeAny: []string{"회계감사"}}}}}
	collector := Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}

	result, err := collector.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 4 {
		t.Fatalf("fetch calls = %d, want four categories", len(source.calls))
	}
	wantCategories := []model.Category{model.CategoryConstruction, model.CategoryService, model.CategoryGoods, model.CategoryForeign}
	for i, call := range source.calls {
		if call.category != wantCategories[i] || !call.start.Equal(now.AddDate(0, 0, -7)) || !call.end.Equal(now) {
			t.Fatalf("call[%d] = %+v", i, call)
		}
	}
	if result.Fetched != 1 || result.Changed != 1 || result.Matched != 1 || result.Warnings != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(repo.matches) != 1 || repo.matches[0].TenantID != "tenant-1" || len(repo.matches[0].Details) == 0 {
		t.Fatalf("matches = %+v", repo.matches)
	}
	if len(repo.warnings) != 1 || !repo.finishedAt.Equal(now) || repo.finishedErr != nil {
		t.Fatalf("warning/finish = %+v, %s, %v", repo.warnings, repo.finishedAt, repo.finishedErr)
	}
}

func TestCollectorOverlapsLastSuccessAndDoesNotAdvanceOnFailure(t *testing.T) {
	loc := time.FixedZone("KST", 9*60*60)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, loc)
	last := now.Add(-time.Hour)
	source := &sourceStub{err: errors.New("temporary API failure")}
	repo := &collectorRepoStub{last: last}
	collector := Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}

	_, err := collector.Run(context.Background())
	if err == nil {
		t.Fatal("collector failure must be returned")
	}
	if len(source.calls) != 1 || !source.calls[0].start.Equal(last.Add(-10*time.Minute)) {
		t.Fatalf("calls = %+v", source.calls)
	}
	if repo.finishedErr == nil || !repo.finishedAt.IsZero() {
		t.Fatalf("failed collection advanced success: at=%s err=%v", repo.finishedAt, repo.finishedErr)
	}
}

func TestCollectorKeepsNoticeAndRetriesRegionAfterLookupFailure(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	notice := model.Notice{Category: model.CategoryGoods, BidNumber: "R-1", BidSequence: "00", Title: "부산 장비", Deadline: now.Add(time.Hour)}
	source := &sourceStub{notices: map[model.Category][]model.Notice{model.CategoryGoods: {notice}}, regionErr: errors.New("temporary region failure")}
	repo := &collectorRepoStub{filters: []StoredFilter{{ID: "f", TenantID: "t", Rule: matcher.Rule{IncludeAny: []string{"장비"}}}}}

	result, err := (Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}).Run(context.Background())
	if err != nil {
		t.Fatalf("region lookup must be non-fatal: %v", err)
	}
	if result.Fetched != 1 || result.Matched != 1 || result.Warnings != 1 || len(repo.markedComplete) != 0 {
		t.Fatalf("result=%+v marked=%+v", result, repo.markedComplete)
	}
	if len(repo.warnings) != 1 || repo.warnings[0].Code != "region_lookup_failed" || repo.finishedErr != nil {
		t.Fatalf("warnings=%+v finish=%v", repo.warnings, repo.finishedErr)
	}
}

func TestCollectorMarksSuccessfulEmptyRegionLookupComplete(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	notice := model.Notice{Category: model.CategoryForeign, BidNumber: "R-2", BidSequence: "00", Title: "외자"}
	source := &sourceStub{notices: map[model.Category][]model.Notice{model.CategoryForeign: {notice}}}
	repo := &collectorRepoStub{}

	if _, err := (Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.regionCalls != 1 || len(repo.markedComplete) != 1 {
		t.Fatalf("lookups=%d marked=%+v", source.regionCalls, repo.markedComplete)
	}
}

func TestCollectorRematchesAllStoredActiveNotices(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	days := 3
	old := model.Notice{Category: model.CategoryService, BidNumber: "OLD-1", BidSequence: "00", Title: "기존 회계 용역", Deadline: now.Add(2 * time.Hour)}
	source := &sourceStub{notices: map[model.Category][]model.Notice{}}
	repo := &collectorRepoStub{
		active:  []ActiveNotice{{ID: old.Identity(), Notice: old, RegionLookupComplete: true}},
		filters: []StoredFilter{{ID: "f", TenantID: "t", Rule: matcher.Rule{IncludeAny: []string{"회계"}, DeadlineWithinDays: &days}}},
	}

	result, err := (Collector{Source: source, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Fetched != 0 || result.Matched != 1 || len(repo.matches) != 1 || repo.matches[0].NoticeID != old.Identity() {
		t.Fatalf("result=%+v matches=%+v", result, repo.matches)
	}
}

func TestCollectorPassesFilterRevisionToMatchWrites(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	revision := now.Add(-time.Minute)
	matching := model.Notice{Category: model.CategoryService, BidNumber: "REV-1", BidSequence: "00", Title: "회계 용역"}
	repo := &collectorRepoStub{
		active: []ActiveNotice{{ID: matching.Identity(), Notice: matching, RegionLookupComplete: true}},
		filters: []StoredFilter{{
			ID: "filter-1", TenantID: "tenant-1", Revision: revision,
			Rule: matcher.Rule{IncludeAny: []string{"회계"}},
		}},
	}
	collector := Collector{Source: &sourceStub{notices: map[model.Category][]model.Notice{}}, Matcher: matcherStub{}, Repository: repo, Now: func() time.Time { return now }}
	if _, err := collector.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.matches) != 1 || !repo.matches[0].FilterRevision.Equal(revision) {
		t.Fatalf("match revision=%+v, want %s", repo.matches, revision)
	}

	repo.matches = nil
	repo.filters[0].Rule = matcher.Rule{IncludeAny: []string{"없는조건"}}
	if _, err := collector.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.deleted) != 1 || !repo.deleted[0].FilterRevision.Equal(revision) {
		t.Fatalf("delete revision=%+v, want %s", repo.deleted, revision)
	}
}
