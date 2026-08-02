package gateway

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

var ErrTripAccessDenied = errors.New("trip access denied")

type TripVersions struct {
	TripRevision                string
	RuntimeEpoch                string
	PlannerStateVersion         string
	AcceptedMutationSequence    string
	AcceptedObservationSequence string
}

func ZeroTripVersions() TripVersions {
	return TripVersions{
		TripRevision: "0", RuntimeEpoch: "0", PlannerStateVersion: "0",
		AcceptedMutationSequence: "0", AcceptedObservationSequence: "0",
	}
}

func BuildTripErrorEnvelope(
	tripID string,
	messageID string,
	versions TripVersions,
	status string,
	retryable bool,
	safeMessage string,
	staleReason string,
) ([]byte, error) {
	if !canonicalUUID(tripID) || !canonicalUUID(messageID) || !validStatus(status) ||
		!validTripVersions(versions) || safeMessage == "" {
		return nil, errors.New("trip error envelope input is invalid")
	}
	payload := map[string]any{"safe_message": safeMessage}
	if staleReason != "" {
		if !validStaleReason(staleReason) {
			return nil, errors.New("trip error stale reason is invalid")
		}
		payload["stale_reason"] = staleReason
	}
	return json.Marshal(map[string]any{
		"protocol_version":  protocolVersion,
		"server_message_id": newUUID(),
		"kind":              "error", "status": status, "retryable": retryable,
		"trip_id":                       tripID,
		"trip_revision":                 versions.TripRevision,
		"runtime_epoch":                 versions.RuntimeEpoch,
		"planner_state_version":         versions.PlannerStateVersion,
		"accepted_mutation_sequence":    versions.AcceptedMutationSequence,
		"accepted_observation_sequence": versions.AcceptedObservationSequence,
		"payload":                       payload,
		"in_reply_to_message_id":        messageID,
	})
}

func ErrorStatus(err error) (status string, retryable bool, staleReason string) {
	switch {
	case errors.Is(err, persistence.ErrTripNotFound), errors.Is(err, persistence.ErrPendingProposalNotFound):
		return "NOT_FOUND", false, ""
	case errors.Is(err, ErrTripAccessDenied):
		return "PERMISSION_DENIED", false, ""
	case errors.Is(err, persistence.ErrTripRevisionStale), errors.Is(err, persistence.ErrProposalStale):
		return "STALE", false, "trip_revision"
	case errors.Is(err, persistence.ErrIdempotencyKeyReused):
		return "IDEMPOTENCY_KEY_REUSED", false, ""
	case errors.Is(err, ErrCanonicalCommandExpired):
		return "COMMAND_EXPIRED", false, ""
	case errors.Is(err, persistence.ErrCanonicalMirrorCapacity), errors.Is(err, ErrConnectionCapacity), errors.Is(err, ErrSubscriptionCapacity):
		return "RESOURCE_EXHAUSTED", true, ""
	case errors.Is(err, persistence.ErrDurableCommandBlocked), errors.Is(err, persistence.ErrLeaseHeld), errors.Is(err, persistence.ErrLeaseLost):
		return "UNAVAILABLE", true, ""
	default:
		return "INTERNAL", false, ""
	}
}

func validStatus(value string) bool {
	statuses := map[string]struct{}{
		"OK": {}, "DUPLICATE": {}, "STALE": {}, "INVALID_ARGUMENT": {},
		"UNAUTHENTICATED": {}, "PERMISSION_DENIED": {}, "NOT_FOUND": {},
		"IDEMPOTENCY_KEY_REUSED": {}, "INACTIVE_TRIP": {}, "RESOURCE_EXHAUSTED": {},
		"DEADLINE_EXCEEDED": {}, "COMMAND_EXPIRED": {}, "CANCELLED": {},
		"INFEASIBLE": {}, "PROVIDER_UNAVAILABLE": {}, "DURABILITY_UNAVAILABLE": {},
		"UNAVAILABLE": {}, "UNSUPPORTED_VERSION": {}, "SNAPSHOT_NOT_READY": {},
		"SNAPSHOT_INCOMPATIBLE": {}, "INTERNAL": {}, "MATRIX_TOO_LARGE": {},
	}
	_, ok := statuses[value]
	return ok
}

func validTripVersions(value TripVersions) bool {
	for name, item := range map[string]string{
		"trip_revision":                 value.TripRevision,
		"runtime_epoch":                 value.RuntimeEpoch,
		"planner_state_version":         value.PlannerStateVersion,
		"accepted_mutation_sequence":    value.AcceptedMutationSequence,
		"accepted_observation_sequence": value.AcceptedObservationSequence,
	} {
		if !canonicalUint64(item) {
			_ = name
			return false
		}
	}
	return true
}

func canonicalUint64(value string) bool {
	if value == "0" {
		return true
	}
	if len(value) == 0 || len(value) > 20 || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validStaleReason(value string) bool {
	_, ok := map[string]struct{}{
		"epoch": {}, "mutation_sequence": {}, "observation_sequence": {},
		"trip_revision": {}, "planner_state_version": {}, "plan_proposal": {},
	}[value]
	return ok
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("request could not be completed: %T", err)
}
