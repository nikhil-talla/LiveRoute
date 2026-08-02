package dispatch

import (
	"context"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
)

type telemetryPlannerFake struct{}

func (telemetryPlannerFake) Exchange(_ context.Context, request *liveroutev1.PlannerStreamRequest) (*liveroutev1.PlannerStreamResponse, error) {
	return &liveroutev1.PlannerStreamResponse{
		RequestId: request.GetRequestId(), TripId: request.GetTripId(), RuntimeEpoch: request.GetRuntimeEpoch(),
		AcceptedMutationSequence: 1, AcceptedObservationSequence: request.GetObservationSequence(), PlannerStateVersion: 1,
		Payload: &liveroutev1.PlannerStreamResponse_EventAcknowledged{EventAcknowledged: &liveroutev1.EventAcknowledged{
			Disposition: liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED,
			Status:      liveroutev1.StatusCode_STATUS_CODE_OK, EventId: request.GetApplyEvent().GetEventId(),
			ResolvedObservationSequence: request.GetObservationSequence(),
		}},
	}, nil
}

func TestTelemetryDispatcherReservesAndCommitsObservation(t *testing.T) {
	leases := &supervisorLeaseFake{}
	bootstrap := &supervisorBootstrapFake{}
	supervisor, err := NewRuntimeSupervisor("550e8400-e29b-41d4-a716-446655440001", 100*time.Millisecond, 20*time.Millisecond, 1, leases, bootstrap, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	tripID := "550e8400-e29b-41d4-a716-446655440002"
	if err := supervisor.Activate(context.Background(), tripID); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewTelemetryDispatcher(telemetryPlannerFake{}, supervisor, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, sequence, err := dispatcher.Dispatch(context.Background(), tripID, &liveroutev1.ApplyTripEvent{
		OccurredAtUnixMs: 1700000000000,
		Event:            &liveroutev1.ApplyTripEvent_HeadingUpdated{HeadingUpdated: &liveroutev1.HeadingUpdated{Degrees: 90}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 1 || response.GetAcceptedObservationSequence() != 1 {
		t.Fatalf("unexpected telemetry response: sequence=%d response=%#v", sequence, response)
	}
	state, ok := supervisor.RuntimeState(tripID)
	if !ok || state.AcceptedObservationSequence != 1 || state.PlannerStateVersion != 1 {
		t.Fatalf("runtime state was not committed: %#v/%t", state, ok)
	}
}
