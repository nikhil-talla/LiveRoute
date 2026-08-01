package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

var (
	ErrCommandKindUnsupported   = errors.New("websocket command kind is not supported")
	ErrClientMessageUnsupported = errors.New("websocket message kind is not supported")
)

type envelopeHandler interface {
	Handle(context.Context, AuthenticatedMessage) ([]byte, error)
}

// CommandDispatcher is the transport-only selection boundary between the
// authenticated WebSocket envelope and the already-validated command
// adapters. It does not perform persistence, trip serialization, or planner
// dispatch itself.
type CommandDispatcher struct {
	createTrip         envelopeHandler
	replaceCurrentPlan envelopeHandler
	tripEdited         envelopeHandler
	runtimeMutation    envelopeHandler
	proposalDecision   envelopeHandler
}

func NewCommandDispatcher(
	createTrip envelopeHandler,
	replaceCurrentPlan envelopeHandler,
	tripEdited envelopeHandler,
	runtimeMutation envelopeHandler,
	proposalDecision envelopeHandler,
) (*CommandDispatcher, error) {
	if createTrip == nil || replaceCurrentPlan == nil || tripEdited == nil ||
		runtimeMutation == nil || proposalDecision == nil {
		return nil, errors.New("all websocket command adapters are required")
	}
	return &CommandDispatcher{
		createTrip:         createTrip,
		replaceCurrentPlan: replaceCurrentPlan,
		tripEdited:         tripEdited,
		runtimeMutation:    runtimeMutation,
		proposalDecision:   proposalDecision,
	}, nil
}

func (dispatcher *CommandDispatcher) Dispatch(
	ctx context.Context,
	message AuthenticatedMessage,
) ([]byte, error) {
	if dispatcher == nil || ctx == nil {
		return nil, errors.New("websocket command dispatcher is not configured")
	}
	kind, ok := message.Message["kind"].(string)
	if !ok {
		return nil, errors.New("websocket message kind is missing")
	}
	switch kind {
	case "create_trip":
		return dispatcher.createTrip.Handle(ctx, message)
	case "trip_command":
		commandKind, err := tripCommandKind(message)
		if err != nil {
			return nil, err
		}
		switch persistence.CommandKind(commandKind) {
		case persistence.CommandReplaceCurrentPlan:
			return dispatcher.replaceCurrentPlan.Handle(ctx, message)
		case persistence.CommandTripEdited:
			return dispatcher.tripEdited.Handle(ctx, message)
		case persistence.CommandAcceptProposal, persistence.CommandRejectProposal:
			return dispatcher.proposalDecision.Handle(ctx, message)
		case persistence.CommandActivityStatusChanged,
			persistence.CommandActivityDelayed,
			persistence.CommandReservationChanged,
			persistence.CommandMandatoryDeadlineChanged,
			persistence.CommandOperatingHoursChanged,
			persistence.CommandPlaceFoundClosed,
			persistence.CommandTravelDelay:
			return dispatcher.runtimeMutation.Handle(ctx, message)
		default:
			return nil, fmt.Errorf("%w: %s", ErrCommandKindUnsupported, commandKind)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrClientMessageUnsupported, kind)
	}
}

// Publish dispatches one admitted message and places only the adapter's
// schema-valid acknowledgement on the authenticated connection. Error
// envelopes require the caller's durable/runtime state and are deliberately
// kept outside this transport-only selection boundary.
func (dispatcher *CommandDispatcher) Publish(
	ctx context.Context,
	message AuthenticatedMessage,
) error {
	if message.Sink == nil {
		return errors.New("authenticated message sink is required")
	}
	acknowledgement, err := dispatcher.Dispatch(ctx, message)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(acknowledgement)
}

func tripCommandKind(message AuthenticatedMessage) (string, error) {
	payload, ok := message.Message["payload"].(map[string]any)
	if !ok {
		return "", errors.New("trip command payload is missing")
	}
	commandKind, ok := payload["command_kind"].(string)
	if !ok || commandKind == "" {
		return "", errors.New("trip command kind is missing")
	}
	return commandKind, nil
}
