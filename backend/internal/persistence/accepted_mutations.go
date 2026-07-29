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

type ActivityState string

const (
	ActivityStatePlanned   ActivityState = "planned"
	ActivityStateStarted   ActivityState = "started"
	ActivityStateCompleted ActivityState = "completed"
	ActivityStateSkipped   ActivityState = "skipped"
)

func (state ActivityState) valid() bool {
	switch state {
	case ActivityStatePlanned,
		ActivityStateStarted,
		ActivityStateCompleted,
		ActivityStateSkipped:
		return true
	default:
		return false
	}
}

type OpenWindow struct {
	OpensAtUnixMilliseconds  int64
	ClosesAtUnixMilliseconds int64
}

type AcceptedMutation struct {
	ActivityStatus    *ActivityStatusMutation
	ActivityDelay     *ActivityDelayMutation
	Reservation       *ReservationMutation
	MandatoryDeadline *MandatoryDeadlineMutation
	OperatingHours    *OperatingHoursMutation
	PlaceFoundClosed  *PlaceFoundClosedMutation
	TravelDelay       *TravelDelayMutation
}

type ActivityStatusMutation struct {
	ActivityID string
	State      ActivityState
}

type ActivityDelayMutation struct {
	ActivityID   string
	DelaySeconds uint32
}

type ReservationMutation struct {
	ActivityID                       string
	ReservationStartUnixMilliseconds *int64
	ReservationGraceSeconds          uint32
}

type MandatoryDeadlineMutation struct {
	ActivityID                   string
	LatestFinishUnixMilliseconds int64
}

type OperatingHoursMutation struct {
	ActivityID  string
	OpenWindows []OpenWindow
}

type PlaceFoundClosedMutation struct {
	ActivityID                 string
	ObservedAtUnixMilliseconds int64
}

type TravelDelayMutation struct {
	FromActivityID             string
	ToActivityID               string
	AdditionalSeconds          uint32
	ObservedAtUnixMilliseconds int64
}

type FinalizeAcceptedMutationRequest struct {
	TripID                       string
	IntentID                     string
	OutboxID                     string
	EventID                      string
	MutationSequence             uint64
	ExpectedTripRevision         uint64
	ResultingPlannerStateVersion uint64
	Mutation                     AcceptedMutation
	OutcomePayload               json.RawMessage
}

func (mutation AcceptedMutation) kind() (CommandKind, error) {
	var kind CommandKind
	count := 0
	set := func(candidate CommandKind, present bool) {
		if present {
			kind = candidate
			count++
		}
	}
	set(CommandActivityStatusChanged, mutation.ActivityStatus != nil)
	set(CommandActivityDelayed, mutation.ActivityDelay != nil)
	set(CommandReservationChanged, mutation.Reservation != nil)
	set(CommandMandatoryDeadlineChanged, mutation.MandatoryDeadline != nil)
	set(CommandOperatingHoursChanged, mutation.OperatingHours != nil)
	set(CommandPlaceFoundClosed, mutation.PlaceFoundClosed != nil)
	set(CommandTravelDelay, mutation.TravelDelay != nil)
	if count != 1 {
		return "", errors.New("exactly one accepted mutation is required")
	}
	return kind, nil
}

func validateAcceptedMutationRequest(
	request FinalizeAcceptedMutationRequest,
) (CommandKind, error) {
	for _, value := range []string{
		request.TripID,
		request.IntentID,
		request.OutboxID,
		request.EventID,
	} {
		if !validCanonicalUUID(value) {
			return "", errors.New(
				"accepted-mutation identifiers must be canonical lowercase UUIDs",
			)
		}
	}
	if request.MutationSequence == 0 ||
		request.MutationSequence > math.MaxInt64 ||
		request.ExpectedTripRevision >= math.MaxInt64 ||
		request.ResultingPlannerStateVersion > math.MaxInt64 {
		return "", errors.New("accepted-mutation versions are out of range")
	}
	if !json.Valid(request.OutcomePayload) {
		return "", errors.New("accepted-mutation outcome must be valid JSON")
	}
	kind, err := request.Mutation.kind()
	if err != nil {
		return "", err
	}
	var activityIDs []string
	switch kind {
	case CommandActivityStatusChanged:
		if !request.Mutation.ActivityStatus.State.valid() {
			return "", errors.New("activity state is invalid")
		}
		activityIDs = []string{request.Mutation.ActivityStatus.ActivityID}
	case CommandActivityDelayed:
		if request.Mutation.ActivityDelay.DelaySeconds > math.MaxInt32 {
			return "", errors.New("activity delay is out of range")
		}
		activityIDs = []string{request.Mutation.ActivityDelay.ActivityID}
	case CommandReservationChanged:
		if request.Mutation.Reservation.ReservationGraceSeconds > math.MaxInt32 {
			return "", errors.New("reservation grace is out of range")
		}
		activityIDs = []string{request.Mutation.Reservation.ActivityID}
	case CommandMandatoryDeadlineChanged:
		activityIDs = []string{request.Mutation.MandatoryDeadline.ActivityID}
	case CommandOperatingHoursChanged:
		activityIDs = []string{request.Mutation.OperatingHours.ActivityID}
		var previousClose int64
		for index, window := range request.Mutation.OperatingHours.OpenWindows {
			if window.OpensAtUnixMilliseconds >=
				window.ClosesAtUnixMilliseconds ||
				(index > 0 &&
					window.OpensAtUnixMilliseconds < previousClose) {
				return "", errors.New(
					"operating-hours windows must be normalized",
				)
			}
			previousClose = window.ClosesAtUnixMilliseconds
		}
	case CommandPlaceFoundClosed:
		activityIDs = []string{request.Mutation.PlaceFoundClosed.ActivityID}
	case CommandTravelDelay:
		if request.Mutation.TravelDelay.AdditionalSeconds > math.MaxInt32 {
			return "", errors.New("travel delay is out of range")
		}
		activityIDs = []string{
			request.Mutation.TravelDelay.FromActivityID,
			request.Mutation.TravelDelay.ToActivityID,
		}
	default:
		return "", errors.New("accepted mutation kind is invalid")
	}
	for _, activityID := range activityIDs {
		if !validCanonicalUUID(activityID) {
			return "", errors.New(
				"activity identifiers must be canonical lowercase UUIDs",
			)
		}
	}
	return kind, nil
}

func unixMilliseconds(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

func applyAcceptedMutation(
	ctx context.Context,
	tx pgx.Tx,
	tripID string,
	mutation AcceptedMutation,
	kind CommandKind,
) error {
	var (
		tag pgconnCommandTag
		err error
	)
	switch kind {
	case CommandActivityStatusChanged:
		value := mutation.ActivityStatus
		tag, err = execMutation(ctx, tx, `
			UPDATE trip_activities
			SET activity_state = $3
			WHERE trip_id = $1 AND id = $2
		`, tripID, value.ActivityID, string(value.State))
	case CommandActivityDelayed:
		value := mutation.ActivityDelay
		tag, err = execMutation(ctx, tx, `
			UPDATE trip_activities
			SET activity_delay_seconds = $3
			WHERE trip_id = $1 AND id = $2
		`, tripID, value.ActivityID, int32(value.DelaySeconds))
	case CommandReservationChanged:
		value := mutation.Reservation
		var reservationStart *time.Time
		if value.ReservationStartUnixMilliseconds != nil {
			converted := unixMilliseconds(
				*value.ReservationStartUnixMilliseconds,
			)
			reservationStart = &converted
		}
		tag, err = execMutation(ctx, tx, `
			UPDATE trip_activities
			SET reservation_start = $3,
			    reservation_grace_seconds = $4
			WHERE trip_id = $1 AND id = $2
		`, tripID, value.ActivityID, reservationStart,
			int32(value.ReservationGraceSeconds))
	case CommandMandatoryDeadlineChanged:
		value := mutation.MandatoryDeadline
		tag, err = execMutation(ctx, tx, `
			UPDATE trip_activities
			SET mandatory_deadline = $3
			WHERE trip_id = $1 AND id = $2
		`, tripID, value.ActivityID,
			unixMilliseconds(value.LatestFinishUnixMilliseconds))
	case CommandOperatingHoursChanged:
		value := mutation.OperatingHours
		var exists bool
		err = tx.QueryRow(ctx, `
			SELECT true
			FROM trip_activities
			WHERE trip_id = $1 AND id = $2
		`, tripID, value.ActivityID).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMutationTargetNotFound
		}
		if err != nil {
			return fmt.Errorf("find operating-hours activity: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			DELETE FROM activity_open_windows
			WHERE trip_id = $1 AND activity_id = $2
		`, tripID, value.ActivityID); err != nil {
			return fmt.Errorf("replace operating-hours windows: %w", err)
		}
		for index, window := range value.OpenWindows {
			if _, err = tx.Exec(ctx, `
				INSERT INTO activity_open_windows (
					trip_id, activity_id, window_index, opens_at, closes_at
				) VALUES ($1, $2, $3, $4, $5)
			`, tripID, value.ActivityID, index,
				unixMilliseconds(window.OpensAtUnixMilliseconds),
				unixMilliseconds(window.ClosesAtUnixMilliseconds)); err != nil {
				return fmt.Errorf("insert operating-hours window: %w", err)
			}
		}
		return nil
	case CommandPlaceFoundClosed:
		value := mutation.PlaceFoundClosed
		tag, err = execMutation(ctx, tx, `
			UPDATE trip_activities
			SET found_closed_at = $3
			WHERE trip_id = $1 AND id = $2
		`, tripID, value.ActivityID,
			unixMilliseconds(value.ObservedAtUnixMilliseconds))
	case CommandTravelDelay:
		value := mutation.TravelDelay
		tag, err = execMutation(ctx, tx, `
			INSERT INTO trip_travel_delays (
				trip_id, from_activity_id, to_activity_id,
				additional_seconds, observed_at
			) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (trip_id, from_activity_id, to_activity_id)
			DO UPDATE SET
				additional_seconds = EXCLUDED.additional_seconds,
				observed_at = EXCLUDED.observed_at
		`, tripID, value.FromActivityID, value.ToActivityID,
			int32(value.AdditionalSeconds),
			unixMilliseconds(value.ObservedAtUnixMilliseconds))
	default:
		return errors.New("unsupported accepted mutation")
	}
	if err != nil {
		return fmt.Errorf("apply %s mutation: %w", kind, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrMutationTargetNotFound
	}
	return nil
}

// pgconnCommandTag is the subset returned by pgx execution that finalization
// needs. Keeping it local avoids exposing PostgreSQL types in the API.
type pgconnCommandTag interface {
	RowsAffected() int64
}

func execMutation(
	ctx context.Context,
	tx pgx.Tx,
	sql string,
	arguments ...any,
) (pgconnCommandTag, error) {
	return tx.Exec(ctx, sql, arguments...)
}

var ErrMutationTargetNotFound = errors.New("canonical mutation target not found")

func (store *CommandStore) FinalizeAcceptedMutation(
	ctx context.Context,
	request FinalizeAcceptedMutationRequest,
) (FinalizedCommand, error) {
	kind, err := validateAcceptedMutationRequest(request)
	if err != nil {
		return FinalizedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return FinalizedCommand{},
			fmt.Errorf("begin accepted-mutation finalization: %w", err)
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
		return FinalizedCommand{}, fmt.Errorf("lock mutation trip: %w", err)
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
		return FinalizedCommand{}, fmt.Errorf("lock mutation intent: %w", err)
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
		return FinalizedCommand{}, fmt.Errorf("lock mutation outbox: %w", err)
	}

	if eventID != request.EventID ||
		mutationSequence != int64(request.MutationSequence) ||
		expectedRevision != int64(request.ExpectedTripRevision) ||
		CommandKind(commandKind) != kind {
		return FinalizedCommand{}, ErrCommandFinalizationConflict
	}
	resultRevision := request.ExpectedTripRevision + 1
	if intentState != "pending" {
		var payloadMatches bool
		err = tx.QueryRow(ctx, `
			SELECT outcome_payload = $2::jsonb
			FROM command_intents
			WHERE id = $1
		`, request.IntentID, request.OutcomePayload).Scan(&payloadMatches)
		if err != nil {
			return FinalizedCommand{},
				fmt.Errorf("compare accepted-mutation outcome: %w", err)
		}
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
			!payloadMatches {
			return FinalizedCommand{}, ErrCommandFinalizationConflict
		}
		result := FinalizedCommand{
			IntentID:                     request.IntentID,
			OutboxID:                     request.OutboxID,
			TripID:                       request.TripID,
			EventID:                      request.EventID,
			MutationSequence:             request.MutationSequence,
			State:                        "applied",
			Status:                       "OK",
			ResultingTripRevision:        resultRevision,
			ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
			FinalizedAt:                  finalizedAt.Time,
			Duplicate:                    true,
		}
		if err := tx.Commit(ctx); err != nil {
			return FinalizedCommand{},
				fmt.Errorf("commit duplicate accepted mutation: %w", err)
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

	if err := applyAcceptedMutation(
		ctx,
		tx,
		request.TripID,
		request.Mutation,
		kind,
	); err != nil {
		return FinalizedCommand{}, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id
		FROM plan_proposals
		WHERE trip_id = $1 AND state = 'pending'
		ORDER BY id
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("lock stale proposals: %w", err)
	}
	for rows.Next() {
		var proposalID string
		if err := rows.Scan(&proposalID); err != nil {
			rows.Close()
			return FinalizedCommand{}, fmt.Errorf("scan stale proposal: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return FinalizedCommand{}, fmt.Errorf("iterate stale proposals: %w", err)
	}
	rows.Close()

	var databaseTime time.Time
	err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("read mutation finalization time: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'stale',
		    decided_at = $2
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID, databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("invalidate stale proposals: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2,
		    finalized_mutation_sequence = $3,
		    updated_at = $4
		WHERE id = $1
	`, request.TripID, int64(resultRevision), mutationSequence, databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("advance mutation trip: %w", err)
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
	`, request.IntentID, request.OutcomePayload, int64(resultRevision),
		int64(request.ResultingPlannerStateVersion), databaseTime)
	if err != nil {
		return FinalizedCommand{}, fmt.Errorf("resolve accepted mutation: %w", err)
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
		return FinalizedCommand{}, fmt.Errorf("resolve accepted outbox: %w", err)
	}

	result := FinalizedCommand{
		IntentID:                     request.IntentID,
		OutboxID:                     request.OutboxID,
		TripID:                       request.TripID,
		EventID:                      request.EventID,
		MutationSequence:             request.MutationSequence,
		State:                        "applied",
		Status:                       "OK",
		ResultingTripRevision:        resultRevision,
		ResultingPlannerStateVersion: request.ResultingPlannerStateVersion,
		FinalizedAt:                  databaseTime,
	}
	if err := tx.Commit(ctx); err != nil {
		return FinalizedCommand{},
			fmt.Errorf("commit accepted mutation: %w", err)
	}
	return result, nil
}
