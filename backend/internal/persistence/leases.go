package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrTripNotFound   = errors.New("trip not found")
	ErrLeaseHeld      = errors.New("runtime lease is still held")
	ErrLeaseLost      = errors.New("runtime lease was lost")
	ErrEpochExhausted = errors.New("runtime epoch exhausted")
)

type RuntimeLease struct {
	TripID         string
	HolderID       string
	RuntimeEpoch   uint64
	LeaseExpiresAt time.Time
	RenewedAt      time.Time
}

type LeaseStore struct {
	pool *pgxpool.Pool
}

func NewLeaseStore(pool *pgxpool.Pool) (*LeaseStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &LeaseStore{pool: pool}, nil
}

func validateLeaseInput(tripID, holderID string, duration time.Duration) error {
	if tripID == "" || holderID == "" {
		return errors.New("trip and holder ids are required")
	}
	if duration.Milliseconds() <= 0 {
		return errors.New("lease duration must be at least one millisecond")
	}
	return nil
}

func lockTrip(ctx context.Context, tx pgx.Tx, tripID string) error {
	var locked bool
	err := tx.QueryRow(
		ctx,
		"SELECT true FROM trips WHERE id = $1 FOR UPDATE",
		tripID,
	).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFound
	}
	if err != nil {
		return fmt.Errorf("lock trip: %w", err)
	}
	return nil
}

func (store *LeaseStore) Acquire(
	ctx context.Context,
	tripID string,
	holderID string,
	duration time.Duration,
) (RuntimeLease, error) {
	if err := validateLeaseInput(tripID, holderID, duration); err != nil {
		return RuntimeLease{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return RuntimeLease{}, fmt.Errorf("begin lease acquisition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockTrip(ctx, tx, tripID); err != nil {
		return RuntimeLease{}, err
	}

	var currentHolder string
	var currentEpoch int64
	var expiresAt time.Time
	var databaseNow time.Time
	err = tx.QueryRow(ctx, `
		SELECT holder_id::text, runtime_epoch, lease_expires_at, clock_timestamp()
		FROM trip_runtime_leases
		WHERE trip_id = $1
		FOR UPDATE
	`, tripID).Scan(
		&currentHolder,
		&currentEpoch,
		&expiresAt,
		&databaseNow,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `
			WITH database_time AS (SELECT clock_timestamp() AS now)
			INSERT INTO trip_runtime_leases (
				trip_id, holder_id, runtime_epoch, lease_expires_at, renewed_at
			)
			SELECT $1, $2, 1, now + ($3 * interval '1 millisecond'), now
			FROM database_time
			RETURNING trip_id::text, holder_id::text, runtime_epoch,
			          lease_expires_at, renewed_at
		`, tripID, holderID, duration.Milliseconds()).Scan(
			&tripID,
			&holderID,
			&currentEpoch,
			&expiresAt,
			&databaseNow,
		)
	case err != nil:
		return RuntimeLease{}, fmt.Errorf("read runtime lease: %w", err)
	case expiresAt.After(databaseNow):
		return RuntimeLease{}, ErrLeaseHeld
	default:
		if currentEpoch == math.MaxInt64 {
			return RuntimeLease{}, ErrEpochExhausted
		}
		err = tx.QueryRow(ctx, `
			WITH database_time AS (SELECT clock_timestamp() AS now)
			UPDATE trip_runtime_leases
			SET holder_id = $2,
			    runtime_epoch = runtime_epoch + 1,
			    lease_expires_at =
			        (SELECT now FROM database_time) +
			        ($3 * interval '1 millisecond'),
			    renewed_at = (SELECT now FROM database_time)
			WHERE trip_id = $1
			RETURNING trip_id::text, holder_id::text, runtime_epoch,
			          lease_expires_at, renewed_at
		`, tripID, holderID, duration.Milliseconds()).Scan(
			&tripID,
			&holderID,
			&currentEpoch,
			&expiresAt,
			&databaseNow,
		)
	}
	if err != nil {
		return RuntimeLease{}, fmt.Errorf("write runtime lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RuntimeLease{}, fmt.Errorf("commit lease acquisition: %w", err)
	}
	return RuntimeLease{
		TripID:         tripID,
		HolderID:       holderID,
		RuntimeEpoch:   uint64(currentEpoch),
		LeaseExpiresAt: expiresAt,
		RenewedAt:      databaseNow,
	}, nil
}

func (store *LeaseStore) Renew(
	ctx context.Context,
	tripID string,
	holderID string,
	runtimeEpoch uint64,
	duration time.Duration,
) (RuntimeLease, error) {
	if err := validateLeaseInput(tripID, holderID, duration); err != nil {
		return RuntimeLease{}, err
	}
	if runtimeEpoch == 0 || runtimeEpoch > math.MaxInt64 {
		return RuntimeLease{}, errors.New("runtime epoch is invalid")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return RuntimeLease{}, fmt.Errorf("begin lease renewal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockTrip(ctx, tx, tripID); err != nil {
		return RuntimeLease{}, err
	}

	var renewed RuntimeLease
	var epoch int64
	err = tx.QueryRow(ctx, `
		WITH database_time AS (SELECT clock_timestamp() AS now)
		UPDATE trip_runtime_leases
		SET lease_expires_at =
		        (SELECT now FROM database_time) +
		        ($4 * interval '1 millisecond'),
		    renewed_at = (SELECT now FROM database_time)
		WHERE trip_id = $1
		  AND holder_id = $2
		  AND runtime_epoch = $3
		  AND lease_expires_at > (SELECT now FROM database_time)
		RETURNING trip_id::text, holder_id::text, runtime_epoch,
		          lease_expires_at, renewed_at
	`, tripID, holderID, int64(runtimeEpoch), duration.Milliseconds()).Scan(
		&renewed.TripID,
		&renewed.HolderID,
		&epoch,
		&renewed.LeaseExpiresAt,
		&renewed.RenewedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeLease{}, ErrLeaseLost
	}
	if err != nil {
		return RuntimeLease{}, fmt.Errorf("renew runtime lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RuntimeLease{}, fmt.Errorf("commit lease renewal: %w", err)
	}
	renewed.RuntimeEpoch = uint64(epoch)
	return renewed, nil
}
