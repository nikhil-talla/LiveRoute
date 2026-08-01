package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

func testUserPlanSegment(
	activityID string,
	state uint64,
	start int64,
	end int64,
) []byte {
	var segment []byte
	segment = appendTestBytesField(segment, 1, []byte(activityID))
	segment = appendTestVarintField(segment, 2, state)
	if state == 1 {
		segment = appendTestVarintField(segment, 3, uint64(start))
		segment = appendTestVarintField(segment, 4, uint64(end))
	}
	return segment
}

func testUserCurrentPlan(
	planID string,
	revision uint64,
	createdAt int64,
	segments ...[]byte,
) []byte {
	var plan []byte
	plan = appendTestBytesField(plan, 1, []byte(planID))
	plan = appendTestVarintField(plan, 2, revision)
	plan = appendTestVarintField(plan, 3, 1)
	for _, segment := range segments {
		plan = appendTestBytesField(plan, 4, segment)
	}
	return appendTestVarintField(plan, 5, uint64(createdAt))
}

type canonicalStateFixture struct {
	trip        commandTripFixture
	startedID   string
	completedID string
	plannedID   string
	planPayload []byte
	createdAt   time.Time
}

func createCanonicalStateFixture(
	t *testing.T,
	prefix string,
) (*CanonicalStateStore, canonicalStateFixture) {
	t.Helper()
	pool, ctx := openPersistenceTestPool(t)
	fixture := canonicalStateFixture{
		trip: commandTripFixture{
			userID: prefix + "111111-1111-1111-1111-111111111111",
			tripID: prefix + "222222-2222-2222-2222-222222222222",
			planID: prefix + "333333-3333-3333-3333-333333333333",
		},
		startedID:   prefix + "444444-4444-4444-4444-444444444444",
		completedID: prefix + "555555-5555-5555-5555-555555555555",
		plannedID:   prefix + "666666-6666-6666-6666-666666666666",
		createdAt:   time.UnixMilli(1_784_000_000_123).UTC(),
	}
	createCommandTrip(t, ctx, pool, fixture.trip)
	fixture.planPayload = testUserCurrentPlan(
		fixture.trip.planID,
		1,
		fixture.createdAt.UnixMilli(),
		testUserPlanSegment(fixture.completedID, 1, 1_000, 2_000),
		testUserPlanSegment(fixture.startedID, 1, 2_000, 3_000),
		testUserPlanSegment(fixture.plannedID, 2, 0, 0),
	)
	checksum := sha256.Sum256(fixture.planPayload)
	if _, err := pool.Exec(ctx, `
		UPDATE itinerary_plans
		SET payload = $2,
		    payload_size_bytes = $3,
		    checksum_sha256 = $4,
		    created_at = $5
		WHERE id = $1
	`, fixture.trip.planID, fixture.planPayload,
		len(fixture.planPayload), checksum[:], fixture.createdAt); err != nil {
		t.Fatal(err)
	}
	activities := []struct {
		id      string
		ordinal int
		state   string
	}{
		{fixture.startedID, 0, "started"},
		{fixture.completedID, 1, "completed"},
		{fixture.plannedID, 2, "planned"},
	}
	for _, activity := range activities {
		if _, err := pool.Exec(ctx, `
			INSERT INTO trip_activities (
				id, trip_id, ordinal, place_id, display_name,
				latitude, longitude, time_zone_name, inbound_travel_mode,
				activity_class, activity_state, activity_delay_seconds,
				priority_rank, utility_score, reservation_start,
				reservation_grace_seconds, mandatory_deadline,
				min_duration_seconds, preferred_duration_seconds,
				max_duration_seconds, mandatory, can_shorten, can_move,
				can_skip
			) VALUES (
				$1, $2, $3, $4, $4,
				40.0 + ($3::integer)::double precision,
				-74.0, 'America/New_York', 'walking',
				'flexible', $5, $3,
				$3, $3, $6,
				$3, $7,
				60, 120, 180, false, false, true, true
			)
		`, activity.id, fixture.trip.tripID, activity.ordinal,
			"place-"+activity.state, activity.state,
			fixture.createdAt, fixture.createdAt.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO activity_open_windows (
			trip_id, activity_id, window_index, opens_at, closes_at
		) VALUES
		  ($1, $2, 0, $3, $4),
		  ($1, $2, 1, $4, $5)
	`, fixture.trip.tripID, fixture.plannedID,
		fixture.createdAt,
		fixture.createdAt.Add(time.Hour),
		fixture.createdAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO trip_travel_delays (
			trip_id, from_activity_id, to_activity_id,
			additional_seconds, observed_at
		) VALUES ($1, $2, $3, 90, $4)
	`, fixture.trip.tripID, fixture.startedID, fixture.plannedID,
		fixture.createdAt); err != nil {
		t.Fatal(err)
	}
	store, err := NewCanonicalStateStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func TestCanonicalStateLoadRebuildsNormalizedAuthoritativeState(
	t *testing.T,
) {
	store, fixture := createCanonicalStateFixture(t, "10")
	state, err := store.Load(context.Background(), fixture.trip.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TripID != fixture.trip.tripID ||
		state.OwnerUserID != fixture.trip.userID ||
		state.TripRevision != 1 ||
		state.FinalizedMutationSequence != 1 ||
		state.CurrentPlanID != fixture.trip.planID ||
		state.CompletedPrefixCount != 1 ||
		state.CurrentActivityID == nil ||
		*state.CurrentActivityID != fixture.startedID {
		t.Fatalf("unexpected canonical trip metadata: %+v", state)
	}
	if len(state.Activities) != 3 ||
		state.Activities[0].ID != fixture.startedID ||
		state.Activities[1].ID != fixture.completedID ||
		state.Activities[2].ID != fixture.plannedID ||
		len(state.Activities[2].OpenWindows) != 2 {
		t.Fatalf("unexpected normalized activities: %+v", state.Activities)
	}
	if len(state.TravelDelays) != 1 ||
		state.TravelDelays[0].FromActivityID != fixture.startedID ||
		state.TravelDelays[0].ToActivityID != fixture.plannedID ||
		state.TravelDelays[0].AdditionalSeconds != 90 {
		t.Fatalf("unexpected travel delays: %+v", state.TravelDelays)
	}
	if state.CurrentPlan.ID != fixture.trip.planID ||
		state.CurrentPlan.Origin != "user_authored" ||
		state.CurrentPlan.SourceProposalID != nil ||
		state.CurrentPlan.CreatedAt != fixture.createdAt ||
		string(state.CurrentPlan.Payload) != string(fixture.planPayload) {
		t.Fatalf("unexpected authoritative plan: %+v", state.CurrentPlan)
	}
}

func TestCanonicalStateLoadRejectsPlanCorruption(t *testing.T) {
	store, fixture := createCanonicalStateFixture(t, "20")
	if _, err := store.pool.Exec(context.Background(), `
		UPDATE itinerary_plans
		SET payload = payload || decode('00', 'hex'),
		    payload_size_bytes = payload_size_bytes + 1
		WHERE id = $1
	`, fixture.trip.planID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(
		context.Background(),
		fixture.trip.tripID,
	); !errors.Is(err, ErrCanonicalStateCorrupt) {
		t.Fatalf("expected canonical corruption, got %v", err)
	}
}

func TestCanonicalStateLoadRejectsInvalidProgress(t *testing.T) {
	store, fixture := createCanonicalStateFixture(t, "30")
	if _, err := store.pool.Exec(context.Background(), `
		UPDATE trip_activities
		SET activity_state = 'completed'
		WHERE id = $1
	`, fixture.plannedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(
		context.Background(),
		fixture.trip.tripID,
	); !errors.Is(err, ErrCanonicalStateCorrupt) {
		t.Fatalf("expected progress corruption, got %v", err)
	}
}

func TestCanonicalStateLoadIsRepeatableUnderConcurrency(t *testing.T) {
	store, fixture := createCanonicalStateFixture(t, "40")
	const callers = 4
	var wait sync.WaitGroup
	results := make(chan CanonicalTripState, callers)
	failures := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			state, err := store.Load(
				context.Background(),
				fixture.trip.tripID,
			)
			if err != nil {
				failures <- err
				return
			}
			results <- state
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	for state := range results {
		if state.CompletedPrefixCount != 1 ||
			state.CurrentActivityID == nil ||
			*state.CurrentActivityID != fixture.startedID ||
			string(state.CurrentPlan.Payload) !=
				string(fixture.planPayload) {
			t.Fatalf("concurrent load differs: %+v", state)
		}
	}
}
