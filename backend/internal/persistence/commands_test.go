package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type commandTripFixture struct {
	userID string
	tripID string
	planID string
}

func createCommandTrip(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture commandTripFixture,
) {
	t.Helper()
	if _, err := pool.Exec(
		ctx,
		"DELETE FROM trips WHERE id = $1",
		fixture.tripID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		"DELETE FROM users WHERE id = $1",
		fixture.userID,
	); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Command test user', 'America/New_York')
	`, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence,
			current_plan_id
		) VALUES ($1, $2, 'America/New_York', 1, 2, 1, $3)
	`, fixture.tripID, fixture.userID, fixture.planID); err != nil {
		t.Fatal(err)
	}
	payload := []byte("{}")
	checksum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256,
			created_at
		) VALUES ($1, $2, 1, 'user_authored', $3, 1, $4, $5, $6,
		          clock_timestamp())
	`, fixture.planID, fixture.tripID, fixture.userID,
		payload, len(payload), checksum[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(
			context.Background(),
			"DELETE FROM trips WHERE id = $1",
			fixture.tripID,
		)
		_, _ = pool.Exec(
			context.Background(),
			"DELETE FROM users WHERE id = $1",
			fixture.userID,
		)
	})
}

func commandRequest(
	fixture commandTripFixture,
	intentID string,
	outboxID string,
	messageID string,
) RecordRuntimeCommandRequest {
	commandPayload := []byte(`{"payload":"command"}`)
	return RecordRuntimeCommandRequest{
		IntentID:             intentID,
		OutboxID:             outboxID,
		TripID:               fixture.tripID,
		OwnerUserID:          fixture.userID,
		MessageID:            messageID,
		EventID:              messageID,
		ExpectedTripRevision: 1,
		Kind:                 CommandActivityDelayed,
		PayloadDigest:        sha256.Sum256(commandPayload),
		CommandPayload:       commandPayload,
		EventPayload:         []byte(`{"event":"activity_delayed"}`),
	}
}

func openPersistenceTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("LIVEROUTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LIVEROUTE_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func TestRecordRuntimeFirstIdempotencyAndSerialization(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "71111111-1111-1111-1111-111111111111",
		tripID: "72222222-2222-2222-2222-222222222222",
		planID: "73333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}

	request := commandRequest(
		fixture,
		"74444444-4444-4444-4444-444444444444",
		"75555555-5555-5555-5555-555555555555",
		"76666666-6666-6666-6666-666666666666",
	)
	recorded, err := store.RecordRuntimeFirst(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Duplicate || recorded.MutationSequence != 2 ||
		recorded.State != "pending" ||
		recorded.RuntimeSyncState != "not_required" ||
		recorded.IntentID != request.IntentID ||
		recorded.OutboxID != request.OutboxID {
		t.Fatalf("unexpected recorded command: %+v", recorded)
	}

	duplicateRequest := request
	duplicateRequest.IntentID = "77777777-7777-7777-7777-777777777777"
	duplicateRequest.OutboxID = "78888888-8888-8888-8888-888888888888"
	duplicate, err := store.RecordRuntimeFirst(ctx, duplicateRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate ||
		duplicate.IntentID != request.IntentID ||
		duplicate.OutboxID != request.OutboxID ||
		duplicate.MutationSequence != recorded.MutationSequence {
		t.Fatalf("unexpected duplicate result: %+v", duplicate)
	}

	reused := duplicateRequest
	reused.PayloadDigest[0] ^= 0xff
	if _, err := store.RecordRuntimeFirst(
		ctx,
		reused,
	); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("expected idempotency reuse rejection, got %v", err)
	}

	blocked := commandRequest(
		fixture,
		"79999999-9999-9999-9999-999999999999",
		"7aaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"7bbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	)
	if _, err := store.RecordRuntimeFirst(
		ctx,
		blocked,
	); !errors.Is(err, ErrDurableCommandBlocked) {
		t.Fatalf("expected unresolved-command block, got %v", err)
	}

	var nextSequence int64
	var intentCount int
	var outboxCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT next_mutation_sequence FROM trips WHERE id = $1",
		fixture.tripID,
	).Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM command_intents WHERE trip_id = $1",
		fixture.tripID,
	).Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM planner_outbox WHERE trip_id = $1",
		fixture.tripID,
	).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if nextSequence != 3 || intentCount != 1 || outboxCount != 1 {
		t.Fatalf(
			"duplicate or blocked command mutated storage: next=%d intents=%d outbox=%d",
			nextSequence,
			intentCount,
			outboxCount,
		)
	}
}

func TestRecordRuntimeFirstConcurrentCommandsAllocateOnce(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "81111111-1111-1111-1111-111111111111",
		tripID: "82222222-2222-2222-2222-222222222222",
		planID: "83333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	requests := []RecordRuntimeCommandRequest{
		commandRequest(
			fixture,
			"84444444-4444-4444-4444-444444444444",
			"85555555-5555-5555-5555-555555555555",
			"86666666-6666-6666-6666-666666666666",
		),
		commandRequest(
			fixture,
			"87777777-7777-7777-7777-777777777777",
			"88888888-8888-8888-8888-888888888888",
			"89999999-9999-9999-9999-999999999999",
		),
	}
	start := make(chan struct{})
	errorsByRequest := make([]error, len(requests))
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByRequest[index] =
				store.RecordRuntimeFirst(ctx, requests[index])
		}(index)
	}
	close(start)
	wait.Wait()

	recordedCount := 0
	blockedCount := 0
	for _, err := range errorsByRequest {
		switch {
		case err == nil:
			recordedCount++
		case errors.Is(err, ErrDurableCommandBlocked):
			blockedCount++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if recordedCount != 1 || blockedCount != 1 {
		t.Fatalf(
			"expected one record and one block, got record=%d block=%d",
			recordedCount,
			blockedCount,
		)
	}
	var nextSequence int64
	if err := pool.QueryRow(
		ctx,
		"SELECT next_mutation_sequence FROM trips WHERE id = $1",
		fixture.tripID,
	).Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	if nextSequence != 3 {
		t.Fatalf("concurrent recording allocated more than once: %d", nextSequence)
	}
}

func TestRecordRuntimeFirstRejectsStaleRevisionAndInvalidPlanShape(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := commandTripFixture{
		userID: "91111111-1111-1111-1111-111111111111",
		tripID: "92222222-2222-2222-2222-222222222222",
		planID: "93333333-3333-3333-3333-333333333333",
	}
	createCommandTrip(t, ctx, pool, fixture)
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	stale := commandRequest(
		fixture,
		"94444444-4444-4444-4444-444444444444",
		"95555555-5555-5555-5555-555555555555",
		"96666666-6666-6666-6666-666666666666",
	)
	stale.ExpectedTripRevision = 0
	if _, err := store.RecordRuntimeFirst(
		ctx,
		stale,
	); !errors.Is(err, ErrTripRevisionStale) {
		t.Fatalf("expected stale revision, got %v", err)
	}

	acceptance := stale
	acceptance.ExpectedTripRevision = 1
	acceptance.Kind = CommandAcceptProposal
	if _, err := store.RecordRuntimeFirst(
		ctx,
		acceptance,
	); err == nil {
		t.Fatal("proposal acceptance without planned current plan was accepted")
	}
}
