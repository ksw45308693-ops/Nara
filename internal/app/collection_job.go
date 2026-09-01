package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrCollectionRunning = errors.New("collection is already running")

const collectionAdvisoryLock int64 = 677266706360138579

type AdvisorySession interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Release()
}

type CollectionJob struct {
	Acquire func(context.Context) (AdvisorySession, error)
	Run     func(context.Context) (CollectionResult, error)
}

func (j CollectionJob) RunLocked(ctx context.Context) (result CollectionResult, runErr error) {
	if j.Acquire == nil || j.Run == nil {
		return result, errors.New("collection job dependencies are required")
	}
	session, err := j.Acquire(ctx)
	if err != nil {
		return result, fmt.Errorf("acquire collection lock session: %w", err)
	}
	if session == nil {
		return result, errors.New("collection lock session is nil")
	}
	defer session.Release()
	if _, err := session.Exec(ctx, `BEGIN`); err != nil {
		return result, fmt.Errorf("begin collection lock transaction: %w", err)
	}
	transactionOpen := true
	defer func() {
		if !transactionOpen {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := session.Exec(rollbackContext, `ROLLBACK`); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("rollback collection lock transaction: %w", err))
		}
	}()
	locked := false
	if err := session.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_xact_lock($1)`, collectionAdvisoryLock).Scan(&locked); err != nil {
		return result, fmt.Errorf("acquire collection advisory lock: %w", err)
	}
	if !locked {
		return result, ErrCollectionRunning
	}
	result, runErr = j.Run(ctx)
	commitContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := session.Exec(commitContext, `COMMIT`); err != nil {
		return result, errors.Join(runErr, fmt.Errorf("commit collection lock transaction: %w", err))
	}
	transactionOpen = false
	return result, runErr
}
