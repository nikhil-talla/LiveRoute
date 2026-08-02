package dispatch

import (
	"context"
	"errors"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/plannertransport"
)

type TelemetryDispatcher struct {
	planner    plannerClient
	supervisor *RuntimeSupervisor
	timeout    time.Duration
	now        func() time.Time
}

func NewTelemetryDispatcher(planner plannerClient, supervisor *RuntimeSupervisor, timeout time.Duration) (*TelemetryDispatcher, error) {
	if planner == nil || supervisor == nil || timeout <= 0 {
		return nil, errors.New("invalid telemetry dispatcher configuration")
	}
	return &TelemetryDispatcher{planner: planner, supervisor: supervisor, timeout: timeout, now: time.Now}, nil
}

func (dispatcher *TelemetryDispatcher) Dispatch(ctx context.Context, tripID string, event *liveroutev1.ApplyTripEvent) (*liveroutev1.PlannerStreamResponse, uint64, error) {
	if ctx == nil || event == nil {
		return nil, 0, errors.New("telemetry dispatch input is invalid")
	}
	state, observationSequence, err := dispatcher.supervisor.ReserveObservation(tripID)
	if err != nil {
		return nil, 0, err
	}
	requestID, err := plannertransport.NewRequestID()
	if err != nil {
		return nil, 0, err
	}
	// Telemetry event ids are backend-generated and are not the client
	// message-id deduplication key. A fresh request id is sufficient here.
	event.EventId = requestID
	request := &liveroutev1.PlannerStreamRequest{
		RequestId: requestID, TripId: tripID, RuntimeEpoch: state.RuntimeEpoch,
		ObservationSequence: observationSequence,
		ExpiresAtUnixMs:     dispatcher.now().Add(dispatcher.timeout).UnixMilli(),
		Payload:             &liveroutev1.PlannerStreamRequest_ApplyEvent{ApplyEvent: event},
	}
	attempt, cancel := context.WithTimeout(ctx, dispatcher.timeout)
	response, err := dispatcher.planner.Exchange(attempt, request)
	cancel()
	if err != nil {
		return nil, observationSequence, err
	}
	ack := response.GetEventAcknowledged()
	if response.GetRequestId() != requestID || response.GetTripId() != tripID ||
		response.GetRuntimeEpoch() != state.RuntimeEpoch || ack == nil || ack.GetEventId() != event.GetEventId() {
		return nil, observationSequence, errors.New("telemetry acknowledgement correlation failed")
	}
	if err := dispatcher.supervisor.CommitObservation(
		tripID, response.GetRuntimeEpoch(), ack.GetResolvedObservationSequence(),
		response.GetAcceptedMutationSequence(), response.GetPlannerStateVersion(),
	); err != nil {
		return nil, observationSequence, err
	}
	return response, observationSequence, nil
}

func TelemetryDisposition(response *liveroutev1.PlannerStreamResponse) (string, string, bool) {
	if response == nil || response.GetEventAcknowledged() == nil {
		return "rejected", "INTERNAL", false
	}
	ack := response.GetEventAcknowledged()
	status := statusName(ack.GetStatus())
	switch ack.GetDisposition() {
	case liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED:
		return "accepted", status, false
	case liveroutev1.EventDisposition_EVENT_DISPOSITION_DUPLICATE:
		return "accepted", status, false
	case liveroutev1.EventDisposition_EVENT_DISPOSITION_STALE:
		return "dropped", status, false
	default:
		return "rejected", status, ack.GetRetryable()
	}
}
