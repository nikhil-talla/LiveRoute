package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestPrepareProposalAcceptanceReadsPendingPlanMetadata(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	fixture := createTripFixture{
		userID:    "91111111-1111-4111-8111-111111111111",
		tripID:    "92222222-2222-4222-8222-222222222222",
		intentID:  "93333333-3333-4333-8333-333333333333",
		messageID: "94444444-4444-4444-8444-444444444444",
		planID:    "95555555-5555-4555-8555-555555555555",
	}
	_, _ = pool.Exec(ctx, "DELETE FROM trips WHERE id = $1", fixture.tripID)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", fixture.userID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Proposal preparation user', 'America/New_York')
	`, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCanonicalStateStore(pool); err != nil {
		t.Fatal(err)
	}
	if _, err := (&CanonicalStateStore{pool: pool}).CreateTrip(ctx, createTripRequest(fixture)); err != nil {
		t.Fatal(err)
	}
	proposalID := "96666666-6666-4666-8666-666666666666"
	payload := []byte("stored-proposal")
	checksum := sha256.Sum256(payload)
	createdAt := time.UnixMilli(1_784_000_123_456).UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO plan_proposals (
			id, trip_id, base_current_plan_id, source_runtime_epoch,
			source_planner_state_version, source_trip_revision,
			source_accepted_mutation_sequence, schema_version, payload,
			payload_size_bytes, checksum_sha256, state, created_at
		) VALUES ($1, $2, $3, 4, 7, 1, 1, 1, $4, $5, $6, 'pending', $7)
	`, proposalID, fixture.tripID, fixture.planID, payload, len(payload), checksum[:], createdAt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM trips WHERE id = $1", fixture.tripID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", fixture.userID)
	})
	store, err := NewProposalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareProposalAcceptance(ctx, fixture.tripID, proposalID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ProposalID != proposalID || prepared.CurrentPlanID != fixture.planID ||
		prepared.NextPlanRevision != 2 || prepared.Source.RuntimeEpoch != 4 ||
		prepared.Source.PlannerStateVersion != 7 || prepared.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 ||
		prepared.CreatedAt.Before(time.Now().Add(-time.Minute)) ||
		string(prepared.Payload) != string(payload) {
		t.Fatalf("unexpected preparation: %+v", prepared)
	}
	if _, err := store.PrepareProposalAcceptance(ctx, fixture.tripID, "97777777-7777-4777-8777-777777777777"); !errors.Is(err, ErrPendingProposalNotFound) {
		t.Fatalf("missing proposal error = %v", err)
	}
}
