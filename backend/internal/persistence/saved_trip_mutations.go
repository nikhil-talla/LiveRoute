package persistence

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrSavedTripNotInactive = errors.New("saved trip is not inactive")

type UpdateSavedTripRequest struct {
	TripName              *string               `json:"trip_name,omitempty"`
	DefaultTimeZoneName   *string               `json:"default_time_zone_name,omitempty"`
	DisplaySchedule       *DisplayScheduleInput `json:"display_schedule,omitempty"`
	RemoveDisplaySchedule *bool                 `json:"remove_display_schedule,omitempty"`
	UserID                string                `json:"-"`
	TripID                string                `json:"-"`
	IdempotencyKey        string                `json:"-"`
	ExpectedRevision      uint64                `json:"-"`
	RequestDigest         [32]byte              `json:"-"`
}

type DeleteSavedTripRequest struct {
	UserID           string
	TripID           string
	IdempotencyKey   string
	ExpectedRevision uint64
	RequestDigest    [32]byte
}

type MutatedSavedTrip struct {
	Trip      TripView
	Duplicate bool
}

func (store *SavedTripStore) Update(ctx context.Context, request UpdateSavedTripRequest) (MutatedSavedTrip, error) {
	if err := validateUpdateSavedTrip(request); err != nil {
		return MutatedSavedTrip{}, err
	}
	path := "/api/v1/trips/" + request.TripID
	tx, recordID, replay, err := store.reserveTripMutation(ctx, request.UserID, request.TripID, request.IdempotencyKey,
		"PATCH", path, "update_trip", request.RequestDigest)
	if err != nil || replay != nil {
		if replay == nil {
			return MutatedSavedTrip{}, err
		}
		var trip TripView
		if replay.Status != 200 || len(replay.Body) == 0 || json.Unmarshal(replay.Body, &trip) != nil {
			_ = tx.Rollback(ctx)
			return MutatedSavedTrip{}, errors.New("saved-trip update replay is invalid")
		}
		if err := tx.Commit(ctx); err != nil {
			return MutatedSavedTrip{}, fmt.Errorf("commit saved-trip update replay: %w", err)
		}
		return MutatedSavedTrip{Trip: trip, Duplicate: true}, nil
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := lockInactiveSavedTrip(ctx, tx, request.UserID, request.TripID, request.ExpectedRevision)
	if err != nil {
		return MutatedSavedTrip{}, err
	}
	planID, err := newAuthUUID()
	if err != nil {
		return MutatedSavedTrip{}, err
	}
	displayDate, displayTime, displayZone := current.DisplayDate, current.DisplayTime, current.DisplayZone
	if request.RemoveDisplaySchedule != nil {
		displayDate, displayTime, displayZone = nil, nil, nil
	} else if request.DisplaySchedule != nil {
		displayDate = &request.DisplaySchedule.LocalDate
		displayTime = &request.DisplaySchedule.LocalTime
		displayZone = &request.DisplaySchedule.TimeZoneName
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_plans (
			id, trip_id, owner_user_id, saved_plan_revision, authored_by_user_id,
			display_local_date, display_local_time, display_time_zone_name
		) VALUES ($1,$2,$3,$4,$3,$5,$6,$7)
	`, planID, request.TripID, request.UserID, current.Revision+1, displayDate, displayTime, displayZone); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("insert updated saved plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_activities (
			saved_plan_id, trip_id, owner_user_id, activity_id, place_id, ordinal,
			schedule_state, start_offset_ms, end_offset_ms, inbound_travel_mode,
			activity_class, priority_rank, utility_score, reservation_start_offset_ms,
			reservation_grace_seconds, mandatory_deadline_offset_ms,
			min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
			mandatory, can_shorten, can_move, can_skip
		)
		SELECT $1, trip_id, owner_user_id, activity_id, place_id, ordinal,
		       schedule_state, start_offset_ms, end_offset_ms, inbound_travel_mode,
		       activity_class, priority_rank, utility_score, reservation_start_offset_ms,
		       reservation_grace_seconds, mandatory_deadline_offset_ms,
		       min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
		       mandatory, can_shorten, can_move, can_skip
		FROM saved_trip_activities WHERE saved_plan_id=$2
	`, planID, current.PlanID); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("copy updated saved activities: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_activity_open_windows (
			saved_plan_id, activity_id, window_index, opens_offset_ms, closes_offset_ms
		)
		SELECT $1, activity_id, window_index, opens_offset_ms, closes_offset_ms
		FROM saved_activity_open_windows WHERE saved_plan_id=$2
	`, planID, current.PlanID); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("copy updated saved windows: %w", err)
	}
	name, zone := current.TripName, current.TimeZoneName
	if request.TripName != nil {
		name = *request.TripName
	}
	if request.DefaultTimeZoneName != nil {
		zone = *request.DefaultTimeZoneName
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips SET trip_name=$2, default_time_zone_name=$3,
			saved_plan_id=$4, trip_revision=trip_revision+1, updated_at=clock_timestamp()
		WHERE id=$1
	`, request.TripID, name, zone, planID); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("select updated saved plan: %w", err)
	}
	trip, err := store.get(ctx, tx, request.UserID, request.TripID)
	if err != nil {
		return MutatedSavedTrip{}, err
	}
	body, err := json.Marshal(trip)
	if err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("encode updated trip: %w", err)
	}
	etag := `"trip-revision-` + trip.TripRevision + `"`
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET trip_id=$2, state='completed',
			response_status=200, response_content_type='application/json',
			response_body=$3, response_etag=$4, resource_id=$2,
			completed_at=clock_timestamp()
		WHERE id=$1 AND state='in_progress'
	`, recordID, request.TripID, body, etag); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("complete saved-trip update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MutatedSavedTrip{}, fmt.Errorf("commit saved-trip update: %w", err)
	}
	return MutatedSavedTrip{Trip: trip}, nil
}

func (store *SavedTripStore) Delete(ctx context.Context, request DeleteSavedTripRequest) (bool, error) {
	if ctx == nil || !validCanonicalUUID(request.UserID) || !validCanonicalUUID(request.TripID) ||
		!validCanonicalUUID(request.IdempotencyKey) {
		return false, ErrSavedTripInput
	}
	path := "/api/v1/trips/" + request.TripID
	tx, recordID, replay, err := store.reserveTripMutation(ctx, request.UserID, request.TripID, request.IdempotencyKey,
		"DELETE", path, "delete_trip", request.RequestDigest)
	if err != nil || replay != nil {
		if replay == nil {
			return false, err
		}
		if replay.Status != 204 {
			_ = tx.Rollback(ctx)
			return false, errors.New("saved-trip delete replay is invalid")
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit saved-trip delete replay: %w", err)
		}
		return true, nil
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockInactiveSavedTrip(ctx, tx, request.UserID, request.TripID, request.ExpectedRevision); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET trip_id=$2, state='completed',
			response_status=204, response_content_type=NULL, response_body=NULL,
			response_etag=NULL, resource_id=$2, completed_at=clock_timestamp()
		WHERE id=$1 AND state='in_progress'
	`, recordID, request.TripID); err != nil {
		return false, fmt.Errorf("complete saved-trip delete: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM trips WHERE id=$1 AND owner_user_id=$2`, request.TripID, request.UserID); err != nil {
		return false, fmt.Errorf("delete saved trip: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit saved-trip delete: %w", err)
	}
	return false, nil
}

type tripMutationReplay struct {
	Status int
	Body   []byte
	ETag   string
}

func (store *SavedTripStore) reserveTripMutation(ctx context.Context, userID, tripID, key, method, path, operation string, digest [32]byte) (pgx.Tx, string, *tripMutationReplay, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, "", nil, fmt.Errorf("begin saved-trip mutation: %w", err)
	}
	recordID, err := newAuthUUID()
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, "", nil, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO http_idempotency_records (
			id,user_id,idempotency_key,http_method,normalized_path,operation_kind,
			request_digest_algorithm,request_digest,state,retain_until
		) VALUES ($1,$2,$3,$4,$5,$6,'rfc8785-sha256-v1',$7,'in_progress',
		          clock_timestamp()+$8::interval)
		ON CONFLICT (user_id,http_method,normalized_path,idempotency_key) DO NOTHING
	`, recordID, userID, key, method, path, operation, digest[:], httpIdempotencyRetention.String())
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, "", nil, fmt.Errorf("reserve saved-trip mutation: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return tx, recordID, nil, nil
	}
	var storedDigest, body []byte
	var state string
	var status *int
	var etag *string
	err = tx.QueryRow(ctx, `
		SELECT request_digest,state,response_status,response_body,response_etag
		FROM http_idempotency_records
		WHERE user_id=$1 AND http_method=$2 AND normalized_path=$3 AND idempotency_key=$4
		FOR UPDATE
	`, userID, method, path, key).Scan(&storedDigest, &state, &status, &body, &etag)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, "", nil, fmt.Errorf("load saved-trip mutation replay: %w", err)
	}
	if len(storedDigest) != len(digest) || subtle.ConstantTimeCompare(storedDigest, digest[:]) != 1 {
		_ = tx.Rollback(ctx)
		return nil, "", nil, ErrHTTPIdempotencyReused
	}
	if state != "completed" || status == nil {
		_ = tx.Rollback(ctx)
		return nil, "", nil, ErrHTTPMutationPending
	}
	replay := &tripMutationReplay{Status: *status, Body: body}
	if etag != nil {
		replay.ETag = *etag
	}
	return tx, recordID, replay, nil
}

type lockedSavedTrip struct {
	Revision     int64
	PlanID       string
	TripName     string
	TimeZoneName string
	DisplayDate  *string
	DisplayTime  *string
	DisplayZone  *string
}

func lockInactiveSavedTrip(ctx context.Context, tx pgx.Tx, userID, tripID string, expected uint64) (lockedSavedTrip, error) {
	var result lockedSavedTrip
	var state string
	err := tx.QueryRow(ctx, `
		SELECT t.trip_revision,t.execution_state,t.saved_plan_id::text,
		       t.trip_name,t.default_time_zone_name,p.display_local_date::text,
		       to_char(p.display_local_time,'HH24:MI:SS'),p.display_time_zone_name
		FROM trips t JOIN saved_trip_plans p ON p.id=t.saved_plan_id
		WHERE t.id=$1 AND t.owner_user_id=$2 FOR UPDATE OF t
	`, tripID, userID).Scan(&result.Revision, &state, &result.PlanID, &result.TripName,
		&result.TimeZoneName, &result.DisplayDate, &result.DisplayTime, &result.DisplayZone)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedSavedTrip{}, ErrTripNotFound
	}
	if err != nil {
		return lockedSavedTrip{}, fmt.Errorf("lock saved trip: %w", err)
	}
	if state != "inactive" {
		return lockedSavedTrip{}, ErrSavedTripNotInactive
	}
	if expected > math.MaxInt64 || result.Revision != int64(expected) {
		return lockedSavedTrip{}, ErrTripRevisionStale
	}
	return result, nil
}

func validateUpdateSavedTrip(request UpdateSavedTripRequest) error {
	if !validCanonicalUUID(request.UserID) || !validCanonicalUUID(request.TripID) ||
		!validCanonicalUUID(request.IdempotencyKey) ||
		(request.TripName == nil && request.DefaultTimeZoneName == nil && request.DisplaySchedule == nil && request.RemoveDisplaySchedule == nil) ||
		(request.DisplaySchedule != nil && request.RemoveDisplaySchedule != nil) {
		return ErrSavedTripInput
	}
	if request.TripName != nil && (*request.TripName != strings.TrimSpace(*request.TripName) || len([]byte(*request.TripName)) == 0 || len([]byte(*request.TripName)) > 120) {
		return ErrSavedTripInput
	}
	if request.DefaultTimeZoneName != nil && !validTimeZoneName(*request.DefaultTimeZoneName) {
		return ErrSavedTripInput
	}
	if request.RemoveDisplaySchedule != nil && !*request.RemoveDisplaySchedule {
		return ErrSavedTripInput
	}
	if request.DisplaySchedule != nil {
		if _, err := time.Parse("2006-01-02", request.DisplaySchedule.LocalDate); err != nil {
			return ErrSavedTripInput
		}
		if _, err := time.Parse("15:04:05", request.DisplaySchedule.LocalTime); err != nil || !validTimeZoneName(request.DisplaySchedule.TimeZoneName) {
			return ErrSavedTripInput
		}
	}
	return nil
}
