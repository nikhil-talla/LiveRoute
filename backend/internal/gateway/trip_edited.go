package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

type canonicalTripEditor interface {
	ReorderActivities(context.Context, persistence.ReorderActivitiesRequest) (persistence.RecordedCommand, error)
	RemoveActivity(context.Context, persistence.RemoveActivityRequest) (persistence.RecordedCommand, error)
	ReplaceActivity(context.Context, persistence.ReplaceActivityRequest) (persistence.RecordedCommand, error)
	AddActivity(context.Context, persistence.AddActivityRequest) (persistence.RecordedCommand, error)
}

type TripEditedAdapter struct {
	editor                    canonicalTripEditor
	maxPendingCanonicalMirror uint32
}

func NewTripEditedAdapter(
	editor canonicalTripEditor,
	maxPendingCanonicalMirror uint32,
) (*TripEditedAdapter, error) {
	if editor == nil || maxPendingCanonicalMirror == 0 {
		return nil, errors.New("trip-edited adapter configuration is invalid")
	}
	return &TripEditedAdapter{
		editor:                    editor,
		maxPendingCanonicalMirror: maxPendingCanonicalMirror,
	}, nil
}

type tripEditedEnvelope struct {
	ProtocolVersion string            `json:"protocol_version"`
	MessageID       string            `json:"message_id"`
	Kind            string            `json:"kind"`
	TripID          string            `json:"trip_id"`
	Payload         tripEditedPayload `json:"payload"`
}

type tripEditedPayload struct {
	CommandKind            string            `json:"command_kind"`
	Command                tripEditedCommand `json:"command"`
	CommandExpiresAtUnixMS *int64            `json:"command_expires_at_unix_ms"`
}

type tripEditedCommand struct {
	ExpectedTripRevision string            `json:"expected_trip_revision"`
	Operation            tripEditOperation `json:"operation"`
	CurrentPlan          createUserPlan    `json:"current_plan"`
}

type tripEditOperation struct {
	Add     *tripEditAdd     `json:"add"`
	Replace *createActivity  `json:"replace"`
	Remove  *tripEditRemove  `json:"remove"`
	Reorder *tripEditReorder `json:"reorder"`
}

type tripEditAdd struct {
	Activity createActivity `json:"activity"`
	Ordinal  uint32         `json:"ordinal"`
}

type tripEditRemove struct {
	ActivityID string `json:"activity_id"`
}

type tripEditReorder struct {
	ActivityIDs []string `json:"activity_ids"`
}

func (adapter *TripEditedAdapter) Handle(
	ctx context.Context,
	message AuthenticatedMessage,
) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("trip-edited context is required")
	}
	canonicalPayload, err := canonicalizeClientMessage(message.Raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize trip-edited command: %w", err)
	}
	var envelope tripEditedEnvelope
	if err := json.Unmarshal(message.Raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode trip-edited command: %w", err)
	}
	if envelope.ProtocolVersion != protocolVersion || envelope.Kind != "trip_command" ||
		persistence.CommandKind(envelope.Payload.CommandKind) != persistence.CommandTripEdited ||
		envelope.MessageID == "" || envelope.TripID == "" || message.UserID == "" {
		return nil, errors.New("trip-edited envelope identity is invalid")
	}
	expectedRevision, err := strconv.ParseUint(envelope.Payload.Command.ExpectedTripRevision, 10, 64)
	if err != nil {
		return nil, errors.New("trip-edited revision is invalid")
	}
	commandExpiresAt, err := canonicalCommandExpiry(envelope.Payload.CommandExpiresAtUnixMS)
	if err != nil {
		return nil, err
	}
	segments, err := planSegments(envelope.Payload.Command.CurrentPlan)
	if err != nil {
		return nil, err
	}
	intentID := newUUID()
	outboxID := newUUID()
	digest := sha256.Sum256(canonicalPayload)
	outcome := map[string]any{"current_plan_id": envelope.Payload.Command.CurrentPlan.PlanID}
	outcomePayload, err := json.Marshal(outcome)
	if err != nil {
		return nil, fmt.Errorf("encode trip-edited outcome: %w", err)
	}
	metadata := persistence.ReplaceCurrentPlanRequest{
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
	}
	result, err := adapter.apply(ctx, envelope, message.UserID, metadata)
	if err != nil {
		return nil, err
	}
	return canonicalCommittedAcknowledgement(envelope.TripID, envelope.MessageID, result, outcome)
}

func (adapter *TripEditedAdapter) Publish(
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

func (adapter *TripEditedAdapter) apply(
	ctx context.Context,
	envelope tripEditedEnvelope,
	ownerUserID string,
	base persistence.ReplaceCurrentPlanRequest,
) (persistence.RecordedCommand, error) {
	operation := envelope.Payload.Command.Operation
	builders := func(metadata persistence.CanonicalPlanMetadata) (json.RawMessage, error) {
		return tripEditedEvent(envelope, metadata)
	}
	switch {
	case operation.Add != nil:
		return adapter.editor.AddActivity(ctx, persistence.AddActivityRequest{
			TripID: base.TripID, OwnerUserID: ownerUserID, IntentID: base.IntentID,
			OutboxID: base.OutboxID, MessageID: base.MessageID, EventID: base.EventID,
			PlanID: base.PlanID, ExpectedTripRevision: base.ExpectedTripRevision,
			CommandExpiresAt:          base.CommandExpiresAt,
			MaxPendingCanonicalMirror: base.MaxPendingCanonicalMirror, Ordinal: operation.Add.Ordinal,
			Activity: toCanonicalActivity(operation.Add.Activity, operation.Add.Ordinal), PlanSegments: base.PlanSegments,
			CommandPayload: base.CommandPayload, PayloadDigest: base.PayloadDigest,
			OutcomePayload: base.OutcomePayload, EventPayloadBuilder: builders,
		})
	case operation.Replace != nil:
		return adapter.editor.ReplaceActivity(ctx, persistence.ReplaceActivityRequest{
			TripID: base.TripID, OwnerUserID: ownerUserID, IntentID: base.IntentID,
			OutboxID: base.OutboxID, MessageID: base.MessageID, EventID: base.EventID,
			PlanID: base.PlanID, ExpectedTripRevision: base.ExpectedTripRevision,
			CommandExpiresAt:          base.CommandExpiresAt,
			MaxPendingCanonicalMirror: base.MaxPendingCanonicalMirror,
			Activity:                  toCanonicalActivity(*operation.Replace, 0), PlanSegments: base.PlanSegments,
			CommandPayload: base.CommandPayload, PayloadDigest: base.PayloadDigest,
			OutcomePayload: base.OutcomePayload, EventPayloadBuilder: builders,
		})
	case operation.Remove != nil:
		return adapter.editor.RemoveActivity(ctx, persistence.RemoveActivityRequest{
			TripID: base.TripID, OwnerUserID: ownerUserID, IntentID: base.IntentID,
			OutboxID: base.OutboxID, MessageID: base.MessageID, EventID: base.EventID,
			PlanID: base.PlanID, ExpectedTripRevision: base.ExpectedTripRevision,
			CommandExpiresAt:          base.CommandExpiresAt,
			MaxPendingCanonicalMirror: base.MaxPendingCanonicalMirror, ActivityID: operation.Remove.ActivityID,
			PlanSegments: base.PlanSegments, CommandPayload: base.CommandPayload,
			PayloadDigest: base.PayloadDigest, OutcomePayload: base.OutcomePayload, EventPayloadBuilder: builders,
		})
	case operation.Reorder != nil:
		return adapter.editor.ReorderActivities(ctx, persistence.ReorderActivitiesRequest{
			TripID: base.TripID, OwnerUserID: ownerUserID, IntentID: base.IntentID,
			OutboxID: base.OutboxID, MessageID: base.MessageID, EventID: base.EventID,
			PlanID: base.PlanID, ExpectedTripRevision: base.ExpectedTripRevision,
			CommandExpiresAt:          base.CommandExpiresAt,
			MaxPendingCanonicalMirror: base.MaxPendingCanonicalMirror, ActivityIDs: operation.Reorder.ActivityIDs,
			PlanSegments: base.PlanSegments, CommandPayload: base.CommandPayload,
			PayloadDigest: base.PayloadDigest, OutcomePayload: base.OutcomePayload, EventPayloadBuilder: builders,
		})
	default:
		return persistence.RecordedCommand{}, errors.New("trip-edited operation is missing")
	}
}

func planSegments(plan createUserPlan) ([]persistence.CanonicalPlanSegmentDraft, error) {
	segments := make([]persistence.CanonicalPlanSegmentDraft, len(plan.Segments))
	for index, segment := range plan.Segments {
		if segment.State == "scheduled" {
			if segment.Start == nil || segment.End == nil {
				return nil, errors.New("scheduled plan segment is incomplete")
			}
			segments[index] = persistence.CanonicalPlanSegmentDraft{
				ActivityID: segment.ActivityID, Scheduled: true, Start: segment.Start, End: segment.End,
			}
		} else {
			segments[index] = persistence.CanonicalPlanSegmentDraft{ActivityID: segment.ActivityID}
		}
	}
	return segments, nil
}

func tripEditedEvent(
	envelope tripEditedEnvelope,
	metadata persistence.CanonicalPlanMetadata,
) (json.RawMessage, error) {
	operation := envelope.Payload.Command.Operation
	tripEdited := &liveroutev1.TripEdited{
		ResultingCurrentPlan: currentPlanProto(metadata, envelope.Payload.Command.CurrentPlan),
	}
	switch {
	case operation.Add != nil:
		tripEdited.Operation = &liveroutev1.TripEdited_Add{Add: &liveroutev1.AddActivity{
			Activity: activityProto(operation.Add.Activity, operation.Add.Ordinal), Ordinal: operation.Add.Ordinal,
		}}
	case operation.Replace != nil:
		tripEdited.Operation = &liveroutev1.TripEdited_Replace{Replace: &liveroutev1.ReplaceActivity{
			Activity: activityProto(*operation.Replace, 0),
		}}
	case operation.Remove != nil:
		tripEdited.Operation = &liveroutev1.TripEdited_Remove{Remove: &liveroutev1.RemoveActivity{
			ActivityId: operation.Remove.ActivityID,
		}}
	case operation.Reorder != nil:
		tripEdited.Operation = &liveroutev1.TripEdited_Reorder{Reorder: &liveroutev1.ReorderActivities{
			ActivityIds: operation.Reorder.ActivityIDs,
		}}
	default:
		return nil, errors.New("trip-edited operation is missing")
	}
	return plannerwire.EncodeStoredEvent(&liveroutev1.ApplyTripEvent{
		EventId: envelope.MessageID, OccurredAtUnixMs: metadata.CreatedAt.UnixMilli(),
		Event: &liveroutev1.ApplyTripEvent_TripEdited{TripEdited: tripEdited},
	})
}

func activityProto(activity createActivity, ordinal uint32) *liveroutev1.Activity {
	result := &liveroutev1.Activity{
		ActivityId: activity.ActivityID, PlaceId: activity.PlaceID, DisplayName: activity.DisplayName,
		Location:     &liveroutev1.Location{Latitude: activity.Location.Latitude, Longitude: activity.Location.Longitude},
		TimeZoneName: activity.TimeZoneName, PriorityRank: activity.PriorityRank, UtilityScore: activity.UtilityScore,
		ActivityDelaySeconds: activity.ActivityDelay, ActivityState: activityStateProto(activity.ActivityState),
		InboundTravelMode: travelModeProto(activity.InboundTravelMode), ActivityClass: activityClassProto(activity.ActivityClass),
		Timing: &liveroutev1.ActivityTiming{
			ReservationGraceSeconds: activity.Timing.ReservationGraceSeconds,
			MinDurationSeconds:      activity.Timing.MinDurationSeconds, PreferredDurationSeconds: activity.Timing.PreferredDurationSeconds,
			MaxDurationSeconds: activity.Timing.MaxDurationSeconds, Mandatory: activity.Timing.Mandatory,
			CanShorten: activity.Timing.CanShorten, CanMove: activity.Timing.CanMove, CanSkip: activity.Timing.CanSkip,
		},
	}
	if activity.Timing.ReservationStart != nil {
		result.Timing.ReservationStartUnixMs = activity.Timing.ReservationStart
	}
	if activity.Timing.MandatoryDeadline != nil {
		result.Timing.MandatoryDeadlineUnixMs = activity.Timing.MandatoryDeadline
	}
	result.Timing.OpenWindows = make([]*liveroutev1.TimeWindow, len(activity.Timing.OpenWindows))
	for index, window := range activity.Timing.OpenWindows {
		result.Timing.OpenWindows[index] = &liveroutev1.TimeWindow{OpensAtUnixMs: window.OpensAt, ClosesAtUnixMs: window.ClosesAt}
	}
	if activity.FoundClosedAt != nil {
		result.FoundClosedAtUnixMs = activity.FoundClosedAt
	}
	_ = ordinal
	return result
}

func activityStateProto(state string) liveroutev1.ActivityState {
	switch state {
	case "planned":
		return liveroutev1.ActivityState_ACTIVITY_STATE_PLANNED
	case "started":
		return liveroutev1.ActivityState_ACTIVITY_STATE_STARTED
	case "completed":
		return liveroutev1.ActivityState_ACTIVITY_STATE_COMPLETED
	case "skipped":
		return liveroutev1.ActivityState_ACTIVITY_STATE_SKIPPED
	default:
		return liveroutev1.ActivityState_ACTIVITY_STATE_UNSPECIFIED
	}
}

func travelModeProto(mode string) liveroutev1.TravelMode {
	if mode == "driving" {
		return liveroutev1.TravelMode_TRAVEL_MODE_DRIVING
	}
	return liveroutev1.TravelMode_TRAVEL_MODE_WALKING
}

func activityClassProto(class string) liveroutev1.ActivityClass {
	if class == "fixed" {
		return liveroutev1.ActivityClass_ACTIVITY_CLASS_FIXED
	}
	return liveroutev1.ActivityClass_ACTIVITY_CLASS_FLEXIBLE
}
