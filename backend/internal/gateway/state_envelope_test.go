package gateway

import (
	"encoding/json"
	"testing"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

func TestBuildTripStateEnvelopeCarriesAuthoritativeAndRuntimeVersions(t *testing.T) {
	raw, err := BuildTripStateEnvelope(
		"subscription_state", "OK", false,
		"550e8400-e29b-41d4-a716-446655440002",
		"550e8400-e29b-41d4-a716-446655440003",
		TripVersions{
			TripRevision: "4", RuntimeEpoch: "2", PlannerStateVersion: "7",
			AcceptedMutationSequence: "4", AcceptedObservationSequence: "9",
		},
		map[string]any{"subscribed": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["trip_revision"] != "4" || envelope["runtime_epoch"] != "2" ||
		envelope["in_reply_to_message_id"] != "550e8400-e29b-41d4-a716-446655440003" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
}

func TestBuildCommandOutcomeMapsCanonicalMirrorRecovery(t *testing.T) {
	status := "OK"
	value, err := BuildCommandOutcome("550e8400-e29b-41d4-a716-446655440003", persistence.CommandOutcome{
		MessageID: "550e8400-e29b-41d4-a716-446655440003", MutationSequence: 8,
		State: "applied", Kind: persistence.CommandReplaceCurrentPlan,
		RuntimeSyncState: "pending", OutcomeStatus: &status,
		OutcomePayload: []byte(`{"trip_revision":"8"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if value["phase"] != "canonical_committed" || value["recovery_state"] != "not_advancing" || value["mutation_sequence"] != "8" {
		t.Fatalf("unexpected command outcome: %#v", value)
	}
}

func TestBuildRuntimeCommandFinalizedEnvelopePublishesPlannerApplied(t *testing.T) {
	raw, err := BuildRuntimeCommandFinalizedEnvelope(persistence.FinalizedCommand{
		TripID:           "550e8400-e29b-41d4-a716-446655440002",
		EventID:          "550e8400-e29b-41d4-a716-446655440003",
		MutationSequence: 8, State: "applied", Status: "OK",
		ResultingTripRevision: 5, ResultingPlannerStateVersion: 9,
	}, RuntimeVersions{
		RuntimeEpoch: 2, AcceptedMutationSequence: 8,
		AcceptedObservationSequence: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	payload, _ := envelope["payload"].(map[string]any)
	if envelope["kind"] != "command_acknowledgement" ||
		envelope["trip_revision"] != "5" ||
		envelope["planner_state_version"] != "9" ||
		payload["phase"] != "planner_applied" || payload["message_id"] != "550e8400-e29b-41d4-a716-446655440003" {
		t.Fatalf("unexpected finalized command envelope: %#v", envelope)
	}
}
