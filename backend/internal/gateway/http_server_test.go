package gateway

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerRoutesHealthEndpoints(t *testing.T) {
	websocketHandler := testHandler(t)
	healthHandler, err := NewHealthHandler(ReadinessChecks{
		MigrationsCurrent:  func(context.Context) error { return nil },
		PostgreSQLPing:     func(context.Context) error { return nil },
		PlannerStreamReady: func(context.Context) error { return nil },
		OSRMCarServing:     func(context.Context) error { return nil },
		OSRMFootServing:    func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewHTTPServer("127.0.0.1:0", websocketHandler, healthHandler)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx, listener) }()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status=%d", response.StatusCode)
	}
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("serve returned %v, want context cancellation", err)
	}
}

func testHandler(t *testing.T) *Handler {
	t.Helper()
	handler, err := NewHandler(Config{
		Validator:                   testValidator(t),
		Authenticator:               AuthenticateFunc(func(context.Context, string) (string, error) { return testUserID, nil }),
		BackendInstanceID:           "22222222-2222-4222-8222-222222222222",
		FrameLimit:                  262144,
		DecodedMessageLimit:         262144,
		InboundQueueCapacity:        2,
		OutboundQueueCapacity:       2,
		HeartbeatInterval:           time.Hour,
		IdleTimeout:                 time.Minute,
		AuthenticationTimeout:       time.Second,
		AllowOriginlessLocalClients: true,
		MaxOutstandingResyncIDs:     128,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
