package persistence

import (
	"errors"
	"testing"
)

func TestConfirmFinalizedMutationsStampsCoveredMirrorAndIsIdempotent(t *testing.T) {
	store, ctx, request, recorded := setupCanonicalMirrorFixture(t, "a")
	if _, err := store.FinalizeCanonicalMirror(ctx, FinalizeCanonicalMirrorRequest{
		TripID:                 request.TripID,
		IntentID:               request.IntentID,
		OutboxID:               request.OutboxID,
		EventID:                request.EventID,
		MutationSequence:       recorded.MutationSequence,
		ExpectedTripRevision:   recorded.ExpectedTripRevision,
		ResultingTripRevision:  *recorded.ResultingTripRevision,
		ResultingCurrentPlanID: *recorded.ResultingCurrentPlanID,
	}); err != nil {
		t.Fatal(err)
	}
	commandStore, err := NewCommandStore(store.pool)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := commandStore.ConfirmFinalizedMutations(ctx, ConfirmFinalizedMutationsRequest{
		TripID:                    request.TripID,
		FinalizedMutationSequence: recorded.MutationSequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Duplicate || confirmed.RowsConfirmed != 1 || confirmed.ConfirmedAt.IsZero() {
		t.Fatalf("unexpected finalization confirmation: %+v", confirmed)
	}
	var confirmedAt interface{}
	if err := store.pool.QueryRow(ctx, `
		SELECT finalization_confirmed_at
		FROM planner_outbox
		WHERE id = $1
	`, request.OutboxID).Scan(&confirmedAt); err != nil {
		t.Fatal(err)
	}
	if confirmedAt == nil {
		t.Fatal("covered outbox row was not timestamped")
	}

	duplicate, err := commandStore.ConfirmFinalizedMutations(ctx, ConfirmFinalizedMutationsRequest{
		TripID:                    request.TripID,
		FinalizedMutationSequence: recorded.MutationSequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.RowsConfirmed != 0 || duplicate.ConfirmedAt.IsZero() {
		t.Fatalf("unexpected duplicate confirmation: %+v", duplicate)
	}
	if _, err := commandStore.ConfirmFinalizedMutations(ctx, ConfirmFinalizedMutationsRequest{
		TripID:                    request.TripID,
		FinalizedMutationSequence: recorded.MutationSequence + 1,
	}); !errors.Is(err, ErrFinalizationConfirmationAhead) {
		t.Fatalf("expected ahead confirmation rejection, got %v", err)
	}
}

func TestConfirmFinalizedMutationsBlocksPendingOutbox(t *testing.T) {
	store, ctx, request, recorded := setupCanonicalMirrorFixture(t, "b")
	commandStore, err := NewCommandStore(store.pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandStore.ConfirmFinalizedMutations(ctx, ConfirmFinalizedMutationsRequest{
		TripID:                    request.TripID,
		FinalizedMutationSequence: recorded.MutationSequence,
	}); !errors.Is(err, ErrFinalizationConfirmationBlocked) {
		t.Fatalf("expected pending outbox block, got %v", err)
	}
}

func TestResolveCanonicalBootstrapConvergesCoveredMirror(t *testing.T) {
	store, ctx, request, recorded := setupCanonicalMirrorFixture(t, "c")
	resolved, err := store.ResolveCanonicalBootstrap(ctx, ResolveCanonicalBootstrapRequest{
		TripID:                    request.TripID,
		TripRevision:              *recorded.ResultingTripRevision,
		AcceptedMutationSequence:  recorded.MutationSequence,
		FinalizedMutationSequence: recorded.MutationSequence,
		CurrentPlanID:             *recorded.ResultingCurrentPlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Duplicate || resolved.MirrorsResolved != 1 ||
		resolved.RowsConfirmed != 1 || resolved.ConfirmedAt.IsZero() {
		t.Fatalf("unexpected bootstrap convergence: %+v", resolved)
	}
	var runtimeSyncState, deliveryState string
	var confirmedAt interface{}
	if err := store.pool.QueryRow(ctx, `
		SELECT intent.runtime_sync_state,
		       outbox.delivery_state,
		       outbox.finalization_confirmed_at
		FROM command_intents AS intent
		JOIN planner_outbox AS outbox
		  ON outbox.command_intent_id = intent.id
		WHERE intent.id = $1 AND outbox.id = $2
	`, request.IntentID, request.OutboxID).Scan(
		&runtimeSyncState, &deliveryState, &confirmedAt,
	); err != nil {
		t.Fatal(err)
	}
	if runtimeSyncState != "synced" || deliveryState != "accepted" || confirmedAt == nil {
		t.Fatalf("bootstrap did not resolve mirror: runtime=%s delivery=%s confirmed=%v", runtimeSyncState, deliveryState, confirmedAt)
	}

	duplicate, err := store.ResolveCanonicalBootstrap(ctx, ResolveCanonicalBootstrapRequest{
		TripID:                    request.TripID,
		TripRevision:              *recorded.ResultingTripRevision,
		AcceptedMutationSequence:  recorded.MutationSequence,
		FinalizedMutationSequence: recorded.MutationSequence,
		CurrentPlanID:             *recorded.ResultingCurrentPlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.MirrorsResolved != 0 || duplicate.RowsConfirmed != 0 {
		t.Fatalf("unexpected duplicate bootstrap convergence: %+v", duplicate)
	}
}
