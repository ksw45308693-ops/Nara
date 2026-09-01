package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type contextBlockingMailer struct {
	mu        sync.Mutex
	calls     int
	deadlines []time.Time
}

func (m *contextBlockingMailer) Send(ctx context.Context, _, _ string, _ []byte) error {
	m.mu.Lock()
	m.calls++
	if deadline, ok := ctx.Deadline(); ok {
		m.deadlines = append(m.deadlines, deadline)
	}
	m.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (m *contextBlockingMailer) snapshot() (int, []time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, append([]time.Time(nil), m.deadlines...)
}

func TestInteractiveMailProductionBudgetFitsHTTPWriteTimeout(t *testing.T) {
	policy := interactiveMailRetryPolicy
	if policy.Attempts != 3 || policy.TotalTimeout != 45*time.Second || policy.AttemptTimeout > 15*time.Second {
		t.Fatalf("interactive mail policy=%+v", policy)
	}
	if time.Duration(policy.Attempts)*policy.AttemptTimeout > policy.TotalTimeout || policy.TotalTimeout >= 60*time.Second {
		t.Fatalf("interactive retry can outlive HTTP response budget: %+v", policy)
	}
}

func TestAdminTestMailSlowAttemptsStayInsideTotalBudgetAndRetryThreeTimes(t *testing.T) {
	mailer := &contextBlockingMailer{}
	policy := mailRetryPolicy{Attempts: 3, TotalTimeout: 120 * time.Millisecond, AttemptTimeout: 25 * time.Millisecond}
	started := time.Now()
	err := sendTestMailWithPolicy(context.Background(), mailer, "from@example.com", "to@example.com", policy)
	elapsed := time.Since(started)
	calls, deadlines := mailer.snapshot()
	if !errors.Is(err, context.DeadlineExceeded) || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if elapsed >= policy.TotalTimeout || len(deadlines) != 3 {
		t.Fatalf("elapsed=%v total=%v deadlines=%v", elapsed, policy.TotalTimeout, deadlines)
	}
}

func TestAdminTestMailHonorsShorterCallerDeadline(t *testing.T) {
	mailer := &contextBlockingMailer{}
	policy := mailRetryPolicy{Attempts: 3, TotalTimeout: time.Second, AttemptTimeout: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := sendTestMailWithPolicy(ctx, mailer, "from@example.com", "to@example.com", policy)
	elapsed := time.Since(started)
	calls, _ := mailer.snapshot()
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || elapsed >= 250*time.Millisecond {
		t.Fatalf("err=%v calls=%d elapsed=%v", err, calls, elapsed)
	}
}

func TestInvitationTimeoutRemainsARecoverableMailError(t *testing.T) {
	mailer := &contextBlockingMailer{}
	service := InvitationService{Mailer: mailer, From: "from@example.com"}
	policy := mailRetryPolicy{Attempts: 3, TotalTimeout: 120 * time.Millisecond, AttemptTimeout: 25 * time.Millisecond}
	err := service.sendInvitationWithPolicy(context.Background(), "to@example.com", []byte("message"), policy)
	if !errors.Is(err, ErrInvitationMail) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invitation timeout error=%v", err)
	}
}
