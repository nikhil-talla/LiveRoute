package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	placeprovider "github.com/liveroute/liveroute/backend/internal/place"
)

type testPlaceProvider struct {
	calls     int
	candidate placeprovider.Candidate
	err       error
}

func (provider *testPlaceProvider) Resolve(_ context.Context, _ string, _ placeprovider.Coordinate, beforeRequest func() error) (placeprovider.Candidate, error) {
	provider.calls++
	if err := beforeRequest(); err != nil {
		return placeprovider.Candidate{}, err
	}
	return provider.candidate, provider.err
}

func TestPlaceStoreResolvesAndConsumesExactlyOnce(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	const (
		userID     = "a1111111-1111-4111-8111-111111111111"
		resolveKey = "a2222222-2222-4222-8222-222222222222"
		createKey  = "a3333333-3333-4333-8333-333333333333"
		requestID  = "a4444444-4444-4444-8444-444444444444"
		expiredKey = "a5555555-5555-4555-8555-555555555555"
	)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, display_name, default_time_zone_name) VALUES ($1, 'Place user', 'America/New_York')`, userID); err != nil {
		t.Fatal(err)
	}
	keys, err := NewHMACKeyRing(HMACKey{ID: "place-test", Value: []byte("0123456789abcdef0123456789abcdef")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &testPlaceProvider{candidate: placeprovider.Candidate{
		Latitude: 41.824, Longitude: -71.4128, FormattedAddress: "1 Main St, Providence, RI",
		DisplayName: "1 Main St, Providence, RI", TimeZoneName: "America/New_York",
	}}
	store, err := NewPlaceStore(pool, keys, provider)
	if err != nil {
		t.Fatal(err)
	}
	resolveInput := ResolvePlaceInput{
		UserID: userID, IdempotencyKey: resolveKey, RequestIdentity: []byte(`{"body":{"latitude":41.824,"longitude":-71.4128}}`),
		RequestID: requestID, Coordinate: placeprovider.Coordinate{Latitude: 41.824, Longitude: -71.4128},
	}
	first, err := store.Resolve(ctx, resolveInput)
	if err != nil || first.Status != 200 {
		t.Fatalf("resolve status=%d err=%v body=%s", first.Status, err, first.Body)
	}
	var resolution struct {
		Token string `json:"resolution_token"`
	}
	if err := json.Unmarshal(first.Body, &resolution); err != nil || len(resolution.Token) != 43 {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	replay, err := store.Resolve(ctx, resolveInput)
	if err != nil || string(replay.Body) != string(first.Body) || provider.calls != 1 {
		t.Fatalf("resolve replay calls=%d err=%v first=%s replay=%s", provider.calls, err, first.Body, replay.Body)
	}

	createDigest := sha256.Sum256([]byte("create-place-request"))
	createInput := CreatePlaceInput{UserID: userID, IdempotencyKey: createKey, RequestDigest: createDigest, ResolutionToken: resolution.Token, RequestID: requestID}
	created, err := store.Create(ctx, createInput)
	if err != nil || created.Status != 201 || created.ResourceID == "" {
		t.Fatalf("create status=%d resource=%q err=%v body=%s", created.Status, created.ResourceID, err, created.Body)
	}
	createdReplay, err := store.Create(ctx, createInput)
	if err != nil || string(createdReplay.Body) != string(created.Body) {
		t.Fatalf("create replay err=%v first=%s replay=%s", err, created.Body, createdReplay.Body)
	}
	var attempts, places int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM place_resolution_attempts WHERE user_id=$1 AND state='consumed'`, userID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM places WHERE owner_user_id=$1`, userID).Scan(&places); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || places != 1 {
		t.Fatalf("attempts=%d places=%d", attempts, places)
	}
	var storedRawToken bool
	if err := pool.QueryRow(ctx, `SELECT response_body IS NOT NULL FROM http_idempotency_records WHERE user_id=$1 AND normalized_path='/api/v1/places/resolve'`, userID).Scan(&storedRawToken); err != nil {
		t.Fatal(err)
	}
	if storedRawToken {
		t.Fatal("successful resolution retained its raw token in response_body")
	}
	expiredDigest := sha256.Sum256([]byte("expired-place-request"))
	expiredInput := CreatePlaceInput{UserID: userID, IdempotencyKey: expiredKey, RequestDigest: expiredDigest, ResolutionToken: resolution.Token, RequestID: requestID}
	expired, err := store.Create(ctx, expiredInput)
	if err != nil || expired.Status != 410 {
		t.Fatalf("consumed resolution status=%d err=%v body=%s", expired.Status, err, expired.Body)
	}
	expiredReplay, err := store.Create(ctx, expiredInput)
	if err != nil || string(expiredReplay.Body) != string(expired.Body) {
		t.Fatalf("consumed replay err=%v first=%s replay=%s", err, expired.Body, expiredReplay.Body)
	}
}

func TestPlaceStorePersistsFailureAndDoesNotRepeatProviderCall(t *testing.T) {
	pool, ctx := openPersistenceTestPool(t)
	const (
		userID     = "b1111111-1111-4111-8111-111111111111"
		resolveKey = "b2222222-2222-4222-8222-222222222222"
		requestID  = "b3333333-3333-4333-8333-333333333333"
	)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, display_name, default_time_zone_name) VALUES ($1, 'Failed place user', 'America/New_York')`, userID); err != nil {
		t.Fatal(err)
	}
	keys, _ := NewHMACKeyRing(HMACKey{ID: "place-test", Value: []byte("0123456789abcdef0123456789abcdef")}, nil)
	provider := &testPlaceProvider{err: &placeprovider.ResolveError{Code: placeprovider.FailureProviderUnavailable, Retryable: true}}
	store, _ := NewPlaceStore(pool, keys, provider)
	input := ResolvePlaceInput{UserID: userID, IdempotencyKey: resolveKey, RequestIdentity: []byte("identity"), RequestID: requestID, Coordinate: placeprovider.Coordinate{Latitude: 41, Longitude: -71}}
	first, err := store.Resolve(ctx, input)
	if err != nil || first.Status != 503 {
		t.Fatalf("first status=%d err=%v", first.Status, err)
	}
	replay, err := store.Resolve(ctx, input)
	if err != nil || string(replay.Body) != string(first.Body) || provider.calls != 1 {
		t.Fatalf("replay calls=%d err=%v first=%s replay=%s", provider.calls, err, first.Body, replay.Body)
	}
}
