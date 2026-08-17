package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

const (
	testTripID   = "11111111-1111-4111-8111-111111111111"
	testIntentID = "22222222-2222-4222-8222-222222222222"
	testOutboxID = "33333333-3333-4333-8333-333333333333"
	testEventID  = "44444444-4444-4444-8444-444444444444"
	testPlanID   = "55555555-5555-4555-8555-555555555555"
)

type fakeOutbox struct {
	rows          []persistence.ClaimedOutboxRow
	confirmations []persistence.PendingFinalizationConfirmation
	retried       bool
	paused        bool
}

func (store *fakeOutbox) ClaimDue(context.Context, string, int, time.Duration) ([]persistence.ClaimedOutboxRow, error) {
	rows := store.rows
	store.rows = nil
	return rows, nil
}
func (store *fakeOutbox) ReleaseForRetry(context.Context, persistence.ClaimedOutboxRow, string, string, time.Duration) error {
	store.retried = true
	return nil
}
func (store *fakeOutbox) PauseInternal(context.Context, persistence.ClaimedOutboxRow, string, string) error {
	store.paused = true
	return nil
}
func (store *fakeOutbox) PendingFinalizationConfirmations(context.Context, int) ([]persistence.PendingFinalizationConfirmation, error) {
	values := store.confirmations
	store.confirmations = nil
	return values, nil
}

type fakeLeases struct{}

func (fakeLeases) Current(context.Context, string, string) (persistence.RuntimeLease, error) {
	return persistence.RuntimeLease{TripID: testTripID, RuntimeEpoch: 7}, nil
}

type fakePlanner struct {
	requests []*liveroutev1.PlannerStreamRequest
}

func (planner *fakePlanner) Exchange(_ context.Context, request *liveroutev1.PlannerStreamRequest) (*liveroutev1.PlannerStreamResponse, error) {
	planner.requests = append(planner.requests, request)
	if request.GetApplyEvent() != nil {
		return &liveroutev1.PlannerStreamResponse{
			RequestId:    request.GetRequestId(),
			TripId:       request.GetTripId(),
			RuntimeEpoch: request.GetRuntimeEpoch(),
			TripRevision: 3,
			Payload: &liveroutev1.PlannerStreamResponse_EventAcknowledged{
				EventAcknowledged: &liveroutev1.EventAcknowledged{
					Disposition:              liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED,
					Status:                   liveroutev1.StatusCode_STATUS_CODE_OK,
					EventId:                  testEventID,
					ResolvedMutationSequence: 3,
					ResultingCurrentPlanId:   testPlanID,
				},
			},
		}, nil
	}
	sequence := request.GetConfirmFinalizedMutations().GetFinalizedMutationSequence()
	return &liveroutev1.PlannerStreamResponse{
		RequestId:    request.GetRequestId(),
		TripId:       request.GetTripId(),
		RuntimeEpoch: request.GetRuntimeEpoch(),
		Payload: &liveroutev1.PlannerStreamResponse_FinalizedMutationsAcknowledged{
			FinalizedMutationsAcknowledged: &liveroutev1.FinalizedMutationsAcknowledged{
				Status:                    liveroutev1.StatusCode_STATUS_CODE_OK,
				FinalizedMutationSequence: sequence,
			},
		},
	}, nil
}

type fakeMirrors struct {
	request *persistence.FinalizeCanonicalMirrorRequest
}

func (store *fakeMirrors) FinalizeCanonicalMirror(_ context.Context, request persistence.FinalizeCanonicalMirrorRequest) (persistence.FinalizedCanonicalMirror, error) {
	store.request = &request
	return persistence.FinalizedCanonicalMirror{}, nil
}

type fakeConfirmations struct {
	sequences []uint64
}

type fakeRuntimeFinalizer struct {
	row *persistence.ClaimedOutboxRow
}

func (finalizer *fakeRuntimeFinalizer) Finalize(
	_ context.Context,
	row persistence.ClaimedOutboxRow,
	_ *liveroutev1.ApplyTripEvent,
	response *liveroutev1.PlannerStreamResponse,
	_ *liveroutev1.EventAcknowledged,
) (persistence.FinalizedCommand, error) {
	finalizer.row = &row
	return persistence.FinalizedCommand{
		TripID: row.TripID, EventID: row.EventID,
		MutationSequence: row.MutationSequence, State: "applied", Status: "OK",
		ResultingTripRevision:        row.ExpectedTripRevision + 1,
		ResultingPlannerStateVersion: response.GetPlannerStateVersion(),
	}, nil
}

func (store *fakeConfirmations) ConfirmFinalizedMutations(_ context.Context, request persistence.ConfirmFinalizedMutationsRequest) (persistence.ConfirmedFinalizedMutations, error) {
	store.sequences = append(store.sequences, request.FinalizedMutationSequence)
	return persistence.ConfirmedFinalizedMutations{}, nil
}

func newTestDispatcher(t *testing.T, outbox *fakeOutbox, planner *fakePlanner, mirrors *fakeMirrors, confirmations *fakeConfirmations) *Dispatcher {
	t.Helper()
	dispatcher, err := New(Config{
		ClaimOwner:     "66666666-6666-4666-8666-666666666666",
		LeaseHolder:    "77777777-7777-4777-8777-777777777777",
		BatchSize:      4,
		ClaimDuration:  time.Minute,
		AttemptTimeout: time.Second,
	}, outbox, fakeLeases{}, planner, mirrors, &fakeRuntimeFinalizer{}, confirmations)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.now = func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	dispatcher.retryDelay = func(uint64) (time.Duration, error) { return 0, nil }
	return dispatcher
}

func canonicalRow(t *testing.T) persistence.ClaimedOutboxRow {
	t.Helper()
	payload, err := plannerwire.EncodeStoredEvent(&liveroutev1.ApplyTripEvent{
		EventId:          testEventID,
		OccurredAtUnixMs: 1_799_999_000_000,
		Event: &liveroutev1.ApplyTripEvent_CurrentPlanReplaced{
			CurrentPlanReplaced: &liveroutev1.CurrentPlanReplaced{
				CurrentPlan: &liveroutev1.CurrentPlan{PlanId: testPlanID},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return persistence.ClaimedOutboxRow{
		ID:                     testOutboxID,
		CommandIntentID:        testIntentID,
		TripID:                 testTripID,
		MutationSequence:       3,
		EventSchemaVersion:     1,
		EventPayload:           payload,
		AttemptCount:           1,
		EventID:                testEventID,
		ExpectedTripRevision:   2,
		ApplicationOrder:       "canonical_first",
		CommandKind:            persistence.CommandReplaceCurrentPlan,
		ResultingTripRevision:  3,
		ResultingCurrentPlanID: testPlanID,
	}
}

func TestDispatcherFinalizesMirrorAndConfirmsWatermark(t *testing.T) {
	outbox := &fakeOutbox{rows: []persistence.ClaimedOutboxRow{canonicalRow(t)}}
	planner := &fakePlanner{}
	mirrors := &fakeMirrors{}
	confirmations := &fakeConfirmations{}
	dispatcher := newTestDispatcher(t, outbox, planner, mirrors, confirmations)
	resolved, err := dispatcher.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || mirrors.request == nil ||
		mirrors.request.ResultingCurrentPlanID != testPlanID ||
		len(confirmations.sequences) != 1 || confirmations.sequences[0] != 3 {
		t.Fatalf("mirror was not fully resolved: %#v %#v", mirrors.request, confirmations.sequences)
	}
	if len(planner.requests) != 2 || planner.requests[0].GetApplyEvent() == nil ||
		planner.requests[0].GetExpectedTripRevision() != 2 ||
		planner.requests[1].GetConfirmFinalizedMutations() == nil {
		t.Fatalf("unexpected planner requests: %#v", planner.requests)
	}
}

func TestDispatcherRecoversConfirmationAfterRestart(t *testing.T) {
	outbox := &fakeOutbox{confirmations: []persistence.PendingFinalizationConfirmation{{
		TripID: testTripID, FinalizedMutationSequence: 9,
	}}}
	planner := &fakePlanner{}
	confirmations := &fakeConfirmations{}
	dispatcher := newTestDispatcher(t, outbox, planner, &fakeMirrors{}, confirmations)
	resolved, err := dispatcher.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 || len(planner.requests) != 1 ||
		planner.requests[0].GetConfirmFinalizedMutations().GetFinalizedMutationSequence() != 9 ||
		len(confirmations.sequences) != 1 || confirmations.sequences[0] != 9 {
		t.Fatalf("confirmation recovery failed: %#v %#v", planner.requests, confirmations.sequences)
	}
}

func TestDispatcherPausesUndocumentedPayload(t *testing.T) {
	row := canonicalRow(t)
	row.EventPayload = []byte(`{"kind":"current_plan_replaced"}`)
	outbox := &fakeOutbox{rows: []persistence.ClaimedOutboxRow{row}}
	dispatcher := newTestDispatcher(t, outbox, &fakePlanner{}, &fakeMirrors{}, &fakeConfirmations{})
	if _, err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !outbox.paused {
		t.Fatal("malformed durable payload was not paused")
	}
}

func TestDispatcherFinalizesRuntimeFirstEvent(t *testing.T) {
	payload, err := plannerwire.EncodeStoredEvent(&liveroutev1.ApplyTripEvent{
		EventId:          testEventID,
		OccurredAtUnixMs: 1_799_999_000_000,
		Event: &liveroutev1.ApplyTripEvent_ActivityDelayed{
			ActivityDelayed: &liveroutev1.ActivityDelayed{
				ActivityId: testPlanID, DelaySeconds: 30,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := canonicalRow(t)
	row.ApplicationOrder = "runtime_first"
	row.CommandKind = persistence.CommandActivityDelayed
	row.EventPayload = payload
	row.ResultingTripRevision = 0
	row.ResultingCurrentPlanID = ""
	outbox := &fakeOutbox{rows: []persistence.ClaimedOutboxRow{row}}
	planner := &fakePlanner{}
	mirrors := &fakeMirrors{}
	confirmations := &fakeConfirmations{}
	runtime := &fakeRuntimeFinalizer{}
	dispatcher, err := New(Config{
		ClaimOwner:  "66666666-6666-4666-8666-666666666666",
		LeaseHolder: "77777777-7777-4777-8777-777777777777",
		BatchSize:   4, ClaimDuration: time.Minute, AttemptTimeout: time.Second,
	}, outbox, fakeLeases{}, planner, mirrors, runtime, confirmations)
	if err != nil {
		t.Fatal(err)
	}
	var published persistence.FinalizedCommand
	dispatcher.SetOnRuntimeFinalized(func(
		finalized persistence.FinalizedCommand,
		_ *liveroutev1.PlannerStreamResponse,
	) {
		published = finalized
	})
	resolved, err := dispatcher.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || runtime.row == nil || mirrors.request != nil ||
		len(confirmations.sequences) != 1 || published.EventID != row.EventID ||
		published.State != "applied" {
		t.Fatalf("runtime outcome did not finalize: %#v %#v", runtime.row, confirmations.sequences)
	}
}

func TestDispatcherRunPollsUntilCancellation(t *testing.T) {
	dispatcher := newTestDispatcher(t, &fakeOutbox{}, &fakePlanner{}, &fakeMirrors{}, &fakeConfirmations{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if err := dispatcher.Run(ctx, time.Millisecond, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatcher run returned %v, want cancellation", err)
	}
}

func TestDispatcherRunRejectsInvalidConfiguration(t *testing.T) {
	dispatcher := newTestDispatcher(t, &fakeOutbox{}, &fakePlanner{}, &fakeMirrors{}, &fakeConfirmations{})
	if err := dispatcher.Run(nil, time.Second, nil); err == nil {
		t.Fatal("nil context was accepted")
	}
	if err := dispatcher.Run(context.Background(), 0, nil); err == nil {
		t.Fatal("zero polling interval was accepted")
	}
}
