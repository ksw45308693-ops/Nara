package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"g2b-monitor/internal/digest"
)

// Mailer is the only outbound-mail boundary used by the application.
type Mailer interface {
	Send(context.Context, string, string, []byte) error
}

type DigestWork struct {
	TenantID, ScheduleID, RecipientID string
	Recipient                         string
	DueAt, WindowEnd                  time.Time
	Notices                           []digest.Notice
}

type DeliveryClaim struct {
	TenantID, ScheduleID, RecipientID, IdempotencyKey, ClaimToken string
	DueAt, WindowEnd                                              time.Time
}

type DeliveryReservation struct {
	Claimed    bool
	Attempts   int
	ClaimToken string
}

type DigestRepository interface {
	DueDigests(context.Context, time.Time) ([]DigestWork, error)
	ClaimDelivery(context.Context, DeliveryClaim) (DeliveryReservation, error)
	FinalizeSent(context.Context, DeliveryClaim, int, time.Time) error
	FinalizeFailure(context.Context, DeliveryClaim, int, error) error
	// CompleteNoop returns nil both when an empty window was completed and when
	// an active delivery lease deferred completion for a later scheduler tick.
	CompleteNoop(context.Context, string, string, time.Time, time.Time) error
}

type DigestResult struct {
	// Skipped includes empty completed windows, duplicate deliveries, and empty
	// windows deferred because another process still owns an active send lease.
	Sent, Failed, Skipped int
	TenantRuns            []DigestTenantResult
}

type DigestTenantResult struct {
	TenantID              string
	Sent, Failed, Skipped int
	Err                   error
}

type DigestRunner struct {
	Repository DigestRepository
	Mailer     Mailer
	From       string
	Now        func() time.Time
}

func (r DigestRunner) Run(ctx context.Context) (DigestResult, error) {
	var result DigestResult
	if r.Repository == nil || r.Mailer == nil {
		return result, errors.New("digest dependencies are required")
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	work, err := r.Repository.DueDigests(ctx, now)
	if err != nil {
		return result, fmt.Errorf("load due digests: %w", err)
	}
	tenantRuns := make(map[string]*DigestTenantResult)
	var tenantOrder []string
	tenantRun := func(tenantID string) *DigestTenantResult {
		if run := tenantRuns[tenantID]; run != nil {
			return run
		}
		run := &DigestTenantResult{TenantID: tenantID}
		tenantRuns[tenantID] = run
		tenantOrder = append(tenantOrder, tenantID)
		return run
	}
	var runErr error
	for _, item := range work {
		tenant := tenantRun(item.TenantID)
		if len(item.Notices) == 0 {
			if err := r.Repository.CompleteNoop(ctx, item.TenantID, item.ScheduleID, item.DueAt, item.WindowEnd); err != nil {
				operationErr := fmt.Errorf("complete empty digest: %w", err)
				result.Failed++
				tenant.Failed++
				tenant.Err = errors.Join(tenant.Err, operationErr)
				runErr = errors.Join(runErr, operationErr)
				continue
			}
			result.Skipped++
			tenant.Skipped++
			continue
		}
		key := digest.DeliveryKey(item.TenantID, item.ScheduleID, item.RecipientID, item.DueAt)
		claim := DeliveryClaim{
			TenantID: item.TenantID, ScheduleID: item.ScheduleID, RecipientID: item.RecipientID,
			IdempotencyKey: key, DueAt: item.DueAt, WindowEnd: item.WindowEnd,
		}
		reservation, err := r.Repository.ClaimDelivery(ctx, claim)
		if err != nil {
			operationErr := fmt.Errorf("claim digest: %w", err)
			result.Failed++
			tenant.Failed++
			tenant.Err = errors.Join(tenant.Err, operationErr)
			runErr = errors.Join(runErr, operationErr)
			continue
		}
		if !reservation.Claimed {
			result.Skipped++
			tenant.Skipped++
			continue
		}
		if reservation.ClaimToken == "" {
			operationErr := errors.New("claim digest: missing fencing token")
			result.Failed++
			tenant.Failed++
			tenant.Err = errors.Join(tenant.Err, operationErr)
			runErr = errors.Join(runErr, operationErr)
			continue
		}
		claim.ClaimToken = reservation.ClaimToken
		if reservation.Attempts < 1 || reservation.Attempts > 3 {
			operationErr := fmt.Errorf("claim digest: invalid reserved attempt %d", reservation.Attempts)
			result.Failed++
			tenant.Failed++
			tenant.Err = errors.Join(tenant.Err, operationErr)
			runErr = errors.Join(runErr, operationErr)
			continue
		}
		attempts := reservation.Attempts
		subject := fmt.Sprintf("나라장터 신규 입찰공고 %d건", len(item.Notices))
		messageID := digestMessageID(key, item.Notices)
		message, err := digest.BuildSMTPMessageWithID(r.From, []string{item.Recipient}, subject, item.Notices, messageID)
		if err != nil {
			if finalizeErr := r.Repository.FinalizeFailure(ctx, claim, attempts, err); finalizeErr != nil {
				err = errors.Join(err, finalizeErr)
			}
			operationErr := fmt.Errorf("build digest: %w", err)
			result.Failed++
			tenant.Failed++
			tenant.Err = errors.Join(tenant.Err, operationErr)
			runErr = errors.Join(runErr, operationErr)
			continue
		}
		sendErr := r.Mailer.Send(ctx, r.From, item.Recipient, message)
		if sendErr != nil && ctx.Err() != nil {
			sendErr = ctx.Err()
		}
		if sendErr != nil {
			if err := r.Repository.FinalizeFailure(ctx, claim, attempts, sendErr); err != nil {
				sendErr = errors.Join(sendErr, err)
			}
			operationErr := fmt.Errorf("send digest: %w", sendErr)
			result.Failed++
			tenant.Failed++
			tenant.Err = errors.Join(tenant.Err, operationErr)
			runErr = errors.Join(runErr, operationErr)
			continue
		}
		sentAt := time.Now()
		if r.Now != nil {
			sentAt = r.Now()
		}
		if sentAt.Before(now) {
			sentAt = now
		}
		if err := r.Repository.FinalizeSent(ctx, claim, attempts, sentAt); err != nil {
			operationErr := fmt.Errorf("finalize digest: %w", err)
			result.Failed++
			tenant.Failed++
			tenant.Err = errors.Join(tenant.Err, operationErr)
			runErr = errors.Join(runErr, operationErr)
			continue
		}
		result.Sent++
		tenant.Sent++
	}
	for _, tenantID := range tenantOrder {
		result.TenantRuns = append(result.TenantRuns, *tenantRuns[tenantID])
	}
	return result, runErr
}

func digestMessageID(deliveryKey string, notices []digest.Notice) string {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	writePart(deliveryKey)
	for _, notice := range notices {
		writePart(notice.Title)
		writePart(notice.URL)
		writePart(notice.Reason)
	}
	return hex.EncodeToString(hash.Sum(nil)) + "@g2b-monitor.invalid"
}
