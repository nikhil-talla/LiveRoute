//go:build processacceptance

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLiveWebSocketProcessAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws://127.0.0.1:8080/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")
	token, err := os.ReadFile("/tmp/liveroute-dev-token")
	if err != nil {
		t.Fatal(err)
	}
	write := func(value map[string]any) {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := connection.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
	}
	readKind := func(expected string) map[string]any {
		_, raw, readErr := connection.Read(ctx)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["kind"] != expected {
			t.Fatalf("expected %s, got %#v", expected, envelope)
		}
		return envelope
	}
	write(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440001",
		"kind": "authenticate", "payload": map[string]any{"token": string(token)},
	})
	readKind("connection_ready")
	tripID := "550e8400-e29b-41d4-a716-446655440030"
	activityID := "550e8400-e29b-41d4-a716-446655440031"
	planID := "550e8400-e29b-41d4-a716-446655440032"
	write(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440013",
		"kind": "create_trip", "trip_id": tripID, "payload": map[string]any{
			"default_time_zone_name": "America/New_York",
			"activities": []any{map[string]any{
				"activity_id": activityID, "place_id": "test-place", "display_name": "Test place",
				"location":       map[string]any{"latitude": 41.82, "longitude": -71.41},
				"time_zone_name": "America/New_York", "inbound_travel_mode": "driving", "activity_class": "flexible", "activity_state": "planned",
				"priority_rank": 1, "utility_score": 1, "activity_delay_seconds": 0,
				"timing": map[string]any{"open_windows": []any{}, "reservation_grace_seconds": 0, "min_duration_seconds": 60, "preferred_duration_seconds": 60, "max_duration_seconds": 60, "mandatory": false, "can_shorten": false, "can_move": true, "can_skip": true},
			}},
			"current_plan": map[string]any{"plan_id": planID, "segments": []any{map[string]any{"activity_id": activityID, "state": "omitted"}}},
		},
	})
	readKind("command_acknowledgement")
	write(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440014",
		"kind": "subscribe_trip", "trip_id": tripID, "payload": map[string]any{},
	})
	readKind("subscription_state")
	write(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440015",
		"kind": "resynchronize_trip", "trip_id": tripID, "payload": map[string]any{
			"last_runtime_epoch": "0", "last_planner_state_version": "0", "last_trip_revision": "1", "outstanding_message_ids": []any{},
		},
	})
	readKind("resynchronization_state")
	write(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440016",
		"kind": "telemetry_update", "trip_id": tripID, "payload": map[string]any{
			"observation_kind": "location", "observed_at_unix_ms": 1700000000000,
			"observation": map[string]any{"latitude": 41.82, "longitude": -71.41},
		},
	})
	readKind("telemetry_status")
	if err := connection.Close(websocket.StatusNormalClosure, "reconnect"); err != nil {
		t.Fatal(err)
	}
	reconnected, _, err := websocket.Dial(ctx, "ws://127.0.0.1:8080/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "done")
	reconnectWrite := func(value map[string]any) {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := reconnected.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
	}
	reconnectReadKind := func(expected string) map[string]any {
		_, raw, readErr := reconnected.Read(ctx)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["kind"] != expected {
			t.Fatalf("expected %s after reconnect, got %#v", expected, envelope)
		}
		return envelope
	}
	reconnectWrite(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440017",
		"kind": "authenticate", "payload": map[string]any{"token": string(token)},
	})
	reconnectReadKind("connection_ready")
	reconnectWrite(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440013",
		"kind": "create_trip", "trip_id": tripID, "payload": map[string]any{
			"default_time_zone_name": "America/New_York",
			"activities": []any{map[string]any{
				"activity_id": activityID, "place_id": "test-place", "display_name": "Test place",
				"location":       map[string]any{"latitude": 41.82, "longitude": -71.41},
				"time_zone_name": "America/New_York", "inbound_travel_mode": "driving", "activity_class": "flexible", "activity_state": "planned",
				"priority_rank": 1, "utility_score": 1, "activity_delay_seconds": 0,
				"timing": map[string]any{"open_windows": []any{}, "reservation_grace_seconds": 0, "min_duration_seconds": 60, "preferred_duration_seconds": 60, "max_duration_seconds": 60, "mandatory": false, "can_shorten": false, "can_move": true, "can_skip": true}}},
			"current_plan": map[string]any{"plan_id": planID, "segments": []any{map[string]any{"activity_id": activityID, "state": "omitted"}}},
		},
	})
	reconnectReadKind("command_acknowledgement")
	reconnectWrite(map[string]any{
		"protocol_version": "liveroute.v1", "message_id": "550e8400-e29b-41d4-a716-446655440018",
		"kind": "resynchronize_trip", "trip_id": tripID, "payload": map[string]any{
			"last_runtime_epoch": "0", "last_planner_state_version": "0", "last_trip_revision": "1", "outstanding_message_ids": []any{"550e8400-e29b-41d4-a716-446655440013"},
		},
	})
	resynchronized := reconnectReadKind("resynchronization_state")
	if payload, ok := resynchronized["payload"].(map[string]any); !ok || payload["outcomes"] == nil {
		t.Fatalf("resynchronization did not return requested outcomes: %#v", resynchronized)
	}
}
