package gateway

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestTripCommandAdmissionIsBoundedAndSerialPerTrip(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mutex sync.Mutex
	processed := make([]string, 0, 2)
	admission, err := NewTripCommandAdmission(context.Background(), 1, 2, func(_ context.Context, message AuthenticatedMessage) {
		mutex.Lock()
		processed = append(processed, message.Message["message_id"].(string))
		first := len(processed) == 1
		mutex.Unlock()
		if first {
			close(started)
			<-release
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()

	first := admissionMessage("11111111-1111-4111-8111-111111111111", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	second := admissionMessage("11111111-1111-4111-8111-111111111111", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	third := admissionMessage("11111111-1111-4111-8111-111111111111", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if err := admission.Submit(first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first trip command was not admitted")
	}
	if err := admission.Submit(second); err != nil {
		t.Fatal(err)
	}
	if err := admission.Submit(third); err != ErrConnectionCapacity {
		t.Fatalf("third command returned %v, want bounded capacity error", err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mutex.Lock()
		count := len(processed)
		mutex.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(processed) != 2 || processed[0] != first.Message["message_id"] || processed[1] != second.Message["message_id"] {
		t.Fatalf("trip queue order=%v", processed)
	}
}

func TestTripCommandAdmissionBoundsActiveTrips(t *testing.T) {
	admission, err := NewTripCommandAdmission(context.Background(), 1, 1, func(context.Context, AuthenticatedMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	if err := admission.Submit(admissionMessage("11111111-1111-4111-8111-111111111111", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	if err := admission.Submit(admissionMessage("22222222-2222-4222-8222-222222222222", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")); err != ErrConnectionCapacity {
		t.Fatalf("second active trip returned %v, want capacity error", err)
	}
}

func admissionMessage(tripID, messageID string) AuthenticatedMessage {
	return AuthenticatedMessage{Message: map[string]any{"trip_id": tripID, "message_id": messageID}}
}
