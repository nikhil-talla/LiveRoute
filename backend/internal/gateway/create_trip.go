package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

type canonicalTripCreator interface {
	CreateTrip(context.Context, persistence.CreateTripRequest) (persistence.RecordedCommand, error)
}

type CreateTripAdapter struct {
	creator canonicalTripCreator
}

func NewCreateTripAdapter(creator canonicalTripCreator) (*CreateTripAdapter, error) {
	if creator == nil {
		return nil, errors.New("canonical trip creator is required")
	}
	return &CreateTripAdapter{creator: creator}, nil
}

type createTripEnvelope struct {
	ProtocolVersion string            `json:"protocol_version"`
	MessageID       string            `json:"message_id"`
	Kind            string            `json:"kind"`
	TripID          string            `json:"trip_id"`
	Payload         createTripPayload `json:"payload"`
}

type createTripPayload struct {
	DefaultTimeZoneName string           `json:"default_time_zone_name"`
	Activities          []createActivity `json:"activities"`
	CurrentPlan         createUserPlan   `json:"current_plan"`
}

type createActivity struct {
	ActivityID        string               `json:"activity_id"`
	PlaceID           string               `json:"place_id"`
	DisplayName       string               `json:"display_name"`
	Location          createLocation       `json:"location"`
	TimeZoneName      string               `json:"time_zone_name"`
	InboundTravelMode string               `json:"inbound_travel_mode"`
	ActivityClass     string               `json:"activity_class"`
	ActivityState     string               `json:"activity_state"`
	PriorityRank      int32                `json:"priority_rank"`
	UtilityScore      int32                `json:"utility_score"`
	Timing            createActivityTiming `json:"timing"`
	ActivityDelay     uint32               `json:"activity_delay_seconds"`
	FoundClosedAt     *int64               `json:"found_closed_at_unix_ms"`
}

type createLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type createActivityTiming struct {
	OpenWindows              []createWindow `json:"open_windows"`
	ReservationStart         *int64         `json:"reservation_start_unix_ms"`
	ReservationGraceSeconds  uint32         `json:"reservation_grace_seconds"`
	MinDurationSeconds       uint32         `json:"min_duration_seconds"`
	PreferredDurationSeconds uint32         `json:"preferred_duration_seconds"`
	MaxDurationSeconds       uint32         `json:"max_duration_seconds"`
	Mandatory                bool           `json:"mandatory"`
	CanShorten               bool           `json:"can_shorten"`
	CanMove                  bool           `json:"can_move"`
	CanSkip                  bool           `json:"can_skip"`
	MandatoryDeadline        *int64         `json:"mandatory_deadline_unix_ms"`
}

type createWindow struct {
	OpensAt  int64 `json:"opens_at_unix_ms"`
	ClosesAt int64 `json:"closes_at_unix_ms"`
}

type createUserPlan struct {
	PlanID   string          `json:"plan_id"`
	Segments []createSegment `json:"segments"`
}

type createSegment struct {
	ActivityID string `json:"activity_id"`
	State      string `json:"state"`
	Start      *int64 `json:"scheduled_start_unix_ms"`
	End        *int64 `json:"scheduled_end_unix_ms"`
}

func (adapter *CreateTripAdapter) Handle(ctx context.Context, message AuthenticatedMessage) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("create-trip context is required")
	}
	canonicalPayload, err := canonicalizeClientMessage(message.Raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize create-trip command: %w", err)
	}
	var envelope createTripEnvelope
	if err := json.Unmarshal(message.Raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode create-trip command: %w", err)
	}
	if envelope.ProtocolVersion != protocolVersion || envelope.Kind != "create_trip" ||
		envelope.MessageID == "" || envelope.TripID == "" || message.UserID == "" {
		return nil, errors.New("create-trip envelope identity is invalid")
	}
	activities := make([]persistence.CanonicalActivity, len(envelope.Payload.Activities))
	for index, activity := range envelope.Payload.Activities {
		activities[index] = toCanonicalActivity(activity, uint32(index))
	}
	segments := make([]persistence.CanonicalPlanSegmentDraft, len(envelope.Payload.CurrentPlan.Segments))
	for index, segment := range envelope.Payload.CurrentPlan.Segments {
		if segment.State == "scheduled" {
			segments[index] = persistence.CanonicalPlanSegmentDraft{
				ActivityID: segment.ActivityID, Scheduled: true, Start: segment.Start, End: segment.End,
			}
		} else {
			segments[index] = persistence.CanonicalPlanSegmentDraft{ActivityID: segment.ActivityID}
		}
	}
	intentID := newUUID()
	digest := sha256.Sum256(canonicalPayload)
	outcome := map[string]any{
		"trip_revision":   "1",
		"current_plan_id": envelope.Payload.CurrentPlan.PlanID,
	}
	outcomePayload, err := json.Marshal(outcome)
	if err != nil {
		return nil, fmt.Errorf("encode create-trip outcome: %w", err)
	}
	result, err := adapter.creator.CreateTrip(ctx, persistence.CreateTripRequest{
		TripID: envelope.TripID, OwnerUserID: message.UserID,
		DefaultTimeZoneName: envelope.Payload.DefaultTimeZoneName,
		IntentID:            intentID, MessageID: envelope.MessageID, EventID: envelope.MessageID,
		PlanID: envelope.Payload.CurrentPlan.PlanID, Activities: activities,
		PlanSegments: segments, CommandPayload: canonicalPayload,
		PayloadDigest: digest, OutcomePayload: outcomePayload,
	})
	if err != nil {
		return nil, err
	}
	return createTripAcknowledgement(envelope, result, outcome)
}

func (adapter *CreateTripAdapter) Publish(ctx context.Context, message AuthenticatedMessage) error {
	if message.Sink == nil {
		return errors.New("authenticated message sink is required")
	}
	acknowledgement, err := adapter.Handle(ctx, message)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(acknowledgement)
}

func toCanonicalActivity(activity createActivity, ordinal uint32) persistence.CanonicalActivity {
	return persistence.CanonicalActivity{
		ID: activity.ActivityID, Ordinal: ordinal, PlaceID: activity.PlaceID,
		DisplayName: activity.DisplayName, Latitude: activity.Location.Latitude,
		Longitude: activity.Location.Longitude, TimeZoneName: activity.TimeZoneName,
		InboundTravelMode: activity.InboundTravelMode, ActivityClass: activity.ActivityClass,
		ActivityState:        persistence.ActivityState(activity.ActivityState),
		ActivityDelaySeconds: activity.ActivityDelay, FoundClosedAt: millisTime(activity.FoundClosedAt),
		PriorityRank: activity.PriorityRank, UtilityScore: activity.UtilityScore,
		ReservationStart:         millisTime(activity.Timing.ReservationStart),
		ReservationGraceSeconds:  activity.Timing.ReservationGraceSeconds,
		MandatoryDeadline:        millisTime(activity.Timing.MandatoryDeadline),
		MinDurationSeconds:       activity.Timing.MinDurationSeconds,
		PreferredDurationSeconds: activity.Timing.PreferredDurationSeconds,
		MaxDurationSeconds:       activity.Timing.MaxDurationSeconds,
		Mandatory:                activity.Timing.Mandatory, CanShorten: activity.Timing.CanShorten,
		CanMove: activity.Timing.CanMove, CanSkip: activity.Timing.CanSkip,
		OpenWindows: toCanonicalWindows(activity.Timing.OpenWindows),
	}
}

func toCanonicalWindows(windows []createWindow) []persistence.CanonicalOpenWindow {
	result := make([]persistence.CanonicalOpenWindow, len(windows))
	for index, window := range windows {
		result[index] = persistence.CanonicalOpenWindow{
			OpensAt: time.UnixMilli(window.OpensAt).UTC(), ClosesAt: time.UnixMilli(window.ClosesAt).UTC(),
		}
	}
	return result
}

func millisTime(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	result := time.UnixMilli(*value).UTC()
	return &result
}

func createTripAcknowledgement(
	envelope createTripEnvelope,
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
		"trip_id": envelope.TripID, "trip_revision": strconv.FormatUint(tripRevision, 10),
		"runtime_epoch": "0", "planner_state_version": "0",
		"accepted_mutation_sequence": mutationSequence, "accepted_observation_sequence": "0",
		"payload": map[string]any{
			"phase": "canonical_committed", "message_id": envelope.MessageID,
			"mutation_sequence": mutationSequence, "outcome": outcome,
			"recovery_state": "not_advancing",
		}, "in_reply_to_message_id": envelope.MessageID,
	}
	return json.Marshal(acknowledgement)
}
