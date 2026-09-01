package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrDeliveryNotClaimed = errors.New("delivery is not actively claimed")

const MaxDeliveryAttempts = 3

type DeliveryTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type DeliveryBeginner interface {
	Begin(context.Context) (DeliveryTx, error)
}

type DeliveryRepository struct{ DB DeliveryBeginner }

type DeliveryClaim struct {
	TenantID, ScheduleID, RecipientID, IdempotencyKey string
	DueAt, WindowEnd                                  time.Time
}

type DeliveryReservation struct {
	Claimed    bool
	Attempts   int
	ClaimToken string
}

func (r DeliveryRepository) Claim(ctx context.Context, claim DeliveryClaim) (DeliveryReservation, error) {
	if claim.DueAt.IsZero() || claim.WindowEnd.Before(claim.DueAt) {
		return DeliveryReservation{}, errors.New("delivery requires a valid fixed window")
	}
	tx, err := r.beginTenant(ctx, claim.TenantID)
	if err != nil {
		return DeliveryReservation{}, fmt.Errorf("begin delivery claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	window, err := tx.Exec(ctx, `SELECT 1 FROM public.digest_windows
WHERE tenant_id = $1 AND schedule_id = $2 AND due_at = $3 AND window_end_at = $4 AND status = 'pending'
FOR UPDATE`, claim.TenantID, claim.ScheduleID, claim.DueAt, claim.WindowEnd)
	if err != nil {
		return DeliveryReservation{}, fmt.Errorf("lock claim digest window: %w", err)
	}
	if window.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return DeliveryReservation{}, fmt.Errorf("commit closed delivery window: %w", err)
		}
		committed = true
		return DeliveryReservation{}, nil
	}
	// Migration 0004 can move a legacy delivery to the surviving recipient while
	// its idempotency key still hashes the removed recipient. Lock that natural
	// delivery identity and adopt the caller's current key only when the key is
	// not already owned by another tenant delivery.
	if _, err := tx.Exec(ctx, `WITH legacy_delivery AS (
    SELECT id FROM public.deliveries
    WHERE tenant_id = $1 AND schedule_id = $2 AND recipient_id = $3
      AND due_at = $5 AND window_end_at = $6 AND idempotency_key <> $4
    FOR UPDATE
)
UPDATE public.deliveries AS delivery
SET idempotency_key = $4
FROM legacy_delivery AS legacy
WHERE delivery.id = legacy.id
  AND NOT EXISTS (
      SELECT 1 FROM public.deliveries AS key_owner
      WHERE key_owner.tenant_id = $1 AND key_owner.idempotency_key = $4
        AND key_owner.id <> delivery.id
  )`, claim.TenantID, claim.ScheduleID, claim.RecipientID, claim.IdempotencyKey, claim.DueAt, claim.WindowEnd); err != nil {
		return DeliveryReservation{}, fmt.Errorf("normalize legacy delivery key: %w", err)
	}
	var attempts int
	var claimToken, status string
	err = tx.QueryRow(ctx, `INSERT INTO public.deliveries
    (tenant_id, schedule_id, recipient_id, idempotency_key, due_at, window_end_at, status, attempts, claimed_at, claim_token)
VALUES ($1, $2, $3, $4, $5, $6, 'sending', 1, now(), gen_random_uuid())
ON CONFLICT (tenant_id, idempotency_key) DO UPDATE
SET status = CASE
        WHEN deliveries.status = 'sending' AND deliveries.attempts >= 3 THEN 'failed'
        ELSE 'sending'
    END,
    attempts = CASE WHEN deliveries.attempts < 3 THEN deliveries.attempts + 1 ELSE deliveries.attempts END,
    last_error = CASE
        WHEN deliveries.status = 'sending' AND deliveries.attempts >= 3 THEN 'delivery lease expired at retry limit'
        ELSE NULL
    END,
    sent_at = NULL,
    claimed_at = now(),
    claim_token = gen_random_uuid()
WHERE ((deliveries.status = 'pending' AND deliveries.attempts < 3)
   OR (deliveries.status = 'failed' AND deliveries.attempts < 3)
   OR (deliveries.status = 'sending' AND deliveries.claimed_at < now() - interval '15 minutes'))
  AND deliveries.schedule_id = EXCLUDED.schedule_id
  AND deliveries.recipient_id = EXCLUDED.recipient_id
  AND deliveries.due_at = EXCLUDED.due_at
  AND deliveries.window_end_at = EXCLUDED.window_end_at
RETURNING attempts, claim_token::text, status`, claim.TenantID, claim.ScheduleID, claim.RecipientID, claim.IdempotencyKey, claim.DueAt, claim.WindowEnd).Scan(&attempts, &claimToken, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := completeScheduleWindow(ctx, tx, claim.TenantID, claim.ScheduleID, claim.DueAt, claim.WindowEnd); err != nil {
			return DeliveryReservation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return DeliveryReservation{}, fmt.Errorf("commit unclaimed delivery: %w", err)
		}
		committed = true
		return DeliveryReservation{}, nil
	}
	if err != nil {
		return DeliveryReservation{}, fmt.Errorf("claim delivery: %w", err)
	}
	if status != "sending" {
		if err := completeScheduleWindow(ctx, tx, claim.TenantID, claim.ScheduleID, claim.DueAt, claim.WindowEnd); err != nil {
			return DeliveryReservation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return DeliveryReservation{}, fmt.Errorf("commit exhausted delivery: %w", err)
		}
		committed = true
		return DeliveryReservation{Attempts: attempts}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return DeliveryReservation{}, fmt.Errorf("commit delivery claim: %w", err)
	}
	committed = true
	return DeliveryReservation{Claimed: true, Attempts: attempts, ClaimToken: claimToken}, nil
}

func (r DeliveryRepository) FinalizeFailure(ctx context.Context, tenantID, scheduleID, recipientID, idempotencyKey, claimToken string, dueAt, windowEnd time.Time, attempts int, sendErr error) error {
	if sendErr == nil {
		return errors.New("delivery failure requires an error")
	}
	if attempts < 1 || attempts > MaxDeliveryAttempts {
		return fmt.Errorf("attempts must be between 1 and %d", MaxDeliveryAttempts)
	}
	if claimToken == "" || dueAt.IsZero() || windowEnd.Before(dueAt) {
		return errors.New("delivery failure requires a valid claim identity")
	}
	tx, err := r.beginTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("begin delivery failure: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	lock, err := tx.Exec(ctx, `SELECT 1 FROM public.digest_windows
WHERE tenant_id = $1 AND schedule_id = $2 AND due_at = $3 AND window_end_at = $4 AND status = 'pending'
FOR UPDATE`, tenantID, scheduleID, dueAt, windowEnd)
	if err != nil {
		return fmt.Errorf("lock failed digest window: %w", err)
	}
	if lock.RowsAffected() != 1 {
		return ErrDeliveryNotClaimed
	}
	tag, err := tx.Exec(ctx, `UPDATE public.deliveries SET status = 'failed', last_error = $9
WHERE tenant_id = $1 AND idempotency_key = $2 AND schedule_id = $3 AND recipient_id = $4
	AND due_at = $5 AND window_end_at = $6 AND claim_token = $7::uuid AND status = 'sending' AND attempts = $8`,
		tenantID, idempotencyKey, scheduleID, recipientID, dueAt, windowEnd, claimToken, attempts, sendErr.Error())
	if err != nil {
		return fmt.Errorf("finalize failed delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrDeliveryNotClaimed
	}
	if err := completeScheduleWindow(ctx, tx, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delivery failure: %w", err)
	}
	committed = true
	return nil
}

func (r DeliveryRepository) FinalizeSent(ctx context.Context, tenantID, scheduleID, recipientID, idempotencyKey, claimToken string, dueAt, windowEnd time.Time, attempts int, sentAt time.Time) error {
	if attempts < 1 || attempts > MaxDeliveryAttempts {
		return fmt.Errorf("attempts must be between 1 and %d", MaxDeliveryAttempts)
	}
	if claimToken == "" || dueAt.IsZero() || windowEnd.Before(dueAt) {
		return errors.New("delivery requires a valid fixed window")
	}
	tx, err := r.beginTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("begin sent delivery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	lock, err := tx.Exec(ctx, `SELECT 1 FROM public.digest_windows
WHERE tenant_id = $1 AND schedule_id = $2 AND due_at = $3 AND window_end_at = $4 AND status = 'pending'
FOR UPDATE`, tenantID, scheduleID, dueAt, windowEnd)
	if err != nil {
		return fmt.Errorf("lock digest window: %w", err)
	}
	if lock.RowsAffected() != 1 {
		return ErrDeliveryNotClaimed
	}
	tag, err := tx.Exec(ctx, `UPDATE public.deliveries SET status = 'sent', sent_at = $9, last_error = NULL
WHERE tenant_id = $1 AND idempotency_key = $2 AND schedule_id = $3 AND recipient_id = $4
	AND due_at = $5 AND window_end_at = $6 AND claim_token = $7::uuid AND status = 'sending' AND attempts = $8`, tenantID, idempotencyKey, scheduleID, recipientID, dueAt, windowEnd, claimToken, attempts, sentAt)
	if err != nil {
		return fmt.Errorf("finalize sent delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrDeliveryNotClaimed
	}
	if err := completeScheduleWindow(ctx, tx, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit sent delivery: %w", err)
	}
	committed = true
	return nil
}

func (r DeliveryRepository) beginTenant(ctx context.Context, tenantID string) (DeliveryTx, error) {
	if r.DB == nil || tenantID == "" {
		return nil, errors.New("delivery database and tenant are required")
	}
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("set delivery tenant context: %w", err)
	}
	return tx, nil
}

func completeScheduleWindow(ctx context.Context, tx DeliveryTx, tenantID, scheduleID string, dueAt, windowEnd time.Time) error {
	_, err := tx.Exec(ctx, `WITH recipient_state AS (
  SELECT count(*) > 0 AS has_recipients,
         bool_and(COALESCE(d.status = 'sent', false)) AS all_sent,
         bool_and(COALESCE(d.status = 'sent' OR (d.status = 'failed' AND d.attempts >= 3), false)) AS all_terminal,
         bool_or(COALESCE(d.status = 'failed' AND d.attempts >= 3, false)) AS any_exhausted
  FROM public.digest_window_recipients r
  LEFT JOIN public.deliveries d ON d.tenant_id = r.tenant_id AND d.schedule_id = r.schedule_id
    AND d.recipient_id = r.recipient_id AND d.due_at = r.due_at AND d.window_end_at = r.window_end_at
  WHERE r.tenant_id = $1 AND r.schedule_id = $2 AND r.due_at = $3 AND r.window_end_at = $4
), terminal_window AS (
  UPDATE public.digest_windows w
  SET status = CASE WHEN recipient_state.all_sent THEN 'completed' ELSE 'failed' END,
      completed_at = COALESCE(w.completed_at, now())
  FROM recipient_state
  WHERE w.tenant_id = $1 AND w.schedule_id = $2 AND w.due_at = $3 AND w.window_end_at = $4
    AND w.status = 'pending'
    AND recipient_state.has_recipients
    AND (recipient_state.all_sent OR (recipient_state.all_terminal AND recipient_state.any_exhausted))
  RETURNING w.window_end_at
)
UPDATE public.schedules s
SET last_success_at = GREATEST(COALESCE(s.last_success_at, '-infinity'::timestamptz), terminal_window.window_end_at)
FROM terminal_window
WHERE s.tenant_id = $1 AND s.id = $2`, tenantID, scheduleID, dueAt, windowEnd)
	if err != nil {
		return fmt.Errorf("complete delivery window: %w", err)
	}
	return nil
}
