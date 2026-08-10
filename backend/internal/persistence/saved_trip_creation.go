package persistence

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const httpIdempotencyRetention = 30 * 24 * time.Hour

var (
	ErrSavedTripInput        = errors.New("saved trip input is invalid")
	ErrHTTPIdempotencyReused = errors.New("HTTP idempotency key was reused")
	ErrHTTPMutationPending   = errors.New("HTTP mutation is still pending")
)

type SavedScheduleInput struct {
	State         string `json:"state"`
	StartOffsetMS *int64 `json:"start_offset_ms,omitempty"`
	EndOffsetMS   *int64 `json:"end_offset_ms,omitempty"`
}

type RelativeWindowInput struct {
	OpensOffsetMS  int64 `json:"opens_offset_ms"`
	ClosesOffsetMS int64 `json:"closes_offset_ms"`
}

type SavedActivityTimingInput struct {
	OpenWindows              []RelativeWindowInput `json:"open_windows"`
	ReservationStartOffsetMS *int64                `json:"reservation_start_offset_ms,omitempty"`
	ReservationGraceSeconds  int64                 `json:"reservation_grace_seconds"`
	MandatoryDeadlineOffset  *int64                `json:"mandatory_deadline_offset_ms,omitempty"`
	MinDurationSeconds       int                   `json:"min_duration_seconds"`
	PreferredDurationSeconds int                   `json:"preferred_duration_seconds"`
	MaxDurationSeconds       int                   `json:"max_duration_seconds"`
	Mandatory                bool                  `json:"mandatory"`
	CanShorten               bool                  `json:"can_shorten"`
	CanMove                  bool                  `json:"can_move"`
	CanSkip                  bool                  `json:"can_skip"`
}

type SavedActivityInput struct {
	PlaceID           string                   `json:"place_id"`
	Ordinal           int                      `json:"ordinal"`
	Schedule          SavedScheduleInput       `json:"schedule"`
	InboundTravelMode string                   `json:"inbound_travel_mode"`
	ActivityClass     string                   `json:"activity_class"`
	PriorityRank      int                      `json:"priority_rank"`
	UtilityScore      int                      `json:"utility_score"`
	Timing            SavedActivityTimingInput `json:"timing"`
}

type DisplayScheduleInput struct {
	LocalDate    string `json:"local_date"`
	LocalTime    string `json:"local_time"`
	TimeZoneName string `json:"time_zone_name"`
}

func (schedule *SavedScheduleInput) UnmarshalJSON(raw []byte) error {
	type wire SavedScheduleInput
	var value wire
	if err := decodeStrictSavedInput(raw, &value); err != nil {
		return err
	}
	if err := requireSavedMembers(raw, "state"); err != nil {
		return err
	}
	*schedule = SavedScheduleInput(value)
	return nil
}

func (window *RelativeWindowInput) UnmarshalJSON(raw []byte) error {
	type wire RelativeWindowInput
	var value wire
	if err := decodeStrictSavedInput(raw, &value); err != nil {
		return err
	}
	if err := requireSavedMembers(raw, "opens_offset_ms", "closes_offset_ms"); err != nil {
		return err
	}
	*window = RelativeWindowInput(value)
	return nil
}

func (timing *SavedActivityTimingInput) UnmarshalJSON(raw []byte) error {
	type wire SavedActivityTimingInput
	var value wire
	if err := decodeStrictSavedInput(raw, &value); err != nil {
		return err
	}
	if err := requireSavedMembers(raw, "open_windows", "reservation_grace_seconds", "min_duration_seconds", "preferred_duration_seconds", "max_duration_seconds", "mandatory", "can_shorten", "can_move", "can_skip"); err != nil {
		return err
	}
	if value.OpenWindows == nil {
		return ErrSavedTripInput
	}
	*timing = SavedActivityTimingInput(value)
	return nil
}

func (activity *SavedActivityInput) UnmarshalJSON(raw []byte) error {
	type wire SavedActivityInput
	var value wire
	if err := decodeStrictSavedInput(raw, &value); err != nil {
		return err
	}
	if err := requireSavedMembers(raw, "place_id", "ordinal", "schedule", "inbound_travel_mode", "activity_class", "priority_rank", "utility_score", "timing"); err != nil {
		return err
	}
	*activity = SavedActivityInput(value)
	return nil
}

func (schedule *DisplayScheduleInput) UnmarshalJSON(raw []byte) error {
	type wire DisplayScheduleInput
	var value wire
	if err := decodeStrictSavedInput(raw, &value); err != nil {
		return err
	}
	if err := requireSavedMembers(raw, "local_date", "local_time", "time_zone_name"); err != nil {
		return err
	}
	*schedule = DisplayScheduleInput(value)
	return nil
}

func decodeStrictSavedInput(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrSavedTripInput
	}
	return nil
}

func requireSavedMembers(raw []byte, names ...string) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return ErrSavedTripInput
	}
	for _, name := range names {
		if _, exists := members[name]; !exists {
			return ErrSavedTripInput
		}
	}
	return nil
}

type CreateSavedTripRequest struct {
	UserID              string                `json:"-"`
	IdempotencyKey      string                `json:"-"`
	RequestDigest       [32]byte              `json:"-"`
	TripName            string                `json:"trip_name"`
	DefaultTimeZoneName string                `json:"default_time_zone_name"`
	DisplaySchedule     *DisplayScheduleInput `json:"display_schedule,omitempty"`
	Activities          []SavedActivityInput  `json:"activities"`
}

type CreatedSavedTrip struct {
	Trip      TripView
	Duplicate bool
}

// Create stores only the inactive relative-plan authority. The absolute V1
// execution tables are deliberately untouched until activation.
func (store *SavedTripStore) Create(ctx context.Context, request CreateSavedTripRequest) (CreatedSavedTrip, error) {
	if err := validateCreateSavedTrip(request); err != nil {
		return CreatedSavedTrip{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("begin saved-trip creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	idempotencyID, err := newAuthUUID()
	if err != nil {
		return CreatedSavedTrip{}, err
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO http_idempotency_records (
			id, user_id, idempotency_key, http_method, normalized_path,
			operation_kind, request_digest_algorithm, request_digest, state,
			retain_until
		) VALUES ($1, $2, $3, 'POST', '/api/v1/trips', 'create_trip',
			'rfc8785-sha256-v1', $4, 'in_progress',
			clock_timestamp() + $5::interval)
		ON CONFLICT (user_id, http_method, normalized_path, idempotency_key)
		DO NOTHING
		RETURNING id::text
	`, idempotencyID, request.UserID, request.IdempotencyKey, request.RequestDigest[:], httpIdempotencyRetention.String()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.replayCreatedTrip(ctx, tx, request)
	}
	if err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("record trip idempotency: %w", err)
	}

	tripID, err := newAuthUUID()
	if err != nil {
		return CreatedSavedTrip{}, err
	}
	savedPlanID, err := newAuthUUID()
	if err != nil {
		return CreatedSavedTrip{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence,
			current_plan_id, trip_name, saved_plan_id, execution_state,
			active_execution_plan_id, activated_at, transition_operation_id
		) VALUES ($1, $2, $3, 1, 1, 0, NULL, $4, $5, 'inactive', NULL, NULL, NULL)
	`, tripID, request.UserID, request.DefaultTimeZoneName, request.TripName, savedPlanID); err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("insert inactive trip: %w", err)
	}

	var displayDate, displayTime, displayZone any
	if request.DisplaySchedule != nil {
		displayDate = request.DisplaySchedule.LocalDate
		displayTime = request.DisplaySchedule.LocalTime
		displayZone = request.DisplaySchedule.TimeZoneName
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_plans (
			id, trip_id, owner_user_id, saved_plan_revision, authored_by_user_id,
			display_local_date, display_local_time, display_time_zone_name
		) VALUES ($1, $2, $3, 1, $3, $4::date, $5::time, $6)
	`, savedPlanID, tripID, request.UserID, displayDate, displayTime, displayZone); err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("insert initial saved plan: %w", err)
	}

	for _, activity := range request.Activities {
		activityID, err := newAuthUUID()
		if err != nil {
			return CreatedSavedTrip{}, err
		}
		var ownedPlaceID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM places WHERE id = $1 AND owner_user_id = $2`, activity.PlaceID, request.UserID).Scan(&ownedPlaceID); errors.Is(err, pgx.ErrNoRows) {
			return CreatedSavedTrip{}, ErrSavedTripInput
		} else if err != nil {
			return CreatedSavedTrip{}, fmt.Errorf("validate saved activity place: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO saved_trip_activities (
				saved_plan_id, trip_id, owner_user_id, activity_id, place_id,
				ordinal, schedule_state, start_offset_ms, end_offset_ms,
				inbound_travel_mode, activity_class, priority_rank, utility_score,
				reservation_start_offset_ms, reservation_grace_seconds,
				mandatory_deadline_offset_ms, min_duration_seconds,
				preferred_duration_seconds, max_duration_seconds, mandatory,
				can_shorten, can_move, can_skip
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
				$14, $15, $16, $17, $18, $19, $20, $21, $22, $23
			)
		`, savedPlanID, tripID, request.UserID, activityID, ownedPlaceID,
			activity.Ordinal, activity.Schedule.State, activity.Schedule.StartOffsetMS,
			activity.Schedule.EndOffsetMS, activity.InboundTravelMode,
			activity.ActivityClass, activity.PriorityRank, activity.UtilityScore,
			activity.Timing.ReservationStartOffsetMS,
			activity.Timing.ReservationGraceSeconds,
			activity.Timing.MandatoryDeadlineOffset,
			activity.Timing.MinDurationSeconds,
			activity.Timing.PreferredDurationSeconds,
			activity.Timing.MaxDurationSeconds, activity.Timing.Mandatory,
			activity.Timing.CanShorten, activity.Timing.CanMove,
			activity.Timing.CanSkip); err != nil {
			return CreatedSavedTrip{}, fmt.Errorf("insert saved activity: %w", err)
		}
		for index, window := range activity.Timing.OpenWindows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO saved_activity_open_windows (
					saved_plan_id, activity_id, window_index,
					opens_offset_ms, closes_offset_ms
				) VALUES ($1, $2, $3, $4, $5)
			`, savedPlanID, activityID, index, window.OpensOffsetMS, window.ClosesOffsetMS); err != nil {
				return CreatedSavedTrip{}, fmt.Errorf("insert saved activity window: %w", err)
			}
		}
	}

	trip, err := store.get(ctx, tx, request.UserID, tripID)
	if err != nil {
		return CreatedSavedTrip{}, err
	}
	response, err := json.Marshal(trip)
	if err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("encode created trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records
		SET trip_id = $1, state = 'completed', response_status = 201,
			response_content_type = 'application/json', response_body = $2::jsonb,
			response_etag = $3, resource_id = $1, completed_at = clock_timestamp()
		WHERE id = $4 AND state = 'in_progress'
	`, tripID, string(response), `"trip-revision-1"`, idempotencyID); err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("complete trip idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("commit saved-trip creation: %w", err)
	}
	return CreatedSavedTrip{Trip: trip}, nil
}

func (store *SavedTripStore) replayCreatedTrip(ctx context.Context, tx pgx.Tx, request CreateSavedTripRequest) (CreatedSavedTrip, error) {
	var digest []byte
	var state string
	var status *int
	var responseText *string
	err := tx.QueryRow(ctx, `
		SELECT request_digest, state, response_status, response_body::text
		FROM http_idempotency_records
		WHERE user_id = $1 AND http_method = 'POST'
		  AND normalized_path = '/api/v1/trips' AND idempotency_key = $2
		FOR UPDATE
	`, request.UserID, request.IdempotencyKey).Scan(&digest, &state, &status, &responseText)
	if err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("load trip idempotency replay: %w", err)
	}
	if len(digest) != len(request.RequestDigest) || subtle.ConstantTimeCompare(digest, request.RequestDigest[:]) != 1 {
		return CreatedSavedTrip{}, ErrHTTPIdempotencyReused
	}
	if state != "completed" || status == nil || *status != 201 || responseText == nil {
		return CreatedSavedTrip{}, ErrHTTPMutationPending
	}
	var trip TripView
	if err := json.Unmarshal([]byte(*responseText), &trip); err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("decode trip idempotency replay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedSavedTrip{}, fmt.Errorf("commit trip idempotency replay: %w", err)
	}
	return CreatedSavedTrip{Trip: trip, Duplicate: true}, nil
}

func validateCreateSavedTrip(request CreateSavedTripRequest) error {
	if !validCanonicalUUID(request.UserID) || !validCanonicalUUID(request.IdempotencyKey) ||
		request.TripName != strings.TrimSpace(request.TripName) || len([]byte(request.TripName)) == 0 ||
		len([]byte(request.TripName)) > 120 || !validTimeZoneName(request.DefaultTimeZoneName) ||
		len(request.Activities) == 0 || len(request.Activities) > 64 {
		return ErrSavedTripInput
	}
	if request.DisplaySchedule != nil {
		if _, err := time.Parse("2006-01-02", request.DisplaySchedule.LocalDate); err != nil {
			return ErrSavedTripInput
		}
		if _, err := time.Parse("15:04:05", request.DisplaySchedule.LocalTime); err != nil ||
			!validTimeZoneName(request.DisplaySchedule.TimeZoneName) {
			return ErrSavedTripInput
		}
	}
	for index, activity := range request.Activities {
		if activity.Ordinal != index || !validCanonicalUUID(activity.PlaceID) ||
			(activity.InboundTravelMode != "walking" && activity.InboundTravelMode != "driving") ||
			(activity.ActivityClass != "fixed" && activity.ActivityClass != "flexible") ||
			activity.Timing.CanShorten || activity.Timing.OpenWindows == nil || len(activity.Timing.OpenWindows) > 32 ||
			activity.Timing.ReservationGraceSeconds < 0 || activity.Timing.ReservationGraceSeconds > 4294967295 ||
			!validDurationRange(activity.Timing.MinDurationSeconds, activity.Timing.PreferredDurationSeconds, activity.Timing.MaxDurationSeconds) ||
			!validOptionalDayOffset(activity.Timing.ReservationStartOffsetMS) ||
			!validOptionalDayOffset(activity.Timing.MandatoryDeadlineOffset) ||
			!validSavedSchedule(activity.Schedule) {
			return ErrSavedTripInput
		}
		for _, window := range activity.Timing.OpenWindows {
			if window.OpensOffsetMS < 0 || window.OpensOffsetMS >= window.ClosesOffsetMS || window.ClosesOffsetMS > 86400000 {
				return ErrSavedTripInput
			}
		}
	}
	return nil
}

func validDurationRange(minimum, preferred, maximum int) bool {
	return minimum >= 0 && minimum <= preferred && preferred <= maximum && maximum <= 86400
}

func validOptionalDayOffset(value *int64) bool {
	return value == nil || (*value >= 0 && *value <= 86400000)
}

func validSavedSchedule(schedule SavedScheduleInput) bool {
	switch schedule.State {
	case "unscheduled":
		return schedule.StartOffsetMS == nil && schedule.EndOffsetMS == nil
	case "scheduled":
		return schedule.StartOffsetMS != nil && schedule.EndOffsetMS != nil &&
			*schedule.StartOffsetMS >= 0 && *schedule.StartOffsetMS < *schedule.EndOffsetMS &&
			*schedule.EndOffsetMS <= 86400000
	default:
		return false
	}
}
