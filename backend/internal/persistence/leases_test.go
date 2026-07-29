package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeLeaseEpochAndFencing(t *testing.T) {
	databaseURL := os.Getenv("LIVEROUTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LIVEROUTE_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const (
		userID  = "11111111-1111-1111-1111-111111111111"
		tripID  = "22222222-2222-2222-2222-222222222222"
		planID  = "33333333-3333-3333-3333-333333333333"
		holderA = "44444444-4444-4444-4444-444444444444"
		holderB = "55555555-5555-5555-5555-555555555555"
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
		VALUES ($1, 'Lease test user', 'America/New_York')
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence,
			current_plan_id
		) VALUES ($1, $2, 'America/New_York', 1, 2, 1, $3)
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
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", tripID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})

	store, err := NewLeaseStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Acquire(ctx, tripID, holderA, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.RuntimeEpoch != 1 || first.HolderID != holderA {
		t.Fatalf("unexpected first lease: %+v", first)
	}
	renewed, err := store.Renew(
		ctx, tripID, holderA, first.RuntimeEpoch, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.RuntimeEpoch != 1 ||
		!renewed.LeaseExpiresAt.After(renewed.RenewedAt) {
		t.Fatalf("unexpected renewal: %+v", renewed)
	}
	if _, err := store.Acquire(ctx, tripID, holderB, 30*time.Second); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("expected held lease, got %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE trip_runtime_leases
		SET lease_expires_at = clock_timestamp() - interval '1 millisecond'
		WHERE trip_id = $1
	`, tripID); err != nil {
		t.Fatal(err)
	}
	second, err := store.Acquire(ctx, tripID, holderB, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.RuntimeEpoch != 2 || second.HolderID != holderB {
		t.Fatalf("unexpected replacement lease: %+v", second)
	}
	if _, err := store.Renew(
		ctx, tripID, holderA, first.RuntimeEpoch, 30*time.Second,
	); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected old holder fencing, got %v", err)
	}
}
