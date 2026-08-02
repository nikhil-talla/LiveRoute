package gateway

import (
	"encoding/json"
	"testing"
)

func TestBuildTripErrorEnvelopeUsesContractVersionsAndReply(t *testing.T) {
	versions := ZeroTripVersions()
	versions.TripRevision = "4"
	raw, err := BuildTripErrorEnvelope(
		"550e8400-e29b-41d4-a716-446655440001",
		"550e8400-e29b-41d4-a716-446655440002",
		versions, "STALE", false, "trip revision is stale", "trip_revision",
	)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["kind"] != "error" || envelope["trip_revision"] != "4" ||
		envelope["in_reply_to_message_id"] != "550e8400-e29b-41d4-a716-446655440002" {
		t.Fatalf("unexpected error envelope=%v", envelope)
	}
}

func TestErrorStatusMapsDurabilityAndCapacityErrors(t *testing.T) {
	status, retryable, _ := ErrorStatus(ErrConnectionCapacity)
	if status != "RESOURCE_EXHAUSTED" || !retryable {
		t.Fatalf("status=%s retryable=%v", status, retryable)
	}
	status, retryable, staleReason := ErrorStatus(nil)
	if status != "INTERNAL" || retryable || staleReason != "" {
		t.Fatalf("nil status=%s retryable=%v stale=%s", status, retryable, staleReason)
	}
}
