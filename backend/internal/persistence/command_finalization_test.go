package persistence

import (
	"errors"
	"sync"
	"testing"
)

func terminalRequest(
	recorded RecordedCommand,
	status TerminalStatus,
) FinalizeTerminalCommandRequest {
	return FinalizeTerminalCommandRequest{
		TripID:                       recorded.TripID,
		IntentID:                     recorded.IntentID,
		OutboxID:                     recorded.OutboxID,
		EventID:                      recorded.EventID,
		MutationSequence:             recorded.MutationSequence,
		ExpectedTripRevision:         recorded.ExpectedTripRevision,
		ResultingPlannerStateVersion: 17,
		Status:                       status,
		OutcomePayload:               []byte(`{"safe_message":"rejected"}`),
	}
}

func TestFinalizeTerminalConsumesSequenceWithoutChangingRevision(
	t *testing.T,
) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "a1111111-1111-1111-1111-111111111111",
		tripID: "a2222222-2222-2222-2222-222222222222",
		planID: "a3333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordRuntimeFirst(ctx, commandRequest(
		fixture,
		"a4444444-4444-4444-4444-444444444444",
		"a5555555-5555-5555-5555-555555555555",
		"a6666666-6666-6666-6666-666666666666",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := terminalRequest(recorded, TerminalStatusInvalidArgument)
	finalized, err := store.FinalizeTerminal(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Duplicate ||
		finalized.State != "rejected" ||
		finalized.Status != "INVALID_ARGUMENT" ||
		finalized.ResultingTripRevision != 1 ||
		finalized.ResultingPlannerStateVersion != 17 ||
		finalized.FinalizedAt.IsZero() {
		t.Fatalf("unexpected terminal result: %+v", finalized)
	}

	var tripRevision int64
	var finalizedSequence int64
	var currentPlanID string
	var intentState string
	var outboxState string
	var lastStatus string
	var claimOwner *string
	if err := pool.QueryRow(ctx, `
		SELECT trip_revision,
		       finalized_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
	`, fixture.tripID).Scan(
		&tripRevision,
		&finalizedSequence,
		&currentPlanID,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT intent.state,
		       outbox.delivery_state,
		       outbox.last_status,
		       outbox.claim_owner::text
		FROM command_intents AS intent
		JOIN planner_outbox AS outbox
		  ON outbox.command_intent_id = intent.id
		WHERE intent.id = $1
	`, recorded.IntentID).Scan(
		&intentState,
		&outboxState,
		&lastStatus,
		&claimOwner,
	); err != nil {
		t.Fatal(err)
	}
	if tripRevision != 1 ||
		finalizedSequence != 2 ||
		currentPlanID != fixture.planID ||
		intentState != "rejected" ||
		outboxState != "terminal_rejected" ||
		lastStatus != "INVALID_ARGUMENT" ||
		claimOwner != nil {
		t.Fatalf(
			"terminal finalization mutated the wrong state: revision=%d finalized=%d plan=%s intent=%s outbox=%s status=%s claim=%v",
			tripRevision,
			finalizedSequence,
			currentPlanID,
			intentState,
			outboxState,
			lastStatus,
			claimOwner,
		)
	}

	duplicate, err := store.FinalizeTerminal(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate ||
		duplicate.FinalizedAt != finalized.FinalizedAt {
		t.Fatalf("unexpected duplicate finalization: %+v", duplicate)
	}

	conflict := request
	conflict.Status = TerminalStatusNotFound
	if _, err := store.FinalizeTerminal(
		ctx,
		conflict,
	); !errors.Is(err, ErrCommandFinalizationConflict) {
		t.Fatalf("expected conflicting replay rejection, got %v", err)
	}
}

func TestFinalizeTerminalMarksLogicalExpiry(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "b1111111-1111-1111-1111-111111111111",
		tripID: "b2222222-2222-2222-2222-222222222222",
		planID: "b3333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordRuntimeFirst(ctx, commandRequest(
		fixture,
		"b4444444-4444-4444-4444-444444444444",
		"b5555555-5555-5555-5555-555555555555",
		"b6666666-6666-6666-6666-666666666666",
	))
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := store.FinalizeTerminal(
		ctx,
		terminalRequest(recorded, TerminalStatusCommandExpired),
	)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.State != "expired" {
		t.Fatalf("logical expiry stored as %q", finalized.State)
	}
}

func TestFinalizeTerminalRejectsTransientAndUncorrelatedOutcomes(
	t *testing.T,
) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "c1111111-1111-1111-1111-111111111111",
		tripID: "c2222222-2222-2222-2222-222222222222",
		planID: "c3333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordRuntimeFirst(ctx, commandRequest(
		fixture,
		"c4444444-4444-4444-4444-444444444444",
		"c5555555-5555-5555-5555-555555555555",
		"c6666666-6666-6666-6666-666666666666",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := terminalRequest(recorded, TerminalStatus("UNAVAILABLE"))
	if _, err := store.FinalizeTerminal(ctx, request); err == nil {
		t.Fatal("transient outcome was finalized terminally")
	}

	request = terminalRequest(recorded, TerminalStatusNotFound)
	request.EventID = "c7777777-7777-7777-7777-777777777777"
	if _, err := store.FinalizeTerminal(
		ctx,
		request,
	); !errors.Is(err, ErrCommandFinalizationConflict) {
		t.Fatalf("expected event-correlation rejection, got %v", err)
	}
	var finalizedSequence int64
	if err := pool.QueryRow(
		ctx,
		"SELECT finalized_mutation_sequence FROM trips WHERE id = $1",
		fixture.tripID,
	).Scan(&finalizedSequence); err != nil {
		t.Fatal(err)
	}
	if finalizedSequence != 1 {
		t.Fatalf("uncorrelated result advanced watermark to %d", finalizedSequence)
	}
}

func TestFinalizeTerminalConcurrentReplayAppliesOnce(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "d1111111-1111-1111-1111-111111111111",
		tripID: "d2222222-2222-2222-2222-222222222222",
		planID: "d3333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordRuntimeFirst(ctx, commandRequest(
		fixture,
		"d4444444-4444-4444-4444-444444444444",
		"d5555555-5555-5555-5555-555555555555",
		"d6666666-6666-6666-6666-666666666666",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := terminalRequest(recorded, TerminalStatusInfeasible)

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
				store.FinalizeTerminal(ctx, request)
		}(index)
	}
	close(start)
	wait.Wait()

	duplicates := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent finalization failed: %v", err)
		}
		if results[index].Duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("expected one duplicate finalization, got %d", duplicates)
	}
}

func TestFinalizeTerminalRequiresContiguousWatermark(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "e1111111-1111-1111-1111-111111111111",
		tripID: "e2222222-2222-2222-2222-222222222222",
		planID: "e3333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordRuntimeFirst(ctx, commandRequest(
		fixture,
		"e4444444-4444-4444-4444-444444444444",
		"e5555555-5555-5555-5555-555555555555",
		"e6666666-6666-6666-6666-666666666666",
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE trips
		SET finalized_mutation_sequence = 0
		WHERE id = $1
	`, fixture.tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeTerminal(
		ctx,
		terminalRequest(recorded, TerminalStatusNotFound),
	); !errors.Is(err, ErrCommandFinalizationOutOfOrder) {
		t.Fatalf("expected non-contiguous watermark rejection, got %v", err)
	}
}

func TestFinalizeTerminalDefersStaleProposalDecision(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "f1111111-1111-1111-1111-111111111111",
		tripID: "f2222222-2222-2222-2222-222222222222",
		planID: "f3333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	request := commandRequest(
		fixture,
		"f4444444-4444-4444-4444-444444444444",
		"f5555555-5555-5555-5555-555555555555",
		"f6666666-6666-6666-6666-666666666666",
	)
	request.Kind = CommandRejectProposal
	recorded, err := store.RecordRuntimeFirst(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeTerminal(
		ctx,
		terminalRequest(recorded, TerminalStatusStale),
	); !errors.Is(err, ErrProposalFinalizationRequired) {
		t.Fatalf("expected proposal-aware finalization requirement, got %v", err)
	}
}
