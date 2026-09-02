package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"namo/internal/digest"
)

type digestRepoStub struct {
	work            []DigestWork
	dueErr          error
	claimed         bool
	priorAttempts   int
	claimCount      int
	sentAttempts    int
	failedAttempt   int
	noopCount       int
	claim           DeliveryClaim
	finalized       DeliveryClaim
	finalizeSentErr error
	noopWindowEnd   time.Time
}

func (r *digestRepoStub) DueDigests(context.Context, time.Time) ([]DigestWork, error) {
	return r.work, r.dueErr
}
func (r *digestRepoStub) ClaimDelivery(_ context.Context, claim DeliveryClaim) (DeliveryReservation, error) {
	r.claimCount++
	r.claim = claim
	token := ""
	if r.claimed {
		token = "claim-token"
	}
	return DeliveryReservation{Claimed: r.claimed, Attempts: r.priorAttempts, ClaimToken: token}, nil
}

func TestDigestRunnerNeverExceedsThreeCumulativeAttempts(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 2, 0, 0, time.FixedZone("KST", 9*60*60))
	repo := &digestRepoStub{claimed: true, priorAttempts: 3, work: []DigestWork{{
		TenantID: "tenant-1", ScheduleID: "schedule-1", RecipientID: "recipient-1",
		Recipient: "manager@example.com", DueAt: now.Add(-2 * time.Minute), WindowEnd: now.Add(-time.Minute),
		Notices: []digest.Notice{{Title: "회계감사 용역"}},
	}}}
	mailer := &mailerStub{failures: 1}
	runner := DigestRunner{Repository: repo, Mailer: mailer, From: "monitor@example.com", Now: func() time.Time { return now }}

	result, err := runner.Run(context.Background())
	if err == nil || result.Failed != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if mailer.calls != 1 || repo.failedAttempt != 3 {
		t.Fatalf("mail calls=%d failed attempts=%d, want cumulative cap 3", mailer.calls, repo.failedAttempt)
	}
}

func TestDigestRunnerCountsMessageBuildFailureAgainstPriorAttempts(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 2, 0, 0, time.FixedZone("KST", 9*60*60))
	repo := &digestRepoStub{claimed: true, priorAttempts: 3, work: []DigestWork{{
		TenantID: "tenant-1", ScheduleID: "schedule-1", RecipientID: "recipient-1",
		Recipient: "invalid-address", DueAt: now.Add(-2 * time.Minute), WindowEnd: now.Add(-time.Minute),
		Notices: []digest.Notice{{Title: "회계감사 용역"}},
	}}}
	runner := DigestRunner{Repository: repo, Mailer: &mailerStub{}, From: "monitor@example.com", Now: func() time.Time { return now }}

	result, err := runner.Run(context.Background())
	if err == nil || result.Failed != 1 || repo.failedAttempt != 3 {
		t.Fatalf("result=%+v err=%v failed attempts=%d", result, err, repo.failedAttempt)
	}
}

func (r *digestRepoStub) FinalizeSent(_ context.Context, claim DeliveryClaim, attempts int, _ time.Time) error {
	r.finalized = claim
	r.sentAttempts = attempts
	return r.finalizeSentErr
}

type digestRunJournalStub struct {
	tenantIDs []string
	records   []DigestRunRecord
	listErr   error
	recordErr error
}

func (j *digestRunJournalStub) DigestTenantIDs(context.Context) ([]string, error) {
	return append([]string(nil), j.tenantIDs...), j.listErr
}

func (j *digestRunJournalStub) RecordDigestRun(_ context.Context, record DigestRunRecord) error {
	j.records = append(j.records, record)
	return j.recordErr
}
func (r *digestRepoStub) FinalizeFailure(_ context.Context, _ DeliveryClaim, attempts int, _ error) error {
	r.failedAttempt = attempts
	return nil
}
func (r *digestRepoStub) CompleteNoop(_ context.Context, _, _ string, _ time.Time, windowEnd time.Time) error {
	r.noopCount++
	r.noopWindowEnd = windowEnd
	return nil
}

type mailerStub struct {
	failures int
	calls    int
	message  []byte
}

func (m *mailerStub) Send(_ context.Context, _, _ string, message []byte) error {
	m.calls++
	m.message = append([]byte(nil), message...)
	if m.calls <= m.failures {
		return errors.New("smtp unavailable")
	}
	return nil
}

func TestDigestRunnerClaimsRetriesAndFinalizesExactlyOnce(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 1, 0, 0, time.FixedZone("KST", 9*60*60))
	windowEnd := now.Add(-30 * time.Second)
	repo := &digestRepoStub{claimed: true, work: []DigestWork{{
		TenantID: "tenant-1", ScheduleID: "schedule-1", RecipientID: "recipient-1",
		Recipient: "manager@example.com", DueAt: now.Add(-time.Minute), WindowEnd: windowEnd,
		Notices: []digest.Notice{{Title: "회계감사 용역", URL: "https://example.test/notices/1", Reason: "키워드 회계감사"}},
	}}}
	mailer := &mailerStub{failures: 2}
	runner := DigestRunner{Repository: repo, Mailer: mailer, From: "monitor@example.com", Now: func() time.Time { return now }}

	for attempt := 1; attempt <= 3; attempt++ {
		repo.priorAttempts = attempt
		result, err := runner.Run(context.Background())
		if attempt < 3 && (err == nil || result.Failed != 1) {
			t.Fatalf("attempt=%d result=%+v err=%v", attempt, result, err)
		}
		if attempt == 3 && (err != nil || result.Sent != 1) {
			t.Fatalf("attempt=%d result=%+v err=%v", attempt, result, err)
		}
	}
	if repo.claimCount != 3 || mailer.calls != 3 || repo.sentAttempts != 3 || repo.failedAttempt != 2 {
		t.Fatalf("claim=%d mail=%d sent=%d failed=%d", repo.claimCount, mailer.calls, repo.sentAttempts, repo.failedAttempt)
	}
	if !repo.claim.WindowEnd.Equal(windowEnd) || repo.finalized.ClaimToken != "claim-token" {
		t.Fatalf("delivery claim lost fixed window or fencing token: claim=%+v finalized=%+v", repo.claim, repo.finalized)
	}
	if !strings.Contains(string(mailer.message), "Content-Transfer-Encoding: quoted-printable") {
		t.Fatalf("message is not safe UTF-8 MIME: %s", mailer.message)
	}
	deliveryKey := digest.DeliveryKey("tenant-1", "schedule-1", "recipient-1", now.Add(-time.Minute))
	wantMessageID := "Message-ID: <" + digestMessageID(deliveryKey, repo.work[0].Notices) + ">"
	if !strings.Contains(string(mailer.message), wantMessageID) {
		t.Fatalf("message does not reuse the delivery key: want %q in %s", wantMessageID, mailer.message)
	}

	// A duplicate delivery claim must not send again.
	repo.claimed = false
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mailer.calls != 3 {
		t.Fatalf("duplicate delivery sent again: calls=%d", mailer.calls)
	}
}

func TestDigestMessageIDIsBoundToStableMessageContent(t *testing.T) {
	key := digest.DeliveryKey("tenant-1", "schedule-1", "recipient-1", time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC))
	original := []digest.Notice{{Title: "공고 A", URL: "https://example.test/a", Reason: "키워드"}, {Title: "공고 B", URL: "https://example.test/b", Reason: "지역"}}
	first := digestMessageID(key, original)
	if again := digestMessageID(key, append([]digest.Notice(nil), original...)); first != again {
		t.Fatalf("same digest body changed Message-ID: %q != %q", first, again)
	}
	if changed := digestMessageID(key, original[:1]); first == changed {
		t.Fatalf("changed digest body reused Message-ID: %q", first)
	}
	if !strings.HasSuffix(first, "@namo.invalid") {
		t.Fatalf("unexpected Message-ID domain: %q", first)
	}
}

func TestDigestRunnerSkipsMailAndCompletesWindowWhenNoMatches(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 1, 0, 0, time.FixedZone("KST", 9*60*60))
	windowEnd := now.Add(-30 * time.Second)
	repo := &digestRepoStub{work: []DigestWork{{TenantID: "tenant-1", ScheduleID: "schedule-1", DueAt: now.Add(-time.Minute), WindowEnd: windowEnd}}}
	mailer := &mailerStub{}
	runner := DigestRunner{Repository: repo, Mailer: mailer, From: "monitor@example.com", Now: func() time.Time { return now }}

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || repo.noopCount != 1 || repo.claimCount != 0 || mailer.calls != 0 {
		t.Fatalf("result=%+v noop=%d claim=%d mail=%d", result, repo.noopCount, repo.claimCount, mailer.calls)
	}
	if !repo.noopWindowEnd.Equal(windowEnd) {
		t.Fatalf("no-op lost fixed window end: %v", repo.noopWindowEnd)
	}
}

func TestScheduledDigestRecordsDueDiscoveryFailureForEveryTenant(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	repo := &digestRepoStub{dueErr: errors.New("snapshot query failed")}
	journal := &digestRunJournalStub{tenantIDs: []string{"tenant-a", "tenant-b"}}
	runner := DigestRunner{Repository: repo, Mailer: &mailerStub{}, From: "monitor@example.com", Now: func() time.Time { return now }}

	err := runScheduledDigest(context.Background(), now, runner, journal)
	if err == nil || !strings.Contains(err.Error(), "snapshot query failed") {
		t.Fatalf("scheduled digest error=%v", err)
	}
	if len(journal.records) != 2 {
		t.Fatalf("recorded runs=%+v", journal.records)
	}
	for index, tenantID := range []string{"tenant-a", "tenant-b"} {
		record := journal.records[index]
		if record.TenantID != tenantID || record.Status != "failed" || record.Err == nil {
			t.Fatalf("record[%d]=%+v", index, record)
		}
	}
}

func TestScheduledDigestRecordsFinalizeErrorForAffectedTenant(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 1, 0, 0, time.FixedZone("KST", 9*60*60))
	repo := &digestRepoStub{
		claimed: true, priorAttempts: 1, finalizeSentErr: errors.New("commit delivery failed"),
		work: []DigestWork{{
			TenantID: "tenant-a", ScheduleID: "schedule-a", RecipientID: "recipient-a",
			Recipient: "manager@example.com", DueAt: now.Add(-time.Minute), WindowEnd: now,
			Notices: []digest.Notice{{Title: "회계감사 용역"}},
		}},
	}
	journal := &digestRunJournalStub{}
	runner := DigestRunner{Repository: repo, Mailer: &mailerStub{}, From: "monitor@example.com", Now: func() time.Time { return now }}

	err := runScheduledDigest(context.Background(), now, runner, journal)
	if err == nil || !strings.Contains(err.Error(), "commit delivery failed") {
		t.Fatalf("scheduled digest error=%v", err)
	}
	if len(journal.records) != 1 || journal.records[0].TenantID != "tenant-a" || journal.records[0].Status != "failed" || journal.records[0].Err == nil {
		t.Fatalf("recorded runs=%+v", journal.records)
	}
}

func TestScheduledDigestRecordsSuccessOnlyWhenTenantHadDueWork(t *testing.T) {
	now := time.Date(2026, 9, 1, 7, 1, 0, 0, time.FixedZone("KST", 9*60*60))
	finishedAt := now.Add(20 * time.Second)
	repo := &digestRepoStub{work: []DigestWork{{
		TenantID: "tenant-a", ScheduleID: "schedule-a", DueAt: now.Add(-time.Minute), WindowEnd: now,
	}}}
	journal := &digestRunJournalStub{tenantIDs: []string{"tenant-a", "idle-tenant"}}
	runner := DigestRunner{Repository: repo, Mailer: &mailerStub{}, From: "monitor@example.com", Now: func() time.Time { return now }}

	if err := runScheduledDigest(context.Background(), now, runner, journal, func() time.Time { return finishedAt }); err != nil {
		t.Fatal(err)
	}
	if len(journal.records) != 1 || journal.records[0].TenantID != "tenant-a" || journal.records[0].Status != "succeeded" || journal.records[0].Skipped != 1 {
		t.Fatalf("recorded runs=%+v", journal.records)
	}
	if !journal.records[0].StartedAt.Equal(now) || !journal.records[0].FinishedAt.Equal(finishedAt) {
		t.Fatalf("digest run timing=%+v", journal.records[0])
	}
}
