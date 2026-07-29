package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ProposalDecisionIdentity struct {
	ProposalID                string
	SourceRuntimeEpoch        uint64
	SourcePlannerStateVersion uint64
	BaseCurrentPlanID         string
}

type FinalizeProposalDecisionRequest struct {
	TripID                       string
	IntentID                     string
	OutboxID                     string
	EventID                      string
	MutationSequence             uint64
	ExpectedTripRevision         uint64
	ResultingPlannerStateVersion uint64
	Identity                     ProposalDecisionIdentity
	OutcomePayload               json.RawMessage
}

func validateProposalDecisionFinalization(
	request FinalizeProposalDecisionRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.IntentID,
		request.OutboxID,
		request.EventID,
		request.Identity.ProposalID,
		request.Identity.BaseCurrentPlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New(
				"proposal-decision identifiers must be canonical lowercase UUIDs",
			)
		}
	}
	for _, value := range []uint64{
		request.MutationSequence,
		request.ExpectedTripRevision,
		request.ResultingPlannerStateVersion,
		request.Identity.SourceRuntimeEpoch,
		request.Identity.SourcePlannerStateVersion,
	} {
		if value > math.MaxInt64 {
			return errors.New("proposal-decision version is out of range")
		}
	}
	if request.MutationSequence == 0 ||
		request.Identity.SourceRuntimeEpoch == 0 ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("proposal-decision finalization is invalid")
	}
	return nil
}

func (store *CommandStore) FinalizeProposalRejection(
	ctx context.Context,
	request FinalizeProposalDecisionRequest,
) (FinalizedCommand, error) {
	if request.ExpectedTripRevision == math.MaxInt64 {
		return FinalizedCommand{}, errors.New("trip revision is exhausted")
	}
	return store.finalizeProposalDecision(ctx, request, false)
}

func (store *CommandStore) FinalizeStaleProposalDecision(
	ctx context.Context,
	request FinalizeProposalDecisionRequest,
) (FinalizedCommand, error) {
	return store.finalizeProposalDecision(ctx, request, true)
}

func (store *CommandStore) finalizeProposalDecision(
	ctx context.Context,
	request FinalizeProposalDecisionRequest,
	stale bool,
) (FinalizedCommand, error) {
	if err := validateProposalDecisionFinalization(request); err != nil {
		return FinalizedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("begin proposal-decision finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tripRevision int64
	var finalizedSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT trip_revision,
		       finalized_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&tripRevision,
		&finalizedSequence,
		&currentPlanID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrTripNotFound
	}
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("lock proposal-decision trip: %w", err)
	}

	var messageID string
	var eventID string
	var mutationSequence int64
	var expectedRevision int64
	var commandKind string
	var intentState string
	var outcomeStatus pgtype.Text
	var resultingRevision pgtype.Int8
	var resultingPlannerVersion pgtype.Int8
	var finalizedAt pgtype.Timestamptz
	err = tx.QueryRow(ctx, `
		SELECT message_id::text,
		       event_id::text,
		       mutation_sequence,
		       expected_trip_revision,
		       command_kind,
		       state,
		       outcome_status,
		       resulting_trip_revision,
		       resulting_planner_state_version,
		       finalized_at
		FROM command_intents
		WHERE id = $1
		  AND trip_id = $2
		  AND application_order = 'runtime_first'
		FOR UPDATE
	`, request.IntentID, request.TripID).Scan(
		&messageID,
		&eventID,
		&mutationSequence,
		&expectedRevision,
		&commandKind,
		&intentState,
		&outcomeStatus,
		&resultingRevision,
		&resultingPlannerVersion,
		&finalizedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrCommandNotFound
	}
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("lock proposal-decision intent: %w", err)
	}

	var outboxState string
	err = tx.QueryRow(ctx, `
		SELECT delivery_state
		FROM planner_outbox
		WHERE id = $1
		  AND command_intent_id = $2
		  AND trip_id = $3
		FOR UPDATE
	`, request.OutboxID, request.IntentID, request.TripID).Scan(&outboxState)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrCommandNotFound
	}
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("lock proposal-decision outbox: %w", err)
	}

	var proposalRuntimeEpoch int64
	var proposalPlannerVersion int64
	var proposalTripRevision int64
	var proposalBasePlanID string
	var proposalState string
	var decisionMessageID pgtype.Text
	err = tx.QueryRow(ctx, `
		SELECT source_runtime_epoch,
		       source_planner_state_version,
		       source_trip_revision,
		       base_current_plan_id::text,
		       state,
		       decision_message_id::text
		FROM plan_proposals
		WHERE id = $1
		  AND trip_id = $2
		FOR UPDATE
	`, request.Identity.ProposalID, request.TripID).Scan(
		&proposalRuntimeEpoch,
		&proposalPlannerVersion,
		&proposalTripRevision,
		&proposalBasePlanID,
		&proposalState,
		&decisionMessageID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrProposalStale
	}
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("lock decided proposal: %w", err)
	}

	if eventID != request.EventID ||
		mutationSequence != int64(request.MutationSequence) ||
		expectedRevision != int64(request.ExpectedTripRevision) ||
		proposalRuntimeEpoch != int64(request.Identity.SourceRuntimeEpoch) ||
		proposalPlannerVersion !=
			int64(request.Identity.SourcePlannerStateVersion) ||
		proposalBasePlanID != request.Identity.BaseCurrentPlanID {
		return FinalizedCommand{}, ErrCommandFinalizationConflict
	}
	kind := CommandKind(commandKind)
	if kind != CommandAcceptProposal && kind != CommandRejectProposal {
		return FinalizedCommand{}, ErrCommandFinalizationConflict
	}
	if !stale && kind != CommandRejectProposal {
		return FinalizedCommand{}, ErrCommandFinalizationConflict
	}

	status := "OK"
	desiredIntentState := "applied"
	desiredOutboxState := "accepted"
	resultRevision := request.ExpectedTripRevision + 1
	desiredProposalState := "rejected"
	if stale {
		status = "STALE"
		desiredIntentState = "rejected"
		desiredOutboxState = "terminal_rejected"
		resultRevision = request.ExpectedTripRevision
		desiredProposalState = "stale"
	}

	if intentState != "pending" {
		var payloadMatches bool
		err = tx.QueryRow(ctx, `
			SELECT outcome_payload = $2::jsonb
			FROM command_intents
			WHERE id = $1
		`, request.IntentID, request.OutcomePayload).Scan(&payloadMatches)
		if err != nil {
			return FinalizedCommand{},
				fmt.Errorf("compare proposal-decision outcome: %w", err)
		}
		proposalMatches := proposalState == desiredProposalState &&
			decisionMessageID.Valid &&
			decisionMessageID.String == messageID
		if stale && proposalState != "pending" &&
			proposalState != "stale" {
			proposalMatches = !decisionMessageID.Valid ||
				decisionMessageID.String == messageID
		}
		if intentState != desiredIntentState ||
			!outcomeStatus.Valid ||
			outcomeStatus.String != status ||
			!resultingRevision.Valid ||
			resultingRevision.Int64 != int64(resultRevision) ||
			!resultingPlannerVersion.Valid ||
			resultingPlannerVersion.Int64 !=
				int64(request.ResultingPlannerStateVersion) ||
			!finalizedAt.Valid ||
			outboxState != desiredOutboxState ||
			!proposalMatches ||
			!payloadMatches {
			return FinalizedCommand{}, ErrCommandFinalizationConflict
		}
		result := FinalizedCommand{
			IntentID:                     request.IntentID,
			OutboxID:                     request.OutboxID,
			TripID:                       request.TripID,
			EventID:                      request.EventID,
			MutationSequence:             request.MutationSequence,
			State:                        desiredIntentState,
			Status:                       status,
			ResultingTripRevision:        resultRevision,
			ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
			FinalizedAt:                  finalizedAt.Time,
			Duplicate:                    true,
		}
		if err := tx.Commit(ctx); err != nil {
			return FinalizedCommand{},
				fmt.Errorf("commit duplicate proposal decision: %w", err)
		}
		return result, nil
	}

	if tripRevision != expectedRevision ||
		(!stale && currentPlanID != request.Identity.BaseCurrentPlanID) {
		return FinalizedCommand{}, ErrTripRevisionStale
	}
	if finalizedSequence < 0 ||
		finalizedSequence == math.MaxInt64 ||
		finalizedSequence+1 != mutationSequence {
		return FinalizedCommand{}, ErrCommandFinalizationOutOfOrder
	}
	if !stale && proposalState != "pending" {
		return FinalizedCommand{}, ErrProposalStale
	}
	if !stale && proposalTripRevision != expectedRevision {
		return FinalizedCommand{}, ErrProposalStale
	}

	var databaseTime time.Time
	if err := tx.QueryRow(
		ctx,
		"SELECT clock_timestamp()",
	).Scan(&databaseTime); err != nil {
		return FinalizedCommand{}, fmt.Errorf("read proposal-decision time: %w", err)
	}
	if !stale || proposalState == "pending" {
		_, err = tx.Exec(ctx, `
			UPDATE plan_proposals
			SET state = $2,
			    decision_message_id = $3,
			    decided_at = $4
			WHERE id = $1
		`, request.Identity.ProposalID, desiredProposalState,
			messageID, databaseTime)
		if err != nil {
			return FinalizedCommand{},
				fmt.Errorf("resolve proposal decision: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2,
		    finalized_mutation_sequence = $3,
		    updated_at = $4
		WHERE id = $1
	`, request.TripID, int64(resultRevision),
		mutationSequence, databaseTime)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("advance proposal-decision trip: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE command_intents
		SET state = $2,
		    outcome_status = $3,
		    outcome_payload = $4,
		    resulting_trip_revision = $5,
		    resulting_planner_state_version = $6,
		    finalized_at = $7
		WHERE id = $1
	`, request.IntentID, desiredIntentState, status,
		request.OutcomePayload, int64(resultRevision),
		int64(request.ResultingPlannerStateVersion), databaseTime)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("resolve proposal-decision intent: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE planner_outbox
		SET delivery_state = $2,
		    last_status = $3,
		    claim_owner = NULL,
		    claim_expires_at = NULL,
		    updated_at = $4
		WHERE id = $1
	`, request.OutboxID, desiredOutboxState, status, databaseTime)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("resolve proposal-decision outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FinalizedCommand{},
			fmt.Errorf("commit proposal-decision finalization: %w", err)
	}
	return FinalizedCommand{
		IntentID:                     request.IntentID,
		OutboxID:                     request.OutboxID,
		TripID:                       request.TripID,
		EventID:                      request.EventID,
		MutationSequence:             request.MutationSequence,
		State:                        desiredIntentState,
		Status:                       status,
		ResultingTripRevision:        resultRevision,
		ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
		FinalizedAt:                  databaseTime,
	}, nil
}
