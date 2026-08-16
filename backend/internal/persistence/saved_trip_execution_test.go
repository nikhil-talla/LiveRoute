package persistence

import (
	"context"
	"errors"
	"testing"
)

func TestSavedTripStoreActivateMaterializesAbsoluteExecutionBaseline(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	const (
		userID        = "b1111111-1111-4111-8111-111111111111"
		placeID       = "b2222222-2222-4222-8222-222222222222"
		resolutionID  = "b3333333-3333-4333-8333-333333333333"
		placeRecord   = "b4444444-4444-4444-8444-444444444444"
		createKey     = "b5555555-5555-4555-8555-555555555555"
		activateKey   = "b6666666-6666-4666-8666-666666666666"
		holderID      = "b9999999-9999-4999-8999-999999999999"
		deactivateKey = "baaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		reactivateKey = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Activator', 'America/New_York')
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO http_idempotency_records (
			id, user_id, idempotency_key, http_method, normalized_path,
			operation_kind, request_digest_algorithm, request_digest_key_id,
			request_digest, state, response_status, response_content_type,
			response_body, completed_at, retain_until
		) VALUES ($1,$2,$3,'POST','/api/v1/places/resolve','resolve_place',
			'rfc8785-hmac-sha256-v1','test',$4,'completed',200,
			'application/json','{}'::jsonb,clock_timestamp(),clock_timestamp()+interval '30 days')
	`, placeRecord, userID, "b7777777-7777-4777-8777-777777777777", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO place_resolution_attempts (
			id, user_id, idempotency_record_id, provider, state,
			provider_request_started_at, resolution_token_sha256, latitude,
			longitude, formatted_address, display_name, time_zone_name, expires_at
		) VALUES ($1,$2,$3,'mapbox_geocoding_v6_permanent','resolved',
			clock_timestamp(),$4,41.824,-71.412,'Providence, RI','Providence',
			'America/New_York',clock_timestamp()+interval '5 minutes')
	`, resolutionID, userID, placeRecord, func() []byte {
		digest := make([]byte, 32)
		digest[0] = 17
		return digest
	}()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO places (
			id, owner_user_id, source_resolution_id, latitude, longitude,
			formatted_address, display_name, time_zone_name
		) VALUES ($1,$2,$3,41.824,-71.412,'Providence, RI','Providence','America/New_York')
	`, placeID, userID, resolutionID); err != nil {
		t.Fatal(err)
	}
	store, err := NewSavedTripStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	start, end := int64(0), int64(1000)
	created, err := store.Create(ctx, CreateSavedTripRequest{
		UserID: userID, IdempotencyKey: createKey, TripName: "Activation test",
		DefaultTimeZoneName: "America/New_York", RequestDigest: [32]byte{1},
		Activities: []SavedActivityInput{{
			PlaceID: placeID, Ordinal: 0,
			Schedule:          SavedScheduleInput{State: "scheduled", StartOffsetMS: &start, EndOffsetMS: &end},
			InboundTravelMode: "walking", ActivityClass: "flexible",
			Timing: SavedActivityTimingInput{OpenWindows: []RelativeWindowInput{{OpensOffsetMS: 0, ClosesOffsetMS: 1000}},
				MinDurationSeconds: 1, PreferredDurationSeconds: 1, MaxDurationSeconds: 1,
				CanMove: true, CanSkip: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := store.Activate(ctx, ActivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: activateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{2}, StartingLatitude: 41.8,
		StartingLongitude: -71.4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if activated.Duplicate || activated.Transition.Trip.ExecutionState != "activating" ||
		activated.Transition.Trip.ActiveExecution == nil ||
		activated.Transition.Operation.LastStep != "recorded" {
		t.Fatalf("unexpected activation: %+v", activated)
	}
	var nextSequence, finalizedSequence int64
	var planID string
	if err := pool.QueryRow(ctx, `
		SELECT next_mutation_sequence, finalized_mutation_sequence, current_plan_id::text
		FROM trips WHERE id = $1
	`, created.Trip.TripID).Scan(&nextSequence, &finalizedSequence, &planID); err != nil {
		t.Fatal(err)
	}
	if nextSequence != 2 || finalizedSequence != 1 || planID != activated.Transition.Trip.ActiveExecution.ExecutionPlanID {
		t.Fatalf("unexpected activation baseline: next=%d finalized=%d plan=%s", nextSequence, finalizedSequence, planID)
	}
	var planCount, activityCount, windowCount, intentCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM itinerary_plans WHERE trip_id=$1),
		       (SELECT count(*) FROM trip_activities WHERE trip_id=$1),
		       (SELECT count(*) FROM activity_open_windows WHERE trip_id=$1),
		       (SELECT count(*) FROM command_intents WHERE trip_id=$1)
	`, created.Trip.TripID).Scan(&planCount, &activityCount, &windowCount, &intentCount); err != nil {
		t.Fatal(err)
	}
	if planCount != 1 || activityCount != 1 || windowCount != 1 || intentCount != 0 {
		t.Fatalf("unexpected materialized execution rows: plans=%d activities=%d windows=%d intents=%d", planCount, activityCount, windowCount, intentCount)
	}
	replay, err := store.Activate(ctx, ActivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: activateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{2}, StartingLatitude: -10,
		StartingLongitude: 20,
	})
	if err != nil || !replay.Duplicate || replay.Transition.Trip.ActiveExecution.ExecutionPlanID != planID {
		t.Fatalf("unexpected activation replay: %+v err=%v", replay, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO trip_runtime_leases (
			trip_id, holder_id, runtime_epoch, lease_expires_at, renewed_at
		) VALUES ($1,$2,1,clock_timestamp()+interval '5 minutes',clock_timestamp())
	`, created.Trip.TripID, holderID); err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteActivation(ctx, CompleteActivationRequest{
		TripID: created.Trip.TripID, OperationID: activated.Transition.Operation.OperationID,
		HolderID: holderID, RuntimeEpoch: 1,
	})
	if err != nil || completed.ExecutionState != "active" {
		t.Fatalf("unexpected activation completion: %+v err=%v", completed, err)
	}
	completedReplay, err := store.Activate(ctx, ActivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: activateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{2}, StartingLatitude: 41.8,
		StartingLongitude: -71.4,
	})
	if err != nil || !completedReplay.Duplicate || completedReplay.Transition.Trip.ExecutionState != "active" ||
		completedReplay.Transition.Operation.State != "succeeded" {
		t.Fatalf("unexpected completed activation replay: %+v err=%v", completedReplay, err)
	}
	_, err = store.Activate(ctx, ActivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: "b8888888-8888-4888-8888-888888888888",
		ExpectedRevision: 1, RequestDigest: [32]byte{3}, StartingLatitude: 41.8,
		StartingLongitude: -71.4,
	})
	if !errors.Is(err, ErrSavedTripNotInactive) {
		t.Fatalf("second activation error=%v, want inactive conflict", err)
	}
	deactivated, err := store.Deactivate(ctx, DeactivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: deactivateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{4},
	})
	if err != nil || deactivated.Transition.Trip.ExecutionState != "deactivating" {
		t.Fatalf("unexpected deactivation: %+v err=%v", deactivated, err)
	}
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseStore.Release(ctx, created.Trip.TripID, holderID, 1); err != nil {
		t.Fatal(err)
	}
	inactive, err := store.CompleteDeactivation(ctx, CompleteDeactivationRequest{
		TripID: created.Trip.TripID, OperationID: deactivated.Transition.Operation.OperationID,
		HolderID: holderID, RuntimeEpoch: 1,
	})
	if err != nil || inactive.ExecutionState != "inactive" || inactive.ActiveExecution != nil || inactive.SavedPlan.SavedPlanID == "" {
		t.Fatalf("unexpected deactivation completion: %+v err=%v", inactive, err)
	}
	var currentPlanID *string
	var nextAfterDeactivation, finalizedAfterDeactivation int64
	var executionActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT current_plan_id::text, next_mutation_sequence, finalized_mutation_sequence,
		       (SELECT count(*) FROM trip_activities WHERE trip_id=$1)
		FROM trips WHERE id=$1
	`, created.Trip.TripID).Scan(&currentPlanID, &nextAfterDeactivation, &finalizedAfterDeactivation, &executionActivityCount); err != nil {
		t.Fatal(err)
	}
	if currentPlanID != nil || nextAfterDeactivation != 2 || finalizedAfterDeactivation != 1 || executionActivityCount != 0 {
		t.Fatalf("deactivation did not reset execution state: plan=%v next=%d finalized=%d activities=%d", currentPlanID, nextAfterDeactivation, finalizedAfterDeactivation, executionActivityCount)
	}
	deactivationReplay, err := store.Deactivate(ctx, DeactivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: deactivateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{4},
	})
	if err != nil || !deactivationReplay.Duplicate || deactivationReplay.Transition.Operation.State != "succeeded" {
		t.Fatalf("unexpected deactivation replay: %+v err=%v", deactivationReplay, err)
	}
	reactivated, err := store.Activate(ctx, ActivateSavedTripRequest{
		UserID: userID, TripID: created.Trip.TripID, IdempotencyKey: reactivateKey,
		ExpectedRevision: 1, RequestDigest: [32]byte{5}, StartingLatitude: 41.8,
		StartingLongitude: -71.4,
	})
	if err != nil || reactivated.Transition.Trip.ExecutionState != "activating" {
		t.Fatalf("unexpected reactivation: %+v err=%v", reactivated, err)
	}
	if err := pool.QueryRow(ctx, `SELECT next_mutation_sequence, finalized_mutation_sequence FROM trips WHERE id=$1`, created.Trip.TripID).Scan(&nextAfterDeactivation, &finalizedAfterDeactivation); err != nil {
		t.Fatal(err)
	}
	if nextAfterDeactivation != 3 || finalizedAfterDeactivation != 2 {
		t.Fatalf("reactivation reused baseline sequence: next=%d finalized=%d", nextAfterDeactivation, finalizedAfterDeactivation)
	}
}
