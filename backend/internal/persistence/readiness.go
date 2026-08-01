package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func PostgreSQLPing(ctx context.Context, pool *pgxpool.Pool) error {
	if ctx == nil || pool == nil {
		return errors.New("postgresql readiness is not configured")
	}
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgresql ping failed: %w", err)
	}
	return nil
}

func MigrationsCurrent(ctx context.Context, pool *pgxpool.Pool, expectedVersion int64) error {
	if ctx == nil || pool == nil || expectedVersion <= 0 {
		return errors.New("migration readiness is not configured")
	}
	var version int64
	err := pool.QueryRow(ctx, `
		SELECT version_id
		FROM goose_db_version
		WHERE is_applied
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&version)
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if version != expectedVersion {
		return fmt.Errorf("migration version %d is not current", version)
	}
	return nil
}
