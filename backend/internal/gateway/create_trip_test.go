package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

type fakeTripCreator struct {
	request persistence.CreateTripRequest
}

type fakeMessageSink struct {
	payload []byte
}

func (sink *fakeMessageSink) PublishServerEnvelope(payload []byte) error {
	sink.payload = append([]byte(nil), payload...)
	return nil
}

func (creator *fakeTripCreator) CreateTrip(_ context.Context, request persistence.CreateTripRequest) (persistence.RecordedCommand, error) {
	creator.request = request
	revision := uint64(1)
	return persistence.RecordedCommand{
		TripID: request.TripID, MessageID: request.MessageID, MutationSequence: 1,
		ResultingTripRevision: &revision,
	}, nil
}

func TestCreateTripAdapterCanonicalizesAndBuildsAcknowledgement(t *testing.T) {
	creator := &fakeTripCreator{}
	adapter, err := NewCreateTripAdapter(creator)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
  "payload": {
    "current_plan": {"segments": [{"state":"omitted","activity_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}], "plan_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
    "activities": [{"activity_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","place_id":"place","display_name":"Place","location":{"longitude":-73.0,"latitude":41.0},"time_zone_name":"America/New_York","inbound_travel_mode":"walking","activity_class":"flexible","activity_state":"planned","priority_rank":1,"utility_score":2,"timing":{"open_windows":[],"reservation_grace_seconds":0,"min_duration_seconds":60,"preferred_duration_seconds":60,"max_duration_seconds":60,"mandatory":false,"can_shorten":false,"can_move":true,"can_skip":true},"activity_delay_seconds":0}],
    "default_time_zone_name":"America/New_York"
  },
  "trip_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "kind":"create_trip",
  "message_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd",
  "protocol_version":"liveroute.v1"
}`)
	message := AuthenticatedMessage{UserID: testUserID, Raw: raw}
	acknowledgement, err := adapter.Handle(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigestBytes, err := canonicalizeClientMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256(expectedDigestBytes)
	if string(creator.request.CommandPayload) != string(expectedDigestBytes) || creator.request.PayloadDigest != expectedDigest {
		t.Fatalf("command was not canonically retained: payload=%s", creator.request.CommandPayload)
	}
	if len(creator.request.Activities) != 1 || len(creator.request.PlanSegments) != 1 || creator.request.PlanSegments[0].Scheduled {
		t.Fatalf("unexpected mapped create request: %#v", creator.request)
	}
	if err := testValidator(t).ValidateServer(acknowledgement); err != nil {
		t.Fatalf("acknowledgement failed server schema: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(acknowledgement, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["kind"] != "command_acknowledgement" || envelope["trip_revision"] != "1" {
		t.Fatalf("unexpected acknowledgement: %s", acknowledgement)
	}
	sink := &fakeMessageSink{}
	message.Sink = sink
	if err := adapter.Publish(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := testValidator(t).ValidateServer(sink.payload); err != nil {
		t.Fatalf("published acknowledgement failed server schema: %v", err)
	}
}

func TestCreateTripAdapterRejectsDuplicateJSONMembers(t *testing.T) {
	adapter, err := NewCreateTripAdapter(&fakeTripCreator{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Handle(context.Background(), AuthenticatedMessage{
		UserID: testUserID,
		Raw:    []byte(`{"protocol_version":"liveroute.v1","protocol_version":"liveroute.v1"}`),
	})
	if err == nil {
		t.Fatal("duplicate command member was accepted")
	}
}

func TestCanonicalClientIdentityExcludesMessageID(t *testing.T) {
	first, err := canonicalizeClientMessage([]byte(`{"message_id":"11111111-1111-4111-8111-111111111111","kind":"ping","payload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalizeClientMessage([]byte(`{"message_id":"22222222-2222-4222-8222-222222222222","kind":"ping","payload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("message_id changed durable identity: %s vs %s", first, second)
	}
}
