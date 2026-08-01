package dispatch

import (
	"context"
	"testing"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
)

type fakeRuntimeStore struct {
	mutation *persistence.FinalizeAcceptedMutationRequest
	accept   *persistence.FinalizeProposalDecisionRequest
	reject   *persistence.FinalizeProposalDecisionRequest
	stale    *persistence.FinalizeProposalDecisionRequest
	terminal *persistence.FinalizeTerminalCommandRequest
}

func (store *fakeRuntimeStore) FinalizeAcceptedMutation(_ context.Context, request persistence.FinalizeAcceptedMutationRequest) (persistence.FinalizedCommand, error) {
	store.mutation = &request
	return persistence.FinalizedCommand{}, nil
}
func (store *fakeRuntimeStore) FinalizeProposalAcceptance(_ context.Context, request persistence.FinalizeProposalDecisionRequest) (persistence.FinalizedCommand, error) {
	store.accept = &request
	return persistence.FinalizedCommand{}, nil
}
func (store *fakeRuntimeStore) FinalizeProposalRejection(_ context.Context, request persistence.FinalizeProposalDecisionRequest) (persistence.FinalizedCommand, error) {
	store.reject = &request
	return persistence.FinalizedCommand{}, nil
}
func (store *fakeRuntimeStore) FinalizeStaleProposalDecision(_ context.Context, request persistence.FinalizeProposalDecisionRequest) (persistence.FinalizedCommand, error) {
	store.stale = &request
	return persistence.FinalizedCommand{}, nil
}
func (store *fakeRuntimeStore) FinalizeTerminal(_ context.Context, request persistence.FinalizeTerminalCommandRequest) (persistence.FinalizedCommand, error) {
	store.terminal = &request
	return persistence.FinalizedCommand{}, nil
}

func runtimeRow() persistence.ClaimedOutboxRow {
	return persistence.ClaimedOutboxRow{
		ID: testOutboxID, CommandIntentID: testIntentID, TripID: testTripID,
		EventID: testEventID, MutationSequence: 2, ExpectedTripRevision: 1,
		ApplicationOrder: "runtime_first",
	}
}

func TestRuntimeFinalizerMapsAcceptedMutation(t *testing.T) {
	store := &fakeRuntimeStore{}
	finalizer, err := NewRuntimeFinalizer(store)
	if err != nil {
		t.Fatal(err)
	}
	event := &liveroutev1.ApplyTripEvent{
		EventId: testEventID, OccurredAtUnixMs: 1234,
		Event: &liveroutev1.ApplyTripEvent_TravelDelay{
			TravelDelay: &liveroutev1.TravelDelay{
				FromActivityId:    testIntentID,
				ToActivityId:      testPlanID,
				AdditionalSeconds: 12,
			},
		},
	}
	response := &liveroutev1.PlannerStreamResponse{PlannerStateVersion: 8}
	ack := &liveroutev1.EventAcknowledged{
		Disposition: liveroutev1.EventDisposition_EVENT_DISPOSITION_DUPLICATE,
		Status:      liveroutev1.StatusCode_STATUS_CODE_DUPLICATE,
	}
	if err := finalizer.Finalize(context.Background(), runtimeRow(), event, response, ack); err != nil {
		t.Fatal(err)
	}
	if store.mutation == nil || store.mutation.Mutation.TravelDelay == nil ||
		store.mutation.Mutation.TravelDelay.ObservedAtUnixMilliseconds != 1234 ||
		store.mutation.ResultingPlannerStateVersion != 8 {
		t.Fatalf("unexpected accepted mutation: %#v", store.mutation)
	}
}

func TestRuntimeFinalizerMapsLogicalExpiry(t *testing.T) {
	store := &fakeRuntimeStore{}
	finalizer, _ := NewRuntimeFinalizer(store)
	ack := &liveroutev1.EventAcknowledged{
		Disposition: liveroutev1.EventDisposition_EVENT_DISPOSITION_REJECTED,
		Status:      liveroutev1.StatusCode_STATUS_CODE_COMMAND_EXPIRED,
	}
	if err := finalizer.Finalize(
		context.Background(), runtimeRow(),
		&liveroutev1.ApplyTripEvent{},
		&liveroutev1.PlannerStreamResponse{PlannerStateVersion: 9}, ack,
	); err != nil {
		t.Fatal(err)
	}
	if store.terminal == nil || store.terminal.Status != persistence.TerminalStatusCommandExpired ||
		store.terminal.ResultingPlannerStateVersion != 9 {
		t.Fatalf("unexpected terminal finalization: %#v", store.terminal)
	}
}

func TestRuntimeFinalizerMapsStaleProposalDecision(t *testing.T) {
	store := &fakeRuntimeStore{}
	finalizer, _ := NewRuntimeFinalizer(store)
	decision := &liveroutev1.PlanDecisionEvent{
		Decision:                  liveroutev1.PlanDecision_PLAN_DECISION_ACCEPT,
		ProposalId:                testPlanID,
		SourceRuntimeEpoch:        7,
		SourcePlannerStateVersion: 6,
		BaseCurrentPlanId:         testIntentID,
	}
	ack := &liveroutev1.EventAcknowledged{
		Disposition: liveroutev1.EventDisposition_EVENT_DISPOSITION_STALE,
		Status:      liveroutev1.StatusCode_STATUS_CODE_STALE,
		StaleReason: liveroutev1.StaleReason_STALE_REASON_PLAN_PROPOSAL,
	}
	if err := finalizer.Finalize(
		context.Background(), runtimeRow(),
		&liveroutev1.ApplyTripEvent{Event: &liveroutev1.ApplyTripEvent_PlanDecision{PlanDecision: decision}},
		&liveroutev1.PlannerStreamResponse{PlannerStateVersion: 10}, ack,
	); err != nil {
		t.Fatal(err)
	}
	if store.stale == nil || store.stale.Identity.ProposalID != testPlanID ||
		store.stale.Identity.SourceRuntimeEpoch != 7 {
		t.Fatalf("unexpected stale proposal finalization: %#v", store.stale)
	}
}
