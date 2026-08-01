package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
)

func setupCanonicalMirrorFixture(t *testing.T, prefix string) (*CanonicalStateStore, context.Context, ReplaceCurrentPlanRequest, RecordedCommand) {
	t.Helper()
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    prefix + "1111111-1111-1111-1111-111111111111",
		tripID:    prefix + "2222222-2222-2222-2222-222222222222",
		intentID:  prefix + "3333333-3333-3333-3333-333333333333",
		messageID: prefix + "4444444-4444-4444-4444-444444444444",
		planID:    prefix + "5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Canonical mirror test user', 'America/New_York')
	`, fixture.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM trips WHERE id = $1", fixture.tripID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", fixture.userID)
	})
	store, err := NewCanonicalStateStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTrip(ctx, createTripRequest(fixture)); err != nil {
		t.Fatal(err)
	}
	request := replaceCurrentPlanRequest(fixture)
	result, err := store.ReplaceCurrentPlan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return store, ctx, request, result
}

func TestFinalizeCanonicalMirrorUsesStoredPlanIdentityAndExactReplay(t *testing.T) {
	store, ctx, request, recorded := setupCanonicalMirrorFixture(t, "e")
	finalize := FinalizeCanonicalMirrorRequest{
		TripID:                 request.TripID,
		IntentID:               request.IntentID,
		OutboxID:               request.OutboxID,
		EventID:                request.EventID,
		MutationSequence:       recorded.MutationSequence,
		ExpectedTripRevision:   recorded.ExpectedTripRevision,
		ResultingTripRevision:  *recorded.ResultingTripRevision,
		ResultingCurrentPlanID: *recorded.ResultingCurrentPlanID,
	}
	result, err := store.FinalizeCanonicalMirror(ctx, finalize)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.RuntimeSyncState != "synced" ||
		result.DeliveryState != "accepted" ||
		result.ResultingCurrentPlanID != request.PlanID {
		t.Fatalf("unexpected mirror finalization: %+v", result)
	}

	newRequest := request
	newRequest.IntentID = "e6666666-6666-6666-6666-666666666666"
	newRequest.OutboxID = "e7777777-7777-7777-7777-777777777777"
	newRequest.MessageID = "e8888888-8888-8888-8888-888888888888"
	newRequest.EventID = newRequest.MessageID
	newRequest.PlanID = "e9999999-9999-9999-9999-999999999999"
	newRequest.ExpectedTripRevision = 2
	newRequest.CommandPayload = []byte(`{"kind":"replace_current_plan","revision":3}`)
	newRequest.EventPayload = []byte(`{"kind":"current_plan_replaced","revision":3}`)
	newRequest.PayloadDigest = sha256.Sum256(newRequest.CommandPayload)
	newRequest.OutcomePayload = []byte(`{"trip_revision":3}`)
	if _, err := store.ReplaceCurrentPlan(ctx, newRequest); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx, request.TripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentPlanID != newRequest.PlanID {
		t.Fatalf("expected later canonical edit to advance live pointer, got %s", state.CurrentPlanID)
	}

	duplicate, err := store.FinalizeCanonicalMirror(ctx, finalize)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.ResultingCurrentPlanID != request.PlanID ||
		duplicate.RuntimeSyncState != "synced" || duplicate.DeliveryState != "accepted" {
		t.Fatalf("unexpected exact mirror replay: %+v", duplicate)
	}
}

func TestFinalizeCanonicalMirrorPausesIdentityMismatch(t *testing.T) {
	store, ctx, request, recorded := setupCanonicalMirrorFixture(t, "f")
	wrongPlan := "f9999999-9999-9999-9999-999999999999"
	_, err := store.FinalizeCanonicalMirror(ctx, FinalizeCanonicalMirrorRequest{
		TripID:                 request.TripID,
		IntentID:               request.IntentID,
		OutboxID:               request.OutboxID,
		EventID:                request.EventID,
		MutationSequence:       recorded.MutationSequence,
		ExpectedTripRevision:   recorded.ExpectedTripRevision,
		ResultingTripRevision:  *recorded.ResultingTripRevision,
		ResultingCurrentPlanID: wrongPlan,
	})
	if !errors.Is(err, ErrCanonicalMirrorIdentity) {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
	var runtimeSyncState, deliveryState, lastStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT intent.runtime_sync_state, outbox.delivery_state, outbox.last_status
		FROM command_intents AS intent
		JOIN planner_outbox AS outbox
		  ON outbox.command_intent_id = intent.id
		WHERE intent.trip_id = $1 AND intent.id = $2 AND outbox.id = $3
	`, request.TripID, request.IntentID, request.OutboxID).Scan(
		&runtimeSyncState, &deliveryState, &lastStatus,
	); err != nil {
		t.Fatal(err)
	}
	if runtimeSyncState != "paused_internal" || deliveryState != "paused_internal" || lastStatus != "INTERNAL" {
		t.Fatalf("mismatch did not pause mirror: runtime=%s delivery=%s status=%s", runtimeSyncState, deliveryState, lastStatus)
	}
}
