package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetryDelayBoundsAndCap(t *testing.T) {
	zeroes := bytes.NewReader(make([]byte, 32))
	delay, err := RetryDelay(1, zeroes)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 0 {
		t.Fatalf("zero jitter source produced %s", delay)
	}
	delay, err = RetryDelay(100, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if delay < 0 || delay > 30*time.Second {
		t.Fatalf("capped delay is out of range: %s", delay)
	}
	if _, err := RetryDelay(0, bytes.NewReader(nil)); err == nil {
		t.Fatal("zero attempt count was accepted")
	}
}

func TestOrderedOutboxClaimsAndAttemptFencing(t *testing.T) {
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

	const (
		userID     = "61111111-1111-1111-1111-111111111111"
		tripID     = "62222222-2222-2222-2222-222222222222"
		planID     = "63333333-3333-3333-3333-333333333333"
		claimOwner = "64444444-4444-4444-4444-444444444444"
	)
	if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Outbox test user', 'America/New_York')
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence,
			current_plan_id
		) VALUES ($1, $2, 'America/New_York', 1, 4, 1, $3)
	`, tripID, userID, planID); err != nil {
		t.Fatal(err)
	}
	emptyPayload := []byte("{}")
	checksum := sha256.Sum256(emptyPayload)
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256,
			created_at
		) VALUES ($1, $2, 1, 'user_authored', $3, 1, $4, $5, $6,
		          clock_timestamp())
	`, planID, tripID, userID, emptyPayload, len(emptyPayload), checksum[:]); err != nil {
		t.Fatal(err)
	}
	for sequence := 2; sequence <= 3; sequence++ {
		intentID := fmt.Sprintf("65555555-5555-5555-5555-%012d", sequence)
		messageID := fmt.Sprintf("66666666-6666-6666-6666-%012d", sequence)
		eventID := fmt.Sprintf("67777777-7777-7777-7777-%012d", sequence)
		outboxID := fmt.Sprintf("68888888-8888-8888-8888-%012d", sequence)
		if _, err := tx.Exec(ctx, `
			INSERT INTO command_intents (
				id, trip_id, message_id, event_id, mutation_sequence,
				expected_trip_revision, command_kind, application_order,
				digest_algorithm, payload_digest, command_payload, state,
				runtime_sync_state, recorded_at
			) VALUES (
				$1, $2, $3, $4, $5, 1, 'trip_edited', 'canonical_first',
				'rfc8785-sha256-v1', $6, '{}'::jsonb, 'applied', 'pending',
				clock_timestamp()
			)
		`, intentID, tripID, messageID, eventID, sequence, checksum[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO planner_outbox (
				id, command_intent_id, trip_id, mutation_sequence,
				event_schema_version, event_payload, delivery_state
			) VALUES ($1, $2, $3, $4, 1, '{}'::jsonb, 'pending')
		`, outboxID, intentID, tripID, sequence); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", tripID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})

	store, err := NewOutboxStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	firstClaim, err := store.ClaimDue(ctx, claimOwner, 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstClaim) != 1 || firstClaim[0].MutationSequence != 2 ||
		firstClaim[0].AttemptCount != 1 {
		t.Fatalf("unexpected first claim: %+v", firstClaim)
	}
	blocked, err := store.ClaimDue(ctx, claimOwner, 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Fatalf("later same-trip row bypassed predecessor: %+v", blocked)
	}
	if err := store.ReleaseForRetry(
		ctx, firstClaim[0], claimOwner, "UNAVAILABLE", 0,
	); err != nil {
		t.Fatal(err)
	}
	secondClaim, err := store.ClaimDue(ctx, claimOwner, 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondClaim) != 1 || secondClaim[0].MutationSequence != 2 ||
		secondClaim[0].AttemptCount != 2 {
		t.Fatalf("unexpected reclaimed row: %+v", secondClaim)
	}
	if err := store.ReleaseForRetry(
		ctx, firstClaim[0], claimOwner, "UNAVAILABLE", 0,
	); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("expected stale attempt fencing, got %v", err)
	}
}
