package persistence

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrCanonicalStateCorrupt = errors.New("canonical trip state is corrupt")

type CanonicalOpenWindow struct {
	OpensAt  time.Time
	ClosesAt time.Time
}

type CanonicalActivity struct {
	ID                       string
	Ordinal                  uint32
	PlaceID                  string
	DisplayName              string
	Latitude                 float64
	Longitude                float64
	TimeZoneName             string
	InboundTravelMode        string
	ActivityClass            string
	ActivityState            ActivityState
	ActivityDelaySeconds     uint32
	FoundClosedAt            *time.Time
	PriorityRank             int32
	UtilityScore             int32
	ReservationStart         *time.Time
	ReservationGraceSeconds  uint32
	MandatoryDeadline        *time.Time
	MinDurationSeconds       uint32
	PreferredDurationSeconds uint32
	MaxDurationSeconds       uint32
	Mandatory                bool
	CanShorten               bool
	CanMove                  bool
	CanSkip                  bool
	OpenWindows              []CanonicalOpenWindow
}

type CanonicalTravelDelay struct {
	FromActivityID    string
	ToActivityID      string
	AdditionalSeconds uint32
	ObservedAt        time.Time
}

type CanonicalCurrentPlan struct {
	ID               string
	Revision         uint64
	Origin           string
	AuthoredByUserID string
	SourceProposalID *string
	SchemaVersion    uint32
	Payload          []byte
	Checksum         [32]byte
	CreatedAt        time.Time
}

type CanonicalTripState struct {
	TripID                    string
	OwnerUserID               string
	DefaultTimeZoneName       string
	TripRevision              uint64
	FinalizedMutationSequence uint64
	CurrentPlanID             string
	CompletedPrefixCount      uint32
	CurrentActivityID         *string
	Activities                []CanonicalActivity
	TravelDelays              []CanonicalTravelDelay
	CurrentPlan               CanonicalCurrentPlan
}

type CanonicalStateStore struct {
	pool *pgxpool.Pool
}

func NewCanonicalStateStore(
	pool *pgxpool.Pool,
) (*CanonicalStateStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &CanonicalStateStore{pool: pool}, nil
}

func corruptCanonicalState(reason string) error {
	return fmt.Errorf("%w: %s", ErrCanonicalStateCorrupt, reason)
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func (store *CanonicalStateStore) Load(
	ctx context.Context,
	tripID string,
) (CanonicalTripState, error) {
	if !validCanonicalUUID(tripID) {
		return CanonicalTripState{}, errors.New("trip id is invalid")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return CanonicalTripState{},
			fmt.Errorf("begin canonical-state load: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result CanonicalTripState
	var tripRevision int64
	var finalizedSequence int64
	err = tx.QueryRow(ctx, `
		SELECT id::text,
		       owner_user_id::text,
		       default_time_zone_name,
		       trip_revision,
		       finalized_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, tripID).Scan(
		&result.TripID,
		&result.OwnerUserID,
		&result.DefaultTimeZoneName,
		&tripRevision,
		&finalizedSequence,
		&result.CurrentPlanID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CanonicalTripState{}, ErrTripNotFound
	}
	if err != nil {
		return CanonicalTripState{},
			fmt.Errorf("lock canonical trip: %w", err)
	}
	if tripRevision <= 0 ||
		finalizedSequence <= 0 ||
		!validCanonicalUUID(result.OwnerUserID) ||
		result.DefaultTimeZoneName == "" ||
		!validCanonicalUUID(result.CurrentPlanID) {
		return CanonicalTripState{},
			corruptCanonicalState("trip metadata is invalid")
	}
	result.TripRevision = uint64(tripRevision)
	result.FinalizedMutationSequence = uint64(finalizedSequence)

	activityRows, err := tx.Query(ctx, `
		SELECT id::text,
		       ordinal,
		       place_id,
		       display_name,
		       latitude,
		       longitude,
		       time_zone_name,
		       inbound_travel_mode,
		       activity_class,
		       activity_state,
		       activity_delay_seconds,
		       found_closed_at,
		       priority_rank,
		       utility_score,
		       reservation_start,
		       reservation_grace_seconds,
		       mandatory_deadline,
		       min_duration_seconds,
		       preferred_duration_seconds,
		       max_duration_seconds,
		       mandatory,
		       can_shorten,
		       can_move,
		       can_skip
		FROM trip_activities
		WHERE trip_id = $1
		ORDER BY ordinal
	`, tripID)
	if err != nil {
		return CanonicalTripState{},
			fmt.Errorf("load canonical activities: %w", err)
	}
	for activityRows.Next() {
		var activity CanonicalActivity
		var ordinal int32
		var delaySeconds int32
		var reservationGrace int32
		var minDuration int32
		var preferredDuration int32
		var maxDuration int32
		var foundClosed pgtype.Timestamptz
		var reservationStart pgtype.Timestamptz
		var mandatoryDeadline pgtype.Timestamptz
		err := activityRows.Scan(
			&activity.ID,
			&ordinal,
			&activity.PlaceID,
			&activity.DisplayName,
			&activity.Latitude,
			&activity.Longitude,
			&activity.TimeZoneName,
			&activity.InboundTravelMode,
			&activity.ActivityClass,
			&activity.ActivityState,
			&delaySeconds,
			&foundClosed,
			&activity.PriorityRank,
			&activity.UtilityScore,
			&reservationStart,
			&reservationGrace,
			&mandatoryDeadline,
			&minDuration,
			&preferredDuration,
			&maxDuration,
			&activity.Mandatory,
			&activity.CanShorten,
			&activity.CanMove,
			&activity.CanSkip,
		)
		if err != nil {
			activityRows.Close()
			return CanonicalTripState{},
				fmt.Errorf("scan canonical activity: %w", err)
		}
		if ordinal < 0 ||
			ordinal != int32(len(result.Activities)) ||
			delaySeconds < 0 ||
			reservationGrace < 0 ||
			minDuration < 0 ||
			preferredDuration < minDuration ||
			maxDuration < preferredDuration ||
			!validCanonicalUUID(activity.ID) ||
			activity.PlaceID == "" ||
			activity.DisplayName == "" ||
			activity.TimeZoneName == "" ||
			math.IsNaN(activity.Latitude) ||
			math.IsInf(activity.Latitude, 0) ||
			activity.Latitude < -90 ||
			activity.Latitude > 90 ||
			math.IsNaN(activity.Longitude) ||
			math.IsInf(activity.Longitude, 0) ||
			activity.Longitude < -180 ||
			activity.Longitude > 180 ||
			(activity.InboundTravelMode != "walking" &&
				activity.InboundTravelMode != "driving") ||
			(activity.ActivityClass != "fixed" &&
				activity.ActivityClass != "flexible") ||
			!activity.ActivityState.valid() {
			activityRows.Close()
			return CanonicalTripState{},
				corruptCanonicalState("activity is invalid")
		}
		activity.Ordinal = uint32(ordinal)
		activity.ActivityDelaySeconds = uint32(delaySeconds)
		activity.ReservationGraceSeconds = uint32(reservationGrace)
		activity.MinDurationSeconds = uint32(minDuration)
		activity.PreferredDurationSeconds = uint32(preferredDuration)
		activity.MaxDurationSeconds = uint32(maxDuration)
		activity.FoundClosedAt = nullableTime(foundClosed)
		activity.ReservationStart = nullableTime(reservationStart)
		activity.MandatoryDeadline = nullableTime(mandatoryDeadline)
		result.Activities = append(result.Activities, activity)
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		return CanonicalTripState{},
			fmt.Errorf("iterate canonical activities: %w", err)
	}
	activityRows.Close()
	if len(result.Activities) > 64 {
		return CanonicalTripState{},
			corruptCanonicalState("activity limit is exceeded")
	}

	activityIndex := make(map[string]int, len(result.Activities))
	for index, activity := range result.Activities {
		activityIndex[activity.ID] = index
	}
	windowRows, err := tx.Query(ctx, `
		SELECT activity_id::text, window_index, opens_at, closes_at
		FROM activity_open_windows
		WHERE trip_id = $1
		ORDER BY activity_id, window_index
	`, tripID)
	if err != nil {
		return CanonicalTripState{},
			fmt.Errorf("load canonical windows: %w", err)
	}
	windowCounts := make(map[string]int)
	for windowRows.Next() {
		var activityID string
		var windowIndex int32
		var window CanonicalOpenWindow
		if err := windowRows.Scan(
			&activityID,
			&windowIndex,
			&window.OpensAt,
			&window.ClosesAt,
		); err != nil {
			windowRows.Close()
			return CanonicalTripState{},
				fmt.Errorf("scan canonical window: %w", err)
		}
		activityPosition, exists := activityIndex[activityID]
		if !exists ||
			windowIndex != int32(windowCounts[activityID]) ||
			!window.OpensAt.Before(window.ClosesAt) {
			windowRows.Close()
			return CanonicalTripState{},
				corruptCanonicalState("open window is invalid")
		}
		windows := result.Activities[activityPosition].OpenWindows
		if len(windows) > 0 &&
			window.OpensAt.Before(windows[len(windows)-1].ClosesAt) {
			windowRows.Close()
			return CanonicalTripState{},
				corruptCanonicalState("open windows overlap")
		}
		window.OpensAt = window.OpensAt.UTC()
		window.ClosesAt = window.ClosesAt.UTC()
		result.Activities[activityPosition].OpenWindows =
			append(windows, window)
		windowCounts[activityID]++
	}
	if err := windowRows.Err(); err != nil {
		windowRows.Close()
		return CanonicalTripState{},
			fmt.Errorf("iterate canonical windows: %w", err)
	}
	windowRows.Close()

	delayRows, err := tx.Query(ctx, `
		SELECT from_activity_id::text,
		       to_activity_id::text,
		       additional_seconds,
		       observed_at
		FROM trip_travel_delays
		WHERE trip_id = $1
		ORDER BY from_activity_id, to_activity_id
	`, tripID)
	if err != nil {
		return CanonicalTripState{},
			fmt.Errorf("load canonical travel delays: %w", err)
	}
	for delayRows.Next() {
		var delay CanonicalTravelDelay
		var additionalSeconds int32
		if err := delayRows.Scan(
			&delay.FromActivityID,
			&delay.ToActivityID,
			&additionalSeconds,
			&delay.ObservedAt,
		); err != nil {
			delayRows.Close()
			return CanonicalTripState{},
				fmt.Errorf("scan canonical travel delay: %w", err)
		}
		if _, exists := activityIndex[delay.FromActivityID]; !exists {
			delayRows.Close()
			return CanonicalTripState{},
				corruptCanonicalState("travel-delay origin is missing")
		}
		if _, exists := activityIndex[delay.ToActivityID]; !exists ||
			additionalSeconds < 0 {
			delayRows.Close()
			return CanonicalTripState{},
				corruptCanonicalState("travel-delay destination is invalid")
		}
		delay.AdditionalSeconds = uint32(additionalSeconds)
		delay.ObservedAt = delay.ObservedAt.UTC()
		result.TravelDelays = append(result.TravelDelays, delay)
	}
	if err := delayRows.Err(); err != nil {
		delayRows.Close()
		return CanonicalTripState{},
			fmt.Errorf("iterate canonical travel delays: %w", err)
	}
	delayRows.Close()

	var planRevision int64
	var schemaVersion int32
	var payloadSize int32
	var checksum []byte
	var sourceProposalID pgtype.Text
	err = tx.QueryRow(ctx, `
		SELECT id::text,
		       plan_revision,
		       origin,
		       authored_by_user_id::text,
		       source_proposal_id::text,
		       schema_version,
		       payload,
		       payload_size_bytes,
		       checksum_sha256,
		       created_at
		FROM itinerary_plans
		WHERE trip_id = $1 AND id = $2
		FOR UPDATE
	`, tripID, result.CurrentPlanID).Scan(
		&result.CurrentPlan.ID,
		&planRevision,
		&result.CurrentPlan.Origin,
		&result.CurrentPlan.AuthoredByUserID,
		&sourceProposalID,
		&schemaVersion,
		&result.CurrentPlan.Payload,
		&payloadSize,
		&checksum,
		&result.CurrentPlan.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CanonicalTripState{},
			corruptCanonicalState("current plan is missing")
	}
	if err != nil {
		return CanonicalTripState{},
			fmt.Errorf("load canonical current plan: %w", err)
	}
	if planRevision <= 0 ||
		schemaVersion != 1 ||
		payloadSize != int32(len(result.CurrentPlan.Payload)) ||
		len(checksum) != sha256.Size ||
		result.CurrentPlan.ID != result.CurrentPlanID ||
		result.CurrentPlan.AuthoredByUserID != result.OwnerUserID {
		return CanonicalTripState{},
			corruptCanonicalState("current-plan row metadata is invalid")
	}
	result.CurrentPlan.Revision = uint64(planRevision)
	result.CurrentPlan.SchemaVersion = uint32(schemaVersion)
	result.CurrentPlan.CreatedAt = result.CurrentPlan.CreatedAt.UTC()
	if sourceProposalID.Valid {
		value := sourceProposalID.String
		result.CurrentPlan.SourceProposalID = &value
	}
	copy(result.CurrentPlan.Checksum[:], checksum)
	actualChecksum := sha256.Sum256(result.CurrentPlan.Payload)
	if subtle.ConstantTimeCompare(
		actualChecksum[:],
		result.CurrentPlan.Checksum[:],
	) != 1 {
		return CanonicalTripState{},
			corruptCanonicalState("current-plan checksum differs")
	}
	plan, err := parseSnapshotCurrentPlan(result.CurrentPlan.Payload)
	if err != nil ||
		plan.planID != result.CurrentPlan.ID ||
		plan.revision != result.CurrentPlan.Revision ||
		plan.createdAtUnixMS != result.CurrentPlan.CreatedAt.UnixMilli() ||
		result.CurrentPlan.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 {
		return CanonicalTripState{},
			corruptCanonicalState("current-plan payload metadata differs")
	}
	expectedOrigin := uint64(1)
	if result.CurrentPlan.Origin == "accepted_engine_proposal" {
		expectedOrigin = 2
	} else if result.CurrentPlan.Origin != "user_authored" {
		return CanonicalTripState{},
			corruptCanonicalState("current-plan origin is invalid")
	}
	if plan.origin != expectedOrigin ||
		!optionalStringEqual(
			result.CurrentPlan.SourceProposalID,
			plan.sourceProposalID,
		) {
		return CanonicalTripState{},
			corruptCanonicalState("current-plan source metadata differs")
	}

	if len(plan.segments) != len(result.Activities) {
		return CanonicalTripState{},
			corruptCanonicalState("current plan omits activities")
	}
	seenActivities := make(map[string]struct{}, len(plan.segments))
	prefixOpen := true
	for planIndex, segment := range plan.segments {
		activityPosition, exists := activityIndex[segment.activityID]
		if !exists {
			return CanonicalTripState{},
				corruptCanonicalState("current plan has an unknown activity")
		}
		if _, duplicate := seenActivities[segment.activityID]; duplicate {
			return CanonicalTripState{},
				corruptCanonicalState("current plan repeats an activity")
		}
		seenActivities[segment.activityID] = struct{}{}
		state := result.Activities[activityPosition].ActivityState
		terminal := state == ActivityStateCompleted ||
			state == ActivityStateSkipped
		if prefixOpen && terminal {
			result.CompletedPrefixCount++
			continue
		}
		prefixOpen = false
		if terminal {
			return CanonicalTripState{},
				corruptCanonicalState("terminal activity is outside the prefix")
		}
		if state == ActivityStateStarted {
			if result.CurrentActivityID != nil ||
				planIndex != int(result.CompletedPrefixCount) {
				return CanonicalTripState{},
					corruptCanonicalState("started activity is out of order")
			}
			value := segment.activityID
			result.CurrentActivityID = &value
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return CanonicalTripState{},
			fmt.Errorf("commit canonical-state load: %w", err)
	}
	return result, nil
}

func optionalStringEqual(value *string, encoded string) bool {
	if value == nil {
		return encoded == ""
	}
	return *value == encoded
}
