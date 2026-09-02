package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type reportRunExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (r *PostgresRepository) ReportTenantIDs(ctx context.Context) ([]string, error) {
	tenants, err := r.tenantCatalog(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		ids = append(ids, tenant.ID)
	}
	return ids, nil
}

func (r *PostgresRepository) RecordReportRun(ctx context.Context, record ReportRunRecord) error {
	if record.TenantID == "" || (record.Status != "succeeded" && record.Status != "failed") || record.StartedAt.IsZero() || record.FinishedAt.IsZero() {
		return errors.New("valid tenant report run is required")
	}
	return r.withTenant(ctx, record.TenantID, func(tx pgx.Tx) error {
		return insertReportRun(ctx, tx, record)
	})
}

func insertReportRun(ctx context.Context, execer reportRunExecer, record ReportRunRecord) error {
	if execer == nil {
		return errors.New("report run database is required")
	}
	detail := map[string]any{"generated": record.Generated, "failed": record.Failed, "skipped": record.Skipped}
	if record.Err != nil {
		detail["error"] = boundedReportRunError(record.Err)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode report run: %w", err)
	}
	if _, err := execer.Exec(ctx, `INSERT INTO public.job_runs
    (tenant_id, kind, status, started_at, finished_at, detail)
VALUES ($1::uuid, 'report', $2, $3, $4, $5::jsonb)`,
		record.TenantID, record.Status, record.StartedAt, record.FinishedAt, string(payload)); err != nil {
		return fmt.Errorf("record tenant report run: %w", err)
	}
	return nil
}

func boundedReportRunError(err error) string {
	const maxBytes = 2048
	message := err.Error()
	if len(message) <= maxBytes {
		return message
	}
	message = message[:maxBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
