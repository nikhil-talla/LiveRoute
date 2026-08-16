package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrSavedActivityNotFound = errors.New("saved activity was not found")

type SavedActivityMutationRequest struct {
	UserID           string
	TripID           string
	ActivityID       string
	IdempotencyKey   string
	ExpectedRevision uint64
	RequestDigest    [32]byte
	Activity         *SavedActivityInput
}

func (store *SavedTripStore) AddActivity(ctx context.Context, request SavedActivityMutationRequest) (MutatedSavedTrip, error) {
	return store.mutateActivity(ctx, request, "add")
}

func (store *SavedTripStore) ReplaceActivity(ctx context.Context, request SavedActivityMutationRequest) (MutatedSavedTrip, error) {
	return store.mutateActivity(ctx, request, "replace")
}

func (store *SavedTripStore) DeleteActivity(ctx context.Context, request SavedActivityMutationRequest) (MutatedSavedTrip, error) {
	return store.mutateActivity(ctx, request, "delete")
}

func (store *SavedTripStore) mutateActivity(ctx context.Context, request SavedActivityMutationRequest, kind string) (MutatedSavedTrip, error) {
	if ctx == nil || !validCanonicalUUID(request.UserID) || !validCanonicalUUID(request.TripID) ||
		!validCanonicalUUID(request.IdempotencyKey) || (kind != "add" && !validCanonicalUUID(request.ActivityID)) ||
		(kind != "add" && kind != "replace" && kind != "delete") ||
		(kind != "delete" && request.Activity == nil) || (kind == "delete" && request.Activity != nil) {
		return MutatedSavedTrip{}, ErrSavedTripInput
	}
	method, operation, path, status := "POST", "add_activity", "/api/v1/trips/"+request.TripID+"/activities", 201
	if kind == "replace" {
		method, operation, path, status = "PATCH", "replace_activity", path+"/"+request.ActivityID, 200
	} else if kind == "delete" {
		method, operation, path, status = "DELETE", "delete_activity", path+"/"+request.ActivityID, 200
	}
	tx, recordID, replay, err := store.reserveTripMutation(ctx, request.UserID, request.TripID, request.IdempotencyKey,
		method, path, operation, request.RequestDigest)
	if err != nil || replay != nil {
		if replay == nil {
			return MutatedSavedTrip{}, err
		}
		var trip TripView
		if replay.Status != status || len(replay.Body) == 0 || json.Unmarshal(replay.Body, &trip) != nil {
			_ = tx.Rollback(ctx)
			return MutatedSavedTrip{}, errors.New("saved activity replay is invalid")
		}
		if err := tx.Commit(ctx); err != nil {
			return MutatedSavedTrip{}, fmt.Errorf("commit saved activity replay: %w", err)
		}
		return MutatedSavedTrip{Trip: trip, Duplicate: true}, nil
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := lockInactiveSavedTrip(ctx, tx, request.UserID, request.TripID, request.ExpectedRevision)
	if err != nil {
		return MutatedSavedTrip{}, err
	}
	var activityCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM saved_trip_activities WHERE saved_plan_id=$1`, current.PlanID).Scan(&activityCount); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("count saved activities: %w", err)
	}
	oldActivityID, oldOrdinal := request.ActivityID, 0
	if kind == "add" {
		if activityCount >= 64 || !validSavedActivityInput(*request.Activity, activityCount) {
			return MutatedSavedTrip{}, ErrSavedTripInput
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT ordinal FROM saved_trip_activities WHERE saved_plan_id=$1 AND activity_id=$2`, current.PlanID, request.ActivityID).Scan(&oldOrdinal); errors.Is(err, pgx.ErrNoRows) {
			return MutatedSavedTrip{}, ErrSavedActivityNotFound
		} else if err != nil {
			return MutatedSavedTrip{}, fmt.Errorf("load saved activity: %w", err)
		}
		if kind == "delete" && activityCount == 1 {
			return MutatedSavedTrip{}, ErrSavedTripInput
		}
		if kind == "replace" && !validSavedActivityInput(*request.Activity, activityCount-1) {
			return MutatedSavedTrip{}, ErrSavedTripInput
		}
	}

	newPlanID, err := newAuthUUID()
	if err != nil {
		return MutatedSavedTrip{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_plans (
			id,trip_id,owner_user_id,saved_plan_revision,authored_by_user_id,
			display_local_date,display_local_time,display_time_zone_name
		) VALUES ($1,$2,$3,$4,$3,$5,$6,$7)
	`, newPlanID, request.TripID, request.UserID, current.Revision+1,
		current.DisplayDate, current.DisplayTime, current.DisplayZone); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("insert activity saved plan: %w", err)
	}

	if err := copySavedActivitiesForMutation(ctx, tx, current.PlanID, newPlanID, kind, oldActivityID, oldOrdinal, request.Activity); err != nil {
		return MutatedSavedTrip{}, err
	}
	if kind != "delete" {
		activityID := oldActivityID
		if kind == "add" {
			activityID, err = newAuthUUID()
			if err != nil {
				return MutatedSavedTrip{}, err
			}
		}
		if err := insertSavedActivity(ctx, tx, newPlanID, request.TripID, request.UserID, activityID, *request.Activity); err != nil {
			return MutatedSavedTrip{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips SET saved_plan_id=$2,trip_revision=trip_revision+1,updated_at=clock_timestamp()
		WHERE id=$1
	`, request.TripID, newPlanID); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("select activity saved plan: %w", err)
	}
	trip, err := store.get(ctx, tx, request.UserID, request.TripID)
	if err != nil {
		return MutatedSavedTrip{}, err
	}
	body, err := json.Marshal(trip)
	if err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("encode activity mutation: %w", err)
	}
	etag := `"trip-revision-` + trip.TripRevision + `"`
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET trip_id=$2,state='completed',response_status=$3,
			response_content_type='application/json',response_body=$4,response_etag=$5,
			resource_id=$2,completed_at=clock_timestamp()
		WHERE id=$1 AND state='in_progress'
	`, recordID, request.TripID, status, body, etag); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("complete activity mutation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("commit activity mutation: %w", err)
	}
	return MutatedSavedTrip{Trip: trip}, nil
}

func copySavedActivitiesForMutation(ctx context.Context, tx pgx.Tx, oldPlanID, newPlanID, kind, oldActivityID string, oldOrdinal int, activity *SavedActivityInput) error {
	targetOrdinal := 0
	var excludedActivityID any
	if activity != nil {
		targetOrdinal = activity.Ordinal
	}
	if kind != "add" {
		excludedActivityID = oldActivityID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_activities (
			saved_plan_id,trip_id,owner_user_id,activity_id,place_id,ordinal,
			schedule_state,start_offset_ms,end_offset_ms,inbound_travel_mode,
			activity_class,priority_rank,utility_score,reservation_start_offset_ms,
			reservation_grace_seconds,mandatory_deadline_offset_ms,min_duration_seconds,
			preferred_duration_seconds,max_duration_seconds,mandatory,can_shorten,can_move,can_skip
		)
		SELECT $1,trip_id,owner_user_id,activity_id,place_id,
			CASE
				WHEN $3='add' AND ordinal >= $5 THEN ordinal+1
				WHEN $3='delete' AND ordinal > $4 THEN ordinal-1
				WHEN $3='replace' AND $5 < $4 AND ordinal >= $5 AND ordinal < $4 THEN ordinal+1
				WHEN $3='replace' AND $5 > $4 AND ordinal > $4 AND ordinal <= $5 THEN ordinal-1
				ELSE ordinal
			END,
			schedule_state,start_offset_ms,end_offset_ms,inbound_travel_mode,
			activity_class,priority_rank,utility_score,reservation_start_offset_ms,
			reservation_grace_seconds,mandatory_deadline_offset_ms,min_duration_seconds,
			preferred_duration_seconds,max_duration_seconds,mandatory,can_shorten,can_move,can_skip
		FROM saved_trip_activities
		WHERE saved_plan_id=$2 AND ($3='add' OR activity_id<>$6)
	`, newPlanID, oldPlanID, kind, oldOrdinal, targetOrdinal, excludedActivityID)
	if err != nil {
		return fmt.Errorf("copy activity mutation rows: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_activity_open_windows (saved_plan_id,activity_id,window_index,opens_offset_ms,closes_offset_ms)
		SELECT $1,activity_id,window_index,opens_offset_ms,closes_offset_ms
		FROM saved_activity_open_windows
		WHERE saved_plan_id=$2 AND ($3='add' OR activity_id<>$4)
	`, newPlanID, oldPlanID, kind, excludedActivityID); err != nil {
		return fmt.Errorf("copy activity mutation windows: %w", err)
	}
	return nil
}

func insertSavedActivity(ctx context.Context, tx pgx.Tx, planID, tripID, userID, activityID string, activity SavedActivityInput) error {
	var ownedPlaceID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM places WHERE id=$1 AND owner_user_id=$2`, activity.PlaceID, userID).Scan(&ownedPlaceID); errors.Is(err, pgx.ErrNoRows) {
		return ErrSavedTripInput
	} else if err != nil {
		return fmt.Errorf("validate activity place: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_activities (
			saved_plan_id,trip_id,owner_user_id,activity_id,place_id,ordinal,schedule_state,
			start_offset_ms,end_offset_ms,inbound_travel_mode,activity_class,priority_rank,
			utility_score,reservation_start_offset_ms,reservation_grace_seconds,
			mandatory_deadline_offset_ms,min_duration_seconds,preferred_duration_seconds,
			max_duration_seconds,mandatory,can_shorten,can_move,can_skip
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
	`, planID, tripID, userID, activityID, ownedPlaceID, activity.Ordinal, activity.Schedule.State,
		activity.Schedule.StartOffsetMS, activity.Schedule.EndOffsetMS, activity.InboundTravelMode,
		activity.ActivityClass, activity.PriorityRank, activity.UtilityScore,
		activity.Timing.ReservationStartOffsetMS, activity.Timing.ReservationGraceSeconds,
		activity.Timing.MandatoryDeadlineOffset, activity.Timing.MinDurationSeconds,
		activity.Timing.PreferredDurationSeconds, activity.Timing.MaxDurationSeconds,
		activity.Timing.Mandatory, activity.Timing.CanShorten, activity.Timing.CanMove, activity.Timing.CanSkip); err != nil {
		return fmt.Errorf("insert activity mutation row: %w", err)
	}
	for index, window := range activity.Timing.OpenWindows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO saved_activity_open_windows (saved_plan_id,activity_id,window_index,opens_offset_ms,closes_offset_ms)
			VALUES ($1,$2,$3,$4,$5)
		`, planID, activityID, index, window.OpensOffsetMS, window.ClosesOffsetMS); err != nil {
			return fmt.Errorf("insert activity mutation window: %w", err)
		}
	}
	return nil
}

func validSavedActivityInput(activity SavedActivityInput, maximumOrdinal int) bool {
	if activity.Ordinal < 0 || activity.Ordinal > maximumOrdinal || !validCanonicalUUID(activity.PlaceID) ||
		(activity.InboundTravelMode != "walking" && activity.InboundTravelMode != "driving") ||
		(activity.ActivityClass != "fixed" && activity.ActivityClass != "flexible") ||
		activity.PriorityRank < -2147483648 || activity.PriorityRank > 2147483647 ||
		activity.UtilityScore < -2147483648 || activity.UtilityScore > 2147483647 ||
		activity.Timing.CanShorten || activity.Timing.OpenWindows == nil || len(activity.Timing.OpenWindows) > 32 ||
		activity.Timing.ReservationGraceSeconds < 0 || activity.Timing.ReservationGraceSeconds > 4294967295 ||
		!validDurationRange(activity.Timing.MinDurationSeconds, activity.Timing.PreferredDurationSeconds, activity.Timing.MaxDurationSeconds) ||
		!validOptionalDayOffset(activity.Timing.ReservationStartOffsetMS) ||
		!validOptionalDayOffset(activity.Timing.MandatoryDeadlineOffset) || !validSavedSchedule(activity.Schedule) {
		return false
	}
	for _, window := range activity.Timing.OpenWindows {
		if window.OpensOffsetMS < 0 || window.OpensOffsetMS >= window.ClosesOffsetMS || window.ClosesOffsetMS > 86400000 {
			return false
		}
	}
	return true
}
