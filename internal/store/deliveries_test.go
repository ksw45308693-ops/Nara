package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type deliveryFakeDB struct {
	tx     *deliveryFakeTx
	begins int
}

func (d *deliveryFakeDB) Begin(context.Context) (DeliveryTx, error) { d.begins++; return d.tx, nil }

type deliveryCall struct {
	sql  string
	args []any
}
type deliveryFakeTx struct {
	calls                 []deliveryCall
	rows                  []int64
	queryResults          []deliveryQueryResult
	committed, rolledBack bool
}

type deliveryQueryResult struct {
	attempts   int
	claimToken string
	status     string
	err        error
}

type deliveryRow struct{ result deliveryQueryResult }

func (r deliveryRow) Scan(dest ...any) error {
	if r.result.err != nil {
		return r.result.err
	}
	*(dest[0].(*int)) = r.result.attempts
	if len(dest) > 1 {
		*(dest[1].(*string)) = r.result.claimToken
	}
	if len(dest) > 2 {
		*(dest[2].(*string)) = r.result.status
	}
	return nil
}

func (d *deliveryFakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.calls = append(d.calls, deliveryCall{sql: sql, args: args})
	rows := int64(1)
	if !strings.Contains(sql, "set_config") && len(d.rows) > 0 {
		rows, d.rows = d.rows[0], d.rows[1:]
	}
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", rows)), nil
}
func (d *deliveryFakeTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	d.calls = append(d.calls, deliveryCall{sql: sql, args: args})
	if len(d.queryResults) == 0 {
		return deliveryRow{result: deliveryQueryResult{err: pgx.ErrNoRows}}
	}
	result := d.queryResults[0]
	d.queryResults = d.queryResults[1:]
	return deliveryRow{result: result}
}
func (d *deliveryFakeTx) Commit(context.Context) error   { d.committed = true; return nil }
func (d *deliveryFakeTx) Rollback(context.Context) error { d.rolledBack = true; return nil }

func TestDeliveryClaimReturnsFalseForExistingIdempotencyKey(t *testing.T) {
	tx := &deliveryFakeTx{rows: []int64{1}}
	repo := DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}
	dueAt := time.Now()
	windowEnd := dueAt.Add(time.Hour)
	claimed, err := repo.Claim(context.Background(), DeliveryClaim{TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient", IdempotencyKey: "key", DueAt: dueAt, WindowEnd: windowEnd})
	if err != nil || claimed.Claimed || !tx.committed || tx.rolledBack {
		t.Fatalf("claimed=%+v err=%v committed=%t rollback=%t", claimed, err, tx.committed, tx.rolledBack)
	}
	if !strings.Contains(tx.calls[1].sql, "FOR UPDATE") || !strings.Contains(tx.calls[3].sql, "deliveries.status = 'failed'") || !strings.Contains(tx.calls[3].sql, "deliveries.attempts < 3") {
		t.Fatalf("window was not locked or sent/exhausted delivery can be reclaimed: %#v", tx.calls)
	}
	if !strings.Contains(tx.calls[3].sql, "window_end_at") || !strings.Contains(tx.calls[3].sql, "recipient_id = EXCLUDED.recipient_id") || tx.calls[3].args[5] != windowEnd {
		t.Fatalf("fixed snapshot or natural identity was not enforced: %#v", tx.calls[3])
	}
	if len(tx.calls) != 5 || !strings.Contains(tx.calls[4].sql, "all_terminal") || !strings.Contains(tx.calls[4].sql, "status = 'failed'") {
		t.Fatalf("unclaimed terminal window was not evaluated: %#v", tx.calls)
	}
}

func TestDeliveryClaimSetsTenantContextBeforeRLSWrite(t *testing.T) {
	tx := &deliveryFakeTx{}
	repo := DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}
	_, _ = repo.Claim(context.Background(), DeliveryClaim{
		TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient", IdempotencyKey: "key", DueAt: time.Now(), WindowEnd: time.Now(),
	})
	if len(tx.calls) == 0 || !strings.Contains(tx.calls[0].sql, "set_config('app.tenant_id'") {
		t.Fatalf("first delivery statement does not establish RLS tenant context: %#v", tx.calls)
	}
}

func TestDeliveryQueriesUsePublicSchema(t *testing.T) {
	tx := &deliveryFakeTx{}
	due := time.Now()
	_, _ = (DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}).Claim(context.Background(), DeliveryClaim{
		TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient", IdempotencyKey: "key", DueAt: due, WindowEnd: due.Add(time.Hour),
	})
	for _, call := range tx.calls[1:] {
		for _, relation := range []string{"digest_windows", "deliveries", "digest_window_recipients", "schedules"} {
			if strings.Contains(call.sql, relation) && !strings.Contains(call.sql, "public."+relation) {
				t.Fatalf("unqualified %s in SQL: %s", relation, call.sql)
			}
		}
	}
}

func TestFreshDeliveryClaimReservesFirstSMTPAttempt(t *testing.T) {
	dueAt := time.Now()
	tx := &deliveryFakeTx{
		rows:         []int64{1},
		queryResults: []deliveryQueryResult{{attempts: 1, claimToken: "first-token", status: "sending"}},
	}
	reservation, err := (DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}).Claim(context.Background(), DeliveryClaim{
		TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient", IdempotencyKey: "key",
		DueAt: dueAt, WindowEnd: dueAt.Add(time.Hour),
	})
	if err != nil || !reservation.Claimed || reservation.Attempts != 1 || reservation.ClaimToken != "first-token" {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	if !strings.Contains(tx.calls[3].sql, "'sending', 1") || !strings.Contains(tx.calls[3].sql, "deliveries.attempts + 1") {
		t.Fatalf("SMTP attempt was not durably reserved: %s", tx.calls[3].sql)
	}
}

func TestDeliveryClaimNormalizesLegacyKeyBeforeReservation(t *testing.T) {
	dueAt := time.Now()
	tx := &deliveryFakeTx{
		rows:         []int64{1},
		queryResults: []deliveryQueryResult{{attempts: 2, claimToken: "legacy-token", status: "sending"}},
	}
	claim := DeliveryClaim{
		TenantID: "tenant", ScheduleID: "schedule", RecipientID: "survivor", IdempotencyKey: "survivor-key",
		DueAt: dueAt, WindowEnd: dueAt.Add(time.Hour),
	}
	reservation, err := (DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}).Claim(context.Background(), claim)
	if err != nil || !reservation.Claimed || reservation.Attempts != 2 {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	if len(tx.calls) < 4 || !strings.Contains(tx.calls[2].sql, "FOR UPDATE") ||
		!strings.Contains(tx.calls[2].sql, "UPDATE public.deliveries") ||
		!strings.Contains(tx.calls[2].sql, "idempotency_key = $4") ||
		!strings.Contains(tx.calls[2].sql, "NOT EXISTS") {
		t.Fatalf("legacy key was not safely normalized: %#v", tx.calls)
	}
	if !strings.Contains(tx.calls[3].sql, "deliveries.status = 'pending'") {
		t.Fatalf("legacy pending delivery cannot resume: %s", tx.calls[3].sql)
	}
}

func TestFinalizeFailureRecordsAttemptCountAndErrorInTransaction(t *testing.T) {
	tx := &deliveryFakeTx{rows: []int64{1, 1, 1}}
	repo := DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}
	dueAt := time.Now()
	windowEnd := dueAt.Add(time.Hour)
	if err := repo.FinalizeFailure(context.Background(), "tenant", "schedule", "recipient", "key", "claim-token", dueAt, windowEnd, 3, fmt.Errorf("SMTP rejected")); err != nil {
		t.Fatal(err)
	}
	if len(tx.calls) != 4 || !strings.Contains(tx.calls[1].sql, "FOR UPDATE") || tx.calls[2].args[7] != 3 || tx.calls[2].args[8] != "SMTP rejected" || !strings.Contains(tx.calls[2].sql, "recipient_id") || !strings.Contains(tx.calls[2].sql, "claim_token") || !strings.Contains(tx.calls[2].sql, "AND attempts = $8") || strings.Contains(tx.calls[2].sql, "SET status = 'failed', attempts") || !strings.Contains(tx.calls[3].sql, "all_terminal") || !strings.Contains(tx.calls[3].sql, "status = 'failed'") || !tx.committed || tx.rolledBack {
		t.Fatalf("calls=%#v committed=%t rollback=%t", tx.calls, tx.committed, tx.rolledBack)
	}
}

func TestFinalizeFailureRejectsAttemptCountsOutsideRetryLimit(t *testing.T) {
	tx := &deliveryFakeTx{}
	dueAt := time.Now()
	err := (DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}).FinalizeFailure(context.Background(), "tenant", "schedule", "recipient", "key", "claim-token", dueAt, dueAt.Add(time.Hour), 4, errors.New("SMTP rejected"))
	if err == nil || len(tx.calls) != 0 {
		t.Fatalf("err=%v calls=%#v", err, tx.calls)
	}
}

func TestFinalizeSentUpdatesDeliveryAndScheduleInOneTransaction(t *testing.T) {
	tx := &deliveryFakeTx{rows: []int64{1, 1, 1}}
	db := &deliveryFakeDB{tx: tx}
	dueAt := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	sentAt := windowEnd.Add(time.Minute)
	if err := (DeliveryRepository{DB: db}).FinalizeSent(context.Background(), "tenant", "schedule", "recipient", "key", "claim-token", dueAt, windowEnd, 2, sentAt); err != nil {
		t.Fatal(err)
	}
	if db.begins != 1 || len(tx.calls) != 4 || !tx.committed || tx.rolledBack {
		t.Fatalf("begins=%d calls=%d committed=%t rollback=%t", db.begins, len(tx.calls), tx.committed, tx.rolledBack)
	}
	if !strings.Contains(tx.calls[1].sql, "FOR UPDATE") || tx.calls[2].args[3] != "recipient" || tx.calls[2].args[6] != "claim-token" || tx.calls[2].args[5] != windowEnd || tx.calls[2].args[7] != 2 || !strings.Contains(tx.calls[2].sql, "AND attempts = $8") || strings.Contains(tx.calls[2].sql, "SET status = 'sent', attempts") || tx.calls[3].args[0] != "tenant" || tx.calls[3].args[1] != "schedule" || tx.calls[3].args[3] != windowEnd || !strings.Contains(tx.calls[3].sql, "digest_windows") {
		t.Fatalf("calls=%#v", tx.calls)
	}
}

func TestFinalizeSentRollsBackWhenClaimIsNotActive(t *testing.T) {
	tx := &deliveryFakeTx{rows: []int64{1, 0}}
	now := time.Now()
	err := (DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}).FinalizeSent(context.Background(), "tenant", "schedule", "recipient", "key", "claim-token", now, now, 1, now)
	if err == nil || tx.committed || !tx.rolledBack {
		t.Fatalf("err=%v committed=%t rollback=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestTwoRecipientWindowAdvancesOnlyAfterFailedRecipientIsReclaimedAndSent(t *testing.T) {
	dueAt := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	partial := &deliveryFakeTx{rows: []int64{1, 1, 0}}
	if err := (DeliveryRepository{DB: &deliveryFakeDB{tx: partial}}).FinalizeSent(context.Background(), "tenant", "schedule", "recipient-a", "recipient-a", "token-a", dueAt, dueAt.Add(time.Hour), 1, dueAt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(partial.calls[3].sql, "all_terminal") || !strings.Contains(partial.calls[3].sql, "digest_window_recipients") || partial.calls[3].args[2] != dueAt {
		t.Fatalf("partial completion=%#v", partial.calls)
	}
	failed := &deliveryFakeTx{rows: []int64{1, 1, 1}}
	if err := (DeliveryRepository{DB: &deliveryFakeDB{tx: failed}}).FinalizeFailure(context.Background(), "tenant", "schedule", "recipient-b", "recipient-b", "token-b", dueAt, dueAt.Add(time.Hour), 3, errors.New("SMTP rejected")); err != nil {
		t.Fatal(err)
	}
	if len(failed.calls) != 4 || !strings.Contains(failed.calls[3].sql, "all_terminal") || !strings.Contains(failed.calls[3].sql, "status = 'failed'") {
		t.Fatalf("failed recipient did not evaluate terminal window: %#v", failed.calls)
	}
	exhausted := &deliveryFakeTx{}
	claim, err := (DeliveryRepository{DB: &deliveryFakeDB{tx: exhausted}}).Claim(context.Background(), DeliveryClaim{TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient-b", IdempotencyKey: "recipient-b", DueAt: dueAt, WindowEnd: dueAt.Add(time.Hour)})
	if err != nil || claim.Claimed {
		t.Fatalf("exhausted delivery reclaimed: %+v err=%v", claim, err)
	}
	reclaimed := &deliveryFakeTx{rows: []int64{1}, queryResults: []deliveryQueryResult{{attempts: 2, claimToken: "new-token", status: "sending"}}}
	claimed, err := (DeliveryRepository{DB: &deliveryFakeDB{tx: reclaimed}}).Claim(context.Background(), DeliveryClaim{TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient-b", IdempotencyKey: "recipient-b", DueAt: dueAt, WindowEnd: dueAt.Add(time.Hour)})
	if err != nil || !claimed.Claimed || claimed.Attempts != 2 || claimed.ClaimToken != "new-token" || !strings.Contains(reclaimed.calls[3].sql, "15 minutes") {
		t.Fatalf("reclaim=%+v err=%v calls=%#v", claimed, err, reclaimed.calls)
	}
	recovered := &deliveryFakeTx{rows: []int64{1, 1, 1}}
	if err := (DeliveryRepository{DB: &deliveryFakeDB{tx: recovered}}).FinalizeSent(context.Background(), "tenant", "schedule", "recipient-b", "recipient-b", "new-token", dueAt, dueAt.Add(time.Hour), 3, dueAt); err != nil {
		t.Fatal(err)
	}
	if recovered.calls[3].args[2] != dueAt || !recovered.committed {
		t.Fatalf("recovery completion=%#v", recovered.calls)
	}
}

func TestStaleClaimAtRetryLimitClosesWithoutReturningWork(t *testing.T) {
	dueAt := time.Now()
	tx := &deliveryFakeTx{
		rows:         []int64{1},
		queryResults: []deliveryQueryResult{{attempts: 3, claimToken: "closed-token", status: "failed"}},
	}
	reservation, err := (DeliveryRepository{DB: &deliveryFakeDB{tx: tx}}).Claim(context.Background(), DeliveryClaim{
		TenantID: "tenant", ScheduleID: "schedule", RecipientID: "recipient", IdempotencyKey: "key",
		DueAt: dueAt, WindowEnd: dueAt.Add(time.Hour),
	})
	if err != nil || reservation.Claimed || reservation.Attempts != 3 || !tx.committed || !strings.Contains(tx.calls[3].sql, "status = CASE") || len(tx.calls) != 5 || !strings.Contains(tx.calls[4].sql, "all_terminal") {
		t.Fatalf("reservation=%+v err=%v calls=%#v committed=%t", reservation, err, tx.calls, tx.committed)
	}
}
