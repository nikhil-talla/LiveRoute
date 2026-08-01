package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

type runtimeMutationRecorder interface {
	RecordRuntimeFirst(context.Context, persistence.RecordRuntimeCommandRequest) (persistence.RecordedCommand, error)
}

type RuntimeMutationAdapter struct {
	recorder runtimeMutationRecorder
}

func NewRuntimeMutationAdapter(recorder runtimeMutationRecorder) (*RuntimeMutationAdapter, error) {
	if recorder == nil {
		return nil, errors.New("runtime mutation recorder is required")
	}
	return &RuntimeMutationAdapter{recorder: recorder}, nil
}

type runtimeMutationEnvelope struct {
	ProtocolVersion string                 `json:"protocol_version"`
	MessageID       string                 `json:"message_id"`
	Kind            string                 `json:"kind"`
	TripID          string                 `json:"trip_id"`
	Payload         runtimeMutationPayload `json:"payload"`
}

type runtimeMutationPayload struct {
	CommandKind            string          `json:"command_kind"`
	Command                json.RawMessage `json:"command"`
	CommandExpiresAtUnixMS *int64          `json:"command_expires_at_unix_ms"`
}

type activityStatusCommand struct {
	ExpectedTripRevision string `json:"expected_trip_revision"`
	ActivityID           string `json:"activity_id"`
	State                string `json:"state"`
}

type activityDelayedCommand struct {
	ExpectedTripRevision string `json:"expected_trip_revision"`
	ActivityID           string `json:"activity_id"`
	DelaySeconds         uint32 `json:"delay_seconds"`
}

type reservationChangedCommand struct {
	ExpectedTripRevision string `json:"expected_trip_revision"`
	ActivityID           string `json:"activity_id"`
	ReservationStart     *int64 `json:"reservation_start_unix_ms"`
	ReservationGrace     uint32 `json:"reservation_grace_seconds"`
}

type mandatoryDeadlineCommand struct {
	ExpectedTripRevision string `json:"expected_trip_revision"`
	ActivityID           string `json:"activity_id"`
	LatestFinish         int64  `json:"latest_finish_unix_ms"`
}

type operatingHoursCommand struct {
	ExpectedTripRevision string         `json:"expected_trip_revision"`
	ActivityID           string         `json:"activity_id"`
	OpenWindows          []createWindow `json:"open_windows"`
}

type placeClosedCommand struct {
	ExpectedTripRevision string `json:"expected_trip_revision"`
	ActivityID           string `json:"activity_id"`
	ObservedAt           int64  `json:"observed_at_unix_ms"`
}

type travelDelayCommand struct {
	ExpectedTripRevision string `json:"expected_trip_revision"`
	FromActivityID       string `json:"from_activity_id"`
	ToActivityID         string `json:"to_activity_id"`
	AdditionalSeconds    uint32 `json:"additional_seconds"`
}

func (adapter *RuntimeMutationAdapter) Handle(
	ctx context.Context,
	message AuthenticatedMessage,
) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("runtime mutation context is required")
	}
	canonicalPayload, err := canonicalizeClientMessage(message.Raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize runtime command: %w", err)
	}
	var envelope runtimeMutationEnvelope
	if err := json.Unmarshal(message.Raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode runtime command: %w", err)
	}
	if envelope.ProtocolVersion != protocolVersion || envelope.Kind != "trip_command" ||
		envelope.MessageID == "" || envelope.TripID == "" || message.UserID == "" {
		return nil, errors.New("runtime command envelope identity is invalid")
	}
	kind := persistence.CommandKind(envelope.Payload.CommandKind)
	if !isOrdinaryRuntimeMutation(kind) {
		return nil, errors.New("runtime command kind is not an ordinary mutation")
	}
	expectedRevision, err := runtimeExpectedRevision(kind, envelope.Payload.Command)
	if err != nil {
		return nil, err
	}
	expiresAt, err := runtimeCommandExpiry(envelope.Payload.CommandExpiresAtUnixMS)
	if err != nil {
		return nil, err
	}
	event, err := runtimeMutationEvent(kind, envelope, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	event.CommandExpiresAtUnixMs = envelope.Payload.CommandExpiresAtUnixMS
	eventPayload, err := plannerwire.EncodeStoredEvent(event)
	if err != nil {
		return nil, fmt.Errorf("encode runtime planner event: %w", err)
	}
	intentID := newUUID()
	outboxID := newUUID()
	digest := sha256.Sum256(canonicalPayload)
	result, err := adapter.recorder.RecordRuntimeFirst(ctx, persistence.RecordRuntimeCommandRequest{
		IntentID: intentID, OutboxID: outboxID, TripID: envelope.TripID,
		OwnerUserID: message.UserID, MessageID: envelope.MessageID, EventID: envelope.MessageID,
		ExpectedTripRevision: expectedRevision, Kind: kind, CommandExpiresAt: expiresAt,
		PayloadDigest: digest, CommandPayload: canonicalPayload, EventPayload: eventPayload,
	})
	if err != nil {
		return nil, err
	}
	return runtimeDurableAcknowledgement(envelope, result)
}

func (adapter *RuntimeMutationAdapter) Publish(
	ctx context.Context,
	message AuthenticatedMessage,
) error {
	if message.Sink == nil {
		return errors.New("authenticated message sink is required")
	}
	acknowledgement, err := adapter.Handle(ctx, message)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(acknowledgement)
}

func isOrdinaryRuntimeMutation(kind persistence.CommandKind) bool {
	switch kind {
	case persistence.CommandActivityStatusChanged,
		persistence.CommandActivityDelayed,
		persistence.CommandReservationChanged,
		persistence.CommandMandatoryDeadlineChanged,
		persistence.CommandOperatingHoursChanged,
		persistence.CommandPlaceFoundClosed,
		persistence.CommandTravelDelay:
		return true
	default:
		return false
	}
}

func runtimeExpectedRevision(kind persistence.CommandKind, command json.RawMessage) (uint64, error) {
	var value struct {
		ExpectedTripRevision string `json:"expected_trip_revision"`
	}
	if err := json.Unmarshal(command, &value); err != nil {
		return 0, fmt.Errorf("decode runtime revision: %w", err)
	}
	result, err := strconv.ParseUint(value.ExpectedTripRevision, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("runtime revision is invalid for %s: %w", kind, err)
	}
	return result, nil
}

func runtimeCommandExpiry(value *int64) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	expiresAt := time.UnixMilli(*value).UTC()
	return &expiresAt, nil
}

func runtimeMutationEvent(
	kind persistence.CommandKind,
	envelope runtimeMutationEnvelope,
	now time.Time,
) (*liveroutev1.ApplyTripEvent, error) {
	base := &liveroutev1.ApplyTripEvent{EventId: envelope.MessageID, OccurredAtUnixMs: now.UnixMilli()}
	switch kind {
	case persistence.CommandActivityStatusChanged:
		var command activityStatusCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode activity status command: %w", err)
		}
		base.Event = &liveroutev1.ApplyTripEvent_ActivityStatusChanged{ActivityStatusChanged: &liveroutev1.ActivityStatusChanged{
			ActivityId: command.ActivityID, State: activityStateProto(command.State),
		}}
	case persistence.CommandActivityDelayed:
		var command activityDelayedCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode activity delay command: %w", err)
		}
		base.Event = &liveroutev1.ApplyTripEvent_ActivityDelayed{ActivityDelayed: &liveroutev1.ActivityDelayed{
			ActivityId: command.ActivityID, DelaySeconds: command.DelaySeconds,
		}}
	case persistence.CommandReservationChanged:
		var command reservationChangedCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode reservation command: %w", err)
		}
		base.Event = &liveroutev1.ApplyTripEvent_ReservationChanged{ReservationChanged: &liveroutev1.ReservationChanged{
			ActivityId: command.ActivityID, ReservationStartUnixMs: command.ReservationStart,
			ReservationGraceSeconds: command.ReservationGrace,
		}}
	case persistence.CommandMandatoryDeadlineChanged:
		var command mandatoryDeadlineCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode mandatory-deadline command: %w", err)
		}
		base.Event = &liveroutev1.ApplyTripEvent_MandatoryDeadlineChanged{MandatoryDeadlineChanged: &liveroutev1.MandatoryDeadlineChanged{
			ActivityId: command.ActivityID, LatestFinishUnixMs: command.LatestFinish,
		}}
	case persistence.CommandOperatingHoursChanged:
		var command operatingHoursCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode operating-hours command: %w", err)
		}
		windows := make([]*liveroutev1.TimeWindow, len(command.OpenWindows))
		for index, window := range command.OpenWindows {
			windows[index] = &liveroutev1.TimeWindow{OpensAtUnixMs: window.OpensAt, ClosesAtUnixMs: window.ClosesAt}
		}
		base.Event = &liveroutev1.ApplyTripEvent_OperatingHoursChanged{OperatingHoursChanged: &liveroutev1.OperatingHoursChanged{
			ActivityId: command.ActivityID, OpenWindows: windows,
		}}
	case persistence.CommandPlaceFoundClosed:
		var command placeClosedCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode place-closed command: %w", err)
		}
		base.Event = &liveroutev1.ApplyTripEvent_PlaceFoundClosed{PlaceFoundClosed: &liveroutev1.PlaceFoundClosed{
			ActivityId: command.ActivityID, ObservedAtUnixMs: command.ObservedAt,
		}}
	case persistence.CommandTravelDelay:
		var command travelDelayCommand
		if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
			return nil, fmt.Errorf("decode travel-delay command: %w", err)
		}
		base.Event = &liveroutev1.ApplyTripEvent_TravelDelay{TravelDelay: &liveroutev1.TravelDelay{
			FromActivityId: command.FromActivityID, ToActivityId: command.ToActivityID,
			AdditionalSeconds: command.AdditionalSeconds,
		}}
	default:
		return nil, errors.New("unsupported runtime mutation")
	}
	return base, nil
}

func runtimeDurableAcknowledgement(
	envelope runtimeMutationEnvelope,
	result persistence.RecordedCommand,
) ([]byte, error) {
	tripRevision := strconv.FormatUint(result.ExpectedTripRevision, 10)
	mutationSequence := strconv.FormatUint(result.MutationSequence, 10)
	return json.Marshal(map[string]any{
		"protocol_version": protocolVersion, "server_message_id": newUUID(),
		"kind": "command_acknowledgement", "status": "OK", "retryable": false,
		"trip_id": envelope.TripID, "trip_revision": tripRevision,
		"runtime_epoch": "0", "planner_state_version": "0",
		"accepted_mutation_sequence": mutationSequence, "accepted_observation_sequence": "0",
		"payload": map[string]any{
			"phase": "durable_recorded", "message_id": envelope.MessageID,
			"mutation_sequence": mutationSequence, "recovery_state": "not_advancing",
		}, "in_reply_to_message_id": envelope.MessageID,
	})
}
