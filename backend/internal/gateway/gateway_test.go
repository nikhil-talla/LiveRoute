package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testUserID = "11111111-1111-4111-8111-111111111111"

func TestTokenVerifierRequiresCanonicalDevelopmentToken(t *testing.T) {
	tokenBytes := make([]byte, 32)
	for index := range tokenBytes {
		tokenBytes[index] = byte(index + 1)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := LoadDevelopmentToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.Verify(token) {
		t.Fatal("valid token was rejected")
	}
	changedToken := "B" + token[1:]
	if verifier.Verify(changedToken) || verifier.Verify(token+"\n") {
		t.Fatal("noncanonical token was accepted")
	}

	if err := os.WriteFile(tokenPath, append([]byte(token), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDevelopmentToken(tokenPath); err == nil {
		t.Fatal("token with a newline was accepted")
	}
}

func TestStrictJSONRejectsDuplicateMembers(t *testing.T) {
	value, err := decodeStrictJSON([]byte(`{"protocol_version":"liveroute.v1","protocol_version":"liveroute.v1"}`))
	if err == nil || value != nil {
		t.Fatalf("duplicate members were accepted: value=%v err=%v", value, err)
	}
}

func TestHandlerAuthenticatesAndAnswersApplicationPing(t *testing.T) {
	validator := testValidator(t)
	handler, err := NewHandler(Config{
		Validator:                   validator,
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
		Now:                         time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	connection, _, err := websocket.Dial(context.Background(), "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")

	authentication := map[string]any{
		"protocol_version": protocolVersion,
		"message_id":       "33333333-3333-4333-8333-333333333333",
		"kind":             "authenticate",
		"payload":          map[string]any{"token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	}
	writeJSON(t, connection, authentication)
	ready := readJSON(t, connection)
	if err := validator.ValidateServer(ready); err != nil {
		t.Fatalf("connection_ready did not satisfy server schema: %v", err)
	}
	var readyEnvelope map[string]any
	if err := json.Unmarshal(ready, &readyEnvelope); err != nil {
		t.Fatal(err)
	}
	if readyEnvelope["kind"] != "connection_ready" || readyEnvelope["status"] != statusOK {
		t.Fatalf("unexpected ready envelope: %s", ready)
	}

	ping := map[string]any{
		"protocol_version": protocolVersion,
		"message_id":       "44444444-4444-4444-8444-444444444444",
		"kind":             "ping",
		"payload":          map[string]any{"nonce": "probe", "sent_at_unix_ms": time.Now().UnixMilli()},
	}
	writeJSON(t, connection, ping)
	pong := readJSON(t, connection)
	if err := validator.ValidateServer(pong); err != nil {
		t.Fatalf("pong did not satisfy server schema: %v", err)
	}
	var pongEnvelope map[string]any
	if err := json.Unmarshal(pong, &pongEnvelope); err != nil {
		t.Fatal(err)
	}
	if pongEnvelope["kind"] != "pong" || pongEnvelope["in_reply_to_message_id"] != ping["message_id"] {
		t.Fatalf("unexpected pong envelope: %s", pong)
	}
}

func TestHandlerClosesUnauthenticatedTripMessage(t *testing.T) {
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
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, _, err := websocket.Dial(context.Background(), "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")

	writeJSON(t, connection, map[string]any{
		"protocol_version": protocolVersion,
		"message_id":       "55555555-5555-4555-8555-555555555555",
		"kind":             "subscribe_trip",
		"trip_id":          "66666666-6666-4666-8666-666666666666",
		"payload":          map[string]any{},
	})
	_, _, err = connection.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusCode(4001) {
		t.Fatalf("expected authentication close 4001, got %v", err)
	}
}

func TestHandlerShutdownClosesAuthenticatedSessionsWithRestartCode(t *testing.T) {
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
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, _, err := websocket.Dial(context.Background(), "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	writeJSON(t, connection, map[string]any{
		"protocol_version": protocolVersion,
		"message_id":       "77777777-7777-4777-8777-777777777777",
		"kind":             "authenticate",
		"payload":          map[string]any{"token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	})
	_ = readJSON(t, connection)

	shutdown := make(chan error, 1)
	go func() {
		shutdown <- handler.Shutdown(context.Background())
	}()
	_, _, err = connection.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("expected restart close 1001, got %v", err)
	}
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
}

func testValidator(t *testing.T) *EnvelopeValidator {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	client, err := os.ReadFile(filepath.Join(root, "schema", "websocket", "liveroute-v1-client-envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := os.ReadFile(filepath.Join(root, "schema", "websocket", "liveroute-v1-server-envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewEnvelopeValidator(client, server)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func writeJSON(t *testing.T, connection *websocket.Conn, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, connection *websocket.Conn) []byte {
	t.Helper()
	_, raw, err := connection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNewUUIDIsCanonical(t *testing.T) {
	if !canonicalUUID(newUUID()) {
		t.Fatal("new UUID was not canonical")
	}
	if canonicalUUID("11111111-1111-6111-8111-111111111111") {
		t.Fatal("unsupported UUID version was accepted")
	}
}
