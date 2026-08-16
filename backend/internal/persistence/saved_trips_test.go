package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCreateSavedTripRequestRequiresNestedContractFields(t *testing.T) {
	valid := `{
		"trip_name":"Trip","default_time_zone_name":"America/New_York",
		"activities":[{"place_id":"92222222-2222-4222-8222-222222222222","ordinal":0,
		"schedule":{"state":"unscheduled"},"inbound_travel_mode":"walking",
		"activity_class":"flexible","priority_rank":0,"utility_score":0,
		"timing":{"open_windows":[],"reservation_grace_seconds":0,
		"min_duration_seconds":0,"preferred_duration_seconds":0,
		"max_duration_seconds":0,"mandatory":false,"can_shorten":false,
		"can_move":true,"can_skip":true}}]}`
	var request CreateSavedTripRequest
	if err := json.Unmarshal([]byte(valid), &request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"missing required boolean": strings.Replace(valid, `,"can_skip":true`, "", 1),
		"null windows":             strings.Replace(valid, `"open_windows":[]`, `"open_windows":null`, 1),
		"unknown nested field":     strings.Replace(valid, `"state":"unscheduled"`, `"state":"unscheduled","extra":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(raw), &request); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}

func TestSavedTripStoreCreateUsesOnlyRelativeSavedAuthority(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	const (
		userID         = "91111111-1111-4111-8111-111111111111"
		placeID        = "92222222-2222-4222-8222-222222222222"
		resolutionID   = "93333333-3333-4333-8333-333333333333"
		placeRecordID  = "94444444-4444-4444-8444-444444444444"
		placeReplayKey = "95555555-5555-4555-8555-555555555555"
		createKey      = "96666666-6666-4666-8666-666666666666"
		updateKey      = "97777777-7777-4777-8777-777777777777"
		staleKey       = "98888888-8888-4888-8888-888888888888"
		deleteKey      = "99999999-9999-4999-8999-999999999999"
		addKey         = "a1111111-1111-4111-8111-111111111111"
		replaceKey     = "a2222222-2222-4222-8222-222222222222"
		deleteActKey   = "a3333333-3333-4333-8333-333333333333"
	)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, display_name, default_time_zone_name) VALUES ($1, 'Creator', 'America/New_York')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO http_idempotency_records (
			id, user_id, idempotency_key, http_method, normalized_path,
			operation_kind, request_digest_algorithm, request_digest_key_id,
			request_digest, state, response_status, response_content_type,
			response_body, completed_at, retain_until
		) VALUES ($1, $2, $3, 'POST', '/api/v1/places/resolve', 'resolve_place',
			'rfc8785-hmac-sha256-v1', 'test', $4, 'completed', 200,
			'application/json', '{}'::jsonb, clock_timestamp(),
			clock_timestamp() + interval '30 days')
	`, placeRecordID, userID, placeReplayKey, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO place_resolution_attempts (
			id, user_id, idempotency_record_id, provider, state,
			provider_request_started_at, resolution_token_sha256, latitude,
			longitude, formatted_address, display_name, time_zone_name, expires_at
		) VALUES ($1, $2, $3, 'mapbox_geocoding_v6_permanent', 'resolved',
			clock_timestamp(), $4, 41.824, -71.412, 'Providence, RI',
			'Providence', 'America/New_York', clock_timestamp() + interval '5 minutes')
	`, resolutionID, userID, placeRecordID, func() []byte {
		digest := make([]byte, 32)
		digest[0] = 9
		return digest
	}()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO places (
			id, owner_user_id, source_resolution_id, latitude, longitude,
			formatted_address, display_name, time_zone_name
		) VALUES ($1, $2, $3, 41.824, -71.412, 'Providence, RI',
			'Providence', 'America/New_York')
	`, placeID, userID, resolutionID); err != nil {
		t.Fatal(err)
	}

	store, err := NewSavedTripStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	start, end := int64(3_600_000), int64(5_400_000)
	request := CreateSavedTripRequest{
		UserID: userID, IdempotencyKey: createKey, TripName: "Providence morning",
		DefaultTimeZoneName: "America/New_York", RequestDigest: [32]byte{1},
		Activities: []SavedActivityInput{{
			PlaceID: placeID, Ordinal: 0,
			Schedule:          SavedScheduleInput{State: "scheduled", StartOffsetMS: &start, EndOffsetMS: &end},
			InboundTravelMode: "walking", ActivityClass: "flexible",
			Timing: SavedActivityTimingInput{
				OpenWindows:        []RelativeWindowInput{{OpensOffsetMS: 0, ClosesOffsetMS: 86_400_000}},
				MinDurationSeconds: 1800, PreferredDurationSeconds: 1800,
				MaxDurationSeconds: 1800, CanMove: true, CanSkip: true,
			},
		}},
	}
	created, err := store.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Duplicate || created.Trip.ExecutionState != "inactive" || created.Trip.TripRevision != "1" || len(created.Trip.SavedPlan.Activities) != 1 {
		t.Fatalf("unexpected created trip: %+v", created)
	}
	var currentPlanID *string
	var nextSequence, finalizedSequence int64
	if err := pool.QueryRow(ctx, `SELECT current_plan_id::text, next_mutation_sequence, finalized_mutation_sequence FROM trips WHERE id = $1`, created.Trip.TripID).Scan(&currentPlanID, &nextSequence, &finalizedSequence); err != nil {
		t.Fatal(err)
	}
	if currentPlanID != nil || nextSequence != 1 || finalizedSequence != 0 {
		t.Fatalf("unexpected inactive authority: current=%v next=%d finalized=%d", currentPlanID, nextSequence, finalizedSequence)
	}
	var itineraryCount, runtimeActivityCount, commandCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM itinerary_plans WHERE trip_id = $1), (SELECT count(*) FROM trip_activities WHERE trip_id = $1), (SELECT count(*) FROM command_intents WHERE trip_id = $1)`, created.Trip.TripID).Scan(&itineraryCount, &runtimeActivityCount, &commandCount); err != nil {
		t.Fatal(err)
	}
	if itineraryCount != 0 || runtimeActivityCount != 0 || commandCount != 0 {
		t.Fatalf("inactive save wrote execution authority: plans=%d activities=%d commands=%d", itineraryCount, runtimeActivityCount, commandCount)
	}

	replay, err := store.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.Trip.TripID != created.Trip.TripID || replay.Trip.SavedPlan.SavedPlanID != created.Trip.SavedPlan.SavedPlanID {
		t.Fatalf("unexpected idempotent replay: %+v", replay)
	}
	request.RequestDigest[0] = 2
	if _, err := store.Create(ctx, request); !errors.Is(err, ErrHTTPIdempotencyReused) {
		t.Fatalf("changed replay error = %v, want idempotency reuse", err)
	}

	updatedName := "Updated Providence morning"
	updatedZone := "America/Chicago"
	update := UpdateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: updateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{3}, TripName: &updatedName,
		DefaultTimeZoneName: &updatedZone,
		DisplaySchedule:     &DisplayScheduleInput{LocalDate: "2026-08-12", LocalTime: "10:15:00", TimeZoneName: updatedZone},
	}
	mutated, err := store.Update(ctx, update)
	if err != nil {
		t.Fatal(err)
	}
	if mutated.Duplicate || mutated.Trip.TripRevision != "2" || mutated.Trip.TripName != updatedName ||
		mutated.Trip.DefaultTimeZoneName != updatedZone || mutated.Trip.DisplaySchedule == nil ||
		mutated.Trip.SavedPlan.SavedPlanID == created.Trip.SavedPlan.SavedPlanID ||
		len(mutated.Trip.SavedPlan.Activities) != 1 || mutated.Trip.SavedPlan.Activities[0].ActivityID != created.Trip.SavedPlan.Activities[0].ActivityID {
		t.Fatalf("unexpected updated trip: %+v", mutated)
	}
	var planCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM saved_trip_plans WHERE trip_id=$1`, created.Trip.TripID).Scan(&planCount); err != nil {
		t.Fatal(err)
	}
	if planCount != 2 {
		t.Fatalf("saved plan revision count=%d, want 2", planCount)
	}
	updateReplay, err := store.Update(ctx, update)
	if err != nil || !updateReplay.Duplicate || updateReplay.Trip.SavedPlan.SavedPlanID != mutated.Trip.SavedPlan.SavedPlanID {
		t.Fatalf("unexpected update replay: %+v err=%v", updateReplay, err)
	}
	stale := update
	stale.IdempotencyKey, stale.RequestDigest = staleKey, [32]byte{4}
	if _, err := store.Update(ctx, stale); !errors.Is(err, ErrTripRevisionStale) {
		t.Fatalf("stale update error=%v", err)
	}
	originalActivityID := mutated.Trip.SavedPlan.Activities[0].ActivityID
	addedActivity := request.Activities[0]
	addedActivity.Ordinal = 0
	addedActivity.Schedule = SavedScheduleInput{State: "unscheduled"}
	addedActivity.UtilityScore = 7
	added, err := store.AddActivity(ctx, SavedActivityMutationRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: addKey,
		ExpectedRevision: 2, RequestDigest: [32]byte{5}, Activity: &addedActivity,
	})
	if err != nil || added.Duplicate || added.Trip.TripRevision != "3" || len(added.Trip.SavedPlan.Activities) != 2 ||
		added.Trip.SavedPlan.Activities[1].ActivityID != originalActivityID {
		t.Fatalf("unexpected added activity: %+v err=%v", added, err)
	}
	addedActivityID := added.Trip.SavedPlan.Activities[0].ActivityID
	addReplay, err := store.AddActivity(ctx, SavedActivityMutationRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: addKey,
		ExpectedRevision: 2, RequestDigest: [32]byte{5}, Activity: &addedActivity,
	})
	if err != nil || !addReplay.Duplicate || addReplay.Trip.TripRevision != "3" {
		t.Fatalf("unexpected add replay: %+v err=%v", addReplay, err)
	}
	replacement := request.Activities[0]
	replacement.Ordinal = 0
	replacement.UtilityScore = 11
	replaced, err := store.ReplaceActivity(ctx, SavedActivityMutationRequest{
		UserID: userID, TripID: created.Trip.TripID, ActivityID: originalActivityID,
		IdempotencyKey: replaceKey, ExpectedRevision: 3, RequestDigest: [32]byte{6}, Activity: &replacement,
	})
	if err != nil || replaced.Trip.TripRevision != "4" || len(replaced.Trip.SavedPlan.Activities) != 2 ||
		replaced.Trip.SavedPlan.Activities[0].ActivityID != originalActivityID ||
		replaced.Trip.SavedPlan.Activities[0].UtilityScore != 11 || replaced.Trip.SavedPlan.Activities[1].ActivityID != addedActivityID {
		t.Fatalf("unexpected replaced activity: %+v err=%v", replaced, err)
	}
	activityDeleted, err := store.DeleteActivity(ctx, SavedActivityMutationRequest{
		UserID: userID, TripID: created.Trip.TripID, ActivityID: addedActivityID,
		IdempotencyKey: deleteActKey, ExpectedRevision: 4, RequestDigest: [32]byte{7},
	})
	if err != nil || activityDeleted.Trip.TripRevision != "5" || len(activityDeleted.Trip.SavedPlan.Activities) != 1 ||
		activityDeleted.Trip.SavedPlan.Activities[0].ActivityID != originalActivityID {
		t.Fatalf("unexpected deleted activity: %+v err=%v", activityDeleted, err)
	}

	deletedReplay, err := store.Delete(ctx, DeleteSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: deleteKey,
		ExpectedRevision: 5, RequestDigest: [32]byte{8},
	})
	if err != nil || deletedReplay {
		t.Fatalf("delete duplicate=%t err=%v", deletedReplay, err)
	}
	deletedReplay, err = store.Delete(ctx, DeleteSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: deleteKey,
		ExpectedRevision: 5, RequestDigest: [32]byte{8},
	})
	if err != nil || !deletedReplay {
		t.Fatalf("delete replay duplicate=%t err=%v", deletedReplay, err)
	}
	var tripCount, retainedDeleteCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM trips WHERE id=$1),
		       (SELECT count(*) FROM http_idempotency_records
		        WHERE user_id=$2 AND normalized_path=$3 AND response_status=204
		          AND trip_id IS NULL AND resource_id=$1)
	`, created.Trip.TripID, userID, "/api/v1/trips/"+created.Trip.TripID).Scan(&tripCount, &retainedDeleteCount); err != nil {
		t.Fatal(err)
	}
	if tripCount != 0 || retainedDeleteCount != 1 {
		t.Fatalf("trip count=%d retained delete count=%d", tripCount, retainedDeleteCount)
	}
}

func TestSavedTripStoreListAndGetReturnsOwnedSavedPlan(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	const (
		userID         = "81111111-1111-4111-8111-111111111111"
		tripID         = "82222222-2222-4222-8222-222222222222"
		planID         = "83333333-3333-4333-8333-333333333333"
		itineraryID    = "84444444-4444-4444-8444-444444444444"
		activityID     = "85555555-5555-4555-8555-555555555555"
		placeID        = "86666666-6666-4666-8666-666666666666"
		resolutionID   = "87777777-7777-4777-8777-777777777777"
		idempotencyID  = "88888888-8888-4888-8888-888888888888"
		idempotencyKey = "89999999-9999-4999-8999-999999999999"
	)
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM trips WHERE id = $1", tripID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Saved trip test user', 'America/New_York')
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence, current_plan_id,
			trip_name
		) VALUES ($1, $2, 'America/New_York', 1, 1, 0, $3, 'Saved trip')
	`, tripID, userID, itineraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, 1, 'user_authored', $3, 1, decode('7b7d', 'hex'), 2, $4, clock_timestamp())
	`, itineraryID, tripID, userID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO http_idempotency_records (
			id, user_id, idempotency_key, http_method, normalized_path, operation_kind,
			request_digest_algorithm, request_digest_key_id, request_digest, state,
			response_status, response_content_type, response_body, completed_at, retain_until
		) VALUES ($1, $2, $3, 'POST', '/api/v1/places/resolve', 'resolve_place',
			'rfc8785-hmac-sha256-v1', 'test', $4, 'completed', 201,
			'application/json', '{}'::jsonb, clock_timestamp(), clock_timestamp() + interval '30 days')
	`, idempotencyID, userID, idempotencyKey, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO place_resolution_attempts (
			id, user_id, idempotency_record_id, provider, state,
			provider_request_started_at, resolution_token_sha256, latitude, longitude,
			formatted_address, display_name, time_zone_name, expires_at
		) VALUES ($1, $2, $3, 'mapbox_geocoding_v6_permanent', 'resolved',
			clock_timestamp(), $4, 40.7128, -74.0060, 'New York, NY', 'New York',
			'America/New_York', clock_timestamp() + interval '5 minutes')
	`, resolutionID, userID, idempotencyID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO places (
			id, owner_user_id, source_resolution_id, latitude, longitude,
			formatted_address, display_name, time_zone_name
		) VALUES ($1, $2, $3, 40.7128, -74.0060, 'New York, NY', 'New York', 'America/New_York')
	`, placeID, userID, resolutionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_plans (
			id, trip_id, owner_user_id, saved_plan_revision, authored_by_user_id,
			display_local_date, display_local_time, display_time_zone_name
		) VALUES ($1, $2, $3, 1, $3, DATE '2026-08-09', TIME '09:05:07', 'America/New_York')
	`, planID, tripID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_trip_activities (
			saved_plan_id, trip_id, owner_user_id, activity_id, place_id, ordinal,
			schedule_state, inbound_travel_mode, activity_class, priority_rank, utility_score,
			reservation_grace_seconds, min_duration_seconds, preferred_duration_seconds,
			max_duration_seconds, mandatory, can_shorten, can_move, can_skip
		) VALUES ($1, $2, $3, $4, $5, 0, 'unscheduled', 'walking', 'flexible',
			0, 0, 0, 3600, 3600, 3600, false, false, true, true)
	`, planID, tripID, userID, activityID, placeID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_activity_open_windows (
			saved_plan_id, activity_id, window_index, opens_offset_ms, closes_offset_ms
		) VALUES ($1, $2, 0, 0, 86400000)
	`, planID, activityID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE trips SET saved_plan_id = $2 WHERE id = $1", tripID, planID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	store, err := NewSavedTripStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.InactiveTrips) != 1 || list.InactiveTrips[0].TripID != tripID || list.CurrentExecutionTrip != nil {
		t.Fatalf("unexpected trip list: %+v", list)
	}
	trip, err := store.Get(ctx, userID, tripID)
	if err != nil {
		t.Fatal(err)
	}
	if trip.TripID != tripID || trip.TripRevision != "1" || trip.DisplaySchedule == nil || trip.DisplaySchedule.LocalTime != "09:05:07" || len(trip.SavedPlan.Activities) != 1 {
		t.Fatalf("unexpected trip view: %+v", trip)
	}
	if trip.SavedPlan.Activities[0].Place.PlaceID != placeID || len(trip.SavedPlan.Activities[0].Timing.OpenWindows) != 1 {
		t.Fatalf("unexpected saved activity: %+v", trip.SavedPlan.Activities[0])
	}

}
