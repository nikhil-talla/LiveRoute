package plannerwire

import (
	"bytes"
	"testing"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"google.golang.org/protobuf/proto"
)

func testEvent() *liveroutev1.ApplyTripEvent {
	return &liveroutev1.ApplyTripEvent{
		EventId:          "11111111-1111-4111-8111-111111111111",
		OccurredAtUnixMs: 1_800_000_000_000,
		Event: &liveroutev1.ApplyTripEvent_ActivityDelayed{
			ActivityDelayed: &liveroutev1.ActivityDelayed{
				ActivityId:   "22222222-2222-4222-8222-222222222222",
				DelaySeconds: 45,
			},
		},
	}
}

func TestStoredEventRoundTripIsStable(t *testing.T) {
	payload, err := EncodeStoredEvent(testEvent())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStoredEvent(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(decoded, testEvent()) {
		t.Fatalf("decoded event differs: %#v", decoded)
	}
	again, err := EncodeStoredEvent(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, again) {
		t.Fatalf("encoding changed: %s != %s", payload, again)
	}
}

func TestStoredEventRejectsAmbiguousJSON(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"format":"x","format":"x","protobuf_base64":""}`),
		[]byte(`{"format":"x","protobuf_base64":"","extra":"x"}`),
		[]byte(`{"format":"x"}`),
		[]byte(`[]`),
	}
	for _, payload := range cases {
		if _, err := DecodeStoredEvent(payload); err == nil {
			t.Fatalf("accepted invalid payload %s", payload)
		}
	}
}
