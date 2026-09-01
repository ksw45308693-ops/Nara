package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTickCollectsImmediatelyThenHourlyAndChecksDigestsEveryTick(t *testing.T) {
	start := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	var collected, digests int
	clockNow := start
	s := newScheduler(time.Hour,
		func(context.Context, time.Time) error { collected++; return nil },
		func(context.Context, time.Time) error { digests++; return nil },
		func() time.Time { return clockNow },
	)

	if err := s.Tick(context.Background(), start); err != nil {
		t.Fatalf("first Tick() error = %v", err)
	}
	clockNow = start.Add(30 * time.Minute)
	if err := s.Tick(context.Background(), clockNow); err != nil {
		t.Fatalf("second Tick() error = %v", err)
	}
	clockNow = start.Add(time.Hour)
	if err := s.Tick(context.Background(), clockNow); err != nil {
		t.Fatalf("third Tick() error = %v", err)
	}

	if collected != 2 {
		t.Fatalf("collection calls = %d, want 2", collected)
	}
	if digests != 3 {
		t.Fatalf("digest calls = %d, want 3", digests)
	}
}

func TestFailedCollectionRetriesOnNextTick(t *testing.T) {
	start := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	var attempts int
	clockNow := start
	s := newScheduler(time.Hour,
		func(context.Context, time.Time) error {
			attempts++
			if attempts == 1 {
				return errors.New("temporary API failure")
			}
			return nil
		},
		func(context.Context, time.Time) error { return nil },
		func() time.Time { return clockNow },
	)

	if err := s.Tick(context.Background(), start); err == nil {
		t.Fatal("first Tick() error = nil, want collection error")
	}
	clockNow = start.Add(time.Minute)
	if err := s.Tick(context.Background(), clockNow); err != nil {
		t.Fatalf("retry Tick() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("collection attempts = %d, want 2", attempts)
	}
}

func TestFailedCollectionUsesBoundedExponentialBackoff(t *testing.T) {
	start := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	var attempts int
	clockNow := start
	s := newScheduler(time.Hour,
		func(context.Context, time.Time) error {
			attempts++
			return errors.New("temporary API failure")
		},
		func(context.Context, time.Time) error { return nil },
		func() time.Time { return clockNow },
	)

	checks := []struct {
		after time.Duration
		want  int
	}{
		{0, 1},
		{30 * time.Second, 1},
		{time.Minute, 2},
		{2 * time.Minute, 2},
		{3 * time.Minute, 3},
		{6 * time.Minute, 3},
		{7 * time.Minute, 4},
	}
	for _, check := range checks {
		clockNow = start.Add(check.after)
		_ = s.Tick(context.Background(), clockNow)
		if attempts != check.want {
			t.Fatalf("after %s attempts=%d, want %d", check.after, attempts, check.want)
		}
	}
	if got := collectionRetryDelay(10); got != 30*time.Minute {
		t.Fatalf("bounded retry delay=%s", got)
	}
}

func TestTickReturnsBothIndependentErrors(t *testing.T) {
	now := time.Now()
	s := newScheduler(time.Hour,
		func(context.Context, time.Time) error { return errors.New("collect failed") },
		func(context.Context, time.Time) error { return errors.New("digest failed") },
		func() time.Time { return now },
	)

	err := s.Tick(context.Background(), now)
	if !errors.Is(err, ErrCollection) || !errors.Is(err, ErrDigest) {
		t.Fatalf("Tick() error = %v, want collection and digest markers", err)
	}
}

func TestFailedCollectionBackoffStartsAfterLongRunCompletes(t *testing.T) {
	start := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	finishedAt := start.Add(2 * time.Minute)
	attempts := 0
	var digestTimes []time.Time
	s := newScheduler(time.Hour,
		func(context.Context, time.Time) error {
			attempts++
			return errors.New("slow API failure")
		},
		func(_ context.Context, at time.Time) error {
			digestTimes = append(digestTimes, at)
			return nil
		},
		func() time.Time { return finishedAt },
	)

	if err := s.Tick(context.Background(), start); err == nil {
		t.Fatal("slow collection error = nil")
	}
	if err := s.Tick(context.Background(), finishedAt); err != nil {
		t.Fatalf("completion-time tick error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("collection retried immediately at completion: attempts=%d", attempts)
	}
	if len(digestTimes) < 1 || !digestTimes[0].Equal(start.Add(2*time.Minute)) {
		t.Fatalf("digest time=%v, want collection completion", digestTimes)
	}
	finishedAt = finishedAt.Add(time.Minute)
	_ = s.Tick(context.Background(), finishedAt)
	if attempts != 2 {
		t.Fatalf("collection attempts=%d, want retry after full backoff", attempts)
	}
}
