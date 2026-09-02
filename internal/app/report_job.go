package app

import (
	"context"
	"errors"
	"time"
)

const reportJournalCleanupTimeout = 5 * time.Second

type ReportRunRecord struct {
	TenantID                   string
	Status                     string
	StartedAt, FinishedAt      time.Time
	Generated, Failed, Skipped int
	Err                        error
}

type ReportRunJournal interface {
	ReportTenantIDs(context.Context) ([]string, error)
	RecordReportRun(context.Context, ReportRunRecord) error
}

func runScheduledReport(ctx context.Context, startedAt time.Time, runner ReportRunner, journal ReportRunJournal, clocks ...func() time.Time) error {
	if journal == nil {
		return errors.New("report run journal is required")
	}
	result, runErr := runner.Run(ctx)
	journalCtx, cancelJournal := context.WithTimeout(context.WithoutCancel(ctx), reportJournalCleanupTimeout)
	defer cancelJournal()
	finishedAt := time.Now()
	if len(clocks) > 0 && clocks[0] != nil {
		finishedAt = clocks[0]()
	}
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	tenantIDs, listErr := journal.ReportTenantIDs(journalCtx)
	if listErr != nil {
		return errors.Join(runErr, listErr)
	}
	records := make(map[string]ReportRunRecord, len(tenantIDs))
	var order []string
	for _, tenant := range result.TenantRuns {
		status := "succeeded"
		if tenant.Err != nil {
			status = "failed"
		}
		records[tenant.TenantID] = ReportRunRecord{
			TenantID: tenant.TenantID, Status: status, StartedAt: startedAt, FinishedAt: finishedAt,
			Generated: tenant.Generated, Failed: tenant.Failed, Skipped: tenant.Skipped, Err: tenant.Err,
		}
		order = append(order, tenant.TenantID)
	}
	for _, tenantID := range tenantIDs {
		if _, exists := records[tenantID]; exists {
			continue
		}
		record := ReportRunRecord{TenantID: tenantID, Status: "succeeded", StartedAt: startedAt, FinishedAt: finishedAt, Skipped: 1}
		if runErr != nil && len(result.TenantRuns) == 0 {
			record.Status, record.Failed, record.Skipped, record.Err = "failed", 1, 0, runErr
		}
		records[tenantID] = record
		order = append(order, tenantID)
	}
	var journalErr error
	for _, tenantID := range order {
		if err := journal.RecordReportRun(journalCtx, records[tenantID]); err != nil {
			journalErr = errors.Join(journalErr, err)
		}
	}
	return errors.Join(runErr, journalErr)
}
