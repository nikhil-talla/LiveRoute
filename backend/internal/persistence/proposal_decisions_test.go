package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type proposalDecisionFixture struct {
	trip     commandTripFixture
	holderID string
	proposal PersistProposalRequest
	recorded RecordedCommand
}

func createProposalDecisionFixture(
	t *testing.T,
	userPrefix string,
) (*CommandStore, proposalDecisionFixture) {
	t.Helper()
	pool, ctx := openPersistenceTestPool(t)
	fixture := proposalDecisionFixture{
		trip: commandTripFixture{
			userID: userPrefix + "111111-1111-1111-1111-111111111111",
			tripID: userPrefix + "222222-2222-2222-2222-222222222222",
			planID: userPrefix + "333333-3333-3333-3333-333333333333",
		},
		holderID: userPrefix + "444444-4444-4444-4444-444444444444",
	}
	createCommandTrip(t, ctx, pool, fixture.trip)
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseStore.Acquire(
		ctx,
		fixture.trip.tripID,
		fixture.holderID,
		30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	fixture.proposal = proposalRequest(
		fixture.trip,
		userPrefix+"555555-5555-5555-5555-555555555555",
		fixture.holderID,
		11,
	)
	proposalStore, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposalStore.Persist(ctx, fixture.proposal); err != nil {
		t.Fatal(err)
	}
	commandStore, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	command := commandRequest(
		fixture.trip,
		userPrefix+"666666-6666-6666-6666-666666666666",
		userPrefix+"777777-7777-7777-7777-777777777777",
		userPrefix+"888888-8888-8888-8888-888888888888",
	)
	command.Kind = CommandRejectProposal
	fixture.recorded, err = commandStore.RecordRuntimeFirst(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	return commandStore, fixture
}

func proposalDecisionRequest(
	fixture proposalDecisionFixture,
) FinalizeProposalDecisionRequest {
	return FinalizeProposalDecisionRequest{
		TripID:                       fixture.recorded.TripID,
		IntentID:                     fixture.recorded.IntentID,
		OutboxID:                     fixture.recorded.OutboxID,
		EventID:                      fixture.recorded.EventID,
		MutationSequence:             fixture.recorded.MutationSequence,
		ExpectedTripRevision:         fixture.recorded.ExpectedTripRevision,
		ResultingPlannerStateVersion: 12,
		Identity: ProposalDecisionIdentity{
			ProposalID:                fixture.proposal.ProposalID,
			SourceRuntimeEpoch:        fixture.proposal.Source.RuntimeEpoch,
			SourcePlannerStateVersion: fixture.proposal.Source.PlannerStateVersion,
			BaseCurrentPlanID:         fixture.proposal.Source.BaseCurrentPlanID,
		},
		OutcomePayload: []byte(`{"safe_message":"proposal rejected"}`),
	}
}

func TestFinalizeProposalRejectionAdvancesRevisionWithoutChangingPlan(
	t *testing.T,
) {
	store, fixture := createProposalDecisionFixture(t, "61")
	ctx := context.Background()
	request := proposalDecisionRequest(fixture)
	finalized, err := store.FinalizeProposalRejection(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Duplicate ||
		finalized.State != "applied" ||
		finalized.Status != "OK" ||
		finalized.ResultingTripRevision != 2 {
		t.Fatalf("unexpected rejected-proposal result: %+v", finalized)
	}

	var revision int64
	var finalizedSequence int64
	var currentPlanID string
	var proposalState string
	var decisionMessageID string
	if err := store.pool.QueryRow(ctx, `
		SELECT trip.trip_revision,
		       trip.finalized_mutation_sequence,
		       trip.current_plan_id::text,
		       proposal.state,
		       proposal.decision_message_id::text
		FROM trips AS trip
		JOIN plan_proposals AS proposal
		  ON proposal.trip_id = trip.id
		WHERE trip.id = $1 AND proposal.id = $2
	`, fixture.trip.tripID, fixture.proposal.ProposalID).Scan(
		&revision,
		&finalizedSequence,
		&currentPlanID,
		&proposalState,
		&decisionMessageID,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 2 ||
		finalizedSequence != 2 ||
		currentPlanID != fixture.trip.planID ||
		proposalState != "rejected" ||
		decisionMessageID != fixture.recorded.MessageID {
		t.Fatalf(
			"proposal rejection stored wrong authority: revision=%d finalized=%d plan=%s proposal=%s decision=%s",
			revision,
			finalizedSequence,
			currentPlanID,
			proposalState,
			decisionMessageID,
		)
	}
	duplicate, err := store.FinalizeProposalRejection(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatalf("exact replay was not duplicate: %+v", duplicate)
	}
}

func TestFinalizeStaleProposalDecisionConsumesOnlySequence(t *testing.T) {
	store, fixture := createProposalDecisionFixture(t, "71")
	ctx := context.Background()
	request := proposalDecisionRequest(fixture)
	finalized, err := store.FinalizeStaleProposalDecision(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.State != "rejected" ||
		finalized.Status != "STALE" ||
		finalized.ResultingTripRevision != 1 {
		t.Fatalf("unexpected stale decision result: %+v", finalized)
	}
	var revision int64
	var finalizedSequence int64
	var currentPlanID string
	var proposalState string
	if err := store.pool.QueryRow(ctx, `
		SELECT trip.trip_revision,
		       trip.finalized_mutation_sequence,
		       trip.current_plan_id::text,
		       proposal.state
		FROM trips AS trip
		JOIN plan_proposals AS proposal
		  ON proposal.trip_id = trip.id
		WHERE trip.id = $1 AND proposal.id = $2
	`, fixture.trip.tripID, fixture.proposal.ProposalID).Scan(
		&revision,
		&finalizedSequence,
		&currentPlanID,
		&proposalState,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 1 ||
		finalizedSequence != 2 ||
		currentPlanID != fixture.trip.planID ||
		proposalState != "stale" {
		t.Fatalf(
			"stale decision changed authority: revision=%d finalized=%d plan=%s proposal=%s",
			revision,
			finalizedSequence,
			currentPlanID,
			proposalState,
		)
	}
}

func TestFinalizeProposalDecisionRejectsIdentityMismatch(t *testing.T) {
	store, fixture := createProposalDecisionFixture(t, "81")
	ctx := context.Background()
	request := proposalDecisionRequest(fixture)
	request.Identity.SourcePlannerStateVersion++
	if _, err := store.FinalizeProposalRejection(
		ctx,
		request,
	); !errors.Is(err, ErrCommandFinalizationConflict) {
		t.Fatalf("expected proposal identity conflict, got %v", err)
	}
	var finalizedSequence int64
	if err := store.pool.QueryRow(
		ctx,
		"SELECT finalized_mutation_sequence FROM trips WHERE id = $1",
		fixture.trip.tripID,
	).Scan(&finalizedSequence); err != nil {
		t.Fatal(err)
	}
	if finalizedSequence != 1 {
		t.Fatalf("identity mismatch advanced watermark to %d", finalizedSequence)
	}
}

func TestFinalizeProposalRejectionConcurrentReplayAppliesOnce(
	t *testing.T,
) {
	store, fixture := createProposalDecisionFixture(t, "91")
	ctx := context.Background()
	request := proposalDecisionRequest(fixture)
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
				store.FinalizeProposalRejection(ctx, request)
		}(index)
	}
	close(start)
	wait.Wait()
	duplicates := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent proposal decision failed: %v", err)
		}
		if results[index].Duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("expected one duplicate proposal decision, got %d", duplicates)
	}
}
