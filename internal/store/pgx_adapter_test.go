package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

var _ PgxTxStarter = (*pgx.Conn)(nil)

type pgxStarterStub struct {
	calls int
	err   error
}

func (s *pgxStarterStub) Begin(context.Context) (pgx.Tx, error) { s.calls++; return nil, s.err }

func TestPgxAdaptersDelegateBeginErrors(t *testing.T) {
	want := errors.New("database unavailable")
	stub := &pgxStarterStub{err: want}
	if _, err := (PgxMigrationBeginner{DB: stub}).Begin(context.Background()); !errors.Is(err, want) {
		t.Fatalf("migration err=%v", err)
	}
	if _, err := (PgxDeliveryBeginner{DB: stub}).Begin(context.Background()); !errors.Is(err, want) {
		t.Fatalf("delivery err=%v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("begins=%d", stub.calls)
	}
}
