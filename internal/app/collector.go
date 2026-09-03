// Package app composes procurement, matching, persistence, mail, and web runtime behavior.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"namo/internal/matcher"
	"namo/internal/model"
	"namo/internal/procurement"
)

var collectionCategories = []model.Category{
	model.CategoryConstruction,
	model.CategoryService,
	model.CategoryGoods,
	model.CategoryForeign,
}

type SourceWarning struct {
	Category   model.Category
	Page, Item int
	Field      string
	Code       string
	RawJSON    json.RawMessage
}

type FetchResult struct {
	Notices  []model.Notice
	Warnings []SourceWarning
}

// BidSource is the only procurement-system boundary used by the application.
type BidSource interface {
	Fetch(context.Context, model.Category, time.Time, time.Time) (FetchResult, error)
}

// regionLookup is an optional capability used after persistence has confirmed
// that a notice is new or revised, so unchanged records do not consume API quota.
type regionLookup interface {
	LookupRegion(context.Context, string, string) (string, error)
}

// NoticeMatcher is the only condition-matching boundary used by the application.
type NoticeMatcher interface {
	Match(time.Time, model.Notice, matcher.Rule) matcher.Result
}

type StoredFilter struct {
	ID, TenantID string
	Rule         matcher.Rule
	Revision     time.Time
}

type StoredNotice struct {
	ID                   string
	Changed              bool
	Region               string
	RegionLookupComplete bool
}

type ActiveNotice struct {
	ID                   string
	Notice               model.Notice
	RegionLookupComplete bool
}

type StoredMatch struct {
	TenantID, FilterID, NoticeID string
	Reasons                      []matcher.Reason
	Details                      []matcher.Detail
	FilterRevision               time.Time
}

type CollectionResult struct {
	Fetched, Changed, Matched, Warnings int
}

type CollectorRepository interface {
	LastSuccessfulCollection(context.Context) (time.Time, error)
	StoreNotice(context.Context, model.Notice) (StoredNotice, error)
	ActiveNotices(context.Context, time.Time) ([]ActiveNotice, error)
	MarkRegionLookupComplete(context.Context, string) error
	StoreWarning(context.Context, SourceWarning) error
	EnabledFilters(context.Context) ([]StoredFilter, error)
	UpsertMatch(context.Context, StoredMatch) (bool, error)
	DeleteMatch(context.Context, StoredMatch) error
	FinishCollection(context.Context, time.Time, CollectionResult, error) error
}

type Collector struct {
	Source     BidSource
	Matcher    NoticeMatcher
	Repository CollectorRepository
	Now        func() time.Time
}

func (c Collector) Run(ctx context.Context) (result CollectionResult, runErr error) {
	if c.Source == nil || c.Matcher == nil || c.Repository == nil {
		return result, errors.New("collector dependencies are required")
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	last, err := c.Repository.LastSuccessfulCollection(ctx)
	if err != nil {
		return result, fmt.Errorf("read collection checkpoint: %w", err)
	}
	start := now.AddDate(0, 0, -7)
	if !last.IsZero() {
		start = last.Add(-10 * time.Minute)
	}
	filters, err := c.Repository.EnabledFilters(ctx)
	if err != nil {
		return result, c.finishFailure(ctx, result, fmt.Errorf("load filters: %w", err))
	}
	for _, category := range collectionCategories {
		batch, err := c.Source.Fetch(ctx, category, start, now)
		if err != nil {
			return result, c.finishFailure(ctx, result, fmt.Errorf("fetch %s notices: %w", category, err))
		}
		for _, warning := range batch.Warnings {
			if warning.Category == "" {
				warning.Category = category
			}
			if err := c.Repository.StoreWarning(ctx, warning); err != nil {
				return result, c.finishFailure(ctx, result, fmt.Errorf("store source warning: %w", err))
			}
			result.Warnings++
		}
		for _, notice := range batch.Notices {
			result.Fetched++
			stored, err := c.Repository.StoreNotice(ctx, notice)
			if err != nil {
				return result, c.finishFailure(ctx, result, fmt.Errorf("store notice %q: %w", notice.BidNumber, err))
			}
			if stored.Changed {
				result.Changed++
			}
		}
	}

	active, err := c.Repository.ActiveNotices(ctx, now)
	if err != nil {
		return result, c.finishFailure(ctx, result, fmt.Errorf("load active notices: %w", err))
	}
	lookup, canLookup := c.Source.(regionLookup)
	canLookup = canLookup && len(filters) > 0
	for _, current := range active {
		notice := current.Notice
		if notice.Region == "" && !current.RegionLookupComplete && canLookup {
			region, lookupErr := lookup.LookupRegion(ctx, notice.BidNumber, notice.BidSequence)
			if lookupErr != nil {
				if errors.Is(lookupErr, procurement.ErrLookupBudget) {
					canLookup = false
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return result, c.finishFailure(ctx, result, ctxErr)
				}
				warning := SourceWarning{Category: notice.Category, Field: "region", Code: "region_lookup_failed", RawJSON: notice.RawJSON}
				if err := c.Repository.StoreWarning(ctx, warning); err != nil {
					return result, c.finishFailure(ctx, result, fmt.Errorf("store region lookup warning: %w", err))
				}
				result.Warnings++
			} else if region == "" {
				if err := c.Repository.MarkRegionLookupComplete(ctx, current.ID); err != nil {
					return result, c.finishFailure(ctx, result, fmt.Errorf("complete empty region lookup: %w", err))
				}
			} else {
				notice.Region = region
				enriched, err := c.Repository.StoreNotice(ctx, notice)
				if err != nil {
					return result, c.finishFailure(ctx, result, fmt.Errorf("store enriched notice %q: %w", notice.BidNumber, err))
				}
				if enriched.ID != "" {
					current.ID = enriched.ID
				}
			}
		}
		for _, filter := range filters {
			matched := c.Matcher.Match(now, notice, filter.Rule)
			storedMatch := StoredMatch{
				TenantID: filter.TenantID, FilterID: filter.ID, NoticeID: current.ID,
				Reasons: matched.Reasons, Details: matched.Details, FilterRevision: filter.Revision,
			}
			if !matched.Matched {
				if err := c.Repository.DeleteMatch(ctx, storedMatch); err != nil {
					return result, c.finishFailure(ctx, result, fmt.Errorf("delete stale match: %w", err))
				}
				continue
			}
			created, err := c.Repository.UpsertMatch(ctx, storedMatch)
			if err != nil {
				return result, c.finishFailure(ctx, result, fmt.Errorf("store match: %w", err))
			}
			if created {
				result.Matched++
			}
		}
	}
	if err := c.Repository.FinishCollection(ctx, now, result, nil); err != nil {
		return result, fmt.Errorf("finish collection: %w", err)
	}
	return result, nil
}

func (c Collector) finishFailure(ctx context.Context, result CollectionResult, runErr error) error {
	if finishErr := c.Repository.FinishCollection(ctx, time.Time{}, result, runErr); finishErr != nil {
		return errors.Join(runErr, fmt.Errorf("record collection failure: %w", finishErr))
	}
	return runErr
}
