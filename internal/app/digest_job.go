package app

import (
	"context"
	"errors"
	"time"
)

type DigestRunRecord struct {
	TenantID              string
	Status                string
	StartedAt, FinishedAt time.Time
	Sent, Failed, Skipped int
	Err                   error
}

type DigestRunJournal interface {
	DigestTenantIDs(context.Context) ([]string, error)
	RecordDigestRun(context.Context, DigestRunRecord) error
}

func runScheduledDigest(ctx context.Context, startedAt time.Time, runner DigestRunner, journal DigestRunJournal, clocks ...func() time.Time) error {
	if journal == nil {
		return errors.New("digest run journal is required")
	}
	result, runErr := runner.Run(ctx)
	finishedAt := time.Now()
	if len(clocks) > 0 && clocks[0] != nil {
		finishedAt = clocks[0]()
	}
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	records := make([]DigestRunRecord, 0, len(result.TenantRuns))
	for _, tenant := range result.TenantRuns {
		status := "succeeded"
		if tenant.Err != nil {
			status = "failed"
		}
		records = append(records, DigestRunRecord{
			TenantID: tenant.TenantID, Status: status, StartedAt: startedAt, FinishedAt: finishedAt,
			Sent: tenant.Sent, Failed: tenant.Failed, Skipped: tenant.Skipped, Err: tenant.Err,
		})
	}
	if len(records) == 0 && runErr != nil {
		tenantIDs, err := journal.DigestTenantIDs(ctx)
		if err != nil {
			return errors.Join(runErr, err)
		}
		for _, tenantID := range tenantIDs {
			records = append(records, DigestRunRecord{
				TenantID: tenantID, Status: "failed", StartedAt: startedAt, FinishedAt: finishedAt, Err: runErr,
			})
		}
	}
	var journalErr error
	for _, record := range records {
		if err := journal.RecordDigestRun(ctx, record); err != nil {
			journalErr = errors.Join(journalErr, err)
		}
	}
	return errors.Join(runErr, journalErr)
}
