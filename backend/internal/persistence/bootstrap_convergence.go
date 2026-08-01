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
	ErrBootstrapConvergenceConflict = errors.New("bootstrap does not match PostgreSQL canonical state")
	ErrBootstrapConvergenceBlocked  = errors.New("bootstrap covers unresolved runtime-first work")
)

type ResolveCanonicalBootstrapRequest struct {
	TripID                    string
	TripRevision              uint64
	AcceptedMutationSequence  uint64
	FinalizedMutationSequence uint64
	CurrentPlanID             string
}

type ResolvedCanonicalBootstrap struct {
	TripID                    string
	TripRevision              uint64
	AcceptedMutationSequence  uint64
	FinalizedMutationSequence uint64
	CurrentPlanID             string
	MirrorsResolved           uint64
	RowsConfirmed             uint64
	ConfirmedAt               time.Time
	Duplicate                 bool
}

func validateCanonicalBootstrapResolution(
	request ResolveCanonicalBootstrapRequest,
) error {
	if !validCanonicalUUID(request.TripID) ||
		!validCanonicalUUID(request.CurrentPlanID) {
		return errors.New("bootstrap resolution identifiers must be canonical lowercase UUIDs")
	}
	if request.TripRevision == 0 ||
		request.TripRevision > math.MaxInt64 ||
		request.AcceptedMutationSequence == 0 ||
		request.AcceptedMutationSequence > math.MaxInt64 ||
		request.FinalizedMutationSequence == 0 ||
		request.FinalizedMutationSequence > math.MaxInt64 {
		return errors.New("bootstrap resolution versions are invalid")
	}
	return nil
}

func (store *CanonicalStateStore) ResolveCanonicalBootstrap(
	ctx context.Context,
	request ResolveCanonicalBootstrapRequest,
) (ResolvedCanonicalBootstrap, error) {
	if err := validateCanonicalBootstrapResolution(request); err != nil {
		return ResolvedCanonicalBootstrap{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ResolvedCanonicalBootstrap{}, fmt.Errorf("begin canonical bootstrap convergence: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tripRevision int64
	var finalizedSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT trip_revision, finalized_mutation_sequence, current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(&tripRevision, &finalizedSequence, &currentPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedCanonicalBootstrap{}, ErrTripNotFound
	}
	if err != nil {
		return ResolvedCanonicalBootstrap{}, fmt.Errorf("lock canonical bootstrap trip: %w", err)
	}
	if tripRevision != int64(request.TripRevision) ||
		finalizedSequence != int64(request.FinalizedMutationSequence) ||
		request.AcceptedMutationSequence != request.FinalizedMutationSequence ||
		currentPlanID != request.CurrentPlanID {
		return ResolvedCanonicalBootstrap{}, ErrBootstrapConvergenceConflict
	}

	type coveredMirror struct {
		intentID              string
		outboxID              string
		applicationOrder      string
		intentSyncState       string
		outboxDeliveryState   string
		finalizationConfirmed pgtype.Timestamptz
	}
	rows, err := tx.Query(ctx, `
		SELECT intent.id::text,
		       outbox.id::text,
		       intent.application_order,
		       intent.runtime_sync_state,
		       outbox.delivery_state,
		       outbox.finalization_confirmed_at
		FROM command_intents AS intent
		JOIN planner_outbox AS outbox
		  ON outbox.command_intent_id = intent.id
		WHERE intent.trip_id = $1
		  AND outbox.mutation_sequence <= $2
		ORDER BY outbox.mutation_sequence
		FOR UPDATE OF intent, outbox
	`, request.TripID, int64(request.FinalizedMutationSequence))
	if err != nil {
		return ResolvedCanonicalBootstrap{}, fmt.Errorf("lock covered bootstrap mirrors: %w", err)
	}
	defer rows.Close()

	covered := make([]coveredMirror, 0)
	for rows.Next() {
		var mirror coveredMirror
		if err := rows.Scan(
			&mirror.intentID,
			&mirror.outboxID,
			&mirror.applicationOrder,
			&mirror.intentSyncState,
			&mirror.outboxDeliveryState,
			&mirror.finalizationConfirmed,
		); err != nil {
			return ResolvedCanonicalBootstrap{}, fmt.Errorf("scan covered bootstrap mirror: %w", err)
		}
		if mirror.applicationOrder == "runtime_first" &&
			(mirror.intentSyncState == "pending" ||
				mirror.outboxDeliveryState == "pending" ||
				mirror.outboxDeliveryState == "paused_internal") {
			return ResolvedCanonicalBootstrap{}, ErrBootstrapConvergenceBlocked
		}
		if mirror.applicationOrder != "canonical_first" &&
			mirror.applicationOrder != "runtime_first" {
			return ResolvedCanonicalBootstrap{}, ErrBootstrapConvergenceConflict
		}
		covered = append(covered, mirror)
	}
	if err := rows.Err(); err != nil {
		return ResolvedCanonicalBootstrap{}, fmt.Errorf("iterate covered bootstrap mirrors: %w", err)
	}

	var mirrorsResolved uint64
	var rowsConfirmed uint64
	for _, mirror := range covered {
		if mirror.applicationOrder == "canonical_first" &&
			(mirror.intentSyncState == "pending" ||
				mirror.intentSyncState == "paused_internal" ||
				mirror.outboxDeliveryState == "pending" ||
				mirror.outboxDeliveryState == "paused_internal") {
			mirrorsResolved++
		}
		if !mirror.finalizationConfirmed.Valid {
			rowsConfirmed++
		}
	}

	var databaseTime time.Time
	if mirrorsResolved > 0 || rowsConfirmed > 0 {
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseTime); err != nil {
			return ResolvedCanonicalBootstrap{}, fmt.Errorf("read bootstrap convergence time: %w", err)
		}
	}
	if databaseTime.IsZero() {
		for _, mirror := range covered {
			if mirror.finalizationConfirmed.Valid {
				databaseTime = mirror.finalizationConfirmed.Time
				break
			}
		}
	}
	for _, mirror := range covered {
		if mirror.applicationOrder == "canonical_first" &&
			(mirror.intentSyncState == "pending" ||
				mirror.intentSyncState == "paused_internal") {
			if _, err := tx.Exec(ctx, `
				UPDATE command_intents
				SET runtime_sync_state = 'synced'
				WHERE id = $1 AND trip_id = $2
			`, mirror.intentID, request.TripID); err != nil {
				return ResolvedCanonicalBootstrap{}, fmt.Errorf("resolve bootstrap mirror intent: %w", err)
			}
		}
		if mirror.applicationOrder == "canonical_first" &&
			(mirror.outboxDeliveryState == "pending" ||
				mirror.outboxDeliveryState == "paused_internal") {
			if _, err := tx.Exec(ctx, `
				UPDATE planner_outbox
				SET delivery_state = 'accepted',
				    last_status = 'OK',
				    claim_owner = NULL,
				    claim_expires_at = NULL,
				    finalization_confirmed_at = $2,
				    updated_at = $2
				WHERE id = $1 AND trip_id = $3
			`, mirror.outboxID, databaseTime, request.TripID); err != nil {
				return ResolvedCanonicalBootstrap{}, fmt.Errorf("resolve bootstrap mirror outbox: %w", err)
			}
		} else if !mirror.finalizationConfirmed.Valid {
			if _, err := tx.Exec(ctx, `
				UPDATE planner_outbox
				SET finalization_confirmed_at = $2,
				    updated_at = $2
				WHERE id = $1 AND trip_id = $3
			`, mirror.outboxID, databaseTime, request.TripID); err != nil {
				return ResolvedCanonicalBootstrap{}, fmt.Errorf("confirm bootstrap outbox: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ResolvedCanonicalBootstrap{}, fmt.Errorf("commit canonical bootstrap convergence: %w", err)
	}
	return ResolvedCanonicalBootstrap{
		TripID:                    request.TripID,
		TripRevision:              request.TripRevision,
		AcceptedMutationSequence:  request.AcceptedMutationSequence,
		FinalizedMutationSequence: request.FinalizedMutationSequence,
		CurrentPlanID:             request.CurrentPlanID,
		MirrorsResolved:           mirrorsResolved,
		RowsConfirmed:             rowsConfirmed,
		ConfirmedAt:               databaseTime.UTC(),
		Duplicate:                 mirrorsResolved == 0 && rowsConfirmed == 0,
	}, nil
}
