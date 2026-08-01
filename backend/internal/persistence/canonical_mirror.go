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
	ErrCanonicalMirrorIdentity = errors.New("canonical mirror acknowledgement conflicts with stored identity")
	ErrCanonicalMirrorPaused   = errors.New("canonical mirror is paused for internal repair")
	ErrCanonicalMirrorConflict = errors.New("canonical mirror finalization conflicts with stored state")
)

type FinalizeCanonicalMirrorRequest struct {
	TripID                 string
	IntentID               string
	OutboxID               string
	EventID                string
	MutationSequence       uint64
	ExpectedTripRevision   uint64
	ResultingTripRevision  uint64
	ResultingCurrentPlanID string
}

type FinalizedCanonicalMirror struct {
	IntentID               string
	OutboxID               string
	TripID                 string
	EventID                string
	MutationSequence       uint64
	ExpectedTripRevision   uint64
	ResultingTripRevision  uint64
	ResultingCurrentPlanID string
	RuntimeSyncState       string
	DeliveryState          string
	Duplicate              bool
}

func validateCanonicalMirrorFinalization(
	request FinalizeCanonicalMirrorRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.IntentID,
		request.OutboxID,
		request.EventID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("canonical mirror identifiers must be canonical lowercase UUIDs")
		}
	}
	if request.MutationSequence > math.MaxInt64 ||
		request.ExpectedTripRevision > math.MaxInt64 ||
		request.ResultingTripRevision > math.MaxInt64 {
		return errors.New("canonical mirror versions are out of range")
	}
	return nil
}

func (store *CanonicalStateStore) FinalizeCanonicalMirror(
	ctx context.Context,
	request FinalizeCanonicalMirrorRequest,
) (FinalizedCanonicalMirror, error) {
	if err := validateCanonicalMirrorFinalization(request); err != nil {
		return FinalizedCanonicalMirror{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("begin canonical mirror finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockCanonicalMirrorTrip(ctx, tx, request.TripID); err != nil {
		return FinalizedCanonicalMirror{}, err
	}

	var storedEventID string
	var storedMutationSequence int64
	var storedExpectedRevision int64
	var storedKind string
	var storedIntentState string
	var storedRuntimeSyncState string
	var storedResultingRevision pgtype.Int8
	var storedPlanID pgtype.Text
	err = tx.QueryRow(ctx, `
		SELECT event_id::text,
		       mutation_sequence,
		       expected_trip_revision,
		       command_kind,
		       state,
		       runtime_sync_state,
		       resulting_trip_revision,
		       resulting_current_plan_id::text
		FROM command_intents
		WHERE id = $1
		  AND trip_id = $2
		  AND application_order = 'canonical_first'
		FOR UPDATE
	`, request.IntentID, request.TripID).Scan(
		&storedEventID,
		&storedMutationSequence,
		&storedExpectedRevision,
		&storedKind,
		&storedIntentState,
		&storedRuntimeSyncState,
		&storedResultingRevision,
		&storedPlanID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCanonicalMirror{}, ErrCommandNotFound
	}
	if err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("lock canonical mirror intent: %w", err)
	}

	var storedDeliveryState string
	err = tx.QueryRow(ctx, `
		SELECT delivery_state
		FROM planner_outbox
		WHERE id = $1
		  AND command_intent_id = $2
		  AND trip_id = $3
		FOR UPDATE
	`, request.OutboxID, request.IntentID, request.TripID).Scan(
		&storedDeliveryState)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCanonicalMirror{}, ErrCommandNotFound
	}
	if err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("lock canonical mirror outbox: %w", err)
	}

	identityMatches := canonicalMirrorIdentityMatches(
		request,
		storedEventID,
		storedMutationSequence,
		storedExpectedRevision,
		storedKind,
		storedIntentState,
		storedResultingRevision,
		storedPlanID,
	)
	if storedRuntimeSyncState == "synced" || storedDeliveryState == "accepted" {
		if !identityMatches ||
			storedRuntimeSyncState != "synced" ||
			storedDeliveryState != "accepted" {
			return FinalizedCanonicalMirror{}, ErrCanonicalMirrorConflict
		}
		result := canonicalMirrorResult(
			request,
			storedPlanID.String,
			storedRuntimeSyncState,
			storedDeliveryState,
			true,
		)
		if err := tx.Commit(ctx); err != nil {
			return FinalizedCanonicalMirror{}, fmt.Errorf("commit duplicate canonical mirror finalization: %w", err)
		}
		return result, nil
	}

	if storedRuntimeSyncState == "paused_internal" ||
		storedDeliveryState == "paused_internal" {
		return FinalizedCanonicalMirror{}, ErrCanonicalMirrorPaused
	}
	if storedRuntimeSyncState != "pending" || storedDeliveryState != "pending" {
		return FinalizedCanonicalMirror{}, ErrCanonicalMirrorConflict
	}
	if !identityMatches {
		if err := pauseCanonicalMirror(ctx, tx, request.IntentID, request.OutboxID); err != nil {
			return FinalizedCanonicalMirror{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return FinalizedCanonicalMirror{}, fmt.Errorf("commit paused canonical mirror: %w", err)
		}
		return FinalizedCanonicalMirror{}, ErrCanonicalMirrorIdentity
	}

	var databaseTime time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseTime); err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("read canonical mirror finalization time: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE planner_outbox
		SET delivery_state = 'accepted',
		    last_status = 'OK',
		    claim_owner = NULL,
		    claim_expires_at = NULL,
		    updated_at = $2
		WHERE id = $1
		  AND command_intent_id = $3
		  AND trip_id = $4
		  AND delivery_state = 'pending'
	`, request.OutboxID, databaseTime, request.IntentID, request.TripID); err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("resolve canonical mirror outbox: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE command_intents
		SET runtime_sync_state = 'synced'
		WHERE id = $1
		  AND trip_id = $2
		  AND runtime_sync_state = 'pending'
	`, request.IntentID, request.TripID); err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("resolve canonical mirror intent: %w", err)
	}
	result := canonicalMirrorResult(
		request,
		storedPlanID.String,
		"synced",
		"accepted",
		false,
	)
	if err := tx.Commit(ctx); err != nil {
		return FinalizedCanonicalMirror{}, fmt.Errorf("commit canonical mirror finalization: %w", err)
	}
	return result, nil
}

func lockCanonicalMirrorTrip(
	ctx context.Context,
	tx pgx.Tx,
	tripID string,
) error {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT true FROM trips WHERE id = $1 FOR UPDATE
	`, tripID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFound
	}
	if err != nil {
		return fmt.Errorf("lock canonical mirror trip: %w", err)
	}
	return nil
}

func canonicalMirrorIdentityMatches(
	request FinalizeCanonicalMirrorRequest,
	storedEventID string,
	storedMutationSequence int64,
	storedExpectedRevision int64,
	storedKind string,
	storedIntentState string,
	storedResultingRevision pgtype.Int8,
	storedPlanID pgtype.Text,
) bool {
	if storedKind != string(CommandTripEdited) &&
		storedKind != string(CommandReplaceCurrentPlan) {
		return false
	}
	return storedIntentState == "applied" &&
		storedEventID == request.EventID &&
		storedMutationSequence == int64(request.MutationSequence) &&
		storedExpectedRevision == int64(request.ExpectedTripRevision) &&
		storedResultingRevision.Valid &&
		storedResultingRevision.Int64 == int64(request.ResultingTripRevision) &&
		storedPlanID.Valid &&
		validCanonicalUUID(storedPlanID.String) &&
		storedPlanID.String == request.ResultingCurrentPlanID
}

func pauseCanonicalMirror(
	ctx context.Context,
	tx pgx.Tx,
	intentID string,
	outboxID string,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE planner_outbox
		SET delivery_state = 'paused_internal',
		    last_status = 'INTERNAL',
		    claim_owner = NULL,
		    claim_expires_at = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1 AND delivery_state = 'pending'
	`, outboxID); err != nil {
		return fmt.Errorf("pause canonical mirror outbox: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE command_intents
		SET runtime_sync_state = 'paused_internal'
		WHERE id = $1 AND runtime_sync_state = 'pending'
	`, intentID); err != nil {
		return fmt.Errorf("pause canonical mirror intent: %w", err)
	}
	return nil
}

func canonicalMirrorResult(
	request FinalizeCanonicalMirrorRequest,
	planID string,
	runtimeSyncState string,
	deliveryState string,
	duplicate bool,
) FinalizedCanonicalMirror {
	return FinalizedCanonicalMirror{
		IntentID:               request.IntentID,
		OutboxID:               request.OutboxID,
		TripID:                 request.TripID,
		EventID:                request.EventID,
		MutationSequence:       request.MutationSequence,
		ExpectedTripRevision:   request.ExpectedTripRevision,
		ResultingTripRevision:  request.ResultingTripRevision,
		ResultingCurrentPlanID: planID,
		RuntimeSyncState:       runtimeSyncState,
		DeliveryState:          deliveryState,
		Duplicate:              duplicate,
	}
}
