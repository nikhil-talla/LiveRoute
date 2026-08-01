package plannertransport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
)

type fakeStream struct {
	context context.Context
	mutex   sync.Mutex
	sent    []*liveroutev1.PlannerStreamRequest
	receive chan received
}

func (stream *fakeStream) Send(request *liveroutev1.PlannerStreamRequest) error {
	stream.mutex.Lock()
	stream.sent = append(stream.sent, request)
	stream.mutex.Unlock()
	if open := request.GetOpenStream(); open != nil {
		stream.receive <- received{response: &liveroutev1.PlannerStreamResponse{
			RequestId: request.GetRequestId(),
			Payload: &liveroutev1.PlannerStreamResponse_StreamReady{
				StreamReady: &liveroutev1.StreamReady{
					CppInstanceId:   "cpp-test",
					ProtocolVersion: ProtocolVersion,
					Capabilities:    append([]string(nil), RequiredCapabilities...),
					MaxMessageBytes: 4 * 1024 * 1024,
					Status:          liveroutev1.StatusCode_STATUS_CODE_OK,
				},
			},
		}}
		return nil
	}
	stream.receive <- received{response: &liveroutev1.PlannerStreamResponse{
		RequestId: request.GetRequestId(),
		TripId:    request.GetTripId(),
		Payload: &liveroutev1.PlannerStreamResponse_EventAcknowledged{
			EventAcknowledged: &liveroutev1.EventAcknowledged{
				Disposition: liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED,
				Status:      liveroutev1.StatusCode_STATUS_CODE_OK,
			},
		},
	}}
	return nil
}

func (stream *fakeStream) Recv() (*liveroutev1.PlannerStreamResponse, error) {
	select {
	case value := <-stream.receive:
		return value.response, value.err
	case <-stream.context.Done():
		return nil, stream.context.Err()
	}
}

func (stream *fakeStream) CloseSend() error { return nil }

func TestClientNegotiatesBeforeCorrelatedExchange(t *testing.T) {
	var stream *fakeStream
	client, err := New(Config{
		BackendInstanceID:    "backend-test",
		AdmissionCapacity:    2,
		NotificationCapacity: 2,
		ReconnectDelay:       func(uint64) time.Duration { return time.Hour },
	}, func(ctx context.Context) (Stream, error) {
		stream = &fakeStream{context: ctx, receive: make(chan received, 4)}
		return stream, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	request := &liveroutev1.PlannerStreamRequest{
		RequestId: "11111111-1111-4111-8111-111111111111",
		TripId:    "22222222-2222-4222-8222-222222222222",
		Payload: &liveroutev1.PlannerStreamRequest_ApplyEvent{
			ApplyEvent: &liveroutev1.ApplyTripEvent{
				EventId: "33333333-3333-4333-8333-333333333333",
			},
		},
	}
	response, err := client.Exchange(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRequestId() != request.GetRequestId() ||
		response.GetEventAcknowledged() == nil {
		t.Fatalf("unexpected response: %#v", response)
	}
	deadline := time.Now().Add(time.Second)
	for !client.StreamReady() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !client.StreamReady() {
		t.Fatal("negotiated planner stream was not reported ready")
	}
	stream.mutex.Lock()
	defer stream.mutex.Unlock()
	if len(stream.sent) != 2 || stream.sent[0].GetOpenStream() == nil ||
		stream.sent[1] != request {
		t.Fatalf("unexpected send order: %#v", stream.sent)
	}
}

func TestClientRejectsInvalidConfiguration(t *testing.T) {
	if _, err := New(Config{}, nil); err == nil {
		t.Fatal("expected invalid configuration")
	}
}

func TestClientReturnsContextCancellationWhileDisconnected(t *testing.T) {
	client, err := New(Config{
		BackendInstanceID:    "backend-test",
		AdmissionCapacity:    1,
		NotificationCapacity: 1,
		ReconnectDelay:       func(uint64) time.Duration { return time.Hour },
	}, func(context.Context) (Stream, error) {
		return nil, errors.New("offline")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = client.Exchange(ctx, &liveroutev1.PlannerStreamRequest{
		RequestId: "11111111-1111-4111-8111-111111111111",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
}

func TestReconnectDelayStartsAtContractedCap(t *testing.T) {
	for range 100 {
		if delay := reconnectDelay(1); delay < 0 || delay > 100*time.Millisecond {
			t.Fatalf("first reconnect delay is out of range: %s", delay)
		}
		if delay := reconnectDelay(100); delay < 0 || delay > 10*time.Second {
			t.Fatalf("capped reconnect delay is out of range: %s", delay)
		}
	}
}
