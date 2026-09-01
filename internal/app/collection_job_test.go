package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type lockRow struct{ locked bool }

func (r lockRow) Scan(dest ...any) error {
	*(dest[0].(*bool)) = r.locked
	return nil
}

type lockSessionStub struct {
	locked   bool
	queries  []string
	released bool
}

func (s *lockSessionStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	s.queries = append(s.queries, sql)
	return lockRow{locked: s.locked}
}
func (s *lockSessionStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.queries = append(s.queries, sql)
	return pgconn.NewCommandTag("SELECT 1"), nil
}
func (s *lockSessionStub) Release() { s.released = true }

func TestCollectionJobUsesTransactionScopedAdvisoryLock(t *testing.T) {
	session := &lockSessionStub{locked: true}
	runs := 0
	job := CollectionJob{
		Acquire: func(context.Context) (AdvisorySession, error) { return session, nil },
		Run: func(context.Context) (CollectionResult, error) {
			runs++
			return CollectionResult{Fetched: 4}, nil
		},
	}
	result, err := job.RunLocked(context.Background())
	if err != nil || result.Fetched != 4 || runs != 1 || !session.released {
		t.Fatalf("result=%+v err=%v runs=%d released=%t", result, err, runs, session.released)
	}
	if len(session.queries) != 3 || session.queries[0] != "BEGIN" || !strings.Contains(session.queries[1], "pg_try_advisory_xact_lock") || session.queries[2] != "COMMIT" {
		t.Fatalf("lock queries = %#v", session.queries)
	}
	for _, query := range session.queries {
		if strings.Contains(query, "pg_advisory_unlock") {
			t.Fatalf("session-scoped unlock must not be used: %#v", session.queries)
		}
	}
}

func TestCollectionJobSkipsConcurrentRun(t *testing.T) {
	session := &lockSessionStub{locked: false}
	job := CollectionJob{
		Acquire: func(context.Context) (AdvisorySession, error) { return session, nil },
		Run: func(context.Context) (CollectionResult, error) {
			t.Fatal("locked collection ran")
			return CollectionResult{}, nil
		},
	}
	_, err := job.RunLocked(context.Background())
	if !errors.Is(err, ErrCollectionRunning) || !session.released {
		t.Fatalf("err=%v released=%t", err, session.released)
	}
	if len(session.queries) != 3 || session.queries[2] != "ROLLBACK" {
		t.Fatalf("unlocked transaction queries = %#v", session.queries)
	}
}
