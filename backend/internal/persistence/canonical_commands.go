package persistence

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrCanonicalTripConflict   = errors.New("canonical trip already exists")
	ErrCanonicalOwnerMissing   = errors.New("canonical trip owner does not exist")
	ErrCanonicalMirrorCapacity = errors.New("canonical mirror capacity exhausted")
)

type CanonicalPlanSegmentDraft struct {
	ActivityID string
	Scheduled  bool
	Start      *int64
	End        *int64
}

type CreateTripRequest struct {
	TripID              string
	OwnerUserID         string
	DefaultTimeZoneName string
	IntentID            string
	MessageID           string
	EventID             string
	PlanID              string
	Activities          []CanonicalActivity
	TravelDelays        []CanonicalTravelDelay
	PlanSegments        []CanonicalPlanSegmentDraft
	CommandPayload      json.RawMessage
	PayloadDigest       [32]byte
	OutcomePayload      json.RawMessage
}

type ReplaceCurrentPlanRequest struct {
	TripID                    string
	OwnerUserID               string
	IntentID                  string
	OutboxID                  string
	MessageID                 string
	EventID                   string
	PlanID                    string
	ExpectedTripRevision      uint64
	MaxPendingCanonicalMirror uint32
	PlanSegments              []CanonicalPlanSegmentDraft
	CommandPayload            json.RawMessage
	EventPayload              json.RawMessage
	PayloadDigest             [32]byte
	OutcomePayload            json.RawMessage
}

type ReorderActivitiesRequest struct {
	TripID                    string
	OwnerUserID               string
	IntentID                  string
	OutboxID                  string
	MessageID                 string
	EventID                   string
	PlanID                    string
	ExpectedTripRevision      uint64
	MaxPendingCanonicalMirror uint32
	ActivityIDs               []string
	PlanSegments              []CanonicalPlanSegmentDraft
	CommandPayload            json.RawMessage
	EventPayload              json.RawMessage
	PayloadDigest             [32]byte
	OutcomePayload            json.RawMessage
}

type RemoveActivityRequest struct {
	TripID                    string
	OwnerUserID               string
	IntentID                  string
	OutboxID                  string
	MessageID                 string
	EventID                   string
	PlanID                    string
	ExpectedTripRevision      uint64
	MaxPendingCanonicalMirror uint32
	ActivityID                string
	PlanSegments              []CanonicalPlanSegmentDraft
	CommandPayload            json.RawMessage
	EventPayload              json.RawMessage
	PayloadDigest             [32]byte
	OutcomePayload            json.RawMessage
}

type ReplaceActivityRequest struct {
	TripID                    string
	OwnerUserID               string
	IntentID                  string
	OutboxID                  string
	MessageID                 string
	EventID                   string
	PlanID                    string
	ExpectedTripRevision      uint64
	MaxPendingCanonicalMirror uint32
	Activity                  CanonicalActivity
	PlanSegments              []CanonicalPlanSegmentDraft
	CommandPayload            json.RawMessage
	EventPayload              json.RawMessage
	PayloadDigest             [32]byte
	OutcomePayload            json.RawMessage
}

type AddActivityRequest struct {
	TripID                    string
	OwnerUserID               string
	IntentID                  string
	OutboxID                  string
	MessageID                 string
	EventID                   string
	PlanID                    string
	ExpectedTripRevision      uint64
	MaxPendingCanonicalMirror uint32
	Ordinal                   uint32
	Activity                  CanonicalActivity
	PlanSegments              []CanonicalPlanSegmentDraft
	CommandPayload            json.RawMessage
	EventPayload              json.RawMessage
	PayloadDigest             [32]byte
	OutcomePayload            json.RawMessage
}

func appendVarint(output []byte, value uint64) []byte {
	for value >= 0x80 {
		output = append(output, byte(value)|0x80)
		value >>= 7
	}
	return append(output, byte(value))
}

func appendBytesField(output []byte, number uint64, value []byte) []byte {
	output = appendVarint(output, number<<3|2)
	output = appendVarint(output, uint64(len(value)))
	return append(output, value...)
}

func appendVarintField(output []byte, number uint64, value uint64) []byte {
	output = appendVarint(output, number<<3)
	return appendVarint(output, value)
}

func userCurrentPlanPayload(
	planID string,
	planRevision uint64,
	createdAtUnixMS int64,
	segments []CanonicalPlanSegmentDraft,
) []byte {
	output := make([]byte, 0, 64+len(segments)*32)
	output = appendBytesField(output, 1, []byte(planID))
	output = appendVarintField(output, 2, planRevision)
	output = appendVarintField(output, 3, 1)
	for _, segment := range segments {
		encoded := make([]byte, 0, 48)
		encoded = appendBytesField(encoded, 1, []byte(segment.ActivityID))
		if segment.Scheduled {
			encoded = appendVarintField(encoded, 2, 1)
			encoded = appendVarintField(encoded, 3, uint64(*segment.Start))
			encoded = appendVarintField(encoded, 4, uint64(*segment.End))
		} else {
			encoded = appendVarintField(encoded, 2, 2)
		}
		output = appendBytesField(output, 4, encoded)
	}
	output = appendVarintField(output, 5, uint64(createdAtUnixMS))
	return output
}

func validateCanonicalActivityDraft(
	activity CanonicalActivity,
	index int,
) error {
	if activity.Ordinal != uint32(index) {
		return errors.New("activity draft ordinal is invalid")
	}
	return validateCanonicalActivity(activity)
}

func validateCanonicalActivity(activity CanonicalActivity) error {
	if !validCanonicalUUID(activity.ID) ||
		activity.PlaceID == "" ||
		activity.DisplayName == "" ||
		activity.TimeZoneName == "" ||
		(activity.InboundTravelMode != "walking" &&
			activity.InboundTravelMode != "driving") ||
		(activity.ActivityClass != "fixed" &&
			activity.ActivityClass != "flexible") ||
		!activity.ActivityState.valid() ||
		activity.ActivityDelaySeconds > math.MaxInt32 ||
		activity.ReservationGraceSeconds > math.MaxInt32 ||
		activity.MinDurationSeconds > math.MaxInt32 ||
		activity.PreferredDurationSeconds > math.MaxInt32 ||
		activity.MaxDurationSeconds > math.MaxInt32 ||
		activity.MinDurationSeconds > activity.PreferredDurationSeconds ||
		activity.PreferredDurationSeconds > activity.MaxDurationSeconds {
		return errors.New("activity draft is invalid")
	}
	if math.IsNaN(activity.Latitude) || math.IsInf(activity.Latitude, 0) ||
		activity.Latitude < -90 || activity.Latitude > 90 ||
		math.IsNaN(activity.Longitude) || math.IsInf(activity.Longitude, 0) ||
		activity.Longitude < -180 || activity.Longitude > 180 {
		return errors.New("activity location is invalid")
	}
	var previousClose time.Time
	for index, window := range activity.OpenWindows {
		if !window.OpensAt.Before(window.ClosesAt) ||
			(index > 0 && window.OpensAt.Before(previousClose)) {
			return errors.New("activity opening windows are not normalized")
		}
		previousClose = window.ClosesAt
	}
	return nil
}

func validateCreateTripRequest(request CreateTripRequest) error {
	for _, value := range []string{
		request.TripID,
		request.OwnerUserID,
		request.IntentID,
		request.MessageID,
		request.EventID,
		request.PlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("create-trip identifiers must be canonical UUIDs")
		}
	}
	if request.MessageID != request.EventID ||
		request.DefaultTimeZoneName == "" ||
		len(request.Activities) == 0 || len(request.Activities) > 64 ||
		!json.Valid(request.CommandPayload) ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("create-trip draft is invalid")
	}
	digest := sha256.Sum256(request.CommandPayload)
	if subtle.ConstantTimeCompare(digest[:], request.PayloadDigest[:]) != 1 {
		return errors.New("create-trip payload digest is invalid")
	}
	if len(request.PlanSegments) != len(request.Activities) {
		return errors.New("create-trip plan must cover every activity")
	}
	activityIDs := make(map[string]struct{}, len(request.Activities))
	activityStates := make(map[string]ActivityState, len(request.Activities))
	for index, activity := range request.Activities {
		if err := validateCanonicalActivityDraft(activity, index); err != nil {
			return err
		}
		if _, exists := activityIDs[activity.ID]; exists {
			return errors.New("create-trip activities must be unique")
		}
		activityIDs[activity.ID] = struct{}{}
		activityStates[activity.ID] = activity.ActivityState
	}
	seen := make(map[string]struct{}, len(request.PlanSegments))
	prefixOpen := true
	started := false
	var previousEnd *int64
	for index, segment := range request.PlanSegments {
		if !validCanonicalUUID(segment.ActivityID) {
			return errors.New("create-trip plan activity id is invalid")
		}
		if _, exists := activityIDs[segment.ActivityID]; !exists {
			return errors.New("create-trip plan references an unknown activity")
		}
		if _, exists := seen[segment.ActivityID]; exists {
			return errors.New("create-trip plan repeats an activity")
		}
		seen[segment.ActivityID] = struct{}{}
		if segment.Scheduled {
			if segment.Start == nil || segment.End == nil ||
				*segment.Start >= *segment.End ||
				(previousEnd != nil && *segment.Start < *previousEnd) {
				return errors.New("create-trip scheduled plan segment is invalid")
			}
			end := *segment.End
			previousEnd = &end
		} else if segment.Start != nil || segment.End != nil {
			return errors.New("create-trip omitted segment has times")
		}
		state := activityStates[segment.ActivityID]
		terminal := state == ActivityStateCompleted || state == ActivityStateSkipped
		if prefixOpen && terminal {
			continue
		}
		prefixOpen = false
		if terminal {
			return errors.New("create-trip terminal activity is outside the prefix")
		}
		if state == ActivityStateStarted {
			if started || index != len(seen)-1 {
				return errors.New("create-trip started activity is out of order")
			}
			started = true
		}
	}
	if len(seen) != len(request.Activities) {
		return errors.New("create-trip plan omits an activity")
	}
	if _, err := parseUserCurrentPlan(userCurrentPlanPayload(
		request.PlanID, 1, 1, request.PlanSegments)); err != nil {
		return fmt.Errorf("create-trip plan is invalid: %w", err)
	}
	for _, delay := range request.TravelDelays {
		if !validCanonicalUUID(delay.FromActivityID) ||
			!validCanonicalUUID(delay.ToActivityID) ||
			delay.AdditionalSeconds > math.MaxInt32 {
			return errors.New("create-trip travel delay is invalid")
		}
		if _, exists := activityIDs[delay.FromActivityID]; !exists {
			return errors.New("create-trip delay origin is unknown")
		}
		if _, exists := activityIDs[delay.ToActivityID]; !exists {
			return errors.New("create-trip delay destination is unknown")
		}
	}
	return nil
}

func validateReplacementPlan(
	segments []CanonicalPlanSegmentDraft,
	activityStates map[string]ActivityState,
) error {
	if len(segments) != len(activityStates) {
		return errors.New("replacement plan must cover every activity")
	}
	seen := make(map[string]struct{}, len(segments))
	prefixOpen := true
	started := false
	var previousEnd *int64
	for index, segment := range segments {
		if !validCanonicalUUID(segment.ActivityID) {
			return errors.New("replacement plan activity id is invalid")
		}
		if _, exists := activityStates[segment.ActivityID]; !exists {
			return errors.New("replacement plan references an unknown activity")
		}
		if _, exists := seen[segment.ActivityID]; exists {
			return errors.New("replacement plan repeats an activity")
		}
		seen[segment.ActivityID] = struct{}{}
		if segment.Scheduled {
			if segment.Start == nil || segment.End == nil ||
				*segment.Start >= *segment.End ||
				(previousEnd != nil && *segment.Start < *previousEnd) {
				return errors.New("replacement scheduled segment is invalid")
			}
			end := *segment.End
			previousEnd = &end
		} else if segment.Start != nil || segment.End != nil {
			return errors.New("replacement omitted segment has times")
		}
		state := activityStates[segment.ActivityID]
		terminal := state == ActivityStateCompleted || state == ActivityStateSkipped
		if prefixOpen && terminal {
			continue
		}
		prefixOpen = false
		if terminal {
			return errors.New("replacement terminal activity is outside the prefix")
		}
		if state == ActivityStateStarted {
			if started || index != len(seen)-1 {
				return errors.New("replacement started activity is out of order")
			}
			started = true
		}
	}
	if len(seen) != len(activityStates) {
		return errors.New("replacement plan omits an activity")
	}
	return nil
}

func validateReplaceCurrentPlanRequest(
	request ReplaceCurrentPlanRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.OwnerUserID,
		request.IntentID,
		request.OutboxID,
		request.MessageID,
		request.EventID,
		request.PlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("replacement identifiers must be canonical UUIDs")
		}
	}
	if request.MessageID != request.EventID ||
		request.MaxPendingCanonicalMirror == 0 ||
		!json.Valid(request.CommandPayload) ||
		!json.Valid(request.EventPayload) ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("replacement request is invalid")
	}
	digest := sha256.Sum256(request.CommandPayload)
	if subtle.ConstantTimeCompare(digest[:], request.PayloadDigest[:]) != 1 {
		return errors.New("replacement payload digest is invalid")
	}
	return nil
}

func validateReorderActivitiesRequest(
	request ReorderActivitiesRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.OwnerUserID,
		request.IntentID,
		request.OutboxID,
		request.MessageID,
		request.EventID,
		request.PlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("trip-edit identifiers must be canonical UUIDs")
		}
	}
	if request.MessageID != request.EventID ||
		request.MaxPendingCanonicalMirror == 0 ||
		len(request.ActivityIDs) == 0 ||
		!json.Valid(request.CommandPayload) ||
		!json.Valid(request.EventPayload) ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("trip-edit request is invalid")
	}
	digest := sha256.Sum256(request.CommandPayload)
	if subtle.ConstantTimeCompare(digest[:], request.PayloadDigest[:]) != 1 {
		return errors.New("trip-edit payload digest is invalid")
	}
	seen := make(map[string]struct{}, len(request.ActivityIDs))
	for _, activityID := range request.ActivityIDs {
		if !validCanonicalUUID(activityID) {
			return errors.New("trip-edit activity id is invalid")
		}
		if _, exists := seen[activityID]; exists {
			return errors.New("trip-edit activity ids must be unique")
		}
		seen[activityID] = struct{}{}
	}
	return nil
}

func validateRemoveActivityRequest(
	request RemoveActivityRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.OwnerUserID,
		request.IntentID,
		request.OutboxID,
		request.MessageID,
		request.EventID,
		request.PlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("trip-edit identifiers must be canonical UUIDs")
		}
	}
	if request.MessageID != request.EventID ||
		request.MaxPendingCanonicalMirror == 0 ||
		!validCanonicalUUID(request.ActivityID) ||
		!json.Valid(request.CommandPayload) ||
		!json.Valid(request.EventPayload) ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("trip-edit remove request is invalid")
	}
	digest := sha256.Sum256(request.CommandPayload)
	if subtle.ConstantTimeCompare(digest[:], request.PayloadDigest[:]) != 1 {
		return errors.New("trip-edit payload digest is invalid")
	}
	return nil
}

func validateReplaceActivityRequest(
	request ReplaceActivityRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.OwnerUserID,
		request.IntentID,
		request.OutboxID,
		request.MessageID,
		request.EventID,
		request.PlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("trip-edit identifiers must be canonical UUIDs")
		}
	}
	if request.MessageID != request.EventID ||
		request.MaxPendingCanonicalMirror == 0 ||
		!json.Valid(request.CommandPayload) ||
		!json.Valid(request.EventPayload) ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("trip-edit replace request is invalid")
	}
	digest := sha256.Sum256(request.CommandPayload)
	if subtle.ConstantTimeCompare(digest[:], request.PayloadDigest[:]) != 1 {
		return errors.New("trip-edit payload digest is invalid")
	}
	return validateCanonicalActivity(request.Activity)
}

func validateAddActivityRequest(
	request AddActivityRequest,
) error {
	for _, value := range []string{
		request.TripID,
		request.OwnerUserID,
		request.IntentID,
		request.OutboxID,
		request.MessageID,
		request.EventID,
		request.PlanID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New("trip-edit identifiers must be canonical UUIDs")
		}
	}
	if request.MessageID != request.EventID ||
		request.MaxPendingCanonicalMirror == 0 ||
		!json.Valid(request.CommandPayload) ||
		!json.Valid(request.EventPayload) ||
		!json.Valid(request.OutcomePayload) {
		return errors.New("trip-edit add request is invalid")
	}
	digest := sha256.Sum256(request.CommandPayload)
	if subtle.ConstantTimeCompare(digest[:], request.PayloadDigest[:]) != 1 {
		return errors.New("trip-edit payload digest is invalid")
	}
	return validateCanonicalActivity(request.Activity)
}

func (store *CanonicalStateStore) CreateTrip(
	ctx context.Context,
	request CreateTripRequest,
) (RecordedCommand, error) {
	if err := validateCreateTripRequest(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin create-trip transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		request.TripID,
	); err != nil {
		return RecordedCommand{}, fmt.Errorf("serialize create-trip: %w", err)
	}
	var ownerID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(&ownerID)
	if err == nil {
		if ownerID != request.OwnerUserID {
			return RecordedCommand{}, ErrCanonicalTripConflict
		}
		existing, storedDigest, algorithm, lookupErr := scanRecordedCommand(
			tx.QueryRow(ctx, recordedCommandSelect, request.TripID, request.MessageID),
			true,
		)
		if lookupErr == nil && existing.Kind == CommandCreateTrip {
			if algorithm != DigestAlgorithmRFC8785SHA256V1 ||
				subtle.ConstantTimeCompare(storedDigest, request.PayloadDigest[:]) != 1 {
				return RecordedCommand{}, ErrIdempotencyKeyReused
			}
			if err := tx.Commit(ctx); err != nil {
				return RecordedCommand{}, fmt.Errorf("commit duplicate create-trip lookup: %w", err)
			}
			return existing, nil
		}
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return RecordedCommand{}, fmt.Errorf("read existing create-trip intent: %w", lookupErr)
		}
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read create-trip: %w", err)
	}
	var ignored string
	if err := tx.QueryRow(ctx, "SELECT id::text FROM users WHERE id = $1", request.OwnerUserID).Scan(&ignored); errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrCanonicalOwnerMissing
	} else if err != nil {
		return RecordedCommand{}, fmt.Errorf("check create-trip owner: %w", err)
	}
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate create-trip timestamp: %w", err)
	}
	createdAt = createdAt.UTC()
	payload := userCurrentPlanPayload(
		request.PlanID, 1, createdAt.UnixMilli(), request.PlanSegments)
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence, current_plan_id
		) VALUES ($1, $2, $3, 1, 2, 1, $4)
	`, request.TripID, request.OwnerUserID, request.DefaultTimeZoneName, request.PlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert create-trip: %w", err)
	}
	for _, activity := range request.Activities {
		var foundClosed any
		if activity.FoundClosedAt != nil {
			foundClosed = *activity.FoundClosedAt
		}
		var reservationStart any
		if activity.ReservationStart != nil {
			reservationStart = *activity.ReservationStart
		}
		var deadline any
		if activity.MandatoryDeadline != nil {
			deadline = *activity.MandatoryDeadline
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO trip_activities (
				id, trip_id, ordinal, place_id, display_name, latitude, longitude,
				time_zone_name, inbound_travel_mode, activity_class, activity_state,
				activity_delay_seconds, found_closed_at, priority_rank, utility_score,
				reservation_start, reservation_grace_seconds, mandatory_deadline,
				min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
				mandatory, can_shorten, can_move, can_skip
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
		`, activity.ID, request.TripID, activity.Ordinal, activity.PlaceID,
			activity.DisplayName, activity.Latitude, activity.Longitude,
			activity.TimeZoneName, activity.InboundTravelMode, activity.ActivityClass,
			activity.ActivityState, activity.ActivityDelaySeconds, foundClosed,
			activity.PriorityRank, activity.UtilityScore, reservationStart,
			activity.ReservationGraceSeconds, deadline, activity.MinDurationSeconds,
			activity.PreferredDurationSeconds, activity.MaxDurationSeconds,
			activity.Mandatory, activity.CanShorten, activity.CanMove,
			activity.CanSkip); err != nil {
			return RecordedCommand{}, fmt.Errorf("insert create-trip activity: %w", err)
		}
		for index, window := range activity.OpenWindows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO activity_open_windows (
					trip_id, activity_id, window_index, opens_at, closes_at
				) VALUES ($1, $2, $3, $4, $5)
			`, request.TripID, activity.ID, index, window.OpensAt, window.ClosesAt); err != nil {
				return RecordedCommand{}, fmt.Errorf("insert create-trip window: %w", err)
			}
		}
	}
	for _, delay := range request.TravelDelays {
		if _, err := tx.Exec(ctx, `
			INSERT INTO trip_travel_delays (
				trip_id, from_activity_id, to_activity_id, additional_seconds, observed_at
			) VALUES ($1, $2, $3, $4, $5)
		`, request.TripID, delay.FromActivityID, delay.ToActivityID,
			delay.AdditionalSeconds, delay.ObservedAt); err != nil {
			return RecordedCommand{}, fmt.Errorf("insert create-trip delay: %w", err)
		}
	}
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, 1, 'user_authored', $3, 1, $4, $5, $6, $7)
	`, request.PlanID, request.TripID, request.OwnerUserID, payload,
		len(payload), checksum[:], createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert create-trip plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			digest_algorithm, payload_digest, command_payload, state,
			outcome_status, outcome_payload, resulting_trip_revision,
			resulting_current_plan_id, runtime_sync_state, recorded_at, finalized_at
		) VALUES ($1, $2, $3, $4, 1, 0, 'create_trip', 'canonical_first',
			'rfc8785-sha256-v1', $5, $6, 'applied', 'OK', $7, 1,
			$8, 'not_required', $9, $9)
	`, request.IntentID, request.TripID, request.MessageID, request.EventID,
		request.PayloadDigest[:], request.CommandPayload, request.OutcomePayload,
		request.PlanID, createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert create-trip intent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit create-trip: %w", err)
	}
	resultingRevision := uint64(1)
	return RecordedCommand{
		IntentID: request.IntentID, TripID: request.TripID,
		MessageID: request.MessageID, EventID: request.EventID,
		MutationSequence: 1, ExpectedTripRevision: 0,
		Kind: CommandCreateTrip, State: "applied",
		RuntimeSyncState: "not_required", OutcomeStatus: stringPtr("OK"),
		OutcomePayload:         request.OutcomePayload,
		ResultingTripRevision:  &resultingRevision,
		ResultingCurrentPlanID: &request.PlanID,
		RecordedAt:             createdAt, FinalizedAt: &createdAt,
	}, nil
}

func (store *CanonicalStateStore) ReplaceCurrentPlan(
	ctx context.Context,
	request ReplaceCurrentPlanRequest,
) (RecordedCommand, error) {
	if err := validateReplaceCurrentPlanRequest(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin replacement transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID string
	var tripRevision int64
	var nextMutationSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, trip_revision, next_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&ownerID, &tripRevision, &nextMutationSequence, &currentPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock replacement trip: %w", err)
	}
	if ownerID != request.OwnerUserID {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	existing, storedDigest, algorithm, lookupErr := scanRecordedCommand(
		tx.QueryRow(ctx, recordedCommandSelect, request.TripID, request.MessageID),
		true,
	)
	if lookupErr == nil {
		if algorithm != DigestAlgorithmRFC8785SHA256V1 ||
			subtle.ConstantTimeCompare(storedDigest, request.PayloadDigest[:]) != 1 {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if existing.Kind != CommandReplaceCurrentPlan {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if err := tx.Commit(ctx); err != nil {
			return RecordedCommand{}, fmt.Errorf("commit duplicate replacement lookup: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read existing replacement intent: %w", lookupErr)
	}
	if tripRevision < 0 || uint64(tripRevision) != request.ExpectedTripRevision {
		return RecordedCommand{}, ErrTripRevisionStale
	}
	if tripRevision == math.MaxInt64 || nextMutationSequence <= 0 ||
		nextMutationSequence == math.MaxInt64 {
		return RecordedCommand{}, ErrMutationSequenceExhausted
	}
	var runtimeCommandCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'runtime_first'
		  AND state = 'pending'
	`, request.TripID).Scan(&runtimeCommandCount); err != nil {
		return RecordedCommand{}, fmt.Errorf("check unresolved runtime commands: %w", err)
	}
	if runtimeCommandCount != 0 {
		return RecordedCommand{}, ErrDurableCommandBlocked
	}
	var pendingMirrors int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'canonical_first'
		  AND runtime_sync_state IN ('pending', 'paused_internal')
	`, request.TripID).Scan(&pendingMirrors); err != nil {
		return RecordedCommand{}, fmt.Errorf("count pending canonical mirrors: %w", err)
	}
	if pendingMirrors >= int(request.MaxPendingCanonicalMirror) {
		return RecordedCommand{}, ErrCanonicalMirrorCapacity
	}
	activityRows, err := tx.Query(ctx, `
		SELECT id::text, ordinal, activity_state
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY ordinal
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("load replacement activities: %w", err)
	}
	activityStates := make(map[string]ActivityState)
	var ordinal int32
	for activityRows.Next() {
		var id string
		var rowOrdinal int32
		var state ActivityState
		if err := activityRows.Scan(&id, &rowOrdinal, &state); err != nil {
			activityRows.Close()
			return RecordedCommand{}, fmt.Errorf("scan replacement activity: %w", err)
		}
		if rowOrdinal != ordinal || !validCanonicalUUID(id) || !state.valid() {
			activityRows.Close()
			return RecordedCommand{}, ErrCanonicalStateCorrupt
		}
		activityStates[id] = state
		ordinal++
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return RecordedCommand{}, fmt.Errorf("iterate replacement activities: %w", err)
	}
	activityRows.Close()
	if err := validateReplacementPlan(request.PlanSegments, activityStates); err != nil {
		return RecordedCommand{}, err
	}
	planRevision := uint64(tripRevision + 1)
	mutationSequence := uint64(nextMutationSequence)
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate replacement timestamp: %w", err)
	}
	createdAt = createdAt.UTC()
	payload := userCurrentPlanPayload(
		request.PlanID, planRevision, createdAt.UnixMilli(), request.PlanSegments)
	var lockedPlanID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM itinerary_plans
		WHERE trip_id = $1 AND id = $2
		FOR UPDATE
	`, request.TripID, currentPlanID).Scan(&lockedPlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("lock current replacement plan: %w", err)
	}
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'superseded', decided_at = clock_timestamp()
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID); err != nil {
		return RecordedCommand{}, fmt.Errorf("supersede replacement proposals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, $3, 'user_authored', $4, 1, $5, $6, $7, $8)
	`, request.PlanID, request.TripID, planRevision, request.OwnerUserID,
		payload, len(payload), checksum[:], createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert replacement plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2, next_mutation_sequence = $3,
			finalized_mutation_sequence = $4, current_plan_id = $5,
			updated_at = clock_timestamp()
		WHERE id = $1
	`, request.TripID, planRevision, mutationSequence+1,
		mutationSequence, request.PlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("update replacement trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			digest_algorithm, payload_digest, command_payload, state,
			outcome_status, outcome_payload, resulting_trip_revision,
			resulting_current_plan_id, runtime_sync_state, recorded_at, finalized_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'replace_current_plan',
			'canonical_first', 'rfc8785-sha256-v1', $7, $8, 'applied',
			'OK', $9, $10, $11, 'pending', $12, $12)
	`, request.IntentID, request.TripID, request.MessageID, request.EventID,
		mutationSequence, request.ExpectedTripRevision, request.PayloadDigest[:],
		request.CommandPayload, request.OutcomePayload, planRevision,
		request.PlanID, createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert replacement intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO planner_outbox (
			id, command_intent_id, trip_id, mutation_sequence,
			event_schema_version, event_payload, delivery_state
		) VALUES ($1, $2, $3, $4, 1, $5, 'pending')
	`, request.OutboxID, request.IntentID, request.TripID,
		mutationSequence, request.EventPayload); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert replacement outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit replacement: %w", err)
	}
	return RecordedCommand{
		IntentID: request.IntentID, OutboxID: request.OutboxID,
		TripID: request.TripID, MessageID: request.MessageID,
		EventID: request.EventID, MutationSequence: mutationSequence,
		ExpectedTripRevision: request.ExpectedTripRevision,
		Kind:                 CommandReplaceCurrentPlan, State: "applied",
		RuntimeSyncState: "pending", OutcomeStatus: stringPtr("OK"),
		OutcomePayload:         request.OutcomePayload,
		ResultingTripRevision:  &planRevision,
		ResultingCurrentPlanID: &request.PlanID,
		RecordedAt:             createdAt, FinalizedAt: &createdAt,
	}, nil
}

func (store *CanonicalStateStore) ReorderActivities(
	ctx context.Context,
	request ReorderActivitiesRequest,
) (RecordedCommand, error) {
	if err := validateReorderActivitiesRequest(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin trip-edit transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID string
	var tripRevision int64
	var nextMutationSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, trip_revision, next_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&ownerID, &tripRevision, &nextMutationSequence, &currentPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock trip-edit trip: %w", err)
	}
	if ownerID != request.OwnerUserID {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	existing, storedDigest, algorithm, lookupErr := scanRecordedCommand(
		tx.QueryRow(ctx, recordedCommandSelect, request.TripID, request.MessageID),
		true,
	)
	if lookupErr == nil {
		if algorithm != DigestAlgorithmRFC8785SHA256V1 ||
			subtle.ConstantTimeCompare(storedDigest, request.PayloadDigest[:]) != 1 ||
			existing.Kind != CommandTripEdited {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if err := tx.Commit(ctx); err != nil {
			return RecordedCommand{}, fmt.Errorf("commit duplicate trip-edit lookup: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read existing trip-edit intent: %w", lookupErr)
	}
	if tripRevision < 0 || uint64(tripRevision) != request.ExpectedTripRevision {
		return RecordedCommand{}, ErrTripRevisionStale
	}
	if tripRevision == math.MaxInt64 || nextMutationSequence <= 0 ||
		nextMutationSequence == math.MaxInt64 {
		return RecordedCommand{}, ErrMutationSequenceExhausted
	}
	var runtimeCommandCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'runtime_first'
		  AND state = 'pending'
	`, request.TripID).Scan(&runtimeCommandCount); err != nil {
		return RecordedCommand{}, fmt.Errorf("check unresolved trip-edit commands: %w", err)
	}
	if runtimeCommandCount != 0 {
		return RecordedCommand{}, ErrDurableCommandBlocked
	}
	var pendingMirrors int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'canonical_first'
		  AND runtime_sync_state IN ('pending', 'paused_internal')
	`, request.TripID).Scan(&pendingMirrors); err != nil {
		return RecordedCommand{}, fmt.Errorf("count pending trip-edit mirrors: %w", err)
	}
	if pendingMirrors >= int(request.MaxPendingCanonicalMirror) {
		return RecordedCommand{}, ErrCanonicalMirrorCapacity
	}
	activityRows, err := tx.Query(ctx, `
		SELECT id::text, ordinal, activity_state
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY ordinal
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("load trip-edit activities: %w", err)
	}
	activityStates := make(map[string]ActivityState)
	currentActivityIDs := make([]string, 0)
	var ordinal int32
	for activityRows.Next() {
		var activityID string
		var rowOrdinal int32
		var state ActivityState
		if err := activityRows.Scan(&activityID, &rowOrdinal, &state); err != nil {
			activityRows.Close()
			return RecordedCommand{}, fmt.Errorf("scan trip-edit activity: %w", err)
		}
		if rowOrdinal != ordinal || !validCanonicalUUID(activityID) || !state.valid() {
			activityRows.Close()
			return RecordedCommand{}, ErrCanonicalStateCorrupt
		}
		currentActivityIDs = append(currentActivityIDs, activityID)
		activityStates[activityID] = state
		ordinal++
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return RecordedCommand{}, fmt.Errorf("iterate trip-edit activities: %w", err)
	}
	activityRows.Close()
	if len(currentActivityIDs) != len(request.ActivityIDs) {
		return RecordedCommand{}, errors.New("trip-edit reorder must cover every activity")
	}
	for index, activityID := range request.ActivityIDs {
		if _, exists := activityStates[activityID]; !exists {
			return RecordedCommand{}, errors.New("trip-edit reorder references unknown activity")
		}
		if index > 0 && request.ActivityIDs[index-1] == activityID {
			return RecordedCommand{}, errors.New("trip-edit reorder repeats an activity")
		}
	}
	if err := validateReplacementPlan(request.PlanSegments, activityStates); err != nil {
		return RecordedCommand{}, err
	}
	var lockedPlanID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM itinerary_plans
		WHERE trip_id = $1 AND id = $2
		FOR UPDATE
	`, request.TripID, currentPlanID).Scan(&lockedPlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("lock trip-edit current plan: %w", err)
	}
	planRevision := uint64(tripRevision + 1)
	mutationSequence := uint64(nextMutationSequence)
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate trip-edit timestamp: %w", err)
	}
	createdAt = createdAt.UTC()
	payload := userCurrentPlanPayload(
		request.PlanID, planRevision, createdAt.UnixMilli(), request.PlanSegments)
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'superseded', decided_at = clock_timestamp()
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID); err != nil {
		return RecordedCommand{}, fmt.Errorf("supersede trip-edit proposals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trip_activities
		SET ordinal = ordinal + $2
		WHERE trip_id = $1
	`, request.TripID, len(currentActivityIDs)); err != nil {
		return RecordedCommand{}, fmt.Errorf("stage trip-edit ordinals: %w", err)
	}
	for index, activityID := range request.ActivityIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE trip_activities
			SET ordinal = $3
			WHERE trip_id = $1 AND id = $2
		`, request.TripID, activityID, index); err != nil {
			return RecordedCommand{}, fmt.Errorf("apply trip-edit ordinal: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, $3, 'user_authored', $4, 1, $5, $6, $7, $8)
	`, request.PlanID, request.TripID, planRevision, request.OwnerUserID,
		payload, len(payload), checksum[:], createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert trip-edit plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2, next_mutation_sequence = $3,
			finalized_mutation_sequence = $4, current_plan_id = $5,
			updated_at = clock_timestamp()
		WHERE id = $1
	`, request.TripID, planRevision, mutationSequence+1,
		mutationSequence, request.PlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("update trip-edit trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			digest_algorithm, payload_digest, command_payload, state,
			outcome_status, outcome_payload, resulting_trip_revision,
			resulting_current_plan_id, runtime_sync_state, recorded_at, finalized_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'trip_edited',
			'canonical_first', 'rfc8785-sha256-v1', $7, $8, 'applied',
			'OK', $9, $10, $11, 'pending', $12, $12)
	`, request.IntentID, request.TripID, request.MessageID, request.EventID,
		mutationSequence, request.ExpectedTripRevision, request.PayloadDigest[:],
		request.CommandPayload, request.OutcomePayload, planRevision,
		request.PlanID, createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert trip-edit intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO planner_outbox (
			id, command_intent_id, trip_id, mutation_sequence,
			event_schema_version, event_payload, delivery_state
		) VALUES ($1, $2, $3, $4, 1, $5, 'pending')
	`, request.OutboxID, request.IntentID, request.TripID,
		mutationSequence, request.EventPayload); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert trip-edit outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit trip-edit: %w", err)
	}
	return RecordedCommand{
		IntentID: request.IntentID, OutboxID: request.OutboxID,
		TripID: request.TripID, MessageID: request.MessageID,
		EventID: request.EventID, MutationSequence: mutationSequence,
		ExpectedTripRevision: request.ExpectedTripRevision,
		Kind:                 CommandTripEdited, State: "applied",
		RuntimeSyncState: "pending", OutcomeStatus: stringPtr("OK"),
		OutcomePayload:         request.OutcomePayload,
		ResultingTripRevision:  &planRevision,
		ResultingCurrentPlanID: &request.PlanID,
		RecordedAt:             createdAt, FinalizedAt: &createdAt,
	}, nil
}

func (store *CanonicalStateStore) RemoveActivity(
	ctx context.Context,
	request RemoveActivityRequest,
) (RecordedCommand, error) {
	if err := validateRemoveActivityRequest(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin remove-activity transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID string
	var tripRevision int64
	var nextMutationSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, trip_revision, next_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&ownerID, &tripRevision, &nextMutationSequence, &currentPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock remove-activity trip: %w", err)
	}
	if ownerID != request.OwnerUserID {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	existing, storedDigest, algorithm, lookupErr := scanRecordedCommand(
		tx.QueryRow(ctx, recordedCommandSelect, request.TripID, request.MessageID),
		true,
	)
	if lookupErr == nil {
		if algorithm != DigestAlgorithmRFC8785SHA256V1 ||
			subtle.ConstantTimeCompare(storedDigest, request.PayloadDigest[:]) != 1 ||
			existing.Kind != CommandTripEdited {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if err := tx.Commit(ctx); err != nil {
			return RecordedCommand{}, fmt.Errorf("commit duplicate remove-activity lookup: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read existing remove-activity intent: %w", lookupErr)
	}
	if tripRevision < 0 || uint64(tripRevision) != request.ExpectedTripRevision {
		return RecordedCommand{}, ErrTripRevisionStale
	}
	if tripRevision == math.MaxInt64 || nextMutationSequence <= 0 ||
		nextMutationSequence == math.MaxInt64 {
		return RecordedCommand{}, ErrMutationSequenceExhausted
	}
	var runtimeCommandCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'runtime_first'
		  AND state = 'pending'
	`, request.TripID).Scan(&runtimeCommandCount); err != nil {
		return RecordedCommand{}, fmt.Errorf("check unresolved remove-activity commands: %w", err)
	}
	if runtimeCommandCount != 0 {
		return RecordedCommand{}, ErrDurableCommandBlocked
	}
	var pendingMirrors int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'canonical_first'
		  AND runtime_sync_state IN ('pending', 'paused_internal')
	`, request.TripID).Scan(&pendingMirrors); err != nil {
		return RecordedCommand{}, fmt.Errorf("count pending remove-activity mirrors: %w", err)
	}
	if pendingMirrors >= int(request.MaxPendingCanonicalMirror) {
		return RecordedCommand{}, ErrCanonicalMirrorCapacity
	}
	activityRows, err := tx.Query(ctx, `
		SELECT id::text, ordinal, activity_state
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY ordinal
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("load remove-activity activities: %w", err)
	}
	activityStates := make(map[string]ActivityState)
	currentActivityIDs := make([]string, 0)
	var ordinal int32
	for activityRows.Next() {
		var activityID string
		var rowOrdinal int32
		var state ActivityState
		if err := activityRows.Scan(&activityID, &rowOrdinal, &state); err != nil {
			activityRows.Close()
			return RecordedCommand{}, fmt.Errorf("scan remove-activity activity: %w", err)
		}
		if rowOrdinal != ordinal || !validCanonicalUUID(activityID) || !state.valid() {
			activityRows.Close()
			return RecordedCommand{}, ErrCanonicalStateCorrupt
		}
		currentActivityIDs = append(currentActivityIDs, activityID)
		activityStates[activityID] = state
		ordinal++
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return RecordedCommand{}, fmt.Errorf("iterate remove-activity activities: %w", err)
	}
	activityRows.Close()
	if _, exists := activityStates[request.ActivityID]; !exists {
		return RecordedCommand{}, errors.New("trip-edit remove references unknown activity")
	}
	resultingActivityIDs := make([]string, 0, len(currentActivityIDs)-1)
	resultingActivityStates := make(map[string]ActivityState, len(currentActivityIDs)-1)
	for _, activityID := range currentActivityIDs {
		if activityID == request.ActivityID {
			continue
		}
		resultingActivityIDs = append(resultingActivityIDs, activityID)
		resultingActivityStates[activityID] = activityStates[activityID]
	}
	if err := validateReplacementPlan(request.PlanSegments, resultingActivityStates); err != nil {
		return RecordedCommand{}, err
	}
	var lockedPlanID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM itinerary_plans
		WHERE trip_id = $1 AND id = $2
		FOR UPDATE
	`, request.TripID, currentPlanID).Scan(&lockedPlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("lock remove-activity current plan: %w", err)
	}
	planRevision := uint64(tripRevision + 1)
	mutationSequence := uint64(nextMutationSequence)
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate remove-activity timestamp: %w", err)
	}
	createdAt = createdAt.UTC()
	payload := userCurrentPlanPayload(
		request.PlanID, planRevision, createdAt.UnixMilli(), request.PlanSegments)
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'superseded', decided_at = clock_timestamp()
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID); err != nil {
		return RecordedCommand{}, fmt.Errorf("supersede remove-activity proposals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trip_activities
		SET ordinal = ordinal + $2
		WHERE trip_id = $1
	`, request.TripID, len(currentActivityIDs)); err != nil {
		return RecordedCommand{}, fmt.Errorf("stage remove-activity ordinals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM trip_travel_delays
		WHERE trip_id = $1
		  AND (from_activity_id = $2 OR to_activity_id = $2)
	`, request.TripID, request.ActivityID); err != nil {
		return RecordedCommand{}, fmt.Errorf("delete remove-activity travel delays: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM activity_open_windows
		WHERE trip_id = $1 AND activity_id = $2
	`, request.TripID, request.ActivityID); err != nil {
		return RecordedCommand{}, fmt.Errorf("delete remove-activity windows: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM trip_activities
		WHERE trip_id = $1 AND id = $2
	`, request.TripID, request.ActivityID); err != nil {
		return RecordedCommand{}, fmt.Errorf("delete remove-activity activity: %w", err)
	}
	for index, activityID := range resultingActivityIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE trip_activities
			SET ordinal = $3
			WHERE trip_id = $1 AND id = $2
		`, request.TripID, activityID, index); err != nil {
			return RecordedCommand{}, fmt.Errorf("apply remove-activity ordinal: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, $3, 'user_authored', $4, 1, $5, $6, $7, $8)
	`, request.PlanID, request.TripID, planRevision, request.OwnerUserID,
		payload, len(payload), checksum[:], createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert remove-activity plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2, next_mutation_sequence = $3,
			finalized_mutation_sequence = $4, current_plan_id = $5,
			updated_at = clock_timestamp()
		WHERE id = $1
	`, request.TripID, planRevision, mutationSequence+1,
		mutationSequence, request.PlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("update remove-activity trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			digest_algorithm, payload_digest, command_payload, state,
			outcome_status, outcome_payload, resulting_trip_revision,
			resulting_current_plan_id, runtime_sync_state, recorded_at, finalized_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'trip_edited',
			'canonical_first', 'rfc8785-sha256-v1', $7, $8, 'applied',
			'OK', $9, $10, $11, 'pending', $12, $12)
	`, request.IntentID, request.TripID, request.MessageID, request.EventID,
		mutationSequence, request.ExpectedTripRevision, request.PayloadDigest[:],
		request.CommandPayload, request.OutcomePayload, planRevision,
		request.PlanID, createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert remove-activity intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO planner_outbox (
			id, command_intent_id, trip_id, mutation_sequence,
			event_schema_version, event_payload, delivery_state
		) VALUES ($1, $2, $3, $4, 1, $5, 'pending')
	`, request.OutboxID, request.IntentID, request.TripID,
		mutationSequence, request.EventPayload); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert remove-activity outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit remove-activity: %w", err)
	}
	return RecordedCommand{
		IntentID: request.IntentID, OutboxID: request.OutboxID,
		TripID: request.TripID, MessageID: request.MessageID,
		EventID: request.EventID, MutationSequence: mutationSequence,
		ExpectedTripRevision: request.ExpectedTripRevision,
		Kind:                 CommandTripEdited, State: "applied",
		RuntimeSyncState: "pending", OutcomeStatus: stringPtr("OK"),
		OutcomePayload:         request.OutcomePayload,
		ResultingTripRevision:  &planRevision,
		ResultingCurrentPlanID: &request.PlanID,
		RecordedAt:             createdAt, FinalizedAt: &createdAt,
	}, nil
}

func (store *CanonicalStateStore) ReplaceActivity(
	ctx context.Context,
	request ReplaceActivityRequest,
) (RecordedCommand, error) {
	if err := validateReplaceActivityRequest(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin replace-activity transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID string
	var tripRevision int64
	var nextMutationSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, trip_revision, next_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&ownerID, &tripRevision, &nextMutationSequence, &currentPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock replace-activity trip: %w", err)
	}
	if ownerID != request.OwnerUserID {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	existing, storedDigest, algorithm, lookupErr := scanRecordedCommand(
		tx.QueryRow(ctx, recordedCommandSelect, request.TripID, request.MessageID),
		true,
	)
	if lookupErr == nil {
		if algorithm != DigestAlgorithmRFC8785SHA256V1 ||
			subtle.ConstantTimeCompare(storedDigest, request.PayloadDigest[:]) != 1 ||
			existing.Kind != CommandTripEdited {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if err := tx.Commit(ctx); err != nil {
			return RecordedCommand{}, fmt.Errorf("commit duplicate replace-activity lookup: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read existing replace-activity intent: %w", lookupErr)
	}
	if tripRevision < 0 || uint64(tripRevision) != request.ExpectedTripRevision {
		return RecordedCommand{}, ErrTripRevisionStale
	}
	if tripRevision == math.MaxInt64 || nextMutationSequence <= 0 ||
		nextMutationSequence == math.MaxInt64 {
		return RecordedCommand{}, ErrMutationSequenceExhausted
	}
	var runtimeCommandCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'runtime_first'
		  AND state = 'pending'
	`, request.TripID).Scan(&runtimeCommandCount); err != nil {
		return RecordedCommand{}, fmt.Errorf("check unresolved replace-activity commands: %w", err)
	}
	if runtimeCommandCount != 0 {
		return RecordedCommand{}, ErrDurableCommandBlocked
	}
	var pendingMirrors int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'canonical_first'
		  AND runtime_sync_state IN ('pending', 'paused_internal')
	`, request.TripID).Scan(&pendingMirrors); err != nil {
		return RecordedCommand{}, fmt.Errorf("count pending replace-activity mirrors: %w", err)
	}
	if pendingMirrors >= int(request.MaxPendingCanonicalMirror) {
		return RecordedCommand{}, ErrCanonicalMirrorCapacity
	}
	activityRows, err := tx.Query(ctx, `
		SELECT id::text, ordinal, activity_state
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY ordinal
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("load replace-activity activities: %w", err)
	}
	activityStates := make(map[string]ActivityState)
	activityOrdinals := make(map[string]uint32)
	currentActivityIDs := make([]string, 0)
	var ordinal int32
	for activityRows.Next() {
		var activityID string
		var rowOrdinal int32
		var state ActivityState
		if err := activityRows.Scan(&activityID, &rowOrdinal, &state); err != nil {
			activityRows.Close()
			return RecordedCommand{}, fmt.Errorf("scan replace-activity activity: %w", err)
		}
		if rowOrdinal != ordinal || !validCanonicalUUID(activityID) || !state.valid() {
			activityRows.Close()
			return RecordedCommand{}, ErrCanonicalStateCorrupt
		}
		currentActivityIDs = append(currentActivityIDs, activityID)
		activityStates[activityID] = state
		activityOrdinals[activityID] = uint32(rowOrdinal)
		ordinal++
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return RecordedCommand{}, fmt.Errorf("iterate replace-activity activities: %w", err)
	}
	activityRows.Close()
	replacementOrdinal, exists := activityOrdinals[request.Activity.ID]
	if !exists {
		return RecordedCommand{}, errors.New("trip-edit replace references unknown activity")
	}
	replacement := request.Activity
	replacement.Ordinal = replacementOrdinal
	activityStates[replacement.ID] = replacement.ActivityState
	if err := validateReplacementPlan(request.PlanSegments, activityStates); err != nil {
		return RecordedCommand{}, err
	}
	var lockedPlanID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM itinerary_plans
		WHERE trip_id = $1 AND id = $2
		FOR UPDATE
	`, request.TripID, currentPlanID).Scan(&lockedPlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("lock replace-activity current plan: %w", err)
	}
	planRevision := uint64(tripRevision + 1)
	mutationSequence := uint64(nextMutationSequence)
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate replace-activity timestamp: %w", err)
	}
	createdAt = createdAt.UTC()
	payload := userCurrentPlanPayload(
		request.PlanID, planRevision, createdAt.UnixMilli(), request.PlanSegments)
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'superseded', decided_at = clock_timestamp()
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID); err != nil {
		return RecordedCommand{}, fmt.Errorf("supersede replace-activity proposals: %w", err)
	}
	var foundClosed any
	if replacement.FoundClosedAt != nil {
		foundClosed = *replacement.FoundClosedAt
	}
	var reservationStart any
	if replacement.ReservationStart != nil {
		reservationStart = *replacement.ReservationStart
	}
	var deadline any
	if replacement.MandatoryDeadline != nil {
		deadline = *replacement.MandatoryDeadline
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trip_activities
		SET place_id = $3, display_name = $4, latitude = $5, longitude = $6,
			time_zone_name = $7, inbound_travel_mode = $8, activity_class = $9,
			activity_state = $10, activity_delay_seconds = $11,
			found_closed_at = $12, priority_rank = $13, utility_score = $14,
			reservation_start = $15, reservation_grace_seconds = $16,
			mandatory_deadline = $17, min_duration_seconds = $18,
			preferred_duration_seconds = $19, max_duration_seconds = $20,
			mandatory = $21, can_shorten = $22, can_move = $23,
			can_skip = $24
		WHERE trip_id = $1 AND id = $2
	`, request.TripID, replacement.ID, replacement.PlaceID,
		replacement.DisplayName, replacement.Latitude, replacement.Longitude,
		replacement.TimeZoneName, replacement.InboundTravelMode,
		replacement.ActivityClass, replacement.ActivityState,
		replacement.ActivityDelaySeconds, foundClosed, replacement.PriorityRank,
		replacement.UtilityScore, reservationStart,
		replacement.ReservationGraceSeconds, deadline,
		replacement.MinDurationSeconds, replacement.PreferredDurationSeconds,
		replacement.MaxDurationSeconds, replacement.Mandatory,
		replacement.CanShorten, replacement.CanMove, replacement.CanSkip); err != nil {
		return RecordedCommand{}, fmt.Errorf("apply replace-activity activity: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM activity_open_windows
		WHERE trip_id = $1 AND activity_id = $2
	`, request.TripID, replacement.ID); err != nil {
		return RecordedCommand{}, fmt.Errorf("delete replace-activity windows: %w", err)
	}
	for index, window := range replacement.OpenWindows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_open_windows (
				trip_id, activity_id, window_index, opens_at, closes_at
			) VALUES ($1, $2, $3, $4, $5)
		`, request.TripID, replacement.ID, index,
			window.OpensAt, window.ClosesAt); err != nil {
			return RecordedCommand{}, fmt.Errorf("insert replace-activity window: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, $3, 'user_authored', $4, 1, $5, $6, $7, $8)
	`, request.PlanID, request.TripID, planRevision, request.OwnerUserID,
		payload, len(payload), checksum[:], createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert replace-activity plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2, next_mutation_sequence = $3,
			finalized_mutation_sequence = $4, current_plan_id = $5,
			updated_at = clock_timestamp()
		WHERE id = $1
	`, request.TripID, planRevision, mutationSequence+1,
		mutationSequence, request.PlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("update replace-activity trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			digest_algorithm, payload_digest, command_payload, state,
			outcome_status, outcome_payload, resulting_trip_revision,
			resulting_current_plan_id, runtime_sync_state, recorded_at, finalized_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'trip_edited',
			'canonical_first', 'rfc8785-sha256-v1', $7, $8, 'applied',
			'OK', $9, $10, $11, 'pending', $12, $12)
	`, request.IntentID, request.TripID, request.MessageID, request.EventID,
		mutationSequence, request.ExpectedTripRevision, request.PayloadDigest[:],
		request.CommandPayload, request.OutcomePayload, planRevision,
		request.PlanID, createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert replace-activity intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO planner_outbox (
			id, command_intent_id, trip_id, mutation_sequence,
			event_schema_version, event_payload, delivery_state
		) VALUES ($1, $2, $3, $4, 1, $5, 'pending')
	`, request.OutboxID, request.IntentID, request.TripID,
		mutationSequence, request.EventPayload); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert replace-activity outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit replace-activity: %w", err)
	}
	return RecordedCommand{
		IntentID: request.IntentID, OutboxID: request.OutboxID,
		TripID: request.TripID, MessageID: request.MessageID,
		EventID: request.EventID, MutationSequence: mutationSequence,
		ExpectedTripRevision: request.ExpectedTripRevision,
		Kind:                 CommandTripEdited, State: "applied",
		RuntimeSyncState: "pending", OutcomeStatus: stringPtr("OK"),
		OutcomePayload:         request.OutcomePayload,
		ResultingTripRevision:  &planRevision,
		ResultingCurrentPlanID: &request.PlanID,
		RecordedAt:             createdAt, FinalizedAt: &createdAt,
	}, nil
}

func (store *CanonicalStateStore) AddActivity(
	ctx context.Context,
	request AddActivityRequest,
) (RecordedCommand, error) {
	if err := validateAddActivityRequest(request); err != nil {
		return RecordedCommand{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("begin add-activity transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID string
	var tripRevision int64
	var nextMutationSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, trip_revision, next_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&ownerID, &tripRevision, &nextMutationSequence, &currentPlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("lock add-activity trip: %w", err)
	}
	if ownerID != request.OwnerUserID {
		return RecordedCommand{}, ErrCanonicalTripConflict
	}
	existing, storedDigest, algorithm, lookupErr := scanRecordedCommand(
		tx.QueryRow(ctx, recordedCommandSelect, request.TripID, request.MessageID),
		true,
	)
	if lookupErr == nil {
		if algorithm != DigestAlgorithmRFC8785SHA256V1 ||
			subtle.ConstantTimeCompare(storedDigest, request.PayloadDigest[:]) != 1 ||
			existing.Kind != CommandTripEdited {
			return RecordedCommand{}, ErrIdempotencyKeyReused
		}
		if err := tx.Commit(ctx); err != nil {
			return RecordedCommand{}, fmt.Errorf("commit duplicate add-activity lookup: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return RecordedCommand{}, fmt.Errorf("read existing add-activity intent: %w", lookupErr)
	}
	if tripRevision < 0 || uint64(tripRevision) != request.ExpectedTripRevision {
		return RecordedCommand{}, ErrTripRevisionStale
	}
	if tripRevision == math.MaxInt64 || nextMutationSequence <= 0 ||
		nextMutationSequence == math.MaxInt64 {
		return RecordedCommand{}, ErrMutationSequenceExhausted
	}
	var runtimeCommandCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'runtime_first'
		  AND state = 'pending'
	`, request.TripID).Scan(&runtimeCommandCount); err != nil {
		return RecordedCommand{}, fmt.Errorf("check unresolved add-activity commands: %w", err)
	}
	if runtimeCommandCount != 0 {
		return RecordedCommand{}, ErrDurableCommandBlocked
	}
	var pendingMirrors int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM command_intents
		WHERE trip_id = $1 AND application_order = 'canonical_first'
		  AND runtime_sync_state IN ('pending', 'paused_internal')
	`, request.TripID).Scan(&pendingMirrors); err != nil {
		return RecordedCommand{}, fmt.Errorf("count pending add-activity mirrors: %w", err)
	}
	if pendingMirrors >= int(request.MaxPendingCanonicalMirror) {
		return RecordedCommand{}, ErrCanonicalMirrorCapacity
	}
	activityRows, err := tx.Query(ctx, `
		SELECT id::text, ordinal, activity_state
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY ordinal
		FOR UPDATE
	`, request.TripID)
	if err != nil {
		return RecordedCommand{}, fmt.Errorf("load add-activity activities: %w", err)
	}
	activityStates := make(map[string]ActivityState)
	currentActivityIDs := make([]string, 0)
	var ordinal int32
	for activityRows.Next() {
		var activityID string
		var rowOrdinal int32
		var state ActivityState
		if err := activityRows.Scan(&activityID, &rowOrdinal, &state); err != nil {
			activityRows.Close()
			return RecordedCommand{}, fmt.Errorf("scan add-activity activity: %w", err)
		}
		if rowOrdinal != ordinal || !validCanonicalUUID(activityID) || !state.valid() {
			activityRows.Close()
			return RecordedCommand{}, ErrCanonicalStateCorrupt
		}
		currentActivityIDs = append(currentActivityIDs, activityID)
		activityStates[activityID] = state
		ordinal++
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return RecordedCommand{}, fmt.Errorf("iterate add-activity activities: %w", err)
	}
	activityRows.Close()
	if len(currentActivityIDs) >= 64 {
		return RecordedCommand{}, errors.New("trip-edit activity limit is exceeded")
	}
	if request.Ordinal > uint32(len(currentActivityIDs)) {
		return RecordedCommand{}, errors.New("trip-edit add ordinal is invalid")
	}
	if _, exists := activityStates[request.Activity.ID]; exists {
		return RecordedCommand{}, errors.New("trip-edit add repeats an activity")
	}
	newActivity := request.Activity
	newActivity.Ordinal = request.Ordinal
	activityStates[newActivity.ID] = newActivity.ActivityState
	resultingActivityIDs := make([]string, 0, len(currentActivityIDs)+1)
	resultingActivityIDs = append(resultingActivityIDs,
		currentActivityIDs[:request.Ordinal]...)
	resultingActivityIDs = append(resultingActivityIDs, newActivity.ID)
	resultingActivityIDs = append(resultingActivityIDs,
		currentActivityIDs[request.Ordinal:]...)
	if err := validateReplacementPlan(request.PlanSegments, activityStates); err != nil {
		return RecordedCommand{}, err
	}
	var lockedPlanID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM itinerary_plans
		WHERE trip_id = $1 AND id = $2
		FOR UPDATE
	`, request.TripID, currentPlanID).Scan(&lockedPlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("lock add-activity current plan: %w", err)
	}
	planRevision := uint64(tripRevision + 1)
	mutationSequence := uint64(nextMutationSequence)
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("allocate add-activity timestamp: %w", err)
	}
	createdAt = createdAt.UTC()
	payload := userCurrentPlanPayload(
		request.PlanID, planRevision, createdAt.UnixMilli(), request.PlanSegments)
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'superseded', decided_at = clock_timestamp()
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID); err != nil {
		return RecordedCommand{}, fmt.Errorf("supersede add-activity proposals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trip_activities
		SET ordinal = ordinal + $2
		WHERE trip_id = $1
	`, request.TripID, len(currentActivityIDs)+1); err != nil {
		return RecordedCommand{}, fmt.Errorf("stage add-activity ordinals: %w", err)
	}
	var foundClosed any
	if newActivity.FoundClosedAt != nil {
		foundClosed = *newActivity.FoundClosedAt
	}
	var reservationStart any
	if newActivity.ReservationStart != nil {
		reservationStart = *newActivity.ReservationStart
	}
	var deadline any
	if newActivity.MandatoryDeadline != nil {
		deadline = *newActivity.MandatoryDeadline
	}
	stagedOrdinal := 2*len(currentActivityIDs) + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO trip_activities (
			id, trip_id, ordinal, place_id, display_name, latitude, longitude,
			time_zone_name, inbound_travel_mode, activity_class, activity_state,
			activity_delay_seconds, found_closed_at, priority_rank, utility_score,
			reservation_start, reservation_grace_seconds, mandatory_deadline,
			min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
			mandatory, can_shorten, can_move, can_skip
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
	`, newActivity.ID, request.TripID, stagedOrdinal, newActivity.PlaceID,
		newActivity.DisplayName, newActivity.Latitude, newActivity.Longitude,
		newActivity.TimeZoneName, newActivity.InboundTravelMode,
		newActivity.ActivityClass, newActivity.ActivityState,
		newActivity.ActivityDelaySeconds, foundClosed, newActivity.PriorityRank,
		newActivity.UtilityScore, reservationStart,
		newActivity.ReservationGraceSeconds, deadline,
		newActivity.MinDurationSeconds, newActivity.PreferredDurationSeconds,
		newActivity.MaxDurationSeconds, newActivity.Mandatory,
		newActivity.CanShorten, newActivity.CanMove, newActivity.CanSkip); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert add-activity activity: %w", err)
	}
	for index, window := range newActivity.OpenWindows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_open_windows (
				trip_id, activity_id, window_index, opens_at, closes_at
			) VALUES ($1, $2, $3, $4, $5)
		`, request.TripID, newActivity.ID, index,
			window.OpensAt, window.ClosesAt); err != nil {
			return RecordedCommand{}, fmt.Errorf("insert add-activity window: %w", err)
		}
	}
	for index, activityID := range resultingActivityIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE trip_activities
			SET ordinal = $3
			WHERE trip_id = $1 AND id = $2
		`, request.TripID, activityID, index); err != nil {
			return RecordedCommand{}, fmt.Errorf("apply add-activity ordinal: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, $3, 'user_authored', $4, 1, $5, $6, $7, $8)
	`, request.PlanID, request.TripID, planRevision, request.OwnerUserID,
		payload, len(payload), checksum[:], createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert add-activity plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips
		SET trip_revision = $2, next_mutation_sequence = $3,
			finalized_mutation_sequence = $4, current_plan_id = $5,
			updated_at = clock_timestamp()
		WHERE id = $1
	`, request.TripID, planRevision, mutationSequence+1,
		mutationSequence, request.PlanID); err != nil {
		return RecordedCommand{}, fmt.Errorf("update add-activity trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO command_intents (
			id, trip_id, message_id, event_id, mutation_sequence,
			expected_trip_revision, command_kind, application_order,
			digest_algorithm, payload_digest, command_payload, state,
			outcome_status, outcome_payload, resulting_trip_revision,
			resulting_current_plan_id, runtime_sync_state, recorded_at, finalized_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'trip_edited',
			'canonical_first', 'rfc8785-sha256-v1', $7, $8, 'applied',
			'OK', $9, $10, $11, 'pending', $12, $12)
	`, request.IntentID, request.TripID, request.MessageID, request.EventID,
		mutationSequence, request.ExpectedTripRevision, request.PayloadDigest[:],
		request.CommandPayload, request.OutcomePayload, planRevision,
		request.PlanID, createdAt); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert add-activity intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO planner_outbox (
			id, command_intent_id, trip_id, mutation_sequence,
			event_schema_version, event_payload, delivery_state
		) VALUES ($1, $2, $3, $4, 1, $5, 'pending')
	`, request.OutboxID, request.IntentID, request.TripID,
		mutationSequence, request.EventPayload); err != nil {
		return RecordedCommand{}, fmt.Errorf("insert add-activity outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordedCommand{}, fmt.Errorf("commit add-activity: %w", err)
	}
	return RecordedCommand{
		IntentID: request.IntentID, OutboxID: request.OutboxID,
		TripID: request.TripID, MessageID: request.MessageID,
		EventID: request.EventID, MutationSequence: mutationSequence,
		ExpectedTripRevision: request.ExpectedTripRevision,
		Kind:                 CommandTripEdited, State: "applied",
		RuntimeSyncState: "pending", OutcomeStatus: stringPtr("OK"),
		OutcomePayload:         request.OutcomePayload,
		ResultingTripRevision:  &planRevision,
		ResultingCurrentPlanID: &request.PlanID,
		RecordedAt:             createdAt, FinalizedAt: &createdAt,
	}, nil
}

func stringPtr(value string) *string { return &value }
