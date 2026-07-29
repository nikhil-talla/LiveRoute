package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *CommandStore) FinalizeProposalAcceptance(
	ctx context.Context,
	request FinalizeProposalDecisionRequest,
) (FinalizedCommand, error) {
	if err := validateProposalDecisionFinalization(request); err != nil {
		return FinalizedCommand{}, err
	}
	if request.ExpectedTripRevision == math.MaxInt64 {
		return FinalizedCommand{}, errors.New("trip revision is exhausted")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("begin proposal-acceptance finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerUserID string
	var tripRevision int64
	var finalizedSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text,
		       trip_revision,
		       finalized_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&ownerUserID,
		&tripRevision,
		&finalizedSequence,
		&currentPlanID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrTripNotFound
	}
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("lock proposal-acceptance trip: %w", err)
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
	var plannedPlanID string
	var plannedPayload []byte
	var plannedChecksum []byte
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
		       finalized_at,
		       planned_current_plan_id::text,
		       planned_current_plan_payload,
		       planned_current_plan_checksum_sha256
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
		&plannedPlanID,
		&plannedPayload,
		&plannedChecksum,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrCommandNotFound
	}
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("lock proposal-acceptance intent: %w", err)
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
			fmt.Errorf("lock proposal-acceptance outbox: %w", err)
	}

	var proposalRuntimeEpoch int64
	var proposalPlannerVersion int64
	var proposalTripRevision int64
	var proposalBasePlanID string
	var proposalState string
	var proposalPayload []byte
	err = tx.QueryRow(ctx, `
		SELECT source_runtime_epoch,
		       source_planner_state_version,
		       source_trip_revision,
		       base_current_plan_id::text,
		       state,
		       payload
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
		&proposalPayload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrProposalStale
	}
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("lock accepted proposal: %w", err)
	}

	type storedPlan struct {
		id        string
		revision  int64
		origin    string
		sourceID  pgtype.Text
		payload   []byte
		checksum  []byte
		createdAt time.Time
	}
	var plans []storedPlan
	rows, err := tx.Query(ctx, `
		SELECT id::text,
		       plan_revision,
		       origin,
		       source_proposal_id::text,
		       payload,
		       checksum_sha256,
		       created_at
		FROM itinerary_plans
		WHERE trip_id = $1
		ORDER BY id
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("lock itinerary plans: %w", err)
	}
	for rows.Next() {
		var plan storedPlan
		if err := rows.Scan(
			&plan.id,
			&plan.revision,
			&plan.origin,
			&plan.sourceID,
			&plan.payload,
			&plan.checksum,
			&plan.createdAt,
		); err != nil {
			rows.Close()
			return FinalizedCommand{}, fmt.Errorf("scan itinerary plan: %w", err)
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return FinalizedCommand{}, fmt.Errorf("iterate itinerary plans: %w", err)
	}
	rows.Close()

	if eventID != request.EventID ||
		mutationSequence != int64(request.MutationSequence) ||
		expectedRevision != int64(request.ExpectedTripRevision) ||
		CommandKind(commandKind) != CommandAcceptProposal ||
		proposalRuntimeEpoch != int64(request.Identity.SourceRuntimeEpoch) ||
		proposalPlannerVersion !=
			int64(request.Identity.SourcePlannerStateVersion) ||
		proposalBasePlanID != request.Identity.BaseCurrentPlanID {
		return FinalizedCommand{}, ErrCommandFinalizationConflict
	}
	calculatedChecksum := sha256.Sum256(plannedPayload)
	if len(plannedChecksum) != sha256.Size ||
		subtle.ConstantTimeCompare(
			calculatedChecksum[:],
			plannedChecksum,
		) != 1 {
		return FinalizedCommand{}, ErrProposalPayloadInvalid
	}
	plan, err := parseCurrentPlan(plannedPayload)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("%w: %v", ErrProposalPayloadInvalid, err)
	}
	if plan.planID != plannedPlanID ||
		plan.sourceProposalID != request.Identity.ProposalID ||
		plan.origin != 2 {
		return FinalizedCommand{}, ErrProposalPayloadInvalid
	}
	if err := validateProposalPlanMapping(plan, proposalPayload); err != nil {
		return FinalizedCommand{},
			fmt.Errorf("%w: %v", ErrProposalPayloadInvalid, err)
	}
	activityRows, err := tx.Query(ctx, `
		SELECT id::text
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY id
	`, request.TripID)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("read accepted-plan activities: %w", err)
	}
	activityIDs := make(map[string]struct{}, len(plan.segments))
	for activityRows.Next() {
		var activityID string
		if err := activityRows.Scan(&activityID); err != nil {
			activityRows.Close()
			return FinalizedCommand{},
				fmt.Errorf("scan accepted-plan activity: %w", err)
		}
		activityIDs[activityID] = struct{}{}
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return FinalizedCommand{},
			fmt.Errorf("iterate accepted-plan activities: %w", err)
	}
	activityRows.Close()
	if len(activityIDs) != len(plan.segments) {
		return FinalizedCommand{}, ErrProposalPayloadInvalid
	}
	for _, segment := range plan.segments {
		if _, exists := activityIDs[segment.activityID]; !exists {
			return FinalizedCommand{}, ErrProposalPayloadInvalid
		}
	}

	var maximumPlanRevision int64
	var existingPlan *storedPlan
	for index := range plans {
		if plans[index].revision > maximumPlanRevision {
			maximumPlanRevision = plans[index].revision
		}
		if plans[index].id == plannedPlanID {
			existingPlan = &plans[index]
		}
	}
	if maximumPlanRevision == math.MaxInt64 ||
		plan.planRevision != uint64(maximumPlanRevision+1) {
		if intentState == "pending" {
			return FinalizedCommand{}, ErrProposalPayloadInvalid
		}
	}
	createdAt := time.UnixMilli(plan.createdAtUnixMS).UTC()
	resultRevision := request.ExpectedTripRevision + 1

	if intentState != "pending" {
		var payloadMatches bool
		if err := tx.QueryRow(ctx, `
			SELECT outcome_payload = $2::jsonb
			FROM command_intents
			WHERE id = $1
		`, request.IntentID, request.OutcomePayload).Scan(&payloadMatches); err != nil {
			return FinalizedCommand{},
				fmt.Errorf("compare proposal-acceptance outcome: %w", err)
		}
		planMatches := existingPlan != nil &&
			existingPlan.revision == int64(plan.planRevision) &&
			existingPlan.origin == "accepted_engine_proposal" &&
			existingPlan.sourceID.Valid &&
			existingPlan.sourceID.String == request.Identity.ProposalID &&
			bytes.Equal(existingPlan.payload, plannedPayload) &&
			subtle.ConstantTimeCompare(
				existingPlan.checksum,
				plannedChecksum,
			) == 1 &&
			existingPlan.createdAt.Equal(createdAt)
		if intentState != "applied" ||
			!outcomeStatus.Valid ||
			outcomeStatus.String != "OK" ||
			!resultingRevision.Valid ||
			resultingRevision.Int64 != int64(resultRevision) ||
			!resultingPlannerVersion.Valid ||
			resultingPlannerVersion.Int64 !=
				int64(request.ResultingPlannerStateVersion) ||
			!finalizedAt.Valid ||
			outboxState != "accepted" ||
			proposalState != "accepted" ||
			currentPlanID != plannedPlanID ||
			!planMatches ||
			!payloadMatches {
			return FinalizedCommand{}, ErrCommandFinalizationConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return FinalizedCommand{},
				fmt.Errorf("commit duplicate proposal acceptance: %w", err)
		}
		return FinalizedCommand{
			IntentID: request.IntentID, OutboxID: request.OutboxID,
			TripID: request.TripID, EventID: request.EventID,
			MutationSequence: request.MutationSequence,
			State:            "applied", Status: "OK",
			ResultingTripRevision:        resultRevision,
			ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
			FinalizedAt:                  finalizedAt.Time, Duplicate: true,
		}, nil
	}

	if tripRevision != expectedRevision ||
		currentPlanID != request.Identity.BaseCurrentPlanID ||
		proposalTripRevision != expectedRevision ||
		proposalState != "pending" ||
		finalizedSequence < 0 ||
		finalizedSequence == math.MaxInt64 ||
		finalizedSequence+1 != mutationSequence ||
		existingPlan != nil {
		return FinalizedCommand{}, ErrProposalStale
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			source_proposal_id, schema_version, payload,
			payload_size_bytes, checksum_sha256, created_at
		) VALUES (
			$1, $2, $3, 'accepted_engine_proposal', $4,
			$5, 1, $6, $7, $8, $9
		)
	`, plannedPlanID, request.TripID, int64(plan.planRevision),
		ownerUserID, request.Identity.ProposalID, plannedPayload,
		len(plannedPayload), plannedChecksum, createdAt)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("insert accepted plan: %w", err)
	}
	var databaseTime time.Time
	if err := tx.QueryRow(
		ctx,
		"SELECT clock_timestamp()",
	).Scan(&databaseTime); err != nil {
		return FinalizedCommand{}, fmt.Errorf("read acceptance time: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'accepted',
		    decision_message_id = $2,
		    resulting_current_plan_id = $3,
		    decided_at = $4
		WHERE id = $1
	`, request.Identity.ProposalID, messageID, plannedPlanID, databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("accept proposal row: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET current_plan_id = $2,
		    trip_revision = $3,
		    finalized_mutation_sequence = $4,
		    updated_at = $5
		WHERE id = $1
	`, request.TripID, plannedPlanID, int64(resultRevision),
		mutationSequence, databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("publish accepted plan: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE command_intents
		SET state = 'applied',
		    outcome_status = 'OK',
		    outcome_payload = $2,
		    resulting_trip_revision = $3,
		    resulting_planner_state_version = $4,
		    finalized_at = $5
		WHERE id = $1
	`, request.IntentID, request.OutcomePayload,
		int64(resultRevision),
		int64(request.ResultingPlannerStateVersion), databaseTime)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("resolve proposal-acceptance intent: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE planner_outbox
		SET delivery_state = 'accepted',
		    last_status = 'OK',
		    claim_owner = NULL,
		    claim_expires_at = NULL,
		    updated_at = $2
		WHERE id = $1
	`, request.OutboxID, databaseTime)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("resolve proposal-acceptance outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FinalizedCommand{},
			fmt.Errorf("commit proposal acceptance: %w", err)
	}
	return FinalizedCommand{
		IntentID: request.IntentID, OutboxID: request.OutboxID,
		TripID: request.TripID, EventID: request.EventID,
		MutationSequence: request.MutationSequence,
		State:            "applied", Status: "OK",
		ResultingTripRevision:        resultRevision,
		ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
		FinalizedAt:                  databaseTime,
	}, nil
}
