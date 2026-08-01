package plannerwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"google.golang.org/protobuf/proto"
)

const StoredEventFormat = "liveroute.v1.ApplyTripEvent/protobuf;version=1"

type storedEventEnvelope struct {
	Format         string `json:"format"`
	ProtobufBase64 string `json:"protobuf_base64"`
}

// EncodeStoredEvent returns the exact lease-neutral JSONB representation used
// by planner_outbox.event_payload. Transport authority belongs to the dispatch
// envelope and is deliberately not persisted here.
func EncodeStoredEvent(event *liveroutev1.ApplyTripEvent) (json.RawMessage, error) {
	if err := validateEvent(event); err != nil {
		return nil, err
	}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal planner event: %w", err)
	}
	encoded, err := json.Marshal(storedEventEnvelope{
		Format:         StoredEventFormat,
		ProtobufBase64: base64.StdEncoding.EncodeToString(wire),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal stored planner event: %w", err)
	}
	return encoded, nil
}

// DecodeStoredEvent rejects non-canonical JSON/base64/protobuf encodings so a
// retry always reconstructs the same stable ApplyTripEvent bytes.
func DecodeStoredEvent(payload json.RawMessage) (*liveroutev1.ApplyTripEvent, error) {
	envelope, err := decodeEnvelope(payload)
	if err != nil {
		return nil, err
	}
	if envelope.Format != StoredEventFormat {
		return nil, errors.New("unsupported stored planner event format")
	}
	wire, err := base64.StdEncoding.Strict().DecodeString(envelope.ProtobufBase64)
	if err != nil || base64.StdEncoding.EncodeToString(wire) != envelope.ProtobufBase64 {
		return nil, errors.New("stored planner event base64 is not canonical")
	}
	event := &liveroutev1.ApplyTripEvent{}
	if err := proto.Unmarshal(wire, event); err != nil {
		return nil, fmt.Errorf("decode stored planner event protobuf: %w", err)
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil || !bytes.Equal(canonical, wire) {
		return nil, errors.New("stored planner event protobuf is not canonical")
	}
	if err := validateEvent(event); err != nil {
		return nil, err
	}
	return event, nil
}

func decodeEnvelope(payload []byte) (storedEventEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return storedEventEnvelope{}, errors.New("stored planner event must be an object")
	}
	var result storedEventEnvelope
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return storedEventEnvelope{}, errors.New("invalid stored planner event object")
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return storedEventEnvelope{}, errors.New("stored planner event has duplicate member")
		}
		seen[name] = true
		switch name {
		case "format":
			err = decoder.Decode(&result.Format)
		case "protobuf_base64":
			err = decoder.Decode(&result.ProtobufBase64)
		default:
			return storedEventEnvelope{}, errors.New("stored planner event has unknown member")
		}
		if err != nil {
			return storedEventEnvelope{}, errors.New("stored planner event member must be a string")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return storedEventEnvelope{}, errors.New("invalid stored planner event object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return storedEventEnvelope{}, errors.New("stored planner event has trailing data")
	}
	if !seen["format"] || !seen["protobuf_base64"] {
		return storedEventEnvelope{}, errors.New("stored planner event is incomplete")
	}
	return result, nil
}

func validateEvent(event *liveroutev1.ApplyTripEvent) error {
	if event == nil || event.GetEventId() == "" || event.GetOccurredAtUnixMs() <= 0 ||
		event.Event == nil {
		return errors.New("stored planner event is incomplete")
	}
	return nil
}
