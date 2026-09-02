package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type reportRunJournalFake struct {
	tenantIDs      []string
	records        []ReportRunRecord
	requireCleanup bool
	cleanupCalls   int
	cancelOnList   context.CancelFunc
}

func (f *reportRunJournalFake) checkContext(ctx context.Context) error {
	if !f.requireCleanup {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("cleanup context has no deadline")
	}
	f.cleanupCalls++
	return nil
}

func (f *reportRunJournalFake) ReportTenantIDs(ctx context.Context) ([]string, error) {
	if f.cancelOnList != nil {
		f.cancelOnList()
		f.cancelOnList = nil
	}
	if err := f.checkContext(ctx); err != nil {
		return nil, err
	}
	return append([]string(nil), f.tenantIDs...), nil
}

func (f *reportRunJournalFake) RecordReportRun(ctx context.Context, record ReportRunRecord) error {
	if err := f.checkContext(ctx); err != nil {
		return err
	}
	f.records = append(f.records, record)
	return nil
}

func TestScheduledReportJobRecordsTenantSuccessAndSkippedResult(t *testing.T) {
	startedAt := time.Date(2026, 9, 3, 0, 5, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	repository := &reportRepositoryFake{due: []ReportWork{scheduledReportWork(1)}}
	journal := &reportRunJournalFake{tenantIDs: []string{"tenant-1", "tenant-idle"}}
	runner := ReportRunner{Repository: repository, Writer: &reportWriterFake{}}

	if err := runScheduledReport(context.Background(), startedAt, runner, journal, func() time.Time { return finishedAt }); err != nil {
		t.Fatal(err)
	}
	if len(journal.records) != 2 {
		t.Fatalf("records=%+v", journal.records)
	}
	if journal.records[0].TenantID != "tenant-1" || journal.records[0].Status != "succeeded" || journal.records[0].Generated != 1 {
		t.Fatalf("generated record=%+v", journal.records[0])
	}
	if journal.records[1].TenantID != "tenant-idle" || journal.records[1].Status != "succeeded" || journal.records[1].Skipped != 1 {
		t.Fatalf("skipped record=%+v", journal.records[1])
	}
	if !journal.records[0].StartedAt.Equal(startedAt) || !journal.records[0].FinishedAt.Equal(finishedAt) {
		t.Fatalf("timing=%+v", journal.records[0])
	}
}

func TestScheduledReportJobRecordsDiscoveryFailureForEveryTenant(t *testing.T) {
	startedAt := time.Date(2026, 9, 3, 0, 5, 0, 0, time.UTC)
	repository := &reportRepositoryFake{dueErr: errors.New("snapshot query failed")}
	journal := &reportRunJournalFake{tenantIDs: []string{"tenant-a", "tenant-b"}}
	err := runScheduledReport(context.Background(), startedAt, ReportRunner{Repository: repository, Writer: &reportWriterFake{}}, journal)
	if err == nil || len(journal.records) != 2 {
		t.Fatalf("records=%+v err=%v", journal.records, err)
	}
	for _, record := range journal.records {
		if record.Status != "failed" || record.Failed != 1 || record.Err == nil {
			t.Fatalf("record=%+v", record)
		}
	}
}

func TestScheduledReportJobKeepsIdleTenantSkippedWhenAnotherTenantFails(t *testing.T) {
	startedAt := time.Date(2026, 9, 3, 0, 5, 0, 0, time.UTC)
	repository := &reportRepositoryFake{
		due:      []ReportWork{scheduledReportWork(1)},
		reclaims: []ReportWork{scheduledReportWork(2), scheduledReportWork(3)},
	}
	journal := &reportRunJournalFake{tenantIDs: []string{"tenant-1", "tenant-idle"}}
	err := runScheduledReport(context.Background(), startedAt, ReportRunner{Repository: repository, Writer: &reportWriterFake{failures: 3}}, journal)
	if err == nil || len(journal.records) != 2 {
		t.Fatalf("records=%+v err=%v", journal.records, err)
	}
	if journal.records[0].TenantID != "tenant-1" || journal.records[0].Status != "failed" || journal.records[0].Failed != 1 {
		t.Fatalf("failed tenant record=%+v", journal.records[0])
	}
	if journal.records[1].TenantID != "tenant-idle" || journal.records[1].Status != "succeeded" || journal.records[1].Skipped != 1 || journal.records[1].Err != nil {
		t.Fatalf("idle tenant record=%+v", journal.records[1])
	}
}

func TestScheduledReportJobUsesBoundedCleanupContextAfterCancellation(t *testing.T) {
	startedAt := time.Date(2026, 9, 3, 0, 5, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	repository := &reportRepositoryFake{due: []ReportWork{scheduledReportWork(1)}}
	journal := &reportRunJournalFake{tenantIDs: []string{"tenant-1", "tenant-idle"}, requireCleanup: true}
	err := runScheduledReport(ctx, startedAt, ReportRunner{Repository: repository, Writer: &reportWriterFake{cancel: cancel}}, journal)
	if !errors.Is(err, context.Canceled) || len(journal.records) != 2 || journal.cleanupCalls != 3 {
		t.Fatalf("records=%+v cleanup calls=%d err=%v", journal.records, journal.cleanupCalls, err)
	}
	if journal.records[0].Status != "failed" || journal.records[0].Err == nil || journal.records[1].Status != "succeeded" || journal.records[1].Skipped != 1 {
		t.Fatalf("canceled records=%+v", journal.records)
	}
}

func TestScheduledReportJobCleanupSurvivesCancellationAfterRunnerReturns(t *testing.T) {
	startedAt := time.Date(2026, 9, 3, 0, 5, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	journal := &reportRunJournalFake{
		tenantIDs: []string{"tenant-idle"}, requireCleanup: true, cancelOnList: cancel,
	}
	err := runScheduledReport(ctx, startedAt, ReportRunner{Repository: &reportRepositoryFake{}, Writer: &reportWriterFake{}}, journal)
	if err != nil || len(journal.records) != 1 || journal.cleanupCalls != 2 {
		t.Fatalf("records=%+v cleanup calls=%d err=%v", journal.records, journal.cleanupCalls, err)
	}
	if journal.records[0].Status != "succeeded" || journal.records[0].Skipped != 1 {
		t.Fatalf("record=%+v", journal.records[0])
	}
}
