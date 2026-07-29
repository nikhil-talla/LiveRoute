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

var (
	ErrCommandNotFound               = errors.New("command was not found")
	ErrCommandFinalizationConflict   = errors.New("command finalization conflicts with stored outcome")
	ErrCommandFinalizationOutOfOrder = errors.New("command finalization is not contiguous")
	ErrProposalFinalizationRequired  = errors.New("proposal-aware finalization is required")
)

type TerminalStatus string

const (
	TerminalStatusStale           TerminalStatus = "STALE"
	TerminalStatusInvalidArgument TerminalStatus = "INVALID_ARGUMENT"
	TerminalStatusNotFound        TerminalStatus = "NOT_FOUND"
	TerminalStatusCommandExpired  TerminalStatus = "COMMAND_EXPIRED"
	TerminalStatusInfeasible      TerminalStatus = "INFEASIBLE"
)

func (status TerminalStatus) valid() bool {
	switch status {
	case TerminalStatusStale,
		TerminalStatusInvalidArgument,
		TerminalStatusNotFound,
		TerminalStatusCommandExpired,
		TerminalStatusInfeasible:
		return true
	default:
		return false
	}
}

type FinalizeTerminalCommandRequest struct {
	TripID                       string
	IntentID                     string
	OutboxID                     string
	EventID                      string
	MutationSequence             uint64
	ExpectedTripRevision         uint64
	ResultingPlannerStateVersion uint64
	Status                       TerminalStatus
	OutcomePayload               json.RawMessage
}

type FinalizedCommand struct {
	IntentID                     string
	OutboxID                     string
	TripID                       string
	EventID                      string
	MutationSequence             uint64
	State                        string
	Status                       string
	ResultingTripRevision        uint64
	ResultingPlannerStateVersion uint64
	FinalizedAt                  time.Time
	Duplicate                    bool
}

func validateTerminalFinalization(
	request FinalizeTerminalCommandRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.IntentID,
		request.OutboxID,
		request.EventID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New(
				"finalization identifiers must be canonical lowercase UUIDs",
			)
		}
	}
	if request.MutationSequence == 0 ||
		request.MutationSequence > math.MaxInt64 ||
		request.ExpectedTripRevision > math.MaxInt64 ||
		request.ResultingPlannerStateVersion > math.MaxInt64 {
		return errors.New("finalization versions are out of range")
	}
	if !request.Status.valid() {
		return errors.New("status is not a terminal durable-command rejection")
	}
	if !json.Valid(request.OutcomePayload) {
		return errors.New("terminal outcome payload must be valid JSON")
	}
	return nil
}

func terminalIntentState(status TerminalStatus) string {
	if status == TerminalStatusCommandExpired {
		return "expired"
	}
	return "rejected"
}

func (store *CommandStore) FinalizeTerminal(
	ctx context.Context,
	request FinalizeTerminalCommandRequest,
) (FinalizedCommand, error) {
	if err := validateTerminalFinalization(request); err != nil {
		return FinalizedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("begin terminal command finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tripRevision int64
	var finalizedSequence int64
	err = tx.QueryRow(ctx, `
		SELECT trip_revision, finalized_mutation_sequence
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(&tripRevision, &finalizedSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizedCommand{}, ErrTripNotFound
	}
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("lock finalization trip: %w", err)
	}

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
		SELECT event_id::text,
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
		return FinalizedCommand{}, fmt.Errorf("lock finalization intent: %w", err)
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
		return FinalizedCommand{}, fmt.Errorf("lock finalization outbox: %w", err)
	}

	if eventID != request.EventID ||
		mutationSequence != int64(request.MutationSequence) ||
		expectedRevision != int64(request.ExpectedTripRevision) {
		return FinalizedCommand{}, ErrCommandFinalizationConflict
	}
	if request.Status == TerminalStatusStale &&
		(CommandKind(commandKind) == CommandAcceptProposal ||
			CommandKind(commandKind) == CommandRejectProposal) {
		return FinalizedCommand{}, ErrProposalFinalizationRequired
	}

	desiredState := terminalIntentState(request.Status)
	if intentState != "pending" {
		var payloadMatches bool
		err = tx.QueryRow(ctx, `
			SELECT outcome_payload = $2::jsonb
			FROM command_intents
			WHERE id = $1
		`, request.IntentID, request.OutcomePayload).Scan(&payloadMatches)
		if err != nil {
			return FinalizedCommand{},
				fmt.Errorf("compare terminal outcome payload: %w", err)
		}
		if intentState != desiredState ||
			!outcomeStatus.Valid ||
			outcomeStatus.String != string(request.Status) ||
			!resultingRevision.Valid ||
			resultingRevision.Int64 != expectedRevision ||
			!resultingPlannerVersion.Valid ||
			resultingPlannerVersion.Int64 !=
				int64(request.ResultingPlannerStateVersion) ||
			!finalizedAt.Valid ||
			outboxState != "terminal_rejected" ||
			!payloadMatches {
			return FinalizedCommand{}, ErrCommandFinalizationConflict
		}
		result := FinalizedCommand{
			IntentID:                     request.IntentID,
			OutboxID:                     request.OutboxID,
			TripID:                       request.TripID,
			EventID:                      request.EventID,
			MutationSequence:             request.MutationSequence,
			State:                        intentState,
			Status:                       outcomeStatus.String,
			ResultingTripRevision:        uint64(expectedRevision),
			ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
			FinalizedAt:                  finalizedAt.Time,
			Duplicate:                    true,
		}
		if err := tx.Commit(ctx); err != nil {
			return FinalizedCommand{},
				fmt.Errorf("commit duplicate finalization: %w", err)
		}
		return result, nil
	}

	if tripRevision != expectedRevision {
		return FinalizedCommand{}, ErrTripRevisionStale
	}
	if finalizedSequence < 0 ||
		finalizedSequence == math.MaxInt64 ||
		finalizedSequence+1 != mutationSequence {
		return FinalizedCommand{}, ErrCommandFinalizationOutOfOrder
	}

	var databaseTime time.Time
	err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("read finalization time: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET finalized_mutation_sequence = $2,
		    updated_at = $3
		WHERE id = $1
	`, request.TripID, mutationSequence, databaseTime)
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("advance finalized mutation sequence: %w", err)
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
	`, request.IntentID, desiredState, string(request.Status),
		request.OutcomePayload, tripRevision,
		int64(request.ResultingPlannerStateVersion), databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("resolve terminal intent: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE planner_outbox
		SET delivery_state = 'terminal_rejected',
		    last_status = $2,
		    claim_owner = NULL,
		    claim_expires_at = NULL,
		    updated_at = $3
		WHERE id = $1
	`, request.OutboxID, string(request.Status), databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("resolve terminal outbox: %w", err)
	}

	result := FinalizedCommand{
		IntentID:                     request.IntentID,
		OutboxID:                     request.OutboxID,
		TripID:                       request.TripID,
		EventID:                      request.EventID,
		MutationSequence:             request.MutationSequence,
		State:                        desiredState,
		Status:                       string(request.Status),
		ResultingTripRevision:        uint64(tripRevision),
		ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
		FinalizedAt:                  databaseTime,
	}
	if err := tx.Commit(ctx); err != nil {
		return FinalizedCommand{},
			fmt.Errorf("commit terminal command finalization: %w", err)
	}
	return result, nil
}
