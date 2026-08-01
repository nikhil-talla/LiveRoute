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
	"github.com/liveroute/liveroute/backend/internal/canonicaljson"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

var ErrCanonicalCommandExpired = errors.New("canonical command expired before persistence")

type canonicalPlanReplacer interface {
	ReplaceCurrentPlan(context.Context, persistence.ReplaceCurrentPlanRequest) (persistence.RecordedCommand, error)
}

type ReplaceCurrentPlanAdapter struct {
	replacer                  canonicalPlanReplacer
	maxPendingCanonicalMirror uint32
}

func NewReplaceCurrentPlanAdapter(
	replacer canonicalPlanReplacer,
	maxPendingCanonicalMirror uint32,
) (*ReplaceCurrentPlanAdapter, error) {
	if replacer == nil || maxPendingCanonicalMirror == 0 {
		return nil, errors.New("replace-current-plan adapter configuration is invalid")
	}
	return &ReplaceCurrentPlanAdapter{
		replacer:                  replacer,
		maxPendingCanonicalMirror: maxPendingCanonicalMirror,
	}, nil
}

type replaceCurrentPlanEnvelope struct {
	ProtocolVersion string                    `json:"protocol_version"`
	MessageID       string                    `json:"message_id"`
	Kind            string                    `json:"kind"`
	TripID          string                    `json:"trip_id"`
	Payload         replaceCurrentPlanPayload `json:"payload"`
}

type replaceCurrentPlanPayload struct {
	CommandKind            string                    `json:"command_kind"`
	Command                replaceCurrentPlanCommand `json:"command"`
	CommandExpiresAtUnixMS *int64                    `json:"command_expires_at_unix_ms"`
}

type replaceCurrentPlanCommand struct {
	ExpectedTripRevision string         `json:"expected_trip_revision"`
	CurrentPlan          createUserPlan `json:"current_plan"`
}

func (adapter *ReplaceCurrentPlanAdapter) Handle(
	ctx context.Context,
	message AuthenticatedMessage,
) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("replace-current-plan context is required")
	}
	canonicalPayload, err := canonicalizeClientMessage(message.Raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize replace-current-plan command: %w", err)
	}
	var envelope replaceCurrentPlanEnvelope
	if err := json.Unmarshal(message.Raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode replace-current-plan command: %w", err)
	}
	if envelope.ProtocolVersion != protocolVersion || envelope.Kind != "trip_command" ||
		persistence.CommandKind(envelope.Payload.CommandKind) != persistence.CommandReplaceCurrentPlan ||
		envelope.MessageID == "" || envelope.TripID == "" || message.UserID == "" {
		return nil, errors.New("replace-current-plan envelope identity is invalid")
	}
	expectedRevision, err := strconv.ParseUint(envelope.Payload.Command.ExpectedTripRevision, 10, 64)
	if err != nil {
		return nil, errors.New("replace-current-plan revision is invalid")
	}
	commandExpiresAt, err := canonicalCommandExpiry(envelope.Payload.CommandExpiresAtUnixMS)
	if err != nil {
		return nil, err
	}
	segments := make([]persistence.CanonicalPlanSegmentDraft, len(envelope.Payload.Command.CurrentPlan.Segments))
	for index, segment := range envelope.Payload.Command.CurrentPlan.Segments {
		if segment.State == "scheduled" {
			if segment.Start == nil || segment.End == nil {
				return nil, errors.New("scheduled plan segment is incomplete")
			}
			segments[index] = persistence.CanonicalPlanSegmentDraft{
				ActivityID: segment.ActivityID, Scheduled: true,
				Start: segment.Start, End: segment.End,
			}
		} else {
			segments[index] = persistence.CanonicalPlanSegmentDraft{ActivityID: segment.ActivityID}
		}
	}
	intentID := newUUID()
	outboxID := newUUID()
	digest := sha256.Sum256(canonicalPayload)
	outcome := map[string]any{
		"trip_revision":   strconv.FormatUint(expectedRevision+1, 10),
		"current_plan_id": envelope.Payload.Command.CurrentPlan.PlanID,
	}
	outcomePayload, err := json.Marshal(outcome)
	if err != nil {
		return nil, fmt.Errorf("encode replace-current-plan outcome: %w", err)
	}
	result, err := adapter.replacer.ReplaceCurrentPlan(ctx, persistence.ReplaceCurrentPlanRequest{
		TripID:                    envelope.TripID,
		OwnerUserID:               message.UserID,
		IntentID:                  intentID,
		OutboxID:                  outboxID,
		MessageID:                 envelope.MessageID,
		EventID:                   envelope.MessageID,
		PlanID:                    envelope.Payload.Command.CurrentPlan.PlanID,
		ExpectedTripRevision:      expectedRevision,
		CommandExpiresAt:          commandExpiresAt,
		MaxPendingCanonicalMirror: adapter.maxPendingCanonicalMirror,
		PlanSegments:              segments,
		CommandPayload:            canonicalPayload,
		PayloadDigest:             digest,
		OutcomePayload:            outcomePayload,
		EventPayloadBuilder: func(metadata persistence.CanonicalPlanMetadata) (json.RawMessage, error) {
			return plannerwire.EncodeStoredEvent(&liveroutev1.ApplyTripEvent{
				EventId:          envelope.MessageID,
				OccurredAtUnixMs: metadata.CreatedAt.UnixMilli(),
				Event: &liveroutev1.ApplyTripEvent_CurrentPlanReplaced{
					CurrentPlanReplaced: &liveroutev1.CurrentPlanReplaced{
						CurrentPlan: currentPlanProto(metadata, envelope.Payload.Command.CurrentPlan),
					},
				},
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return canonicalCommittedAcknowledgement(envelope.TripID, envelope.MessageID, result, outcome)
}

func (adapter *ReplaceCurrentPlanAdapter) Publish(
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

func currentPlanProto(
	metadata persistence.CanonicalPlanMetadata,
	draft createUserPlan,
) *liveroutev1.CurrentPlan {
	segments := make([]*liveroutev1.CurrentPlanSegment, len(draft.Segments))
	for index, segment := range draft.Segments {
		converted := &liveroutev1.CurrentPlanSegment{
			ActivityId: segment.ActivityID,
		}
		if segment.State == "scheduled" {
			converted.State = liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_SCHEDULED
			converted.ScheduledStartUnixMs = segment.Start
			converted.ScheduledEndUnixMs = segment.End
		} else {
			converted.State = liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_OMITTED
		}
		segments[index] = converted
	}
	return &liveroutev1.CurrentPlan{
		PlanId:          metadata.ID,
		PlanRevision:    metadata.Revision,
		Origin:          liveroutev1.PlanOrigin_PLAN_ORIGIN_USER_AUTHORED,
		Segments:        segments,
		CreatedAtUnixMs: metadata.CreatedAt.UnixMilli(),
	}
}

func canonicalizeClientMessage(raw []byte) ([]byte, error) {
	if _, err := canonicaljson.Marshal(raw); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode client envelope for canonical identity: %w", err)
	}
	delete(envelope, "message_id")
	withoutMessageID, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("remove client message id from canonical identity: %w", err)
	}
	return canonicaljson.Marshal(withoutMessageID)
}

func canonicalCommittedAcknowledgement(
	tripID string,
	messageID string,
	result persistence.RecordedCommand,
	outcome map[string]any,
) ([]byte, error) {
	tripRevision := uint64(0)
	if result.ResultingTripRevision != nil {
		tripRevision = *result.ResultingTripRevision
	}
	mutationSequence := strconv.FormatUint(result.MutationSequence, 10)
	acknowledgement := map[string]any{
		"protocol_version": protocolVersion, "server_message_id": newUUID(),
		"kind": "command_acknowledgement", "status": "OK", "retryable": false,
		"trip_id": tripID, "trip_revision": strconv.FormatUint(tripRevision, 10),
		"runtime_epoch": "0", "planner_state_version": "0",
		"accepted_mutation_sequence": mutationSequence, "accepted_observation_sequence": "0",
		"payload": map[string]any{
			"phase": "canonical_committed", "message_id": messageID,
			"mutation_sequence": mutationSequence, "outcome": outcome,
			"recovery_state": "not_advancing",
		}, "in_reply_to_message_id": messageID,
	}
	return json.Marshal(acknowledgement)
}

func canonicalCommandExpiry(value *int64) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	expiresAt := time.UnixMilli(*value).UTC()
	if !expiresAt.After(time.Now().UTC()) {
		return nil, ErrCanonicalCommandExpired
	}
	return &expiresAt, nil
}
