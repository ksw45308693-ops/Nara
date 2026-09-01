package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runtimePoolConfig(databaseURL string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse runtime database URL: %w", err)
	}
	config.MaxConns = 8
	config.MinConns = 1
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = time.Minute
	config.ConnConfig.RuntimeParams["search_path"] = "pg_catalog,public"
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return VerifyRuntimeRole(ctx, conn)
	}
	return config, nil
}

func OpenRuntimePool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := runtimePoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open runtime database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping runtime database: %w", err)
	}
	return pool, nil
}

func OpenOwnerPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse migration database URL: %w", err)
	}
	config.MaxConns = 2
	config.MinConns = 0
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return VerifyMigrationRole(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open migration database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping migration database: %w", err)
	}
	return pool, nil
}
