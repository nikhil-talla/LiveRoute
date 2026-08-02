package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
)

type telemetryEnvelope struct {
	ProtocolVersion string `json:"protocol_version"`
	MessageID       string `json:"message_id"`
	Kind            string `json:"kind"`
	TripID          string `json:"trip_id"`
	Payload         struct {
		ObservationKind string          `json:"observation_kind"`
		ObservedAt      int64           `json:"observed_at_unix_ms"`
		Observation     json.RawMessage `json:"observation"`
	} `json:"payload"`
}

func ParseTelemetryEvent(raw []byte) (string, *liveroutev1.ApplyTripEvent, error) {
	var envelope telemetryEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", nil, fmt.Errorf("decode telemetry envelope: %w", err)
	}
	if envelope.ProtocolVersion != protocolVersion || envelope.Kind != "telemetry_update" ||
		!canonicalUUID(envelope.MessageID) || !canonicalUUID(envelope.TripID) ||
		envelope.Payload.ObservedAt == 0 {
		return "", nil, errors.New("telemetry envelope identity is invalid")
	}
	event := &liveroutev1.ApplyTripEvent{OccurredAtUnixMs: envelope.Payload.ObservedAt}
	switch envelope.Payload.ObservationKind {
	case "location":
		var value struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		}
		if err := json.Unmarshal(envelope.Payload.Observation, &value); err != nil ||
			!finite(value.Latitude) || !finite(value.Longitude) ||
			value.Latitude < -90 || value.Latitude > 90 || value.Longitude < -180 || value.Longitude > 180 {
			return "", nil, errors.New("telemetry location is invalid")
		}
		event.Event = &liveroutev1.ApplyTripEvent_LocationUpdated{LocationUpdated: &liveroutev1.LocationUpdated{
			Location: &liveroutev1.Location{Latitude: value.Latitude, Longitude: value.Longitude},
		}}
	case "velocity":
		var value struct {
			MetersPerSecond float64 `json:"meters_per_second"`
		}
		if err := json.Unmarshal(envelope.Payload.Observation, &value); err != nil ||
			!finite(value.MetersPerSecond) || value.MetersPerSecond < 0 {
			return "", nil, errors.New("telemetry velocity is invalid")
		}
		event.Event = &liveroutev1.ApplyTripEvent_VelocityUpdated{VelocityUpdated: &liveroutev1.VelocityUpdated{MetersPerSecond: value.MetersPerSecond}}
	case "heading":
		var value struct {
			Degrees float64 `json:"degrees"`
		}
		if err := json.Unmarshal(envelope.Payload.Observation, &value); err != nil ||
			!finite(value.Degrees) || value.Degrees < 0 || value.Degrees > 360 {
			return "", nil, errors.New("telemetry heading is invalid")
		}
		event.Event = &liveroutev1.ApplyTripEvent_HeadingUpdated{HeadingUpdated: &liveroutev1.HeadingUpdated{Degrees: value.Degrees}}
	case "route_deviation":
		var value struct {
			Location struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"location"`
			Distance uint64 `json:"distance_from_route_meters"`
		}
		if err := json.Unmarshal(envelope.Payload.Observation, &value); err != nil ||
			!finite(value.Location.Latitude) || !finite(value.Location.Longitude) ||
			value.Location.Latitude < -90 || value.Location.Latitude > 90 ||
			value.Location.Longitude < -180 || value.Location.Longitude > 180 || value.Distance > uint64(^uint32(0)) {
			return "", nil, errors.New("telemetry route deviation is invalid")
		}
		event.Event = &liveroutev1.ApplyTripEvent_RouteDeviationDetected{RouteDeviationDetected: &liveroutev1.RouteDeviationDetected{
			Location:                &liveroutev1.Location{Latitude: value.Location.Latitude, Longitude: value.Location.Longitude},
			DistanceFromRouteMeters: uint32(value.Distance),
		}}
	default:
		return "", nil, errors.New("telemetry observation kind is invalid")
	}
	return envelope.MessageID, event, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
