package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrExecutionTripConflict = errors.New("another trip is already executing for this user")
	ErrActivationUnscheduled = errors.New("saved trip contains an unscheduled activity")
	ErrActivationOutsideDay  = errors.New("saved trip does not fit the activation day")
	ErrSavedTripNotExecuting = errors.New("saved trip is not executing")
)

type ActivateSavedTripRequest struct {
	UserID            string
	TripID            string
	IdempotencyKey    string
	ExpectedRevision  uint64
	RequestDigest     [32]byte
	StartingLatitude  float64
	StartingLongitude float64
}

type ExecutionTransitionView struct {
	Trip      TripView               `json:"trip"`
	Operation ExecutionOperationView `json:"operation"`
}

type ActivatedSavedTrip struct {
	Transition ExecutionTransitionView
	Duplicate  bool
}

type CompleteActivationRequest struct {
	TripID       string
	OperationID  string
	HolderID     string
	RuntimeEpoch uint64
}

type PendingActivation struct {
	TripID      string
	OperationID string
}

type DeactivateSavedTripRequest struct {
	UserID           string
	TripID           string
	IdempotencyKey   string
	ExpectedRevision uint64
	RequestDigest    [32]byte
}

func (store *SavedTripStore) Deactivate(
	ctx context.Context,
	request DeactivateSavedTripRequest,
) (ActivatedSavedTrip, error) {
	if !validCanonicalUUID(request.UserID) || !validCanonicalUUID(request.TripID) ||
		!validCanonicalUUID(request.IdempotencyKey) || request.ExpectedRevision > math.MaxInt64 {
		return ActivatedSavedTrip{}, ErrSavedTripInput
	}
	path := "/api/v1/trips/" + request.TripID + "/deactivate"
	tx, recordID, replay, err := store.reserveTripMutation(ctx, request.UserID, request.TripID, request.IdempotencyKey,
		"POST", path, "deactivate_trip", request.RequestDigest)
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	if replay != nil {
		var transition ExecutionTransitionView
		if (replay.Status != 200 && replay.Status != 202) || len(replay.Body) == 0 || json.Unmarshal(replay.Body, &transition) != nil {
			_ = tx.Rollback(ctx)
			return ActivatedSavedTrip{}, errors.New("deactivation replay is invalid")
		}
		if err := tx.Commit(ctx); err != nil {
			return ActivatedSavedTrip{}, fmt.Errorf("commit deactivation replay: %w", err)
		}
		return ActivatedSavedTrip{Transition: transition, Duplicate: true}, nil
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	var state, planID string
	err = tx.QueryRow(ctx, `
		SELECT trip_revision, execution_state, current_plan_id::text
		FROM trips WHERE id = $1 AND owner_user_id = $2 FOR UPDATE
	`, request.TripID, request.UserID).Scan(&revision, &state, &planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivatedSavedTrip{}, ErrTripNotFound
	}
	if err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("lock deactivation trip: %w", err)
	}
	if request.ExpectedRevision != uint64(revision) {
		return ActivatedSavedTrip{}, ErrTripRevisionStale
	}
	if state != "active" && state != "activating" {
		return ActivatedSavedTrip{}, ErrSavedTripNotExecuting
	}
	operationID, err := newAuthUUID()
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET trip_id = $2, resource_id = $2
		WHERE id = $1 AND state = 'in_progress'
	`, recordID, request.TripID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("bind deactivation idempotency record: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trip_execution_operations (
			id, trip_id, owner_user_id, idempotency_record_id, kind, state,
			last_step, source_trip_revision, target_execution_plan_id,
			resulting_trip_revision
		) VALUES ($1,$2,$3,$4,'deactivate','pending','recorded',$5,NULL,$5)
	`, operationID, request.TripID, request.UserID, recordID, revision); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("record deactivation operation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE plan_proposals SET state = 'stale', decided_at = clock_timestamp()
		WHERE trip_id = $1 AND state = 'pending'
	`, request.TripID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("invalidate deactivation proposals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips SET execution_state = 'deactivating', transition_operation_id = $2,
			updated_at = clock_timestamp() WHERE id = $1
	`, request.TripID, operationID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("enter deactivating state: %w", err)
	}
	trip, err := store.get(ctx, tx, request.UserID, request.TripID)
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	transition := ExecutionTransitionView{Trip: trip, Operation: *trip.TransitionOperation}
	body, err := json.Marshal(transition)
	if err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("encode deactivation response: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET state = 'completed', response_status = 202,
			response_content_type = 'application/json', response_body = $2,
			response_etag = $3, completed_at = clock_timestamp()
		WHERE id = $1 AND state = 'in_progress'
	`, recordID, body, `"trip-revision-`+trip.TripRevision+`"`); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("complete deactivation idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("commit deactivation: %w", err)
	}
	return ActivatedSavedTrip{Transition: transition}, nil
}

type CompleteDeactivationRequest struct {
	TripID       string
	OperationID  string
	HolderID     string
	RuntimeEpoch uint64
}

func (store *SavedTripStore) CompleteDeactivation(ctx context.Context, request CompleteDeactivationRequest) (TripView, error) {
	if !validCanonicalUUID(request.TripID) || !validCanonicalUUID(request.OperationID) ||
		!validCanonicalUUID(request.HolderID) {
		return TripView{}, ErrSavedTripInput
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return TripView{}, fmt.Errorf("begin deactivation completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID, idempotencyRecordID string
	var state, operationState, operationKind string
	var revision int64
	if err := tx.QueryRow(ctx, `
		SELECT owner_user_id::text, trip_revision, execution_state
		FROM trips WHERE id = $1 FOR UPDATE
	`, request.TripID).Scan(&ownerID, &revision, &state); errors.Is(err, pgx.ErrNoRows) {
		return TripView{}, ErrTripNotFound
	} else if err != nil {
		return TripView{}, fmt.Errorf("lock deactivation completion trip: %w", err)
	}
	var leaseExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trip_runtime_leases WHERE trip_id = $1)`, request.TripID).Scan(&leaseExists); err != nil {
		return TripView{}, fmt.Errorf("verify released deactivation lease: %w", err)
	}
	if leaseExists {
		return TripView{}, ErrLeaseHeld
	}
	if err := tx.QueryRow(ctx, `
		SELECT idempotency_record_id::text, kind, state
		FROM trip_execution_operations WHERE id=$1 AND trip_id=$2 FOR UPDATE
	`, request.OperationID, request.TripID).Scan(&idempotencyRecordID, &operationKind, &operationState); err != nil {
		return TripView{}, fmt.Errorf("lock deactivation operation: %w", err)
	}
	if state != "deactivating" || operationKind != "deactivate" || operationState != "pending" {
		return TripView{}, errors.New("deactivation operation no longer matches trip")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM trip_activities WHERE trip_id=$1`, request.TripID); err != nil {
		return TripView{}, fmt.Errorf("reset execution activities: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips SET current_plan_id=NULL, active_execution_plan_id=NULL,
			activated_at=NULL, transition_operation_id=NULL, execution_state='inactive',
			updated_at=clock_timestamp() WHERE id=$1 AND execution_state='deactivating'
	`, request.TripID); err != nil {
		return TripView{}, fmt.Errorf("publish inactive trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trip_execution_operations SET state='succeeded', last_step='completed',
			updated_at=clock_timestamp(), completed_at=clock_timestamp()
		WHERE id=$1 AND state='pending'
	`, request.OperationID); err != nil {
		return TripView{}, fmt.Errorf("complete deactivation operation: %w", err)
	}
	trip, err := store.get(ctx, tx, ownerID, request.TripID)
	if err != nil {
		return TripView{}, err
	}
	body, err := json.Marshal(ExecutionTransitionView{Trip: trip, Operation: ExecutionOperationView{
		OperationID: request.OperationID, Kind: "deactivate", State: "succeeded", LastStep: "completed",
	}})
	if err != nil {
		return TripView{}, fmt.Errorf("encode completed deactivation: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET response_status=200, response_body=$2,
			response_etag=$3 WHERE id=$4 AND user_id=$1 AND state='completed' AND response_status=202
	`, ownerID, body, `"trip-revision-`+trip.TripRevision+`"`, idempotencyRecordID)
	if err != nil {
		return TripView{}, fmt.Errorf("publish completed deactivation response: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return TripView{}, errors.New("deactivation idempotency record is not pending completion")
	}
	if err := tx.Commit(ctx); err != nil {
		return TripView{}, fmt.Errorf("commit deactivation completion: %w", err)
	}
	return trip, nil
}

func (store *SavedTripStore) PendingActivations(ctx context.Context) ([]PendingActivation, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id::text, transition_operation_id::text
		FROM trips
		WHERE execution_state = 'activating' AND transition_operation_id IS NOT NULL
		ORDER BY updated_at, id
		LIMIT 64
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending activations: %w", err)
	}
	defer rows.Close()
	result := make([]PendingActivation, 0)
	for rows.Next() {
		var activation PendingActivation
		if err := rows.Scan(&activation.TripID, &activation.OperationID); err != nil {
			return nil, fmt.Errorf("scan pending activation: %w", err)
		}
		result = append(result, activation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending activations: %w", err)
	}
	return result, nil
}

func (store *SavedTripStore) PendingDeactivations(ctx context.Context) ([]PendingActivation, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id::text, transition_operation_id::text
		FROM trips
		WHERE execution_state = 'deactivating' AND transition_operation_id IS NOT NULL
		ORDER BY updated_at, id
		LIMIT 64
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending deactivations: %w", err)
	}
	defer rows.Close()
	result := make([]PendingActivation, 0)
	for rows.Next() {
		var transition PendingActivation
		if err := rows.Scan(&transition.TripID, &transition.OperationID); err != nil {
			return nil, fmt.Errorf("scan pending deactivation: %w", err)
		}
		result = append(result, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending deactivations: %w", err)
	}
	return result, nil
}

type activationActivity struct {
	ID                       string
	Ordinal                  int
	PlaceID                  string
	DisplayName              string
	Latitude                 float64
	Longitude                float64
	TimeZoneName             string
	InboundTravelMode        string
	ActivityClass            string
	PriorityRank             int
	UtilityScore             int
	StartOffset              int64
	EndOffset                int64
	ReservationStartOffset   *int64
	ReservationGraceSeconds  int64
	DeadlineOffset           *int64
	MinDurationSeconds       int
	PreferredDurationSeconds int
	MaxDurationSeconds       int
	Mandatory                bool
	CanShorten               bool
	CanMove                  bool
	CanSkip                  bool
	Windows                  []RelativeWindowInput
}

func (store *SavedTripStore) Activate(
	ctx context.Context,
	request ActivateSavedTripRequest,
) (ActivatedSavedTrip, error) {
	if err := validateActivateSavedTrip(request); err != nil {
		return ActivatedSavedTrip{}, err
	}
	path := "/api/v1/trips/" + request.TripID + "/activate"
	tx, recordID, replay, err := store.reserveTripMutation(
		ctx, request.UserID, request.TripID, request.IdempotencyKey,
		"POST", path, "activate_trip", request.RequestDigest,
	)
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	if replay != nil {
		var transition ExecutionTransitionView
		if replay.Status != 200 && replay.Status != 202 ||
			len(replay.Body) == 0 || json.Unmarshal(replay.Body, &transition) != nil {
			_ = tx.Rollback(ctx)
			return ActivatedSavedTrip{}, errors.New("activation replay is invalid")
		}
		if err := tx.Commit(ctx); err != nil {
			return ActivatedSavedTrip{}, fmt.Errorf("commit activation replay: %w", err)
		}
		return ActivatedSavedTrip{Transition: transition, Duplicate: true}, nil
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockActivationUser(ctx, tx, request.UserID); err != nil {
		return ActivatedSavedTrip{}, err
	}
	current, err := lockActivationTrip(ctx, tx, request.UserID, request.TripID, request.ExpectedRevision)
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	var executingTrip string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM trips
		WHERE owner_user_id = $1 AND execution_state IN ('activating', 'active', 'deactivating')
		  AND id <> $2
		LIMIT 1
	`, request.UserID, request.TripID).Scan(&executingTrip)
	if err == nil {
		return ActivatedSavedTrip{}, ErrExecutionTripConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ActivatedSavedTrip{}, fmt.Errorf("check executing trip: %w", err)
	}

	activities, err := loadActivationActivities(ctx, tx, current.PlanID, request.TripID, request.UserID)
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	if len(activities) == 0 {
		return ActivatedSavedTrip{}, ErrSavedTripInput
	}
	location, err := time.LoadLocation(current.TimeZoneName)
	if err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("load trip timezone: %w", err)
	}
	var activatedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&activatedAt); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("choose activation time: %w", err)
	}
	activatedAt = activatedAt.UTC()
	horizonStart, horizonEnd := activationDay(activatedAt, location)
	segments := make([]CanonicalPlanSegmentDraft, 0, len(activities))
	for _, activity := range activities {
		if activity.StartOffset < 0 || activity.EndOffset <= activity.StartOffset {
			return ActivatedSavedTrip{}, ErrActivationUnscheduled
		}
		start := activatedAt.Add(time.Duration(activity.StartOffset) * time.Millisecond)
		end := activatedAt.Add(time.Duration(activity.EndOffset) * time.Millisecond)
		if !insideActivationDay(start, horizonStart, horizonEnd) ||
			!insideActivationDay(end, horizonStart, horizonEnd) {
			return ActivatedSavedTrip{}, ErrActivationOutsideDay
		}
		if !activationOptionalTimeInsideDay(activatedAt, activity.ReservationStartOffset, horizonStart, horizonEnd) ||
			!activationOptionalTimeInsideDay(activatedAt, activity.DeadlineOffset, horizonStart, horizonEnd) {
			return ActivatedSavedTrip{}, ErrActivationOutsideDay
		}
		for _, window := range activity.Windows {
			windowStart := activatedAt.Add(time.Duration(window.OpensOffsetMS) * time.Millisecond)
			windowEnd := activatedAt.Add(time.Duration(window.ClosesOffsetMS) * time.Millisecond)
			if !insideActivationDay(windowStart, horizonStart, horizonEnd) ||
				!insideActivationDay(windowEnd, horizonStart, horizonEnd) {
				return ActivatedSavedTrip{}, ErrActivationOutsideDay
			}
		}
		startMS, endMS := start.UnixMilli(), end.UnixMilli()
		segments = append(segments, CanonicalPlanSegmentDraft{
			ActivityID: activity.ID, Scheduled: true, Start: &startMS, End: &endMS,
		})
	}

	planID, err := newAuthUUID()
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	operationID, err := newAuthUUID()
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	var planRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(plan_revision), 0) + 1
		FROM itinerary_plans WHERE trip_id = $1
	`, request.TripID).Scan(&planRevision); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("allocate execution plan revision: %w", err)
	}
	if planRevision <= 0 {
		return ActivatedSavedTrip{}, errors.New("execution plan revision overflow")
	}
	payload := userCurrentPlanPayload(planID, uint64(planRevision), activatedAt.UnixMilli(), segments)
	checksum := activationChecksum(payload)
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			source_proposal_id, schema_version, payload, payload_size_bytes,
			checksum_sha256, created_at
		) VALUES ($1, $2, $3, 'user_authored', $4, NULL, 1, $5, $6, $7, $8)
	`, planID, request.TripID, planRevision, request.UserID, payload,
		len(payload), checksum[:], activatedAt); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("insert execution plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM trip_travel_delays WHERE trip_id = $1
	`, request.TripID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("clear prior travel delays: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM trip_activities WHERE trip_id = $1
	`, request.TripID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("clear prior execution activities: %w", err)
	}
	for _, activity := range activities {
		var reservationStart any
		if activity.ReservationStartOffset != nil {
			reservationStart = activatedAt.Add(time.Duration(*activity.ReservationStartOffset) * time.Millisecond)
		}
		var deadline any
		if activity.DeadlineOffset != nil {
			deadline = activatedAt.Add(time.Duration(*activity.DeadlineOffset) * time.Millisecond)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO trip_activities (
				id, trip_id, ordinal, place_id, display_name, latitude, longitude,
				time_zone_name, inbound_travel_mode, activity_class, activity_state,
				activity_delay_seconds, priority_rank, utility_score,
				reservation_start, reservation_grace_seconds, mandatory_deadline,
				min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
				mandatory, can_shorten, can_move, can_skip
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'planned',0,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		`, activity.ID, request.TripID, activity.Ordinal, activity.PlaceID,
			activity.DisplayName, activity.Latitude, activity.Longitude,
			activity.TimeZoneName, activity.InboundTravelMode, activity.ActivityClass,
			activity.PriorityRank, activity.UtilityScore, reservationStart,
			activity.ReservationGraceSeconds, deadline, activity.MinDurationSeconds,
			activity.PreferredDurationSeconds, activity.MaxDurationSeconds,
			activity.Mandatory, activity.CanShorten, activity.CanMove,
			activity.CanSkip); err != nil {
			return ActivatedSavedTrip{}, fmt.Errorf("insert execution activity: %w", err)
		}
		for index, window := range activity.Windows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO activity_open_windows (
					trip_id, activity_id, window_index, opens_at, closes_at
				) VALUES ($1,$2,$3,$4,$5)
			`, request.TripID, activity.ID, index,
				activatedAt.Add(time.Duration(window.OpensOffsetMS)*time.Millisecond),
				activatedAt.Add(time.Duration(window.ClosesOffsetMS)*time.Millisecond)); err != nil {
				return ActivatedSavedTrip{}, fmt.Errorf("insert execution window: %w", err)
			}
		}
	}
	var nextSequence int64
	if err := tx.QueryRow(ctx, `
		SELECT next_mutation_sequence FROM trips WHERE id = $1 FOR UPDATE
	`, request.TripID).Scan(&nextSequence); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("read activation sequence: %w", err)
	}
	if nextSequence <= 0 || nextSequence == math.MaxInt64 {
		return ActivatedSavedTrip{}, errors.New("activation mutation sequence overflow")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET trip_id = $2, resource_id = $2
		WHERE id = $1 AND state = 'in_progress'
	`, recordID, request.TripID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("bind activation idempotency record: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trip_execution_operations (
			id, trip_id, owner_user_id, idempotency_record_id, kind, state,
			last_step, source_trip_revision, target_execution_plan_id,
			resulting_trip_revision
		) VALUES ($1,$2,$3,$4,'activate','pending','recorded',$5,$6,$5)
	`, operationID, request.TripID, request.UserID, recordID,
		current.Revision, planID); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("record activation operation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips SET current_plan_id = $2, active_execution_plan_id = $2,
			activated_at = $3, execution_state = 'activating',
			transition_operation_id = $4, next_mutation_sequence = $5,
			finalized_mutation_sequence = $6, updated_at = $3
		WHERE id = $1 AND execution_state = 'inactive'
	`, request.TripID, planID, activatedAt, operationID, nextSequence+1, nextSequence); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("enter activating state: %w", err)
	}
	trip, err := store.get(ctx, tx, request.UserID, request.TripID)
	if err != nil {
		return ActivatedSavedTrip{}, err
	}
	operation := *trip.TransitionOperation
	transition := ExecutionTransitionView{Trip: trip, Operation: operation}
	body, err := json.Marshal(transition)
	if err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("encode activation response: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET state = 'completed', response_status = 202,
			response_content_type = 'application/json', response_body = $2,
			response_etag = $3, completed_at = clock_timestamp()
		WHERE id = $1 AND state = 'in_progress'
	`, recordID, body, `"trip-revision-`+trip.TripRevision+`"`); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("complete activation idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ActivatedSavedTrip{}, fmt.Errorf("commit activation: %w", err)
	}
	return ActivatedSavedTrip{Transition: transition}, nil
}

func (store *SavedTripStore) CompleteActivation(
	ctx context.Context,
	request CompleteActivationRequest,
) (TripView, error) {
	if !validCanonicalUUID(request.TripID) || !validCanonicalUUID(request.OperationID) ||
		!validCanonicalUUID(request.HolderID) || request.RuntimeEpoch == 0 ||
		request.RuntimeEpoch > math.MaxInt64 {
		return TripView{}, ErrSavedTripInput
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return TripView{}, fmt.Errorf("begin activation completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID, planID, operationState, operationKind string
	var idempotencyRecordID string
	var revision int64
	if err := tx.QueryRow(ctx, `
		SELECT owner_user_id::text, current_plan_id::text, trip_revision
		FROM trips WHERE id = $1 FOR UPDATE
	`, request.TripID).Scan(&ownerID, &planID, &revision); errors.Is(err, pgx.ErrNoRows) {
		return TripView{}, ErrTripNotFound
	} else if err != nil {
		return TripView{}, fmt.Errorf("lock activation completion trip: %w", err)
	}
	var leaseEpoch int64
	var leaseExpiresAt, databaseNow time.Time
	if err := tx.QueryRow(ctx, `
		SELECT runtime_epoch, lease_expires_at
		FROM trip_runtime_leases
		WHERE trip_id = $1 AND holder_id = $2
		FOR UPDATE
	`, request.TripID, request.HolderID).Scan(&leaseEpoch, &leaseExpiresAt); err != nil {
		return TripView{}, fmt.Errorf("verify activation lease: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return TripView{}, fmt.Errorf("read activation completion time: %w", err)
	}
	if leaseEpoch != int64(request.RuntimeEpoch) || !leaseExpiresAt.After(databaseNow) {
		return TripView{}, ErrLeaseLost
	}
	var sourceRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT idempotency_record_id::text, kind, state, source_trip_revision
		FROM trip_execution_operations
		WHERE id = $1 AND trip_id = $2
		FOR UPDATE
	`, request.OperationID, request.TripID).Scan(&idempotencyRecordID, &operationKind, &operationState, &sourceRevision); errors.Is(err, pgx.ErrNoRows) {
		return TripView{}, errors.New("activation operation not found")
	} else if err != nil {
		return TripView{}, fmt.Errorf("lock activation operation: %w", err)
	}
	if operationKind != "activate" || operationState != "pending" ||
		sourceRevision != revision || planID == "" {
		return TripView{}, errors.New("activation operation no longer matches trip")
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT execution_state FROM trips WHERE id = $1`, request.TripID).Scan(&state); err != nil {
		return TripView{}, fmt.Errorf("read activation state: %w", err)
	}
	if state != "activating" {
		return TripView{}, errors.New("trip is not activating")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trips SET execution_state = 'active', transition_operation_id = NULL,
			updated_at = clock_timestamp() WHERE id = $1 AND execution_state = 'activating'
	`, request.TripID); err != nil {
		return TripView{}, fmt.Errorf("publish active trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trip_execution_operations
		SET state = 'succeeded', last_step = 'completed',
			updated_at = clock_timestamp(), completed_at = clock_timestamp()
		WHERE id = $1 AND trip_id = $2 AND state = 'pending'
	`, request.OperationID, request.TripID); err != nil {
		return TripView{}, fmt.Errorf("complete activation operation: %w", err)
	}
	trip, err := store.get(ctx, tx, ownerID, request.TripID)
	if err != nil {
		return TripView{}, err
	}
	operation := ExecutionOperationView{
		OperationID: request.OperationID, Kind: "activate", State: "succeeded",
		LastStep: "completed",
	}
	body, err := json.Marshal(ExecutionTransitionView{Trip: trip, Operation: operation})
	if err != nil {
		return TripView{}, fmt.Errorf("encode completed activation: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records
		SET response_status = 200, response_body = $2, response_etag = $3,
			completed_at = clock_timestamp()
		WHERE user_id = $1 AND id = $4 AND operation_kind = 'activate_trip'
		  AND state = 'completed' AND response_status = 202
	`, ownerID, body, `"trip-revision-`+trip.TripRevision+`"`, idempotencyRecordID)
	if err != nil {
		return TripView{}, fmt.Errorf("publish completed activation response: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return TripView{}, errors.New("activation idempotency record is not pending completion")
	}
	if err := tx.Commit(ctx); err != nil {
		return TripView{}, fmt.Errorf("commit activation completion: %w", err)
	}
	return trip, nil
}

func validateActivateSavedTrip(request ActivateSavedTripRequest) error {
	if !validCanonicalUUID(request.UserID) || !validCanonicalUUID(request.TripID) ||
		!validCanonicalUUID(request.IdempotencyKey) || request.ExpectedRevision == 0 ||
		request.ExpectedRevision > math.MaxInt64 || math.IsNaN(request.StartingLatitude) ||
		math.IsInf(request.StartingLatitude, 0) || math.IsNaN(request.StartingLongitude) ||
		math.IsInf(request.StartingLongitude, 0) || request.StartingLatitude < -90 ||
		request.StartingLatitude > 90 || request.StartingLongitude < -180 ||
		request.StartingLongitude > 180 {
		return ErrSavedTripInput
	}
	return nil
}

type lockedActivationTrip struct {
	Revision     int64
	PlanID       string
	TimeZoneName string
}

func lockActivationUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFound
	}
	if err != nil {
		return fmt.Errorf("lock activation user: %w", err)
	}
	return nil
}

func lockActivationTrip(ctx context.Context, tx pgx.Tx, userID, tripID string, expected uint64) (lockedActivationTrip, error) {
	var result lockedActivationTrip
	var state string
	err := tx.QueryRow(ctx, `
		SELECT trip_revision, saved_plan_id::text, default_time_zone_name, execution_state
		FROM trips WHERE id = $1 AND owner_user_id = $2 FOR UPDATE
	`, tripID, userID).Scan(&result.Revision, &result.PlanID, &result.TimeZoneName, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedActivationTrip{}, ErrTripNotFound
	}
	if err != nil {
		return lockedActivationTrip{}, fmt.Errorf("lock activation trip: %w", err)
	}
	if state != "inactive" {
		return lockedActivationTrip{}, ErrSavedTripNotInactive
	}
	if expected > math.MaxInt64 || result.Revision != int64(expected) {
		return lockedActivationTrip{}, ErrTripRevisionStale
	}
	if result.PlanID == "" || !validCanonicalUUID(result.PlanID) {
		return lockedActivationTrip{}, ErrSavedTripInput
	}
	return result, nil
}

func loadActivationActivities(ctx context.Context, tx pgx.Tx, savedPlanID, tripID, userID string) ([]activationActivity, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.activity_id::text, a.ordinal, a.place_id::text,
			COALESCE(NULLIF(p.formatted_address, ''),
				format('%.6f, %.6f', p.latitude, p.longitude)),
			p.latitude, p.longitude, p.time_zone_name, a.inbound_travel_mode,
			a.activity_class, a.priority_rank, a.utility_score, a.schedule_state,
			a.start_offset_ms, a.end_offset_ms,
			a.reservation_start_offset_ms, a.reservation_grace_seconds,
			a.mandatory_deadline_offset_ms, a.min_duration_seconds,
			a.preferred_duration_seconds, a.max_duration_seconds, a.mandatory,
			a.can_shorten, a.can_move, a.can_skip
		FROM saved_trip_activities a
		JOIN places p ON p.id = a.place_id AND p.owner_user_id = a.owner_user_id
		WHERE a.saved_plan_id = $1 AND a.trip_id = $2 AND a.owner_user_id = $3
		ORDER BY a.ordinal
	`, savedPlanID, tripID, userID)
	if err != nil {
		return nil, fmt.Errorf("load activation activities: %w", err)
	}
	defer rows.Close()
	activities := make([]activationActivity, 0)
	for rows.Next() {
		var activity activationActivity
		var state string
		var startOffset, endOffset *int64
		if err := rows.Scan(&activity.ID, &activity.Ordinal, &activity.PlaceID,
			&activity.DisplayName, &activity.Latitude, &activity.Longitude,
			&activity.TimeZoneName, &activity.InboundTravelMode, &activity.ActivityClass,
			&activity.PriorityRank, &activity.UtilityScore, &state, &startOffset, &endOffset, &activity.ReservationStartOffset,
			&activity.ReservationGraceSeconds, &activity.DeadlineOffset,
			&activity.MinDurationSeconds, &activity.PreferredDurationSeconds,
			&activity.MaxDurationSeconds, &activity.Mandatory, &activity.CanShorten,
			&activity.CanMove, &activity.CanSkip); err != nil {
			return nil, fmt.Errorf("scan activation activity: %w", err)
		}
		if state != "scheduled" || startOffset == nil || endOffset == nil {
			return nil, ErrActivationUnscheduled
		}
		activity.StartOffset, activity.EndOffset = *startOffset, *endOffset
		activities = append(activities, activity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activation activities: %w", err)
	}
	rows.Close()
	for index := range activities {
		activity := &activities[index]
		windowRows, err := tx.Query(ctx, `
			SELECT opens_offset_ms, closes_offset_ms
			FROM saved_activity_open_windows
			WHERE saved_plan_id = $1 AND activity_id = $2
			ORDER BY window_index
		`, savedPlanID, activity.ID)
		if err != nil {
			return nil, fmt.Errorf("load activation windows: %w", err)
		}
		for windowRows.Next() {
			var window RelativeWindowInput
			if err := windowRows.Scan(&window.OpensOffsetMS, &window.ClosesOffsetMS); err != nil {
				windowRows.Close()
				return nil, fmt.Errorf("scan activation window: %w", err)
			}
			activity.Windows = append(activity.Windows, window)
		}
		if err := windowRows.Err(); err != nil {
			windowRows.Close()
			return nil, fmt.Errorf("iterate activation windows: %w", err)
		}
		windowRows.Close()
	}
	return activities, nil
}

func activationDay(instant time.Time, location *time.Location) (time.Time, time.Time) {
	local := instant.In(location)
	startLocal := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	endLocal := startLocal.AddDate(0, 0, 1)
	return startLocal.UTC(), endLocal.UTC()
}

func insideActivationDay(value, start, end time.Time) bool {
	return !value.Before(start) && value.Before(end)
}

func activationOptionalTimeInsideDay(activatedAt time.Time, offset *int64, start, end time.Time) bool {
	if offset == nil {
		return true
	}
	return insideActivationDay(activatedAt.Add(time.Duration(*offset)*time.Millisecond), start, end)
}

func activationChecksum(value []byte) [32]byte {
	return sha256.Sum256(value)
}
