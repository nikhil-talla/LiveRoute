package gateway

import "testing"

func TestParseTelemetryEventMapsLocationWithoutTransportMetadata(t *testing.T) {
	messageID := "550e8400-e29b-41d4-a716-446655440003"
	tripID := "550e8400-e29b-41d4-a716-446655440002"
	clientID, event, err := ParseTelemetryEvent([]byte(`{"protocol_version":"liveroute.v1","message_id":"550e8400-e29b-41d4-a716-446655440003","kind":"telemetry_update","trip_id":"550e8400-e29b-41d4-a716-446655440002","payload":{"observation_kind":"location","observed_at_unix_ms":1700000000000,"observation":{"latitude":41.7,"longitude":-71.4}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if clientID != messageID || event.GetEventId() != "" || event.GetOccurredAtUnixMs() != 1700000000000 || event.GetLocationUpdated().GetLocation().GetLatitude() != 41.7 {
		t.Fatalf("unexpected telemetry event for %s/%s: %#v", messageID, tripID, event)
	}
}
