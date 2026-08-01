package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
)

func testSnapshotCurrentPlan(planID string, revision uint64) []byte {
	var plan []byte
	plan = appendTestBytesField(plan, 1, []byte(planID))
	plan = appendTestVarintField(plan, 2, revision)
	plan = appendTestVarintField(plan, 3, 1)
	return appendTestVarintField(plan, 5, 1_784_000_000_123)
}

func testSnapshotPayload(
	fixture commandTripFixture,
	tripRevision uint64,
	coveredSequence uint64,
) []byte {
	var trip []byte
	trip = appendTestBytesField(trip, 1, []byte(fixture.tripID))
	trip = appendTestBytesField(trip, 2, []byte(fixture.userID))
	trip = appendTestBytesField(trip, 3, []byte("America/New_York"))
	trip = appendTestBytesField(trip, 7, []byte(fixture.planID))

	var snapshot []byte
	snapshot = appendTestBytesField(snapshot, 1, trip)
	snapshot = appendTestVarintField(snapshot, 2, tripRevision)
	snapshot = appendTestVarintField(snapshot, 3, coveredSequence)
	snapshot = appendTestVarintField(snapshot, 4, coveredSequence)
	snapshot = appendTestBytesField(
		snapshot,
		5,
		testSnapshotCurrentPlan(fixture.planID, tripRevision),
	)
	return appendTestVarintField(snapshot, 6, 1)
}

func testSnapshotBlob(
	fixture commandTripFixture,
	id string,
	runtimeEpoch uint64,
	plannerVersion uint64,
	tripRevision uint64,
	coveredSequence uint64,
) SnapshotBlob {
	payload := testSnapshotPayload(
		fixture,
		tripRevision,
		coveredSequence,
	)
	return SnapshotBlob{
		ID:                               id,
		TripID:                           fixture.tripID,
		SchemaVersion:                    1,
		SourceRuntimeEpoch:               runtimeEpoch,
		SourcePlannerStateVersion:        plannerVersion,
		TripRevision:                     tripRevision,
		CoveredFinalizedMutationSequence: coveredSequence,
		Payload:                          payload,
		Checksum:                         sha256.Sum256(payload),
	}
}

func createSnapshotFixture(
	t *testing.T,
	prefix string,
) (*SnapshotStore, commandTripFixture) {
	t.Helper()
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: prefix + "111111-1111-1111-1111-111111111111",
		tripID: prefix + "222222-2222-2222-2222-222222222222",
		planID: prefix + "333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewSnapshotStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func TestSnapshotPersistenceRetainsTwoNewestAndReplaysExactly(
	t *testing.T,
) {
	store, fixture := createSnapshotFixture(t, "b0")
	ctx := context.Background()
	first := testSnapshotBlob(
		fixture,
		"b0444444-4444-4444-4444-444444444444",
		1,
		1,
		1,
		1,
	)
	second := testSnapshotBlob(
		fixture,
		"b0555555-5555-5555-5555-555555555555",
		1,
		2,
		1,
		1,
	)
	third := testSnapshotBlob(
		fixture,
		"b0666666-6666-6666-6666-666666666666",
		1,
		3,
		1,
		1,
	)
	for _, blob := range []SnapshotBlob{first, second, third} {
		stored, err := store.Persist(ctx, blob)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Duplicate || stored.CreatedAt.IsZero() {
			t.Fatalf("unexpected stored snapshot: %+v", stored)
		}
	}

	var count int
	var oldestCount int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE id = $2)
		FROM planner_snapshots
		WHERE trip_id = $1 AND invalidated_at IS NULL
	`, fixture.tripID, first.ID).Scan(&count, &oldestCount); err != nil {
		t.Fatal(err)
	}
	if count != 2 || oldestCount != 0 {
		t.Fatalf("unexpected retained snapshots: %d/%d", count, oldestCount)
	}
	loaded, err := store.LoadForRecovery(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != third.ID {
		t.Fatalf("loaded snapshot %s, want %s", loaded.ID, third.ID)
	}
	duplicate, err := store.Persist(ctx, third)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate ||
		duplicate.ID != third.ID ||
		duplicate.CreatedAt != loaded.CreatedAt {
		t.Fatalf("unexpected duplicate snapshot: %+v", duplicate)
	}

	conflict := third
	conflict.ID = "b0777777-7777-7777-7777-777777777777"
	if _, err := store.Persist(
		ctx,
		conflict,
	); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected snapshot conflict, got %v", err)
	}
}

func TestSnapshotPersistencePrunesOnlyCoveredTerminalOutbox(
	t *testing.T,
) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "c0111111-1111-1111-1111-111111111111",
		tripID: "c0222222-2222-2222-2222-222222222222",
		planID: "c0333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	commandStore, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := commandStore.RecordRuntimeFirst(ctx, commandRequest(
		fixture,
		"c0444444-4444-4444-4444-444444444444",
		"c0555555-5555-5555-5555-555555555555",
		"c0666666-6666-6666-6666-666666666666",
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandStore.FinalizeTerminal(
		ctx,
		terminalRequest(recorded, TerminalStatusInvalidArgument),
	); err != nil {
		t.Fatal(err)
	}
	snapshotStore, err := NewSnapshotStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	blob := testSnapshotBlob(
		fixture,
		"c0777777-7777-7777-7777-777777777777",
		1,
		17,
		1,
		2,
	)
	if _, err := snapshotStore.Persist(ctx, blob); !errors.Is(err, ErrSnapshotNotReady) {
		t.Fatalf("expected snapshot readiness gate, got %v", err)
	}
	if _, err := commandStore.ConfirmFinalizedMutations(ctx, ConfirmFinalizedMutationsRequest{
		TripID:                    fixture.tripID,
		FinalizedMutationSequence: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotStore.Persist(ctx, blob); err != nil {
		t.Fatal(err)
	}

	var outboxCount int
	var intentCount int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM planner_outbox WHERE trip_id = $1),
		  (SELECT count(*) FROM command_intents WHERE trip_id = $1)
	`, fixture.tripID).Scan(&outboxCount, &intentCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 || intentCount != 1 {
		t.Fatalf(
			"snapshot pruning removed wrong rows: outbox=%d intents=%d",
			outboxCount,
			intentCount,
		)
	}
}

func TestSnapshotRecoveryInvalidatesCorruptionAndFallsBack(
	t *testing.T,
) {
	store, fixture := createSnapshotFixture(t, "d0")
	ctx := context.Background()
	previous := testSnapshotBlob(
		fixture,
		"d0444444-4444-4444-4444-444444444444",
		1,
		1,
		1,
		1,
	)
	newest := testSnapshotBlob(
		fixture,
		"d0555555-5555-5555-5555-555555555555",
		1,
		2,
		1,
		1,
	)
	if _, err := store.Persist(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, newest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE planner_snapshots
		SET payload = payload || decode('00', 'hex'),
		    payload_size_bytes = payload_size_bytes + 1
		WHERE id = $1
	`, newest.ID); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadForRecovery(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != previous.ID {
		t.Fatalf("loaded %s, want fallback %s", loaded.ID, previous.ID)
	}
	var invalidated bool
	var reason string
	if err := store.pool.QueryRow(ctx, `
		SELECT invalidated_at IS NOT NULL, invalidation_reason
		FROM planner_snapshots
		WHERE id = $1
	`, newest.ID).Scan(&invalidated, &reason); err != nil {
		t.Fatal(err)
	}
	if !invalidated || reason != "checksum_mismatch" {
		t.Fatalf("unexpected invalidation: %t/%s", invalidated, reason)
	}

	sizeMismatch := testSnapshotBlob(
		fixture,
		"d0666666-6666-6666-6666-666666666666",
		1,
		3,
		1,
		1,
	)
	if _, err := store.Persist(ctx, sizeMismatch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE planner_snapshots
		SET payload_size_bytes = payload_size_bytes + 1
		WHERE id = $1
	`, sizeMismatch.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadForRecovery(ctx, fixture.tripID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != previous.ID {
		t.Fatalf("loaded %s after size mismatch, want %s",
			loaded.ID, previous.ID)
	}
	if err := store.pool.QueryRow(ctx, `
		SELECT invalidated_at IS NOT NULL, invalidation_reason
		FROM planner_snapshots
		WHERE id = $1
	`, sizeMismatch.ID).Scan(&invalidated, &reason); err != nil {
		t.Fatal(err)
	}
	if !invalidated || reason != "payload_size_invalid" {
		t.Fatalf("unexpected size invalidation: %t/%s", invalidated, reason)
	}
}

func TestSnapshotOrderingAllowsEpochResetAndRejectsRegression(
	t *testing.T,
) {
	store, fixture := createSnapshotFixture(t, "e0")
	ctx := context.Background()
	epochOne := testSnapshotBlob(
		fixture,
		"e0444444-4444-4444-4444-444444444444",
		1,
		10,
		1,
		1,
	)
	epochTwo := testSnapshotBlob(
		fixture,
		"e0555555-5555-5555-5555-555555555555",
		2,
		0,
		1,
		1,
	)
	if _, err := store.Persist(ctx, epochOne); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, epochTwo); err != nil {
		t.Fatal(err)
	}
	oldEpoch := testSnapshotBlob(
		fixture,
		"e0666666-6666-6666-6666-666666666666",
		1,
		11,
		1,
		1,
	)
	if _, err := store.Persist(
		ctx,
		oldEpoch,
	); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("expected stale snapshot, got %v", err)
	}

	ahead := testSnapshotBlob(
		fixture,
		"e0777777-7777-7777-7777-777777777777",
		2,
		1,
		2,
		2,
	)
	if _, err := store.Persist(
		ctx,
		ahead,
	); !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("expected ahead snapshot rejection, got %v", err)
	}
}

func TestSnapshotPersistenceExcludesCorruptionFromRetention(t *testing.T) {
	store, fixture := createSnapshotFixture(t, "a1")
	ctx := context.Background()
	first := testSnapshotBlob(
		fixture,
		"a1444444-4444-4444-4444-444444444444",
		1,
		1,
		1,
		1,
	)
	corrupt := testSnapshotBlob(
		fixture,
		"a1555555-5555-5555-5555-555555555555",
		1,
		2,
		1,
		1,
	)
	newest := testSnapshotBlob(
		fixture,
		"a1666666-6666-6666-6666-666666666666",
		1,
		3,
		1,
		1,
	)
	if _, err := store.Persist(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, corrupt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE planner_snapshots
		SET payload_size_bytes = payload_size_bytes + 1
		WHERE id = $1
	`, corrupt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, newest); err != nil {
		t.Fatal(err)
	}

	var validCount int
	var firstValid bool
	var corruptInvalid bool
	if err := store.pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE invalidated_at IS NULL),
		  bool_or(id = $2 AND invalidated_at IS NULL),
		  bool_or(id = $3 AND invalidated_at IS NOT NULL)
		FROM planner_snapshots
		WHERE trip_id = $1
	`, fixture.tripID, first.ID, corrupt.ID).Scan(
		&validCount,
		&firstValid,
		&corruptInvalid,
	); err != nil {
		t.Fatal(err)
	}
	if validCount != 2 || !firstValid || !corruptInvalid {
		t.Fatalf(
			"corrupt retention state is invalid: %d/%t/%t",
			validCount,
			firstValid,
			corruptInvalid,
		)
	}
}

func TestSnapshotValidationRejectsChecksumAndPayloadMetadata(
	t *testing.T,
) {
	store, fixture := createSnapshotFixture(t, "f0")
	ctx := context.Background()
	badChecksum := testSnapshotBlob(
		fixture,
		"f0444444-4444-4444-4444-444444444444",
		1,
		1,
		1,
		1,
	)
	badChecksum.Checksum[0] ^= 0xff
	if _, err := store.Persist(
		ctx,
		badChecksum,
	); !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("expected checksum rejection, got %v", err)
	}

	badMetadata := testSnapshotBlob(
		fixture,
		"f0555555-5555-5555-5555-555555555555",
		1,
		1,
		1,
		1,
	)
	badMetadata.TripRevision = 2
	if _, err := store.Persist(
		ctx,
		badMetadata,
	); !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("expected payload metadata rejection, got %v", err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM planner_snapshots WHERE trip_id = $1
	`, fixture.tripID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid candidate replaced storage")
	}
}
