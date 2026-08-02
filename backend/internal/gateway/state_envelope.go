package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

// BuildTripStateEnvelope creates the common versioned envelope used by
// subscription and resynchronization responses. State payload conversion is
// performed before this function so malformed durable data is never emitted.
func BuildTripStateEnvelope(
	kind string,
	status string,
	retryable bool,
	tripID string,
	messageID string,
	versions TripVersions,
	payload map[string]any,
) ([]byte, error) {
	if kind != "subscription_state" && kind != "resynchronization_state" {
		return nil, errors.New("trip state envelope kind is invalid")
	}
	if !canonicalUUID(tripID) || (messageID != "" && !canonicalUUID(messageID)) ||
		!validStatus(status) || !validTripVersions(versions) || payload == nil {
		return nil, errors.New("trip state envelope input is invalid")
	}
	envelope := map[string]any{
		"protocol_version": protocolVersion, "server_message_id": newUUID(),
		"kind": kind, "status": status, "retryable": retryable,
		"trip_id":                       tripID,
		"trip_revision":                 versions.TripRevision,
		"runtime_epoch":                 versions.RuntimeEpoch,
		"planner_state_version":         versions.PlannerStateVersion,
		"accepted_mutation_sequence":    versions.AcceptedMutationSequence,
		"accepted_observation_sequence": versions.AcceptedObservationSequence,
		"payload":                       payload,
	}
	if messageID != "" {
		envelope["in_reply_to_message_id"] = messageID
	}
	return json.Marshal(envelope)
}

func BuildPlanProposalEnvelope(
	tripID string,
	versions TripVersions,
	payload []byte,
) ([]byte, error) {
	if !canonicalUUID(tripID) || !validTripVersions(versions) || len(payload) == 0 {
		return nil, errors.New("plan proposal envelope input is invalid")
	}
	proposal, err := StoredProposalPayload(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"protocol_version": protocolVersion, "server_message_id": newUUID(),
		"kind": "plan_proposal", "status": "OK", "retryable": false,
		"trip_id":                       tripID,
		"trip_revision":                 versions.TripRevision,
		"runtime_epoch":                 versions.RuntimeEpoch,
		"planner_state_version":         versions.PlannerStateVersion,
		"accepted_mutation_sequence":    versions.AcceptedMutationSequence,
		"accepted_observation_sequence": versions.AcceptedObservationSequence,
		"payload":                       proposal,
	})
}

func BuildTelemetryStatusEnvelope(
	tripID string,
	messageID string,
	versions TripVersions,
	status string,
	retryable bool,
	disposition string,
	observationSequence uint64,
) ([]byte, error) {
	if !canonicalUUID(tripID) || !canonicalUUID(messageID) || !validTripVersions(versions) || !validStatus(status) ||
		(disposition != "accepted" && disposition != "coalesced" && disposition != "dropped" && disposition != "rejected") {
		return nil, errors.New("telemetry status envelope input is invalid")
	}
	payload := map[string]any{
		"message_id": messageID, "disposition": disposition,
	}
	if observationSequence != 0 {
		payload["observation_sequence"] = strconv.FormatUint(observationSequence, 10)
	}
	return json.Marshal(map[string]any{
		"protocol_version": protocolVersion, "server_message_id": newUUID(),
		"kind": "telemetry_status", "status": status, "retryable": retryable,
		"trip_id":                       tripID,
		"trip_revision":                 versions.TripRevision,
		"runtime_epoch":                 versions.RuntimeEpoch,
		"planner_state_version":         versions.PlannerStateVersion,
		"accepted_mutation_sequence":    versions.AcceptedMutationSequence,
		"accepted_observation_sequence": versions.AcceptedObservationSequence,
		"payload":                       payload, "in_reply_to_message_id": messageID,
	})
}

func StateVersions(
	state persistence.CanonicalTripState,
	runtime RuntimeVersions,
) TripVersions {
	return TripVersions{
		TripRevision:                strconv.FormatUint(state.TripRevision, 10),
		RuntimeEpoch:                strconv.FormatUint(runtime.RuntimeEpoch, 10),
		PlannerStateVersion:         strconv.FormatUint(runtime.PlannerStateVersion, 10),
		AcceptedMutationSequence:    strconv.FormatUint(runtime.AcceptedMutationSequence, 10),
		AcceptedObservationSequence: strconv.FormatUint(runtime.AcceptedObservationSequence, 10),
	}
}

// RuntimeVersions is the bounded runtime watermark exposed by the supervisor
// for gateway state messages. A zero value means the trip is not active.
type RuntimeVersions struct {
	RuntimeEpoch                uint64
	PlannerStateVersion         uint64
	AcceptedMutationSequence    uint64
	AcceptedObservationSequence uint64
}

func BuildCommandOutcome(
	messageID string,
	outcome persistence.CommandOutcome,
) (map[string]any, error) {
	if !canonicalUUID(messageID) {
		return nil, errors.New("command outcome message id is invalid")
	}
	phase, status, retryable, recovery := commandOutcomeMetadata(outcome)
	value := map[string]any{
		"message_id": messageID, "phase": phase,
		"status": status, "retryable": retryable,
		"mutation_sequence": strconv.FormatUint(outcome.MutationSequence, 10),
		"recovery_state":    recovery,
	}
	if len(outcome.OutcomePayload) != 0 {
		var payload map[string]any
		if err := json.Unmarshal(outcome.OutcomePayload, &payload); err != nil {
			return nil, fmt.Errorf("decode command outcome: %w", err)
		}
		value["outcome"] = payload
	}
	return value, nil
}

func commandOutcomeMetadata(outcome persistence.CommandOutcome) (string, string, bool, string) {
	status := "OK"
	if outcome.OutcomeStatus != nil && validStatus(*outcome.OutcomeStatus) {
		status = *outcome.OutcomeStatus
	}
	retryable := false
	phase := "durable_recorded"
	recovery := "current"
	switch outcome.State {
	case "applied":
		switch {
		case outcome.RuntimeSyncState == "pending" &&
			(outcome.Kind == persistence.CommandTripEdited || outcome.Kind == persistence.CommandReplaceCurrentPlan):
			phase = "canonical_committed"
			recovery = "not_advancing"
		case outcome.RuntimeSyncState == "synced":
			phase = "runtime_synced"
		default:
			phase = "planner_applied"
		}
	case "rejected":
		phase = "rejected"
	case "expired":
		phase = "expired"
		status = "COMMAND_EXPIRED"
	}
	if outcome.RuntimeSyncState == "pending" || outcome.RuntimeSyncState == "paused_internal" {
		recovery = "not_advancing"
	}
	if outcome.State == "pending" {
		retryable = true
	}
	return phase, status, retryable, recovery
}
