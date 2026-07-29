package persistence

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DigestAlgorithmRFC8785SHA256V1 = "rfc8785-sha256-v1"

var (
	ErrIdempotencyKeyReused      = errors.New("idempotency key reused")
	ErrTripRevisionStale         = errors.New("trip revision is stale")
	ErrDurableCommandBlocked     = errors.New("another durable command is unresolved")
	ErrMutationSequenceExhausted = errors.New("mutation sequence exhausted")
)

type CommandKind string

const (
	CommandActivityStatusChanged    CommandKind = "activity_status_changed"
	CommandActivityDelayed          CommandKind = "activity_delayed"
	CommandReservationChanged       CommandKind = "reservation_changed"
	CommandMandatoryDeadlineChanged CommandKind = "mandatory_deadline_changed"
	CommandOperatingHoursChanged    CommandKind = "operating_hours_changed"
	CommandPlaceFoundClosed         CommandKind = "place_found_closed"
	CommandTravelDelay              CommandKind = "travel_delay"
	CommandAcceptProposal           CommandKind = "accept_proposal"
	CommandRejectProposal           CommandKind = "reject_proposal"
)

func (kind CommandKind) isRuntimeFirst() bool {
	switch kind {
	case CommandActivityStatusChanged,
		CommandActivityDelayed,
		CommandReservationChanged,
		CommandMandatoryDeadlineChanged,
		CommandOperatingHoursChanged,
		CommandPlaceFoundClosed,
		CommandTravelDelay,
		CommandAcceptProposal,
		CommandRejectProposal:
		return true
	default:
		return false
	}
}

type PlannedCurrentPlan struct {
	ID       string
	Payload  []byte
	Checksum [32]byte
}

type RecordRuntimeCommandRequest struct {
	IntentID             string
	OutboxID             string
	TripID               string
	OwnerUserID          string
	MessageID            string
	EventID              string
	ExpectedTripRevision uint64
	Kind                 CommandKind
	CommandExpiresAt     *time.Time
	PayloadDigest        [32]byte
	CommandPayload       json.RawMessage
	EventPayload         json.RawMessage
	PlannedCurrentPlan   *PlannedCurrentPlan
}

type RecordedCommand struct {
	IntentID                     string
	OutboxID                     string
	TripID                       string
	MessageID                    string
	EventID                      string
	MutationSequence             uint64
	ExpectedTripRevision         uint64
	Kind                         CommandKind
	State                        string
	RuntimeSyncState             string
	OutcomeStatus                *string
	OutcomePayload               json.RawMessage
	ResultingTripRevision        *uint64
	ResultingPlannerStateVersion *uint64
	RecordedAt                   time.Time
	FinalizedAt                  *time.Time
	Duplicate                    bool
}

type CommandStore struct {
	pool *pgxpool.Pool
}

func NewCommandStore(pool *pgxpool.Pool) (*CommandStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &CommandStore{pool: pool}, nil
}

func validCanonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index := range value {
		switch index {
		case 8, 13, 18, 23:
			if value[index] != '-' {
				return false
			}
		default:
			character := value[index]
			if !((character >= '0' && character <= '9') ||
				(character >= 'a' && character <= 'f')) {
				return false
			}
		}
	}
	return true
}

func validateRecordRuntimeCommand(request RecordRuntimeCommandRequest) error {
	for _, value := range []string{
		request.IntentID,
		request.OutboxID,
		request.TripID,
		request.OwnerUserID,
		request.MessageID,
		request.EventID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("command identifiers must be canonical lowercase UUIDs")
		}
	}
	if request.EventID != request.MessageID {
		return errors.New("durable event id must equal message id")
	}
	if request.ExpectedTripRevision > math.MaxInt64 {
		return errors.New("expected trip revision is out of range")
	}
	if !request.Kind.isRuntimeFirst() {
		return errors.New("command kind is not runtime-first")
	}
	if !json.Valid(request.CommandPayload) ||
		!json.Valid(request.EventPayload) {
		return errors.New("command and event payloads must be valid JSON")
	}
	if request.Kind == CommandAcceptProposal {
		if request.PlannedCurrentPlan == nil ||
			!validCanonicalUUID(request.PlannedCurrentPlan.ID) ||
			len(request.PlannedCurrentPlan.Payload) == 0 {
			return errors.New("proposal acceptance requires a planned current plan")
		}
	} else if request.PlannedCurrentPlan != nil {
		return errors.New("planned current plan is valid only for proposal acceptance")
	}
	return nil
}

func scanRecordedCommand(
	row pgx.Row,
	duplicate bool,
) (RecordedCommand, []byte, string, error) {
	var result RecordedCommand
	var outboxID pgtype.Text
	var mutationSequence int64
	var expectedRevision int64
	var kind string
	var digestAlgorithm string
	var payloadDigest []byte
	var outcomeStatus pgtype.Text
	var outcomePayload []byte
	var resultingRevision pgtype.Int8
	var resultingPlannerVersion pgtype.Int8
	var finalizedAt pgtype.Timestamptz
	err := row.Scan(
		&result.IntentID,
		&outboxID,
		&result.TripID,
		&result.MessageID,
		&result.EventID,
		&mutationSequence,
		&expectedRevision,
		&kind,
		&digestAlgorithm,
		&payloadDigest,
		&result.State,
		&outcomeStatus,
		&outcomePayload,
		&resultingRevision,
		&resultingPlannerVersion,
		&result.RuntimeSyncState,
		&result.RecordedAt,
		&finalizedAt,
	)
	if err != nil {
		return RecordedCommand{}, nil, "", err
	}
	if mutationSequence <= 0 || expectedRevision < 0 ||
		len(payloadDigest) != 32 {
		return RecordedCommand{}, nil, "",
			errors.New("stored command violates persistence contract")
	}
	result.MutationSequence = uint64(mutationSequence)
	result.ExpectedTripRevision = uint64(expectedRevision)
	result.Kind = CommandKind(kind)
	result.Duplicate = duplicate
	if outboxID.Valid {
		result.OutboxID = outboxID.String
	}
	if outcomeStatus.Valid {
		value := outcomeStatus.String
		result.OutcomeStatus = &value
	}
	if outcomePayload != nil {
		if !json.Valid(outcomePayload) {
			return RecordedCommand{}, nil, "",
				errors.New("stored command outcome is invalid JSON")
		}
		result.OutcomePayload = outcomePayload
	}
	if resultingRevision.Valid {
		if resultingRevision.Int64 < 0 {
			return RecordedCommand{}, nil, "",
				errors.New("stored trip revision is negative")
		}
		value := uint64(resultingRevision.Int64)
		result.ResultingTripRevision = &value
	}
	if resultingPlannerVersion.Valid {
		if resultingPlannerVersion.Int64 < 0 {
			return RecordedCommand{}, nil, "",
				errors.New("stored planner version is negative")
		}
		value := uint64(resultingPlannerVersion.Int64)
		result.ResultingPlannerStateVersion = &value
	}
	if finalizedAt.Valid {
		value := finalizedAt.Time
		result.FinalizedAt = &value
	}
	return result, payloadDigest, digestAlgorithm, nil
}

const recordedCommandSelect = `
	SELECT intent.id::text,
	       outbox.id::text,
	       intent.trip_id::text,
	       intent.message_id::text,
	       intent.event_id::text,
	       intent.mutation_sequence,
	       intent.expected_trip_revision,
	       intent.command_kind,
	       intent.digest_algorithm,
	       intent.payload_digest,
	       intent.state,
	       intent.outcome_status,
	       intent.outcome_payload,
	       intent.resulting_trip_revision,
	       intent.resulting_planner_state_version,
	       intent.runtime_sync_state,
	       intent.recorded_at,
	       intent.finalized_at
	FROM command_intents AS intent
	LEFT JOIN planner_outbox AS outbox
	  ON outbox.command_intent_id = intent.id
	WHERE intent.trip_id = $1 AND intent.message_id = $2
	FOR UPDATE OF intent
`

func (store *CommandStore) RecordRuntimeFirst(
	ctx context.Context,
	request RecordRuntimeCommandRequest,
) (RecordedCommand, error) {
	if err := validateRecordRuntimeCommand(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin command recording: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tripRevision int64
	var nextMutationSequence int64
	err = tx.QueryRow(ctx, `
		SELECT trip_revision, next_mutation_sequence
		FROM trips
		WHERE id = $1 AND owner_user_id = $2
		FOR UPDATE
	`, request.TripID, request.OwnerUserID).Scan(
		&tripRevision,
		&nextMutationSequence,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrTripNotFound
	}
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock command trip: %w", err)
	}

	existing, storedDigest, digestAlgorithm, err :=
		scanRecordedCommand(
			tx.QueryRow(
				ctx,
				recordedCommandSelect,
				request.TripID,
				request.MessageID,
			),
			true,
		)
	if err == nil {
		if digestAlgorithm != DigestAlgorithmRFC8785SHA256V1 ||
			subtle.ConstantTimeCompare(
				storedDigest,
				request.PayloadDigest[:],
			) != 1 {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if err := tx.Commit(ctx); err != nil {
			return RecordedCommand{},
				fmt.Errorf("commit duplicate command lookup: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read existing command: %w", err)
	}
	if tripRevision < 0 ||
		uint64(tripRevision) != request.ExpectedTripRevision {
		return RecordedCommand{}, ErrTripRevisionStale
	}
	if nextMutationSequence <= 0 ||
		nextMutationSequence == math.MaxInt64 {
		return RecordedCommand{}, ErrMutationSequenceExhausted
	}

	intentRows, err := tx.Query(ctx, `
		SELECT id
		FROM command_intents
		WHERE trip_id = $1
		  AND (
		    (application_order = 'runtime_first' AND state = 'pending') OR
		    (application_order = 'canonical_first' AND
		     runtime_sync_state IN ('pending', 'paused_internal'))
		  )
		ORDER BY id
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock unresolved commands: %w", err)
	}
	hasUnresolved := intentRows.Next()
	for intentRows.Next() {
	}
	if err := intentRows.Err(); err != nil {
		intentRows.Close()
		return RecordedCommand{}, fmt.Errorf("scan unresolved commands: %w", err)
	}
	intentRows.Close()

	outboxRows, err := tx.Query(ctx, `
		SELECT id
		FROM planner_outbox
		WHERE trip_id = $1
		  AND delivery_state IN ('pending', 'paused_internal')
		ORDER BY id
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock unresolved outbox: %w", err)
	}
	for outboxRows.Next() {
	}
	if err := outboxRows.Err(); err != nil {
		outboxRows.Close()
		return RecordedCommand{}, fmt.Errorf("scan unresolved outbox: %w", err)
	}
	outboxRows.Close()
	if hasUnresolved {
		return RecordedCommand{}, ErrDurableCommandBlocked
	}

	var plannedID any
	var plannedPayload any
	var plannedChecksum any
	if request.PlannedCurrentPlan != nil {
		plannedID = request.PlannedCurrentPlan.ID
		plannedPayload = request.PlannedCurrentPlan.Payload
		plannedChecksum = request.PlannedCurrentPlan.Checksum[:]
	}
	var expiresAt any
	if request.CommandExpiresAt != nil {
		expiresAt = *request.CommandExpiresAt
	}
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET next_mutation_sequence = next_mutation_sequence + 1,
		    updated_at = clock_timestamp()
		WHERE id = $1
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate mutation sequence: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			command_expires_at, digest_algorithm, payload_digest,
			command_payload, state, planned_current_plan_id,
			planned_current_plan_payload,
			planned_current_plan_checksum_sha256, runtime_sync_state,
			recorded_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, 'runtime_first',
			$8, 'rfc8785-sha256-v1', $9, $10, 'pending',
			$11, $12, $13, 'not_required', clock_timestamp()
		)
	`, request.IntentID, request.TripID, request.MessageID,
		request.EventID, nextMutationSequence,
		int64(request.ExpectedTripRevision), string(request.Kind),
		expiresAt, request.PayloadDigest[:], request.CommandPayload,
		plannedID, plannedPayload, plannedChecksum)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("insert command intent: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO planner_outbox (
			id, command_intent_id, trip_id, mutation_sequence,
			event_schema_version, event_payload, delivery_state
		) VALUES ($1, $2, $3, $4, 1, $5, 'pending')
	`, request.OutboxID, request.IntentID, request.TripID,
		nextMutationSequence, request.EventPayload)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("insert planner outbox: %w", err)
	}

	recorded, _, _, err := scanRecordedCommand(
		tx.QueryRow(
			ctx,
			recordedCommandSelect,
			request.TripID,
			request.MessageID,
		),
		false,
	)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("read recorded command: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit command recording: %w", err)
	}
	return recorded, nil
}
