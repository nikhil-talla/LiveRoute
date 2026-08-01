package plannertransport

import (
	"context"
	"os"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
)

func TestPinnedPlannerStreamIntegration(t *testing.T) {
	target := os.Getenv("LIVEROUTE_TEST_PLANNER_TARGET")
	if target == "" {
		t.Skip("LIVEROUTE_TEST_PLANNER_TARGET is not configured")
	}
	client, connection, err := Dial(target, Config{
		BackendInstanceID:    "backend-integration-test",
		AdmissionCapacity:    4,
		NotificationCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		client.Close()
		_ = connection.Close()
	}()
	requestID, err := NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := client.Exchange(ctx, &liveroutev1.PlannerStreamRequest{
		RequestId: requestID,
		Payload: &liveroutev1.PlannerStreamRequest_Ping{
			Ping: &liveroutev1.Ping{
				Nonce:        "go-cpp-integration",
				SentAtUnixMs: time.Now().UnixMilli(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRequestId() != requestID ||
		response.GetPong().GetNonce() != "go-cpp-integration" {
		t.Fatalf("unexpected planner response: %#v", response)
	}
}
