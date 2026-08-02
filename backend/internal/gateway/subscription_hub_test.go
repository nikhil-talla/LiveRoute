package gateway

import (
	"errors"
	"testing"
)

type hubSink struct {
	published [][]byte
}

func (sink *hubSink) PublishServerEnvelope(payload []byte) error {
	sink.published = append(sink.published, append([]byte(nil), payload...))
	return nil
}

func TestSubscriptionHubBoundsPerSinkAndFansOut(t *testing.T) {
	hub, err := NewSubscriptionHub(1)
	if err != nil {
		t.Fatal(err)
	}
	sink := &hubSink{}
	firstTrip := "550e8400-e29b-41d4-a716-446655440001"
	secondTrip := "550e8400-e29b-41d4-a716-446655440002"
	if err := hub.Subscribe(firstTrip, sink); err != nil {
		t.Fatal(err)
	}
	if err := hub.Subscribe(firstTrip, sink); err != nil {
		t.Fatalf("duplicate subscribe error=%v", err)
	}
	if err := hub.Subscribe(secondTrip, sink); !errors.Is(err, ErrSubscriptionCapacity) {
		t.Fatalf("second subscription error=%v", err)
	}
	if err := hub.PublishTrip(firstTrip, []byte("state")); err != nil {
		t.Fatal(err)
	}
	if len(sink.published) != 1 || string(sink.published[0]) != "state" {
		t.Fatalf("published=%q", sink.published)
	}
	hub.RemoveSink(sink)
	if hub.SubscriptionCount(firstTrip) != 0 {
		t.Fatal("sink remained subscribed after removal")
	}
}
