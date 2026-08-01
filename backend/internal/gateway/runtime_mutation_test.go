package gateway

import (
	"context"
	"strconv"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

type fakeRuntimeMutationRecorder struct {
	request persistence.RecordRuntimeCommandRequest
}

func (fake *fakeRuntimeMutationRecorder) RecordRuntimeFirst(
	_ context.Context,
	request persistence.RecordRuntimeCommandRequest,
) (persistence.RecordedCommand, error) {
	fake.request = request
	return persistence.RecordedCommand{
		ExpectedTripRevision: request.ExpectedTripRevision,
		MutationSequence:     6,
	}, nil
}

func TestRuntimeMutationAdapterRecordsStatusAndPreservesLogicalExpiry(t *testing.T) {
	fake := &fakeRuntimeMutationRecorder{}
	adapter, err := NewRuntimeMutationAdapter(fake)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Minute).UnixMilli()
	message := AuthenticatedMessage{
		UserID: "11111111-1111-1111-1111-111111111111",
	}
	// Build the numeric expiry without relying on a JSON float conversion.
	message.Raw = []byte(`{"protocol_version":"liveroute.v1","message_id":"22222222-2222-2222-2222-222222222222","kind":"trip_command","trip_id":"33333333-3333-3333-3333-333333333333","payload":{"command_kind":"activity_status_changed","command_expires_at_unix_ms":` + formatInt64(expiresAt) + `,"command":{"expected_trip_revision":"4","activity_id":"44444444-4444-4444-4444-444444444444","state":"started"}}}`)
	if _, err := adapter.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if fake.request.Kind != persistence.CommandActivityStatusChanged || fake.request.ExpectedTripRevision != 4 ||
		fake.request.CommandExpiresAt == nil || fake.request.EventPayload == nil {
		t.Fatalf("unexpected runtime request: %+v", fake.request)
	}
	event, err := plannerwire.DecodeStoredEvent(fake.request.EventPayload)
	if err != nil {
		t.Fatal(err)
	}
	if event.GetActivityStatusChanged().GetActivityId() != "44444444-4444-4444-4444-444444444444" ||
		event.GetActivityStatusChanged().GetState().String() != "ACTIVITY_STATE_STARTED" ||
		event.CommandExpiresAtUnixMs == nil || *event.CommandExpiresAtUnixMs != expiresAt {
		t.Fatalf("unexpected stored runtime event: %v", event)
	}
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestRuntimeMutationEventCoversAllOrdinaryKinds(t *testing.T) {
	commands := []struct {
		kind    persistence.CommandKind
		command string
		present func(*liveroutev1.ApplyTripEvent) bool
	}{
		{persistence.CommandActivityStatusChanged, `{"activity_id":"11111111-1111-1111-1111-111111111111","state":"started"}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetActivityStatusChanged() != nil }},
		{persistence.CommandActivityDelayed, `{"activity_id":"11111111-1111-1111-1111-111111111111","delay_seconds":4}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetActivityDelayed() != nil }},
		{persistence.CommandReservationChanged, `{"activity_id":"11111111-1111-1111-1111-111111111111","reservation_grace_seconds":4}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetReservationChanged() != nil }},
		{persistence.CommandMandatoryDeadlineChanged, `{"activity_id":"11111111-1111-1111-1111-111111111111","latest_finish_unix_ms":4}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetMandatoryDeadlineChanged() != nil }},
		{persistence.CommandOperatingHoursChanged, `{"activity_id":"11111111-1111-1111-1111-111111111111","open_windows":[{"opens_at_unix_ms":1,"closes_at_unix_ms":2}]}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetOperatingHoursChanged() != nil }},
		{persistence.CommandPlaceFoundClosed, `{"activity_id":"11111111-1111-1111-1111-111111111111","observed_at_unix_ms":4}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetPlaceFoundClosed() != nil }},
		{persistence.CommandTravelDelay, `{"from_activity_id":"11111111-1111-1111-1111-111111111111","to_activity_id":"22222222-2222-2222-2222-222222222222","additional_seconds":4}`, func(event *liveroutev1.ApplyTripEvent) bool { return event.GetTravelDelay() != nil }},
	}
	for _, testCase := range commands {
		t.Run(string(testCase.kind), func(t *testing.T) {
			event, err := runtimeMutationEvent(testCase.kind, runtimeMutationEnvelope{
				MessageID: "33333333-3333-3333-3333-333333333333",
				Payload:   runtimeMutationPayload{Command: []byte(testCase.command)},
			}, time.UnixMilli(1_784_000_000_000).UTC())
			if err != nil || !testCase.present(event) {
				t.Fatalf("event conversion failed: event=%v err=%v", event, err)
			}
		})
	}
}
