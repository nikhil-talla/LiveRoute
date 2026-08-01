package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

type fakePlanReplacer struct {
	request persistence.ReplaceCurrentPlanRequest
}

func (fake *fakePlanReplacer) ReplaceCurrentPlan(
	_ context.Context,
	request persistence.ReplaceCurrentPlanRequest,
) (persistence.RecordedCommand, error) {
	fake.request = request
	revision := request.ExpectedTripRevision + 1
	return persistence.RecordedCommand{
		TripID:                request.TripID,
		MessageID:             request.MessageID,
		MutationSequence:      4,
		ResultingTripRevision: &revision,
	}, nil
}

func TestReplaceCurrentPlanAdapterBuildsTransactionExactMirrorEvent(t *testing.T) {
	fake := &fakePlanReplacer{}
	adapter, err := NewReplaceCurrentPlanAdapter(fake, 2)
	if err != nil {
		t.Fatal(err)
	}
	message := AuthenticatedMessage{
		UserID: "11111111-1111-1111-1111-111111111111",
		Raw:    []byte(`{"protocol_version":"liveroute.v1","message_id":"22222222-2222-2222-2222-222222222222","kind":"trip_command","trip_id":"33333333-3333-3333-3333-333333333333","payload":{"command_kind":"replace_current_plan","command":{"expected_trip_revision":"7","current_plan":{"plan_id":"44444444-4444-4444-4444-444444444444","segments":[{"activity_id":"55555555-5555-5555-5555-555555555555","state":"scheduled","scheduled_start_unix_ms":1000,"scheduled_end_unix_ms":2000},{"activity_id":"66666666-6666-6666-6666-666666666666","state":"omitted"}]}}}}`),
	}
	acknowledgement, err := adapter.Handle(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if len(acknowledgement) == 0 || fake.request.EventPayloadBuilder == nil {
		t.Fatal("adapter did not retain canonical event builder")
	}
	createdAt := time.UnixMilli(1_784_000_123_456).UTC()
	payload, err := fake.request.EventPayloadBuilder(persistence.CanonicalPlanMetadata{
		ID: "44444444-4444-4444-4444-444444444444", Revision: 8, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := plannerwire.DecodeStoredEvent(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.GetEventId() != messageID(message.Raw) || event.GetOccurredAtUnixMs() != createdAt.UnixMilli() ||
		event.CommandExpiresAtUnixMs != nil {
		t.Fatalf("event identity/time mismatch: %v", event)
	}
	plan := event.GetCurrentPlanReplaced().GetCurrentPlan()
	if plan.GetPlanId() != "44444444-4444-4444-4444-444444444444" ||
		plan.GetPlanRevision() != 8 || plan.GetCreatedAtUnixMs() != createdAt.UnixMilli() ||
		len(plan.GetSegments()) != 2 ||
		plan.GetSegments()[0].GetState() != liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_SCHEDULED {
		t.Fatalf("unexpected mirrored plan: %v", plan)
	}
	var response map[string]any
	if err := json.Unmarshal(acknowledgement, &response); err != nil {
		t.Fatal(err)
	}
	if response["kind"] != "command_acknowledgement" || response["status"] != "OK" {
		t.Fatalf("unexpected acknowledgement: %s", acknowledgement)
	}
}

func TestCanonicalCommandExpiryRejectsBeforePersistence(t *testing.T) {
	past := time.Now().Add(-time.Minute).UnixMilli()
	if _, err := canonicalCommandExpiry(&past); err != ErrCanonicalCommandExpired {
		t.Fatalf("expected canonical expiry error, got %v", err)
	}
	future := time.Now().Add(time.Minute).UnixMilli()
	value, err := canonicalCommandExpiry(&future)
	if err != nil || value == nil {
		t.Fatalf("expected future expiry, value=%v err=%v", value, err)
	}
}

func messageID(raw []byte) string {
	var envelope struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return envelope.MessageID
}
