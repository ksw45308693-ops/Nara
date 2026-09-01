package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// PgxTxStarter is implemented by both pgx.Conn and pgxpool.Pool.
type PgxTxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

type PgxMigrationBeginner struct{ DB PgxTxStarter }

func (p PgxMigrationBeginner) Begin(ctx context.Context) (MigrationTx, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, errors.New("pgx returned a nil migration transaction")
	}
	return tx, nil
}

type PgxDeliveryBeginner struct{ DB PgxTxStarter }

func (p PgxDeliveryBeginner) Begin(ctx context.Context) (DeliveryTx, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, errors.New("pgx returned a nil delivery transaction")
	}
	return tx, nil
}
