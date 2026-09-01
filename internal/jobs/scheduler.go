package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrCollection = errors.New("collection job failed")
	ErrDigest     = errors.New("digest job failed")
)

type Job func(context.Context, time.Time) error

type Scheduler struct {
	mu                 sync.Mutex
	collectEvery       time.Duration
	nextCollection     time.Time
	collectionFailures int
	collect            Job
	digest             Job
	clock              func() time.Time
}

func NewScheduler(collectEvery time.Duration, collect, digest Job) *Scheduler {
	return newScheduler(collectEvery, collect, digest, time.Now)
}

func newScheduler(collectEvery time.Duration, collect, digest Job, clock func() time.Time) *Scheduler {
	if collectEvery <= 0 {
		collectEvery = time.Hour
	}
	if collect == nil {
		collect = func(context.Context, time.Time) error { return nil }
	}
	if digest == nil {
		digest = func(context.Context, time.Time) error { return nil }
	}
	if clock == nil {
		clock = time.Now
	}
	return &Scheduler{collectEvery: collectEvery, collect: collect, digest: digest, clock: clock}
}

func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var failures []error
	digestAt := now
	if s.nextCollection.IsZero() || !now.Before(s.nextCollection) {
		collectErr := s.collect(ctx, now)
		finishedAt := s.clock()
		if finishedAt.Before(now) {
			finishedAt = now
		}
		digestAt = finishedAt
		if collectErr != nil {
			failures = append(failures, fmt.Errorf("%w: %v", ErrCollection, collectErr))
			s.collectionFailures++
			s.nextCollection = finishedAt.Add(collectionRetryDelay(s.collectionFailures))
		} else {
			s.nextCollection = finishedAt.Add(s.collectEvery)
			s.collectionFailures = 0
		}
	}
	if err := s.digest(ctx, digestAt); err != nil {
		failures = append(failures, fmt.Errorf("%w: %v", ErrDigest, err))
	}
	return errors.Join(failures...)
}

func collectionRetryDelay(failures int) time.Duration {
	const maximum = 30 * time.Minute
	delay := time.Minute
	for attempt := 1; attempt < failures && delay < maximum; attempt++ {
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
