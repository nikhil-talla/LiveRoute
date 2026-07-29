package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

func appendTestVarint(output []byte, value uint64) []byte {
	for value >= 0x80 {
		output = append(output, byte(value)|0x80)
		value >>= 7
	}
	return append(output, byte(value))
}

func appendTestVarintField(output []byte, number, value uint64) []byte {
	output = appendTestVarint(output, number<<3)
	return appendTestVarint(output, value)
}

func appendTestBytesField(output []byte, number uint64, value []byte) []byte {
	output = appendTestVarint(output, number<<3|2)
	output = appendTestVarint(output, uint64(len(value)))
	return append(output, value...)
}

func testProposalSegment(
	activityID string,
	start int64,
	end int64,
) []byte {
	var output []byte
	output = appendTestBytesField(output, 1, []byte(activityID))
	output = appendTestVarintField(output, 4, uint64(start))
	output = appendTestVarintField(output, 5, uint64(end))
	return appendTestVarintField(output, 7, 2)
}

func testStoredProposal(
	proposalID string,
	activityID string,
) []byte {
	var proposal []byte
	proposal = appendTestBytesField(proposal, 1, []byte(proposalID))
	proposal = appendTestBytesField(
		proposal,
		8,
		testProposalSegment(activityID, 1_000, 2_000),
	)
	return appendTestBytesField(nil, 1, proposal)
}

func testCurrentPlan(
	planID string,
	proposalID string,
	activityID string,
	end int64,
) []byte {
	var segment []byte
	segment = appendTestBytesField(segment, 1, []byte(activityID))
	segment = appendTestVarintField(segment, 2, 1)
	segment = appendTestVarintField(segment, 3, 1_000)
	segment = appendTestVarintField(segment, 4, uint64(end))

	var plan []byte
	plan = appendTestBytesField(plan, 1, []byte(planID))
	plan = appendTestVarintField(plan, 2, 2)
	plan = appendTestVarintField(plan, 3, 2)
	plan = appendTestBytesField(plan, 4, segment)
	plan = appendTestVarintField(plan, 5, uint64(1_784_000_000_123))
	return appendTestBytesField(plan, 6, []byte(proposalID))
}

type acceptanceFixture struct {
	trip       commandTripFixture
	proposalID string
	activityID string
	planID     string
	recorded   RecordedCommand
}

func createAcceptanceFixture(
	t *testing.T,
	prefix string,
	plannedEnd int64,
) (*CommandStore, acceptanceFixture) {
	t.Helper()
	pool, ctx := openPersistenceTestPool(t)
	fixture := acceptanceFixture{
		trip: commandTripFixture{
			userID: prefix + "111111-1111-1111-1111-111111111111",
			tripID: prefix + "222222-2222-2222-2222-222222222222",
			planID: prefix + "333333-3333-3333-3333-333333333333",
		},
		proposalID: prefix + "444444-4444-4444-4444-444444444444",
		activityID: prefix + "555555-5555-5555-5555-555555555555",
		planID:     prefix + "666666-6666-6666-6666-666666666666",
	}
	holderID := prefix + "777777-7777-7777-7777-777777777777"
	createCommandTrip(t, ctx, pool, fixture.trip)
	if _, err := pool.Exec(ctx, `
		INSERT INTO trip_activities (
			id, trip_id, ordinal, place_id, display_name,
			latitude, longitude, time_zone_name, inbound_travel_mode,
			activity_class, activity_state, priority_rank, utility_score,
			reservation_grace_seconds, min_duration_seconds,
			preferred_duration_seconds, max_duration_seconds,
			mandatory, can_shorten, can_move, can_skip
		) VALUES (
			$1, $2, 0, 'fixture-place', 'Fixture activity',
			40.0, -74.0, 'America/New_York', 'walking',
			'flexible', 'planned', 1, 1,
			0, 60, 60, 60, false, false, true, true
		)
	`, fixture.activityID, fixture.trip.tripID); err != nil {
		t.Fatal(err)
	}
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseStore.Acquire(
		ctx,
		fixture.trip.tripID,
		holderID,
		30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	proposalPayload := testStoredProposal(
		fixture.proposalID,
		fixture.activityID,
	)
	proposalRequest := proposalRequest(
		fixture.trip,
		fixture.proposalID,
		holderID,
		13,
	)
	proposalRequest.Payload = proposalPayload
	proposalRequest.Checksum = sha256.Sum256(proposalPayload)
	proposalStore, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposalStore.Persist(ctx, proposalRequest); err != nil {
		t.Fatal(err)
	}

	plannedPayload := testCurrentPlan(
		fixture.planID,
		fixture.proposalID,
		fixture.activityID,
		plannedEnd,
	)
	command := commandRequest(
		fixture.trip,
		prefix+"888888-8888-8888-8888-888888888888",
		prefix+"999999-9999-9999-9999-999999999999",
		prefix+"aaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	)
	command.Kind = CommandAcceptProposal
	command.PlannedCurrentPlan = &PlannedCurrentPlan{
		ID:       fixture.planID,
		Payload:  plannedPayload,
		Checksum: sha256.Sum256(plannedPayload),
	}
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture.recorded, err = store.RecordRuntimeFirst(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func acceptanceRequest(
	fixture acceptanceFixture,
) FinalizeProposalDecisionRequest {
	return FinalizeProposalDecisionRequest{
		TripID:                       fixture.recorded.TripID,
		IntentID:                     fixture.recorded.IntentID,
		OutboxID:                     fixture.recorded.OutboxID,
		EventID:                      fixture.recorded.EventID,
		MutationSequence:             fixture.recorded.MutationSequence,
		ExpectedTripRevision:         fixture.recorded.ExpectedTripRevision,
		ResultingPlannerStateVersion: 14,
		Identity: ProposalDecisionIdentity{
			ProposalID:                fixture.proposalID,
			SourceRuntimeEpoch:        1,
			SourcePlannerStateVersion: 13,
			BaseCurrentPlanID:         fixture.trip.planID,
		},
		OutcomePayload: []byte(`{"safe_message":"proposal accepted"}`),
	}
}

func TestFinalizeProposalAcceptancePublishesExactImmutablePlan(
	t *testing.T,
) {
	store, fixture := createAcceptanceFixture(t, "a1", 2_000)
	ctx := context.Background()
	request := acceptanceRequest(fixture)
	finalized, err := store.FinalizeProposalAcceptance(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Duplicate ||
		finalized.Status != "OK" ||
		finalized.ResultingTripRevision != 2 {
		t.Fatalf("unexpected proposal acceptance: %+v", finalized)
	}
	var currentPlanID string
	var tripRevision int64
	var finalizedSequence int64
	var proposalState string
	var resultingPlanID string
	var origin string
	var sourceProposalID string
	var planRevision int64
	if err := store.pool.QueryRow(ctx, `
		SELECT trip.current_plan_id::text,
		       trip.trip_revision,
		       trip.finalized_mutation_sequence,
		       proposal.state,
		       proposal.resulting_current_plan_id::text,
		       plan.origin,
		       plan.source_proposal_id::text,
		       plan.plan_revision
		FROM trips AS trip
		JOIN plan_proposals AS proposal ON proposal.trip_id = trip.id
		JOIN itinerary_plans AS plan
		  ON plan.trip_id = trip.id
		 AND plan.id = trip.current_plan_id
		WHERE trip.id = $1 AND proposal.id = $2
	`, fixture.trip.tripID, fixture.proposalID).Scan(
		&currentPlanID,
		&tripRevision,
		&finalizedSequence,
		&proposalState,
		&resultingPlanID,
		&origin,
		&sourceProposalID,
		&planRevision,
	); err != nil {
		t.Fatal(err)
	}
	if currentPlanID != fixture.planID ||
		tripRevision != 2 ||
		finalizedSequence != 2 ||
		proposalState != "accepted" ||
		resultingPlanID != fixture.planID ||
		origin != "accepted_engine_proposal" ||
		sourceProposalID != fixture.proposalID ||
		planRevision != 2 {
		t.Fatalf(
			"accepted plan transaction is inconsistent: current=%s revision=%d finalized=%d proposal=%s resulting=%s origin=%s source=%s plan_revision=%d",
			currentPlanID,
			tripRevision,
			finalizedSequence,
			proposalState,
			resultingPlanID,
			origin,
			sourceProposalID,
			planRevision,
		)
	}
	duplicate, err := store.FinalizeProposalAcceptance(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatalf("exact acceptance replay was not duplicate: %+v", duplicate)
	}
}

func TestFinalizeProposalAcceptanceRejectsMappingMismatchAtomically(
	t *testing.T,
) {
	store, fixture := createAcceptanceFixture(t, "b1", 2_001)
	ctx := context.Background()
	if _, err := store.FinalizeProposalAcceptance(
		ctx,
		acceptanceRequest(fixture),
	); !errors.Is(err, ErrProposalPayloadInvalid) {
		t.Fatalf("expected proposal-plan mapping rejection, got %v", err)
	}
	var currentPlanID string
	var revision int64
	var finalizedSequence int64
	var proposalState string
	var planCount int
	if err := store.pool.QueryRow(ctx, `
		SELECT trip.current_plan_id::text,
		       trip.trip_revision,
		       trip.finalized_mutation_sequence,
		       proposal.state,
		       (SELECT count(*) FROM itinerary_plans
		        WHERE trip_id = trip.id)
		FROM trips AS trip
		JOIN plan_proposals AS proposal ON proposal.trip_id = trip.id
		WHERE trip.id = $1
	`, fixture.trip.tripID).Scan(
		&currentPlanID,
		&revision,
		&finalizedSequence,
		&proposalState,
		&planCount,
	); err != nil {
		t.Fatal(err)
	}
	if currentPlanID != fixture.trip.planID ||
		revision != 1 ||
		finalizedSequence != 1 ||
		proposalState != "pending" ||
		planCount != 1 {
		t.Fatalf(
			"invalid acceptance partially committed: current=%s revision=%d finalized=%d proposal=%s plans=%d",
			currentPlanID,
			revision,
			finalizedSequence,
			proposalState,
			planCount,
		)
	}
}

func TestFinalizeProposalAcceptanceConcurrentReplayAppliesOnce(
	t *testing.T,
) {
	store, fixture := createAcceptanceFixture(t, "c1", 2_000)
	ctx := context.Background()
	request := acceptanceRequest(fixture)
	start := make(chan struct{})
	results := make([]FinalizedCommand, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] =
				store.FinalizeProposalAcceptance(ctx, request)
		}(index)
	}
	close(start)
	wait.Wait()
	duplicates := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent acceptance failed: %v", err)
		}
		if results[index].Duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("expected one duplicate acceptance, got %d", duplicates)
	}
}
