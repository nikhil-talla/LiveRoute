package persistence

import (
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

func proposalRequest(
	fixture commandTripFixture,
	proposalID string,
	holderID string,
	plannerStateVersion uint64,
) PersistProposalRequest {
	payload := []byte("stored-proposal-" + proposalID)
	return PersistProposalRequest{
		ProposalID: proposalID,
		TripID:     fixture.tripID,
		Source: ProposalSource{
			RuntimeEpoch:             1,
			PlannerStateVersion:      plannerStateVersion,
			TripRevision:             1,
			AcceptedMutationSequence: 1,
			BaseCurrentPlanID:        fixture.planID,
		},
		Current: RuntimeFreshness{
			HolderID:                 holderID,
			RuntimeEpoch:             1,
			PlannerStateVersion:      plannerStateVersion,
			AcceptedMutationSequence: 1,
		},
		Payload:   payload,
		Checksum:  sha256.Sum256(payload),
		CreatedAt: time.UnixMilli(1_784_000_000_123).UTC(),
	}
}

func TestPersistProposalIsIdempotentAndSupersedesOlderPending(
	t *testing.T,
) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "10111111-1111-1111-1111-111111111111",
		tripID: "10222222-2222-2222-2222-222222222222",
		planID: "10333333-3333-3333-3333-333333333333",
	}
	holderID := "10444444-4444-4444-4444-444444444444"
	createCommandTrip(t, ctx, pool, fixture)
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseStore.Acquire(
		ctx,
		fixture.tripID,
		holderID,
		30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	store, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}

	firstRequest := proposalRequest(
		fixture,
		"10555555-5555-5555-5555-555555555555",
		holderID,
		7,
	)
	first, err := store.Persist(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !first.Publishable ||
		first.State != "pending" ||
		first.SupersededProposalCount != 0 {
		t.Fatalf("unexpected first proposal: %+v", first)
	}

	duplicate, err := store.Persist(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || !duplicate.Publishable ||
		duplicate.State != "pending" {
		t.Fatalf("unexpected duplicate proposal: %+v", duplicate)
	}

	conflict := firstRequest
	conflict.Payload = []byte("different")
	conflict.Checksum = sha256.Sum256(conflict.Payload)
	if _, err := store.Persist(
		ctx,
		conflict,
	); !errors.Is(err, ErrProposalIdentityConflict) {
		t.Fatalf("expected proposal identity conflict, got %v", err)
	}

	secondRequest := proposalRequest(
		fixture,
		"10666666-6666-6666-6666-666666666666",
		holderID,
		7,
	)
	secondRequest.CreatedAt = firstRequest.CreatedAt.Add(time.Millisecond)
	second, err := store.Persist(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Publishable || second.SupersededProposalCount != 1 {
		t.Fatalf("unexpected replacement proposal: %+v", second)
	}

	var tripRevision int64
	var finalizedSequence int64
	var currentPlanID string
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
	var firstState string
	var firstDecidedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT state, decided_at
		FROM plan_proposals
		WHERE id = $1
	`, firstRequest.ProposalID).Scan(
		&firstState,
		&firstDecidedAt,
	); err != nil {
		t.Fatal(err)
	}
	if tripRevision != 1 ||
		finalizedSequence != 1 ||
		currentPlanID != fixture.planID ||
		firstState != "superseded" ||
		firstDecidedAt == nil {
		t.Fatalf(
			"proposal persistence changed authority incorrectly: revision=%d finalized=%d plan=%s first=%s decided=%v",
			tripRevision,
			finalizedSequence,
			currentPlanID,
			firstState,
			firstDecidedAt,
		)
	}
}

func TestPersistProposalRejectsStaleRuntimeAndUnfinalizedSource(
	t *testing.T,
) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "20111111-1111-1111-1111-111111111111",
		tripID: "20222222-2222-2222-2222-222222222222",
		planID: "20333333-3333-3333-3333-333333333333",
	}
	holderID := "20444444-4444-4444-4444-444444444444"
	createCommandTrip(t, ctx, pool, fixture)
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseStore.Acquire(
		ctx,
		fixture.tripID,
		holderID,
		30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	store, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}

	staleVersion := proposalRequest(
		fixture,
		"20555555-5555-5555-5555-555555555555",
		holderID,
		7,
	)
	staleVersion.Current.PlannerStateVersion = 8
	if _, err := store.Persist(
		ctx,
		staleVersion,
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("expected planner-version stale result, got %v", err)
	}

	unfinalized := proposalRequest(
		fixture,
		"20666666-6666-6666-6666-666666666666",
		holderID,
		8,
	)
	unfinalized.Source.AcceptedMutationSequence = 2
	unfinalized.Current.AcceptedMutationSequence = 2
	if _, err := store.Persist(
		ctx,
		unfinalized,
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("expected unfinalized-source rejection, got %v", err)
	}

	wrongHolder := proposalRequest(
		fixture,
		"20777777-7777-7777-7777-777777777777",
		"20888888-8888-8888-8888-888888888888",
		8,
	)
	if _, err := store.Persist(
		ctx,
		wrongHolder,
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("expected old-holder rejection, got %v", err)
	}

	var count int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM plan_proposals WHERE trip_id = $1",
		fixture.tripID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale proposal attempts inserted %d rows", count)
	}
}

func TestPersistProposalConcurrentExactReplayInsertsOnce(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "30111111-1111-1111-1111-111111111111",
		tripID: "30222222-2222-2222-2222-222222222222",
		planID: "30333333-3333-3333-3333-333333333333",
	}
	holderID := "30444444-4444-4444-4444-444444444444"
	createCommandTrip(t, ctx, pool, fixture)
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseStore.Acquire(
		ctx,
		fixture.tripID,
		holderID,
		30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	store, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	request := proposalRequest(
		fixture,
		"30555555-5555-5555-5555-555555555555",
		holderID,
		9,
	)

	start := make(chan struct{})
	results := make([]PersistedProposal, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = store.Persist(ctx, request)
		}(index)
	}
	close(start)
	wait.Wait()

	duplicates := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent proposal persistence failed: %v", err)
		}
		if results[index].Duplicate {
			duplicates++
		}
		if !results[index].Publishable {
			t.Fatalf("exact replay was not publishable: %+v", results[index])
		}
	}
	if duplicates != 1 {
		t.Fatalf("expected one duplicate persistence, got %d", duplicates)
	}
	var count int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM plan_proposals WHERE trip_id = $1",
		fixture.tripID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent replay inserted %d proposals", count)
	}
}

func TestLeaseTakeoverMarksOlderPendingProposalStale(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "50111111-1111-1111-1111-111111111111",
		tripID: "50222222-2222-2222-2222-222222222222",
		planID: "50333333-3333-3333-3333-333333333333",
	}
	firstHolder := "50444444-4444-4444-4444-444444444444"
	secondHolder := "50555555-5555-5555-5555-555555555555"
	createCommandTrip(t, ctx, pool, fixture)
	leaseStore, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseStore.Acquire(
		ctx,
		fixture.tripID,
		firstHolder,
		30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	proposalStore, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	request := proposalRequest(
		fixture,
		"50666666-6666-6666-6666-666666666666",
		firstHolder,
		10,
	)
	if _, err := proposalStore.Persist(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE trip_runtime_leases
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE trip_id = $1
	`, fixture.tripID); err != nil {
		t.Fatal(err)
	}
	taken, err := leaseStore.Acquire(
		ctx,
		fixture.tripID,
		secondHolder,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if taken.RuntimeEpoch != 2 {
		t.Fatalf("takeover epoch = %d", taken.RuntimeEpoch)
	}
	var state string
	var decidedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT state, decided_at
		FROM plan_proposals
		WHERE id = $1
	`, request.ProposalID).Scan(&state, &decidedAt); err != nil {
		t.Fatal(err)
	}
	if state != "stale" || decidedAt == nil {
		t.Fatalf("old proposal remains actionable: state=%s decided=%v", state, decidedAt)
	}
	duplicate, err := proposalStore.Persist(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.Publishable ||
		duplicate.State != "stale" {
		t.Fatalf("old-epoch replay became actionable: %+v", duplicate)
	}
}

func TestPersistProposalRejectsInvalidPayloadAndTimestamp(t *testing.T) {
	payload := []byte("proposal")
	request := PersistProposalRequest{
		ProposalID: "40111111-1111-1111-1111-111111111111",
		TripID:     "40222222-2222-2222-2222-222222222222",
		Source: ProposalSource{
			RuntimeEpoch:             1,
			PlannerStateVersion:      1,
			TripRevision:             1,
			AcceptedMutationSequence: 1,
			BaseCurrentPlanID:        "40333333-3333-3333-3333-333333333333",
		},
		Current: RuntimeFreshness{
			HolderID:                 "40444444-4444-4444-4444-444444444444",
			RuntimeEpoch:             1,
			PlannerStateVersion:      1,
			AcceptedMutationSequence: 1,
		},
		Payload:   payload,
		Checksum:  sha256.Sum256([]byte("different")),
		CreatedAt: time.UnixMilli(1_784_000_000_123).UTC(),
	}
	if err := validatePersistProposal(request); !errors.Is(
		err,
		ErrProposalPayloadInvalid,
	) {
		t.Fatalf("expected checksum validation failure, got %v", err)
	}
	request.Checksum = sha256.Sum256(payload)
	request.CreatedAt = request.CreatedAt.Add(time.Nanosecond)
	if err := validatePersistProposal(request); err == nil {
		t.Fatal("sub-millisecond proposal timestamp was accepted")
	}
}
