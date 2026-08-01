package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrFinalizationConfirmationAhead   = errors.New("finalization confirmation is ahead of PostgreSQL")
	ErrFinalizationConfirmationBlocked = errors.New("finalization confirmation covers unresolved outbox work")
)

type ConfirmFinalizedMutationsRequest struct {
	TripID                    string
	FinalizedMutationSequence uint64
}

type ConfirmedFinalizedMutations struct {
	TripID                    string
	FinalizedMutationSequence uint64
	ConfirmedAt               time.Time
	RowsConfirmed             uint64
	Duplicate                 bool
}

func validateFinalizationConfirmation(
	request ConfirmFinalizedMutationsRequest,
) error {
	if !validCanonicalUUID(request.TripID) {
		return errors.New("finalization confirmation trip id must be a canonical lowercase UUID")
	}
	if request.FinalizedMutationSequence == 0 ||
		request.FinalizedMutationSequence > math.MaxInt64 {
		return errors.New("finalization confirmation sequence is invalid")
	}
	return nil
}

func (store *CommandStore) ConfirmFinalizedMutations(
	ctx context.Context,
	request ConfirmFinalizedMutationsRequest,
) (ConfirmedFinalizedMutations, error) {
	if err := validateFinalizationConfirmation(request); err != nil {
		return ConfirmedFinalizedMutations{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ConfirmedFinalizedMutations{}, fmt.Errorf("begin finalization confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var finalizedSequence int64
	err = tx.QueryRow(ctx, `
		SELECT finalized_mutation_sequence
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(&finalizedSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfirmedFinalizedMutations{}, ErrTripNotFound
	}
	if err != nil {
		return ConfirmedFinalizedMutations{}, fmt.Errorf("lock finalization confirmation trip: %w", err)
	}
	if finalizedSequence < 0 || request.FinalizedMutationSequence > uint64(finalizedSequence) {
		return ConfirmedFinalizedMutations{}, ErrFinalizationConfirmationAhead
	}

	rows, err := tx.Query(ctx, `
		SELECT delivery_state, finalization_confirmed_at
		FROM planner_outbox
		WHERE trip_id = $1
		  AND mutation_sequence <= $2
		ORDER BY mutation_sequence
		FOR UPDATE
	`, request.TripID, int64(request.FinalizedMutationSequence))
	if err != nil {
		return ConfirmedFinalizedMutations{}, fmt.Errorf("lock covered outbox rows: %w", err)
	}
	defer rows.Close()

	var alreadyConfirmed bool
	var rowsToConfirm uint64
	for rows.Next() {
		var deliveryState string
		var confirmedAt pgtype.Timestamptz
		if err := rows.Scan(&deliveryState, &confirmedAt); err != nil {
			return ConfirmedFinalizedMutations{}, fmt.Errorf("scan covered outbox row: %w", err)
		}
		switch deliveryState {
		case "accepted", "terminal_rejected":
			if !confirmedAt.Valid {
				rowsToConfirm++
			} else {
				alreadyConfirmed = true
			}
		case "pending", "paused_internal":
			return ConfirmedFinalizedMutations{}, ErrFinalizationConfirmationBlocked
		default:
			return ConfirmedFinalizedMutations{}, fmt.Errorf("unknown outbox delivery state %q", deliveryState)
		}
	}
	if err := rows.Err(); err != nil {
		return ConfirmedFinalizedMutations{}, fmt.Errorf("iterate covered outbox rows: %w", err)
	}

	var databaseTime time.Time
	if rowsToConfirm > 0 {
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseTime); err != nil {
			return ConfirmedFinalizedMutations{}, fmt.Errorf("read finalization confirmation time: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE planner_outbox
			SET finalization_confirmed_at = $2,
			    updated_at = $2
			WHERE trip_id = $1
			  AND mutation_sequence <= $3
			  AND delivery_state IN ('accepted', 'terminal_rejected')
			  AND finalization_confirmed_at IS NULL
		`, request.TripID, databaseTime,
			int64(request.FinalizedMutationSequence)); err != nil {
			return ConfirmedFinalizedMutations{}, fmt.Errorf("confirm covered outbox rows: %w", err)
		}
	} else if alreadyConfirmed {
		if err := tx.QueryRow(ctx, `
			SELECT max(finalization_confirmed_at)
			FROM planner_outbox
			WHERE trip_id = $1
			  AND mutation_sequence <= $2
			  AND finalization_confirmed_at IS NOT NULL
		`, request.TripID, int64(request.FinalizedMutationSequence)).Scan(&databaseTime); err != nil {
			return ConfirmedFinalizedMutations{}, fmt.Errorf("read existing finalization confirmation: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfirmedFinalizedMutations{}, fmt.Errorf("commit finalization confirmation: %w", err)
	}
	return ConfirmedFinalizedMutations{
		TripID:                    request.TripID,
		FinalizedMutationSequence: request.FinalizedMutationSequence,
		ConfirmedAt:               databaseTime.UTC(),
		RowsConfirmed:             rowsToConfirm,
		Duplicate:                 rowsToConfirm == 0,
	}, nil
}
