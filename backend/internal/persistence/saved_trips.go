package persistence

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TripSummary struct {
	TripID         string `json:"trip_id"`
	TripName       string `json:"trip_name"`
	TripRevision   uint64 `json:"trip_revision"`
	ExecutionState string `json:"execution_state"`
}

type TripList struct {
	InactiveTrips        []TripSummary `json:"inactive_trips"`
	CurrentExecutionTrip *TripSummary  `json:"current_execution_trip,omitempty"`
}

type PlaceView struct {
	PlaceID          string  `json:"place_id"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	FormattedAddress string  `json:"formatted_address,omitempty"`
	DisplayName      string  `json:"display_name"`
	TimeZoneName     string  `json:"time_zone_name"`
	CreatedAtUnixMS  int64   `json:"created_at_unix_ms"`
}

type SavedActivityView struct {
	ActivityID        string             `json:"activity_id"`
	Place             PlaceView          `json:"place"`
	Ordinal           int                `json:"ordinal"`
	Schedule          map[string]any     `json:"schedule"`
	InboundTravelMode string             `json:"inbound_travel_mode"`
	ActivityClass     string             `json:"activity_class"`
	PriorityRank      int                `json:"priority_rank"`
	UtilityScore      int                `json:"utility_score"`
	Timing            ActivityTimingView `json:"timing"`
}

type ActivityTimingView struct {
	OpenWindows              []map[string]int64 `json:"open_windows"`
	ReservationStartOffset   *int64             `json:"reservation_start_offset_ms,omitempty"`
	ReservationGraceSeconds  int64              `json:"reservation_grace_seconds"`
	MandatoryDeadlineOffset  *int64             `json:"mandatory_deadline_offset_ms,omitempty"`
	MinDurationSeconds       int                `json:"min_duration_seconds"`
	PreferredDurationSeconds int                `json:"preferred_duration_seconds"`
	MaxDurationSeconds       int                `json:"max_duration_seconds"`
	Mandatory                bool               `json:"mandatory"`
	CanShorten               bool               `json:"can_shorten"`
	CanMove                  bool               `json:"can_move"`
	CanSkip                  bool               `json:"can_skip"`
}

type SavedPlanView struct {
	SavedPlanID       string              `json:"saved_plan_id"`
	SavedPlanRevision string              `json:"saved_plan_revision"`
	Activities        []SavedActivityView `json:"activities"`
	CreatedAtUnixMS   int64               `json:"created_at_unix_ms"`
}

type DisplayScheduleView struct {
	LocalDate    string `json:"local_date"`
	LocalTime    string `json:"local_time"`
	TimeZoneName string `json:"time_zone_name"`
}

type ExecutionOperationView struct {
	OperationID           string `json:"operation_id"`
	Kind                  string `json:"kind"`
	State                 string `json:"state"`
	LastStep              string `json:"last_step"`
	TargetExecutionPlanID string `json:"target_execution_plan_id,omitempty"`
	SafeErrorCode         string `json:"safe_error_code,omitempty"`
	CreatedAtUnixMS       int64  `json:"created_at_unix_ms"`
	UpdatedAtUnixMS       int64  `json:"updated_at_unix_ms"`
	CompletedAtUnixMS     *int64 `json:"completed_at_unix_ms,omitempty"`
}

type ActiveExecutionView struct {
	ExecutionPlanID   string `json:"execution_plan_id"`
	ActivatedAtUnixMS int64  `json:"activated_at_unix_ms"`
}

type TripView struct {
	TripID              string                  `json:"trip_id"`
	TripName            string                  `json:"trip_name"`
	DefaultTimeZoneName string                  `json:"default_time_zone_name"`
	TripRevision        string                  `json:"trip_revision"`
	ExecutionState      string                  `json:"execution_state"`
	DisplaySchedule     *DisplayScheduleView    `json:"display_schedule,omitempty"`
	SavedPlan           SavedPlanView           `json:"saved_plan"`
	TransitionOperation *ExecutionOperationView `json:"transition_operation,omitempty"`
	ActiveExecution     *ActiveExecutionView    `json:"active_execution,omitempty"`
	CreatedAtUnixMS     int64                   `json:"created_at_unix_ms"`
	UpdatedAtUnixMS     int64                   `json:"updated_at_unix_ms"`
}

type SavedTripStore struct {
	pool *pgxpool.Pool
}

type savedTripQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func NewSavedTripStore(pool *pgxpool.Pool) (*SavedTripStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &SavedTripStore{pool: pool}, nil
}

func (store *SavedTripStore) List(ctx context.Context, userID string) (TripList, error) {
	if ctx == nil || !validCanonicalUUID(userID) {
		return TripList{}, errors.New("user id is invalid")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT id::text, trip_name, trip_revision, execution_state
		FROM trips
		WHERE owner_user_id = $1 AND execution_state = 'inactive'
		ORDER BY updated_at DESC, id
	`, userID)
	if err != nil {
		return TripList{}, fmt.Errorf("list inactive trips: %w", err)
	}
	defer rows.Close()
	result := TripList{InactiveTrips: make([]TripSummary, 0)}
	for rows.Next() {
		var summary TripSummary
		var revision int64
		if err := rows.Scan(&summary.TripID, &summary.TripName, &revision, &summary.ExecutionState); err != nil {
			return TripList{}, fmt.Errorf("scan inactive trip: %w", err)
		}
		if revision < 0 {
			return TripList{}, errors.New("trip revision is invalid")
		}
		summary.TripRevision = uint64(revision)
		result.InactiveTrips = append(result.InactiveTrips, summary)
	}
	if err := rows.Err(); err != nil {
		return TripList{}, fmt.Errorf("iterate inactive trips: %w", err)
	}
	var summary TripSummary
	var revision int64
	err = store.pool.QueryRow(ctx, `
		SELECT id::text, trip_name, trip_revision, execution_state
		FROM trips
		WHERE owner_user_id = $1 AND execution_state <> 'inactive'
		ORDER BY updated_at DESC, id
		LIMIT 1
	`, userID).Scan(&summary.TripID, &summary.TripName, &revision, &summary.ExecutionState)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return TripList{}, fmt.Errorf("load current execution trip: %w", err)
	}
	if revision < 0 {
		return TripList{}, errors.New("trip revision is invalid")
	}
	summary.TripRevision = uint64(revision)
	result.CurrentExecutionTrip = &summary
	return result, nil
}

func (store *SavedTripStore) Get(ctx context.Context, userID, tripID string) (TripView, error) {
	if ctx == nil || !validCanonicalUUID(userID) || !validCanonicalUUID(tripID) {
		return TripView{}, errors.New("trip or user id is invalid")
	}
	return store.get(ctx, store.pool, userID, tripID)
}

func (store *SavedTripStore) get(ctx context.Context, query savedTripQuerier, userID, tripID string) (TripView, error) {
	var result TripView
	var revision int64
	var savedPlanID *string
	var displayDate, displayTime, displayZone *string
	var activePlanID *string
	var activatedAt *int64
	var createdAt, updatedAt time.Time
	err := query.QueryRow(ctx, `
		SELECT t.id::text, t.trip_name, t.default_time_zone_name, t.trip_revision, t.execution_state,
		       t.saved_plan_id::text, p.display_local_date::text, to_char(p.display_local_time, 'HH24:MI:SS'), p.display_time_zone_name,
		       t.active_execution_plan_id::text, (EXTRACT(EPOCH FROM t.activated_at) * 1000)::bigint,
		       t.created_at, t.updated_at
		FROM trips t
		LEFT JOIN saved_trip_plans p ON p.id = t.saved_plan_id
		WHERE t.owner_user_id = $1 AND t.id = $2
	`, userID, tripID).Scan(&result.TripID, &result.TripName, &result.DefaultTimeZoneName, &revision, &result.ExecutionState, &savedPlanID, &displayDate, &displayTime, &displayZone, &activePlanID, &activatedAt, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TripView{}, ErrTripNotFound
	}
	if err != nil {
		return TripView{}, fmt.Errorf("load trip: %w", err)
	}
	if revision < 0 || savedPlanID == nil || *savedPlanID == "" {
		return TripView{}, errors.New("saved trip metadata is invalid")
	}
	savedPlanIDValue := *savedPlanID
	result.TripRevision = strconv.FormatInt(revision, 10)
	result.CreatedAtUnixMS, result.UpdatedAtUnixMS = createdAt.UnixMilli(), updatedAt.UnixMilli()
	if displayDate != nil && displayTime != nil && displayZone != nil {
		result.DisplaySchedule = &DisplayScheduleView{LocalDate: *displayDate, LocalTime: *displayTime, TimeZoneName: *displayZone}
	}
	if activePlanID != nil && activatedAt != nil {
		result.ActiveExecution = &ActiveExecutionView{ExecutionPlanID: *activePlanID, ActivatedAtUnixMS: *activatedAt}
	}

	var planRevision int64
	var planCreated time.Time
	if err := query.QueryRow(ctx, `SELECT saved_plan_revision, created_at FROM saved_trip_plans WHERE id = $1 AND trip_id = $2 AND owner_user_id = $3`, savedPlanIDValue, tripID, userID).Scan(&planRevision, &planCreated); err != nil {
		return TripView{}, fmt.Errorf("load saved plan: %w", err)
	}
	result.SavedPlan = SavedPlanView{SavedPlanID: savedPlanIDValue, SavedPlanRevision: strconv.FormatInt(planRevision, 10), Activities: make([]SavedActivityView, 0), CreatedAtUnixMS: planCreated.UnixMilli()}
	rows, err := query.Query(ctx, `
		SELECT a.activity_id::text, a.ordinal, a.schedule_state, a.start_offset_ms, a.end_offset_ms,
		       a.inbound_travel_mode, a.activity_class, a.priority_rank, a.utility_score,
		       a.reservation_start_offset_ms, a.reservation_grace_seconds, a.mandatory_deadline_offset_ms,
		       a.min_duration_seconds, a.preferred_duration_seconds, a.max_duration_seconds,
		       a.mandatory, a.can_shorten, a.can_move, a.can_skip,
		       p.id::text, p.latitude, p.longitude, p.formatted_address, p.display_name, p.time_zone_name, p.created_at
		FROM saved_trip_activities a JOIN places p ON p.id = a.place_id
		WHERE a.saved_plan_id = $1
		ORDER BY a.ordinal
	`, savedPlanIDValue)
	if err != nil {
		return TripView{}, fmt.Errorf("load saved activities: %w", err)
	}
	for rows.Next() {
		var activity SavedActivityView
		var state string
		var startOffset, endOffset, reservationOffset, deadlineOffset *int64
		var reservationGrace int64
		var minDuration, preferredDuration, maxDuration int
		var place PlaceView
		var formattedAddress *string
		var placeCreated time.Time
		if err := rows.Scan(&activity.ActivityID, &activity.Ordinal, &state, &startOffset, &endOffset, &activity.InboundTravelMode, &activity.ActivityClass, &activity.PriorityRank, &activity.UtilityScore, &reservationOffset, &reservationGrace, &deadlineOffset, &minDuration, &preferredDuration, &maxDuration, &activity.Timing.Mandatory, &activity.Timing.CanShorten, &activity.Timing.CanMove, &activity.Timing.CanSkip, &place.PlaceID, &place.Latitude, &place.Longitude, &formattedAddress, &place.DisplayName, &place.TimeZoneName, &placeCreated); err != nil {
			return TripView{}, fmt.Errorf("scan saved activity: %w", err)
		}
		if formattedAddress != nil {
			place.FormattedAddress = *formattedAddress
		}
		place.CreatedAtUnixMS = placeCreated.UnixMilli()
		activity.Place = place
		activity.Schedule = map[string]any{"state": state}
		if state == "scheduled" {
			activity.Schedule["start_offset_ms"], activity.Schedule["end_offset_ms"] = *startOffset, *endOffset
		}
		activity.Timing.ReservationStartOffset, activity.Timing.ReservationGraceSeconds = reservationOffset, reservationGrace
		activity.Timing.MandatoryDeadlineOffset = deadlineOffset
		activity.Timing.MinDurationSeconds, activity.Timing.PreferredDurationSeconds, activity.Timing.MaxDurationSeconds = minDuration, preferredDuration, maxDuration
		activity.Timing.OpenWindows = make([]map[string]int64, 0)
		result.SavedPlan.Activities = append(result.SavedPlan.Activities, activity)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TripView{}, fmt.Errorf("iterate saved activities: %w", err)
	}
	rows.Close()
	for index := range result.SavedPlan.Activities {
		activity := &result.SavedPlan.Activities[index]
		windowRows, windowErr := query.Query(ctx, `SELECT opens_offset_ms, closes_offset_ms FROM saved_activity_open_windows WHERE saved_plan_id = $1 AND activity_id = $2 ORDER BY window_index`, savedPlanIDValue, activity.ActivityID)
		if windowErr != nil {
			return TripView{}, fmt.Errorf("load activity windows: %w", windowErr)
		}
		for windowRows.Next() {
			var opens, closes int64
			if err := windowRows.Scan(&opens, &closes); err != nil {
				windowRows.Close()
				return TripView{}, fmt.Errorf("scan activity window: %w", err)
			}
			activity.Timing.OpenWindows = append(activity.Timing.OpenWindows, map[string]int64{"opens_offset_ms": opens, "closes_offset_ms": closes})
		}
		if err := windowRows.Err(); err != nil {
			windowRows.Close()
			return TripView{}, fmt.Errorf("iterate activity windows: %w", err)
		}
		windowRows.Close()
	}
	if len(result.SavedPlan.Activities) == 0 {
		return TripView{}, errors.New("saved trip plan has no activities")
	}
	if result.ExecutionState == "activating" || result.ExecutionState == "deactivating" {
		result.TransitionOperation, err = store.loadExecutionOperation(ctx, userID, tripID)
		if err != nil {
			return TripView{}, err
		}
	}
	return result, nil
}

func (store *SavedTripStore) loadExecutionOperation(ctx context.Context, userID, tripID string) (*ExecutionOperationView, error) {
	var operation ExecutionOperationView
	var createdAt, updatedAt time.Time
	var targetID, safeError *string
	var completedAt *time.Time
	err := store.pool.QueryRow(ctx, `
		SELECT id::text, kind, state, last_step, target_execution_plan_id::text, safe_error_code, created_at, updated_at, completed_at
		FROM trip_execution_operations WHERE owner_user_id = $1 AND trip_id = $2
		ORDER BY updated_at DESC LIMIT 1
	`, userID, tripID).Scan(&operation.OperationID, &operation.Kind, &operation.State, &operation.LastStep, &targetID, &safeError, &createdAt, &updatedAt, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("trip transition operation is missing")
	}
	if err != nil {
		return nil, fmt.Errorf("load trip transition operation: %w", err)
	}
	if targetID != nil {
		operation.TargetExecutionPlanID = *targetID
	}
	if safeError != nil {
		operation.SafeErrorCode = *safeError
	}
	operation.CreatedAtUnixMS, operation.UpdatedAtUnixMS = createdAt.UnixMilli(), updatedAt.UnixMilli()
	if completedAt != nil {
		value := completedAt.UnixMilli()
		operation.CompletedAtUnixMS = &value
	}
	return &operation, nil
}
