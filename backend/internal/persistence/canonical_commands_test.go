package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

type createTripFixture struct {
	userID    string
	tripID    string
	intentID  string
	messageID string
	planID    string
}

func createTripRequest(fixture createTripFixture) CreateTripRequest {
	base := time.UnixMilli(1_784_000_000_000).UTC()
	firstID := fixture.tripID[:8] + "-4444-4444-4444-444444444444"
	secondID := fixture.tripID[:8] + "-5555-5555-5555-555555555555"
	commandPayload := []byte(`{"kind":"create_trip","trip_id":"` + fixture.tripID + `"}`)
	return CreateTripRequest{
		TripID:              fixture.tripID,
		OwnerUserID:         fixture.userID,
		DefaultTimeZoneName: "America/New_York",
		IntentID:            fixture.intentID,
		MessageID:           fixture.messageID,
		EventID:             fixture.messageID,
		PlanID:              fixture.planID,
		Activities: []CanonicalActivity{
			{
				ID:                       firstID,
				Ordinal:                  0,
				PlaceID:                  "place-first",
				DisplayName:              "First",
				Latitude:                 40,
				Longitude:                -74,
				TimeZoneName:             "America/New_York",
				InboundTravelMode:        "walking",
				ActivityClass:            "flexible",
				ActivityState:            ActivityStatePlanned,
				PriorityRank:             1,
				UtilityScore:             10,
				MinDurationSeconds:       60,
				PreferredDurationSeconds: 120,
				MaxDurationSeconds:       180,
				CanMove:                  true,
				CanSkip:                  true,
				OpenWindows: []CanonicalOpenWindow{{
					OpensAt: base, ClosesAt: base.Add(time.Hour),
				}},
			},
			{
				ID:                       secondID,
				Ordinal:                  1,
				PlaceID:                  "place-second",
				DisplayName:              "Second",
				Latitude:                 41,
				Longitude:                -73,
				TimeZoneName:             "America/New_York",
				InboundTravelMode:        "driving",
				ActivityClass:            "fixed",
				ActivityState:            ActivityStatePlanned,
				PriorityRank:             2,
				UtilityScore:             20,
				MinDurationSeconds:       60,
				PreferredDurationSeconds: 120,
				MaxDurationSeconds:       180,
				Mandatory:                true,
				CanMove:                  false,
				CanSkip:                  false,
			},
		},
		TravelDelays: []CanonicalTravelDelay{{
			FromActivityID: firstID, ToActivityID: secondID,
			AdditionalSeconds: 90, ObservedAt: base,
		}},
		PlanSegments: []CanonicalPlanSegmentDraft{
			{ActivityID: firstID, Scheduled: true,
				Start: int64Ptr(base.UnixMilli()),
				End:   int64Ptr(base.Add(20 * time.Minute).UnixMilli())},
			{ActivityID: secondID},
		},
		CommandPayload: commandPayload,
		PayloadDigest:  sha256.Sum256(commandPayload),
		OutcomePayload: []byte(`{"trip_revision":1}`),
	}
}

func int64Ptr(value int64) *int64 { return &value }

func replaceCurrentPlanRequest(
	fixture createTripFixture,
) ReplaceCurrentPlanRequest {
	initial := createTripRequest(fixture)
	commandPayload := []byte(`{"kind":"replace_current_plan","trip_id":"` + fixture.tripID + `"}`)
	start := int64(1_784_000_100_000)
	end := start + 20*60*1000
	return ReplaceCurrentPlanRequest{
		TripID:                    fixture.tripID,
		OwnerUserID:               fixture.userID,
		IntentID:                  "86666666-6666-6666-6666-666666666666",
		OutboxID:                  "87777777-7777-7777-7777-777777777777",
		MessageID:                 "88888888-8888-8888-8888-888888888888",
		EventID:                   "88888888-8888-8888-8888-888888888888",
		PlanID:                    "89999999-9999-9999-9999-999999999999",
		ExpectedTripRevision:      1,
		MaxPendingCanonicalMirror: 2,
		PlanSegments: []CanonicalPlanSegmentDraft{
			{ActivityID: initial.Activities[1].ID, Scheduled: true,
				Start: int64Ptr(start), End: int64Ptr(end)},
			{ActivityID: initial.Activities[0].ID},
		},
		CommandPayload: commandPayload,
		EventPayload:   []byte(`{"kind":"current_plan_replaced"}`),
		PayloadDigest:  sha256.Sum256(commandPayload),
		OutcomePayload: []byte(`{"trip_revision":2}`),
	}
}

func TestCreateTripCanonicalFirstPersistsAuthorityAndExactReplay(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "81111111-1111-1111-1111-111111111111",
		tripID:    "82222222-2222-2222-2222-222222222222",
		intentID:  "83333333-3333-3333-3333-333333333333",
		messageID: "84444444-4444-4444-4444-444444444444",
		planID:    "85555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Create test user', 'America/New_York')
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
	request := createTripRequest(fixture)
	result, err := store.CreateTrip(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Kind != CommandCreateTrip ||
		result.State != "applied" || result.RuntimeSyncState != "not_required" ||
		result.MutationSequence != 1 || result.ExpectedTripRevision != 0 ||
		result.ResultingTripRevision == nil || *result.ResultingTripRevision != 1 ||
		result.ResultingCurrentPlanID == nil ||
		*result.ResultingCurrentPlanID != fixture.planID {
		t.Fatalf("unexpected create result: %+v", result)
	}
	state, err := store.Load(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TripRevision != 1 || state.FinalizedMutationSequence != 1 ||
		state.CurrentPlanID != fixture.planID || len(state.Activities) != 2 ||
		len(state.TravelDelays) != 1 || state.CurrentPlan.Origin != "user_authored" ||
		state.CurrentPlan.SourceProposalID != nil {
		t.Fatalf("unexpected canonical state: %+v", state)
	}
	duplicate := request
	duplicate.IntentID = "86666666-6666-6666-6666-666666666666"
	duplicate.PlanID = "87777777-7777-7777-7777-777777777777"
	duplicateResult, err := store.CreateTrip(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateResult.Duplicate || duplicateResult.IntentID != fixture.intentID ||
		duplicateResult.MutationSequence != result.MutationSequence {
		t.Fatalf("unexpected duplicate result: %+v", duplicateResult)
	}
	reused := request
	reused.CommandPayload = []byte(`{"kind":"changed"}`)
	reused.PayloadDigest = sha256.Sum256(reused.CommandPayload)
	if _, err := store.CreateTrip(ctx, reused); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("expected idempotency-key rejection, got %v", err)
	}
}

func TestCreateTripCanonicalFirstSerializesExactConcurrentReplay(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "91111111-1111-1111-1111-111111111111",
		tripID:    "92222222-2222-2222-2222-222222222222",
		intentID:  "93333333-3333-3333-3333-333333333333",
		messageID: "94444444-4444-4444-4444-444444444444",
		planID:    "95555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Concurrent create user', 'America/New_York')
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
	request := createTripRequest(fixture)
	results := make([]RecordedCommand, 2)
	errors := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errors[index] = store.CreateTrip(ctx, request)
		}(index)
	}
	wait.Wait()
	for index, err := range errors {
		if err != nil {
			t.Fatalf("concurrent create %d failed: %v", index, err)
		}
	}
	if results[0].Duplicate == results[1].Duplicate {
		t.Fatalf("expected one insert and one exact replay: %+v", results)
	}
}

func TestReplaceCurrentPlanCommitsMirrorAndSupersedesProposal(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "a1111111-1111-1111-1111-111111111111",
		tripID:    "a2222222-2222-2222-2222-222222222222",
		intentID:  "a3333333-3333-3333-3333-333333333333",
		messageID: "a4444444-4444-4444-4444-444444444444",
		planID:    "a5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Replacement test user', 'America/New_York')
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
	proposalPayload := []byte("pending-proposal")
	proposalChecksum := sha256.Sum256(proposalPayload)
	proposalID := "a6666666-6666-6666-6666-666666666666"
	if _, err := pool.Exec(ctx, `
		INSERT INTO plan_proposals (
			id, trip_id, base_current_plan_id, source_runtime_epoch,
			source_planner_state_version, source_trip_revision,
			source_accepted_mutation_sequence, schema_version, payload,
			payload_size_bytes, checksum_sha256, state, created_at
		) VALUES ($1, $2, $3, 1, 1, 1, 1, 1, $4, $5, $6, 'pending', clock_timestamp())
	`, proposalID, fixture.tripID, fixture.planID, proposalPayload,
		len(proposalPayload), proposalChecksum[:]); err != nil {
		t.Fatal(err)
	}
	request := replaceCurrentPlanRequest(fixture)
	result, err := store.ReplaceCurrentPlan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Kind != CommandReplaceCurrentPlan ||
		result.MutationSequence != 2 || result.RuntimeSyncState != "pending" ||
		result.ResultingTripRevision == nil || *result.ResultingTripRevision != 2 ||
		result.ResultingCurrentPlanID == nil ||
		*result.ResultingCurrentPlanID != request.PlanID ||
		result.OutboxID != request.OutboxID {
		t.Fatalf("unexpected replacement result: %+v", result)
	}
	state, err := store.Load(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TripRevision != 2 || state.FinalizedMutationSequence != 2 ||
		state.CurrentPlanID != request.PlanID || state.CurrentPlan.Revision != 2 ||
		state.CurrentPlan.Origin != "user_authored" {
		t.Fatalf("replacement did not become canonical: %+v", state)
	}
	var proposalState string
	var outboxState string
	if err := pool.QueryRow(ctx, `
		SELECT state FROM plan_proposals WHERE id = $1
	`, proposalID).Scan(&proposalState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT delivery_state FROM planner_outbox WHERE id = $1
	`, request.OutboxID).Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if proposalState != "superseded" || outboxState != "pending" {
		t.Fatalf("unexpected mirror/proposal state: proposal=%s outbox=%s", proposalState, outboxState)
	}
	duplicate := request
	duplicate.PlanID = "a7777777-7777-7777-7777-777777777777"
	duplicate.IntentID = "a8888888-8888-8888-8888-888888888888"
	duplicate.OutboxID = "a9999999-9999-9999-9999-999999999999"
	duplicateResult, err := store.ReplaceCurrentPlan(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateResult.Duplicate || duplicateResult.IntentID != request.IntentID ||
		duplicateResult.ResultingCurrentPlanID == nil ||
		*duplicateResult.ResultingCurrentPlanID != request.PlanID {
		t.Fatalf("unexpected replacement replay: %+v", duplicateResult)
	}
}

func TestReplaceCurrentPlanHonorsMirrorCapacityAndRevision(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "b1111111-1111-1111-1111-111111111111",
		tripID:    "b2222222-2222-2222-2222-222222222222",
		intentID:  "b3333333-3333-3333-3333-333333333333",
		messageID: "b4444444-4444-4444-4444-444444444444",
		planID:    "b5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Capacity test user', 'America/New_York')
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
	request.MaxPendingCanonicalMirror = 1
	if _, err := store.ReplaceCurrentPlan(ctx, request); err != nil {
		t.Fatal(err)
	}
	second := request
	second.IntentID = "b6666666-6666-6666-6666-666666666666"
	second.OutboxID = "b7777777-7777-7777-7777-777777777777"
	second.MessageID = "b8888888-8888-8888-8888-888888888888"
	second.EventID = second.MessageID
	second.PlanID = "b9999999-9999-9999-9999-999999999999"
	second.ExpectedTripRevision = 2
	second.CommandPayload = []byte(`{"kind":"second_replace"}`)
	second.PayloadDigest = sha256.Sum256(second.CommandPayload)
	if _, err := store.ReplaceCurrentPlan(ctx, second); !errors.Is(err, ErrCanonicalMirrorCapacity) {
		t.Fatalf("expected mirror capacity rejection, got %v", err)
	}
	stale := request
	stale.IntentID = "b6666666-6666-6666-6666-666666666666"
	stale.OutboxID = "b7777777-7777-7777-7777-777777777777"
	stale.MessageID = "b8888888-8888-8888-8888-888888888888"
	stale.EventID = stale.MessageID
	stale.PlanID = "b9999999-9999-9999-9999-999999999999"
	stale.MaxPendingCanonicalMirror = 4
	if _, err := store.ReplaceCurrentPlan(ctx, stale); !errors.Is(err, ErrTripRevisionStale) {
		t.Fatalf("expected stale revision rejection, got %v", err)
	}
}

func TestReorderActivitiesCommitsCanonicalMirrorAndExactReplay(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "c1111111-1111-1111-1111-111111111111",
		tripID:    "c2222222-2222-2222-2222-222222222222",
		intentID:  "c3333333-3333-3333-3333-333333333333",
		messageID: "c4444444-4444-4444-4444-444444444444",
		planID:    "c5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Reorder test user', 'America/New_York')
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
	initial := createTripRequest(fixture)
	if _, err := store.CreateTrip(ctx, initial); err != nil {
		t.Fatal(err)
	}
	start := int64(1_784_000_200_000)
	end := start + 20*60*1000
	commandPayload := []byte(`{"kind":"trip_edited","operation":"reorder"}`)
	request := ReorderActivitiesRequest{
		TripID:                    fixture.tripID,
		OwnerUserID:               fixture.userID,
		IntentID:                  "c6666666-6666-6666-6666-666666666666",
		OutboxID:                  "c7777777-7777-7777-7777-777777777777",
		MessageID:                 "c8888888-8888-8888-8888-888888888888",
		EventID:                   "c8888888-8888-8888-8888-888888888888",
		PlanID:                    "c9999999-9999-9999-9999-999999999999",
		ExpectedTripRevision:      1,
		MaxPendingCanonicalMirror: 2,
		ActivityIDs:               []string{initial.Activities[1].ID, initial.Activities[0].ID},
		PlanSegments: []CanonicalPlanSegmentDraft{
			{ActivityID: initial.Activities[1].ID, Scheduled: true,
				Start: int64Ptr(start), End: int64Ptr(end)},
			{ActivityID: initial.Activities[0].ID},
		},
		CommandPayload: commandPayload,
		EventPayload:   []byte(`{"kind":"trip_edited","operation":"reorder"}`),
		PayloadDigest:  sha256.Sum256(commandPayload),
		OutcomePayload: []byte(`{"trip_revision":2}`),
	}
	result, err := store.ReorderActivities(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Kind != CommandTripEdited ||
		result.MutationSequence != 2 || result.RuntimeSyncState != "pending" ||
		result.ResultingTripRevision == nil || *result.ResultingTripRevision != 2 {
		t.Fatalf("unexpected reorder result: %+v", result)
	}
	state, err := store.Load(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TripRevision != 2 || state.CurrentPlanID != request.PlanID ||
		state.Activities[0].ID != initial.Activities[1].ID ||
		state.Activities[1].ID != initial.Activities[0].ID ||
		state.CurrentPlan.Revision != 2 {
		t.Fatalf("reorder did not become canonical: %+v", state)
	}
	duplicate := request
	duplicate.IntentID = "ca666666-6666-6666-6666-666666666666"
	duplicate.OutboxID = "ca777777-7777-7777-7777-777777777777"
	duplicate.PlanID = "ca888888-8888-8888-8888-888888888888"
	duplicateResult, err := store.ReorderActivities(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateResult.Duplicate || duplicateResult.IntentID != request.IntentID ||
		duplicateResult.OutboxID != request.OutboxID {
		t.Fatalf("unexpected reorder replay: %+v", duplicateResult)
	}
}

func TestRemoveActivityCommitsNormalizedStateAndExactReplay(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "d1111111-1111-1111-1111-111111111111",
		tripID:    "d2222222-2222-2222-2222-222222222222",
		intentID:  "d3333333-3333-3333-3333-333333333333",
		messageID: "d4444444-4444-4444-4444-444444444444",
		planID:    "d5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Remove test user', 'America/New_York')
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
	initial := createTripRequest(fixture)
	if _, err := store.CreateTrip(ctx, initial); err != nil {
		t.Fatal(err)
	}
	start := int64(1_784_000_300_000)
	commandPayload := []byte(`{"kind":"trip_edited","operation":"remove","activity_id":"` + initial.Activities[0].ID + `"}`)
	request := RemoveActivityRequest{
		TripID:                    fixture.tripID,
		OwnerUserID:               fixture.userID,
		IntentID:                  "d6666666-6666-6666-6666-666666666666",
		OutboxID:                  "d7777777-7777-7777-7777-777777777777",
		MessageID:                 "d8888888-8888-8888-8888-888888888888",
		EventID:                   "d8888888-8888-8888-8888-888888888888",
		PlanID:                    "d9999999-9999-9999-9999-999999999999",
		ExpectedTripRevision:      1,
		MaxPendingCanonicalMirror: 2,
		ActivityID:                initial.Activities[0].ID,
		PlanSegments: []CanonicalPlanSegmentDraft{{
			ActivityID: initial.Activities[1].ID,
			Scheduled:  true,
			Start:      int64Ptr(start),
			End:        int64Ptr(start + 20*60*1000),
		}},
		CommandPayload: commandPayload,
		EventPayload:   []byte(`{"kind":"trip_edited","operation":"remove"}`),
		PayloadDigest:  sha256.Sum256(commandPayload),
		OutcomePayload: []byte(`{"trip_revision":2}`),
	}
	result, err := store.RemoveActivity(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Kind != CommandTripEdited ||
		result.MutationSequence != 2 || result.RuntimeSyncState != "pending" ||
		result.ResultingTripRevision == nil || *result.ResultingTripRevision != 2 {
		t.Fatalf("unexpected remove result: %+v", result)
	}
	state, err := store.Load(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TripRevision != 2 || state.CurrentPlanID != request.PlanID ||
		len(state.Activities) != 1 || state.Activities[0].ID != initial.Activities[1].ID ||
		state.Activities[0].Ordinal != 0 || len(state.Activities[0].OpenWindows) != 0 ||
		len(state.TravelDelays) != 0 || state.CurrentPlan.Revision != 2 ||
		state.CurrentPlan.Origin != "user_authored" || len(state.CurrentPlan.Payload) == 0 {
		t.Fatalf("remove did not become canonical: %+v", state)
	}
	duplicate := request
	duplicate.IntentID = "da666666-6666-6666-6666-666666666666"
	duplicate.OutboxID = "da777777-7777-7777-7777-777777777777"
	duplicate.PlanID = "da888888-8888-8888-8888-888888888888"
	duplicateResult, err := store.RemoveActivity(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateResult.Duplicate || duplicateResult.IntentID != request.IntentID ||
		duplicateResult.OutboxID != request.OutboxID {
		t.Fatalf("unexpected remove replay: %+v", duplicateResult)
	}
}

func TestReplaceActivityCommitsNormalizedFieldsAndExactReplay(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "e1111111-1111-1111-1111-111111111111",
		tripID:    "e2222222-2222-2222-2222-222222222222",
		intentID:  "e3333333-3333-3333-3333-333333333333",
		messageID: "e4444444-4444-4444-4444-444444444444",
		planID:    "e5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Replace activity user', 'America/New_York')
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
	initial := createTripRequest(fixture)
	if _, err := store.CreateTrip(ctx, initial); err != nil {
		t.Fatal(err)
	}
	base := time.UnixMilli(1_784_001_000_000).UTC()
	replacement := initial.Activities[0]
	replacement.Ordinal = 99
	replacement.PlaceID = "place-replaced"
	replacement.DisplayName = "Replaced first"
	replacement.Latitude = 39
	replacement.Longitude = -75
	replacement.InboundTravelMode = "driving"
	replacement.ActivityClass = "fixed"
	replacement.ActivityDelaySeconds = 7
	replacement.PriorityRank = 4
	replacement.UtilityScore = 44
	replacement.OpenWindows = []CanonicalOpenWindow{{
		OpensAt: base.Add(2 * time.Hour), ClosesAt: base.Add(3 * time.Hour),
	}}
	commandPayload := []byte(`{"kind":"trip_edited","operation":"replace","activity_id":"` + replacement.ID + `"}`)
	request := ReplaceActivityRequest{
		TripID:                    fixture.tripID,
		OwnerUserID:               fixture.userID,
		IntentID:                  "e6666666-6666-6666-6666-666666666666",
		OutboxID:                  "e7777777-7777-7777-7777-777777777777",
		MessageID:                 "e8888888-8888-8888-8888-888888888888",
		EventID:                   "e8888888-8888-8888-8888-888888888888",
		PlanID:                    "e9999999-9999-9999-9999-999999999999",
		ExpectedTripRevision:      1,
		MaxPendingCanonicalMirror: 2,
		Activity:                  replacement,
		PlanSegments: []CanonicalPlanSegmentDraft{
			{ActivityID: replacement.ID, Scheduled: true,
				Start: int64Ptr(base.UnixMilli()),
				End:   int64Ptr(base.Add(20 * time.Minute).UnixMilli())},
			{ActivityID: initial.Activities[1].ID},
		},
		CommandPayload: commandPayload,
		EventPayload:   []byte(`{"kind":"trip_edited","operation":"replace"}`),
		PayloadDigest:  sha256.Sum256(commandPayload),
		OutcomePayload: []byte(`{"trip_revision":2}`),
	}
	result, err := store.ReplaceActivity(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Kind != CommandTripEdited ||
		result.MutationSequence != 2 || result.RuntimeSyncState != "pending" ||
		result.ResultingTripRevision == nil || *result.ResultingTripRevision != 2 {
		t.Fatalf("unexpected replace result: %+v", result)
	}
	state, err := store.Load(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	activity := state.Activities[0]
	if state.TripRevision != 2 || state.CurrentPlanID != request.PlanID ||
		activity.ID != replacement.ID || activity.Ordinal != 0 ||
		activity.PlaceID != replacement.PlaceID ||
		activity.DisplayName != replacement.DisplayName ||
		activity.Latitude != replacement.Latitude ||
		activity.Longitude != replacement.Longitude ||
		activity.InboundTravelMode != replacement.InboundTravelMode ||
		activity.ActivityClass != replacement.ActivityClass ||
		activity.ActivityDelaySeconds != replacement.ActivityDelaySeconds ||
		activity.PriorityRank != replacement.PriorityRank ||
		activity.UtilityScore != replacement.UtilityScore ||
		len(activity.OpenWindows) != 1 ||
		!activity.OpenWindows[0].OpensAt.Equal(replacement.OpenWindows[0].OpensAt) ||
		state.CurrentPlan.Revision != 2 || len(state.TravelDelays) != 1 {
		t.Fatalf("replace did not become canonical: %+v", state)
	}
	duplicate := request
	duplicate.IntentID = "ea666666-6666-6666-6666-666666666666"
	duplicate.OutboxID = "ea777777-7777-7777-7777-777777777777"
	duplicate.PlanID = "ea888888-8888-8888-8888-888888888888"
	duplicateResult, err := store.ReplaceActivity(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateResult.Duplicate || duplicateResult.IntentID != request.IntentID ||
		duplicateResult.OutboxID != request.OutboxID {
		t.Fatalf("unexpected replace replay: %+v", duplicateResult)
	}
}

func TestAddActivityCommitsNormalizedStateAndExactReplay(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "f1111111-1111-1111-1111-111111111111",
		tripID:    "f2222222-2222-2222-2222-222222222222",
		intentID:  "f3333333-3333-3333-3333-333333333333",
		messageID: "f4444444-4444-4444-4444-444444444444",
		planID:    "f5555555-5555-5555-5555-555555555555",
	}
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Add activity user', 'America/New_York')
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
	initial := createTripRequest(fixture)
	if _, err := store.CreateTrip(ctx, initial); err != nil {
		t.Fatal(err)
	}
	base := time.UnixMilli(1_784_002_000_000).UTC()
	newActivity := CanonicalActivity{
		ID:                       "f2222222-6666-6666-6666-666666666666",
		Ordinal:                  123,
		PlaceID:                  "place-added",
		DisplayName:              "Added activity",
		Latitude:                 42,
		Longitude:                -72,
		TimeZoneName:             "America/New_York",
		InboundTravelMode:        "walking",
		ActivityClass:            "flexible",
		ActivityState:            ActivityStatePlanned,
		PriorityRank:             3,
		UtilityScore:             30,
		MinDurationSeconds:       60,
		PreferredDurationSeconds: 120,
		MaxDurationSeconds:       180,
		CanMove:                  true,
		CanSkip:                  true,
		OpenWindows: []CanonicalOpenWindow{{
			OpensAt: base, ClosesAt: base.Add(time.Hour),
		}},
	}
	commandPayload := []byte(`{"kind":"trip_edited","operation":"add","activity_id":"` + newActivity.ID + `"}`)
	request := AddActivityRequest{
		TripID:                    fixture.tripID,
		OwnerUserID:               fixture.userID,
		IntentID:                  "f6666666-6666-6666-6666-666666666666",
		OutboxID:                  "f7777777-7777-7777-7777-777777777777",
		MessageID:                 "f8888888-8888-8888-8888-888888888888",
		EventID:                   "f8888888-8888-8888-8888-888888888888",
		PlanID:                    "f9999999-9999-9999-9999-999999999999",
		ExpectedTripRevision:      1,
		MaxPendingCanonicalMirror: 2,
		Ordinal:                   1,
		Activity:                  newActivity,
		PlanSegments: []CanonicalPlanSegmentDraft{
			{ActivityID: initial.Activities[0].ID, Scheduled: true,
				Start: int64Ptr(base.UnixMilli()),
				End:   int64Ptr(base.Add(20 * time.Minute).UnixMilli())},
			{ActivityID: newActivity.ID},
			{ActivityID: initial.Activities[1].ID},
		},
		CommandPayload: commandPayload,
		EventPayload:   []byte(`{"kind":"trip_edited","operation":"add"}`),
		PayloadDigest:  sha256.Sum256(commandPayload),
		OutcomePayload: []byte(`{"trip_revision":2}`),
	}
	result, err := store.AddActivity(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Kind != CommandTripEdited ||
		result.MutationSequence != 2 || result.RuntimeSyncState != "pending" ||
		result.ResultingTripRevision == nil || *result.ResultingTripRevision != 2 {
		t.Fatalf("unexpected add result: %+v", result)
	}
	state, err := store.Load(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TripRevision != 2 || state.CurrentPlanID != request.PlanID ||
		len(state.Activities) != 3 || state.Activities[0].ID != initial.Activities[0].ID ||
		state.Activities[1].ID != newActivity.ID ||
		state.Activities[2].ID != initial.Activities[1].ID ||
		state.Activities[1].Ordinal != 1 ||
		state.Activities[1].PlaceID != newActivity.PlaceID ||
		state.Activities[1].DisplayName != newActivity.DisplayName ||
		len(state.Activities[1].OpenWindows) != 1 ||
		!state.Activities[1].OpenWindows[0].OpensAt.Equal(newActivity.OpenWindows[0].OpensAt) ||
		len(state.TravelDelays) != 1 || state.CurrentPlan.Revision != 2 {
		t.Fatalf("add did not become canonical: %+v", state)
	}
	duplicate := request
	duplicate.IntentID = "fa666666-6666-6666-6666-666666666666"
	duplicate.OutboxID = "fa777777-7777-7777-7777-777777777777"
	duplicate.PlanID = "fa888888-8888-8888-8888-888888888888"
	duplicateResult, err := store.AddActivity(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateResult.Duplicate || duplicateResult.IntentID != request.IntentID ||
		duplicateResult.OutboxID != request.OutboxID {
		t.Fatalf("unexpected add replay: %+v", duplicateResult)
	}
}
