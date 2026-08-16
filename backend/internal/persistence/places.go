package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/liveroute/liveroute/backend/internal/canonicaljson"
	placeprovider "github.com/liveroute/liveroute/backend/internal/place"
)

const (
	placeResolutionLifetime = 10 * time.Minute
	httpRecordLifetime      = 30 * 24 * time.Hour
)

var (
	ErrPlaceInput          = errors.New("place input is invalid")
	ErrPlaceResolutionGone = errors.New("place resolution is unavailable")
)

type PlaceStore struct {
	pool     *pgxpool.Pool
	keys     HMACKeyRing
	provider placeprovider.Provider
}

type ResolvePlaceInput struct {
	UserID          string
	IdempotencyKey  string
	RequestIdentity []byte
	RequestID       string
	Coordinate      placeprovider.Coordinate
}

type PlaceHTTPResult struct {
	Status      int
	ContentType string
	Body        json.RawMessage
	ResourceID  string
}

type CreatePlaceInput struct {
	UserID          string
	IdempotencyKey  string
	RequestDigest   [sha256.Size]byte
	ResolutionToken string
	RequestID       string
}

func NewPlaceStore(pool *pgxpool.Pool, keys HMACKeyRing, provider placeprovider.Provider) (*PlaceStore, error) {
	if pool == nil || keys.CurrentID() == "" || provider == nil {
		return nil, errors.New("place store dependencies are required")
	}
	return &PlaceStore{pool: pool, keys: keys, provider: provider}, nil
}

func (store *PlaceStore) Resolve(ctx context.Context, input ResolvePlaceInput) (PlaceHTTPResult, error) {
	if ctx == nil || !validCanonicalUUID(input.UserID) || !validCanonicalUUID(input.IdempotencyKey) ||
		len(input.RequestIdentity) == 0 || !validCanonicalUUID(input.RequestID) {
		return PlaceHTTPResult{}, ErrPlaceInput
	}
	keyID := store.keys.CurrentID()
	digest, ok := store.keys.HMAC(keyID, input.RequestIdentity)
	if !ok {
		return PlaceHTTPResult{}, errors.New("current place request HMAC key is unavailable")
	}
	attemptID, err := newAuthUUID()
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	recordID, err := newAuthUUID()
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	fresh, replay, err := store.reserveResolution(ctx, input, recordID, attemptID, keyID, digest)
	if err != nil || !fresh {
		return replay, err
	}

	marked := false
	candidate, resolveErr := store.provider.Resolve(ctx, input.UserID, input.Coordinate, func() error {
		tag, markErr := store.pool.Exec(ctx, `
			UPDATE place_resolution_attempts
			SET provider_request_started_at = clock_timestamp()
			WHERE id = $1 AND user_id = $2 AND state = 'pending'
		`, attemptID, input.UserID)
		marked = markErr == nil && tag.RowsAffected() == 1
		if markErr == nil && !marked {
			markErr = errors.New("place resolution attempt is no longer pending")
		}
		return markErr
	})
	if resolveErr == nil && !marked {
		resolveErr = &placeprovider.ResolveError{Code: placeprovider.FailureProviderUnavailable, Retryable: true}
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if resolveErr != nil {
		return store.finalizeResolutionFailure(finalizeContext, recordID, attemptID, input.RequestID, resolveErr)
	}
	token, err := store.placeToken(keyID, attemptID)
	if err != nil {
		return store.finalizeResolutionFailure(finalizeContext, recordID, attemptID, input.RequestID,
			&placeprovider.ResolveError{Code: placeprovider.FailureProviderUnavailable, Retryable: true})
	}
	return store.finalizeResolutionSuccess(finalizeContext, recordID, attemptID, token, candidate)
}

func (store *PlaceStore) reserveResolution(ctx context.Context, input ResolvePlaceInput, recordID, attemptID, keyID string, digest [sha256.Size]byte) (bool, PlaceHTTPResult, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, PlaceHTTPResult{}, fmt.Errorf("begin place resolution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO http_idempotency_records (
			id, user_id, idempotency_key, http_method, normalized_path,
			operation_kind, request_digest_algorithm, request_digest_key_id,
			request_digest, state, retain_until
		) VALUES ($1, $2, $3, 'POST', '/api/v1/places/resolve', 'resolve_place',
		          'rfc8785-hmac-sha256-v1', $4, $5, 'in_progress',
		          clock_timestamp() + $6::interval)
		ON CONFLICT (user_id, http_method, normalized_path, idempotency_key) DO NOTHING
	`, recordID, input.UserID, input.IdempotencyKey, keyID, digest[:], httpRecordLifetime.String())
	if err != nil {
		return false, PlaceHTTPResult{}, fmt.Errorf("reserve place resolution: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO place_resolution_attempts (
				id, user_id, idempotency_record_id, provider, state, created_at, expires_at
			) VALUES ($1, $2, $3, 'mapbox_geocoding_v6_permanent', 'pending',
			          clock_timestamp(), clock_timestamp() + $4::interval)
		`, attemptID, input.UserID, recordID, placeResolutionLifetime.String()); err != nil {
			return false, PlaceHTTPResult{}, fmt.Errorf("create place resolution attempt: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, PlaceHTTPResult{}, fmt.Errorf("commit place resolution reservation: %w", err)
		}
		return true, PlaceHTTPResult{}, nil
	}

	var storedRecordID, storedKeyID, state string
	var storedDigest []byte
	var status *int
	var contentType *string
	var body []byte
	err = tx.QueryRow(ctx, `
		SELECT id::text, request_digest_key_id, request_digest, state, response_status,
		       response_content_type, response_body
		FROM http_idempotency_records
		WHERE user_id = $1 AND http_method = 'POST'
		  AND normalized_path = '/api/v1/places/resolve' AND idempotency_key = $2
		FOR UPDATE
	`, input.UserID, input.IdempotencyKey).Scan(&storedRecordID, &storedKeyID, &storedDigest, &state, &status, &contentType, &body)
	if err != nil {
		return false, PlaceHTTPResult{}, fmt.Errorf("load place resolution replay: %w", err)
	}
	replayDigest, available := store.keys.HMAC(storedKeyID, input.RequestIdentity)
	if !available {
		return false, PlaceHTTPResult{}, errors.New("place resolution replay HMAC key is unavailable")
	}
	if !equalDigest(storedDigest, replayDigest[:]) {
		return false, PlaceHTTPResult{}, ErrHTTPIdempotencyReused
	}
	if state != "completed" || status == nil || contentType == nil {
		return false, PlaceHTTPResult{}, ErrHTTPMutationPending
	}
	if *status == 200 {
		body, err = store.resolutionReplayBody(ctx, tx, storedRecordID, storedKeyID)
		if err != nil {
			return false, PlaceHTTPResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, PlaceHTTPResult{}, fmt.Errorf("commit place resolution replay: %w", err)
	}
	body, err = canonicalHTTPBody(body)
	if err != nil {
		return false, PlaceHTTPResult{}, fmt.Errorf("canonicalize place resolution replay: %w", err)
	}
	return false, PlaceHTTPResult{Status: *status, ContentType: *contentType, Body: body}, nil
}

func (store *PlaceStore) finalizeResolutionSuccess(ctx context.Context, recordID, attemptID, token string, candidate placeprovider.Candidate) (PlaceHTTPResult, error) {
	var expiresAt time.Time
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `
		UPDATE place_resolution_attempts SET
			state = 'resolved', resolution_token_sha256 = $2,
			latitude = $3, longitude = $4, formatted_address = NULLIF($5, ''),
			display_name = $6, time_zone_name = $7,
			expires_at = clock_timestamp() + $8::interval
		WHERE id = $1 AND state = 'pending'
		RETURNING expires_at
	`, attemptID, sha256Bytes(token), candidate.Latitude, candidate.Longitude, candidate.FormattedAddress,
		candidate.DisplayName, candidate.TimeZoneName, placeResolutionLifetime.String()).Scan(&expiresAt); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("store place candidate: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"resolution_token": token, "expires_at_unix_ms": expiresAt.UnixMilli(),
		"candidate": candidateJSON(candidate),
	})
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	body, err = canonicalHTTPBody(body)
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET state = 'completed', response_status = 200,
			response_content_type = 'application/json', response_body = NULL,
			resource_id = $2, completed_at = clock_timestamp()
		WHERE id = $1 AND state = 'in_progress'
	`, recordID, attemptID); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("complete place resolution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("commit place resolution: %w", err)
	}
	return PlaceHTTPResult{Status: 200, ContentType: "application/json", Body: body, ResourceID: attemptID}, nil
}

func (store *PlaceStore) finalizeResolutionFailure(ctx context.Context, recordID, attemptID, requestID string, cause error) (PlaceHTTPResult, error) {
	code, status, retryable := placeFailure(cause)
	detail := map[string]string{
		"RESOURCE_EXHAUSTED":   "place resolution capacity is exhausted",
		"PLACE_NOT_RESOLVED":   "place could not be resolved",
		"PROVIDER_UNAVAILABLE": "place provider is unavailable",
	}[code]
	body, _ := json.Marshal(map[string]any{
		"type": "https://liveroute.invalid/problems/" + lowerASCII(code), "title": detail,
		"status": status, "code": code, "request_id": requestID, "retryable": retryable,
	})
	body, _ = canonicalHTTPBody(body)
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE place_resolution_attempts SET state = 'failed', failure_code = $2 WHERE id = $1 AND state = 'pending'`, attemptID, code); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("fail place resolution attempt: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE http_idempotency_records SET state = 'completed', response_status = $2,
			response_content_type = 'application/problem+json', response_body = $3,
			completed_at = clock_timestamp()
		WHERE id = $1 AND state = 'in_progress'
	`, recordID, status, body); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("complete failed place resolution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("commit failed place resolution: %w", err)
	}
	return PlaceHTTPResult{Status: status, ContentType: "application/problem+json", Body: body}, nil
}

func (store *PlaceStore) Create(ctx context.Context, input CreatePlaceInput) (PlaceHTTPResult, error) {
	if ctx == nil || !validCanonicalUUID(input.UserID) || !validCanonicalUUID(input.IdempotencyKey) ||
		!validOpaqueToken(input.ResolutionToken) || !validCanonicalUUID(input.RequestID) {
		return PlaceHTTPResult{}, ErrPlaceInput
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("begin place creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	recordID, err := newAuthUUID()
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO http_idempotency_records (
			id, user_id, idempotency_key, http_method, normalized_path,
			operation_kind, request_digest_algorithm, request_digest,
			state, retain_until
		) VALUES ($1, $2, $3, 'POST', '/api/v1/places', 'create_place',
		          'rfc8785-sha256-v1', $4, 'in_progress', clock_timestamp() + $5::interval)
		ON CONFLICT (user_id, http_method, normalized_path, idempotency_key) DO NOTHING
	`, recordID, input.UserID, input.IdempotencyKey, input.RequestDigest[:], httpRecordLifetime.String())
	if err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("reserve place creation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var storedDigest, body []byte
		var status int
		var contentType, state string
		err := tx.QueryRow(ctx, `SELECT request_digest, state, response_status, response_content_type, response_body FROM http_idempotency_records WHERE user_id=$1 AND http_method='POST' AND normalized_path='/api/v1/places' AND idempotency_key=$2 FOR UPDATE`, input.UserID, input.IdempotencyKey).Scan(&storedDigest, &state, &status, &contentType, &body)
		if err != nil {
			return PlaceHTTPResult{}, fmt.Errorf("load place creation replay: %w", err)
		}
		if !equalDigest(storedDigest, input.RequestDigest[:]) {
			return PlaceHTTPResult{}, ErrHTTPIdempotencyReused
		}
		if state != "completed" {
			return PlaceHTTPResult{}, ErrHTTPMutationPending
		}
		if err := tx.Commit(ctx); err != nil {
			return PlaceHTTPResult{}, err
		}
		body, err = canonicalHTTPBody(body)
		if err != nil {
			return PlaceHTTPResult{}, fmt.Errorf("canonicalize place creation replay: %w", err)
		}
		return PlaceHTTPResult{Status: status, ContentType: contentType, Body: body}, nil
	}

	placeID, err := newAuthUUID()
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	var latitude, longitude float64
	var formattedAddress *string
	var displayName, timeZoneName, resolutionID string
	err = tx.QueryRow(ctx, `
		SELECT id::text, latitude, longitude, formatted_address, display_name, time_zone_name
		FROM place_resolution_attempts
		WHERE user_id = $1 AND resolution_token_sha256 = $2
		  AND state = 'resolved' AND expires_at > clock_timestamp()
		FOR UPDATE
	`, input.UserID, sha256Bytes(input.ResolutionToken)).Scan(&resolutionID, &latitude, &longitude, &formattedAddress, &displayName, &timeZoneName)
	if errors.Is(err, pgx.ErrNoRows) {
		body, bodyErr := placeProblemBody(410, "NOT_FOUND", false, "place resolution has expired or was consumed", input.RequestID)
		if bodyErr != nil {
			return PlaceHTTPResult{}, bodyErr
		}
		if _, updateErr := tx.Exec(ctx, `UPDATE http_idempotency_records SET state='completed', response_status=410, response_content_type='application/problem+json', response_body=$2, completed_at=clock_timestamp() WHERE id=$1`, recordID, body); updateErr != nil {
			return PlaceHTTPResult{}, fmt.Errorf("complete unavailable place creation: %w", updateErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return PlaceHTTPResult{}, fmt.Errorf("commit unavailable place creation: %w", commitErr)
		}
		return PlaceHTTPResult{Status: 410, ContentType: "application/problem+json", Body: body}, nil
	}
	if err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("lock place resolution: %w", err)
	}
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO places (id, owner_user_id, source_resolution_id, latitude, longitude, formatted_address, display_name, time_zone_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at
	`, placeID, input.UserID, resolutionID, latitude, longitude, formattedAddress, displayName, timeZoneName).Scan(&createdAt)
	if err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("insert place: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE place_resolution_attempts SET state='consumed', consumed_at=clock_timestamp() WHERE id=$1`, resolutionID); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("consume place resolution: %w", err)
	}
	bodyValue := map[string]any{"place_id": placeID, "latitude": latitude, "longitude": longitude, "display_name": displayName, "time_zone_name": timeZoneName, "created_at_unix_ms": createdAt.UnixMilli()}
	if formattedAddress != nil {
		bodyValue["formatted_address"] = *formattedAddress
	}
	body, _ := json.Marshal(bodyValue)
	body, err = canonicalHTTPBody(body)
	if err != nil {
		return PlaceHTTPResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE http_idempotency_records SET state='completed', response_status=201, response_content_type='application/json', response_body=$2, resource_id=$3, completed_at=clock_timestamp() WHERE id=$1`, recordID, body, placeID); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("complete place creation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PlaceHTTPResult{}, fmt.Errorf("commit place creation: %w", err)
	}
	return PlaceHTTPResult{Status: 201, ContentType: "application/json", Body: body, ResourceID: placeID}, nil
}

func candidateJSON(candidate placeprovider.Candidate) map[string]any {
	result := map[string]any{"latitude": candidate.Latitude, "longitude": candidate.Longitude, "display_name": candidate.DisplayName, "time_zone_name": candidate.TimeZoneName}
	if candidate.FormattedAddress != "" {
		result["formatted_address"] = candidate.FormattedAddress
	}
	return result
}

func placeFailure(err error) (string, int, bool) {
	var resolved *placeprovider.ResolveError
	if errors.As(err, &resolved) {
		switch resolved.Code {
		case placeprovider.FailureResourceExhausted:
			return string(resolved.Code), 429, true
		case placeprovider.FailurePlaceNotResolved:
			return string(resolved.Code), 422, false
		case placeprovider.FailureProviderUnavailable:
			return string(resolved.Code), 503, resolved.Retryable
		}
	}
	return "PROVIDER_UNAVAILABLE", 503, true
}

func (store *PlaceStore) placeToken(keyID, attemptID string) (string, error) {
	digest, ok := store.keys.HMAC(keyID, []byte("liveroute.place-resolution-token.v1\x00"+attemptID))
	if !ok {
		return "", errors.New("place resolution token key is unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func equalDigest(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func lowerASCII(value string) string {
	bytes := []byte(value)
	for index, character := range bytes {
		if character >= 'A' && character <= 'Z' {
			bytes[index] += 'a' - 'A'
		}
	}
	return string(bytes)
}

func canonicalHTTPBody(value []byte) ([]byte, error) {
	canonical, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func (store *PlaceStore) resolutionReplayBody(ctx context.Context, tx pgx.Tx, recordID, keyID string) ([]byte, error) {
	var attemptID string
	var latitude, longitude float64
	var formattedAddress *string
	var displayName, timeZoneName string
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT id::text, latitude, longitude, formatted_address, display_name,
		       time_zone_name, expires_at
		FROM place_resolution_attempts
		WHERE idempotency_record_id=$1 AND state IN ('resolved','consumed')
	`, recordID).Scan(&attemptID, &latitude, &longitude, &formattedAddress, &displayName, &timeZoneName, &expiresAt); err != nil {
		return nil, fmt.Errorf("load place resolution replay candidate: %w", err)
	}
	token, err := store.placeToken(keyID, attemptID)
	if err != nil {
		return nil, err
	}
	candidate := placeprovider.Candidate{Latitude: latitude, Longitude: longitude, DisplayName: displayName, TimeZoneName: timeZoneName}
	if formattedAddress != nil {
		candidate.FormattedAddress = *formattedAddress
	}
	body, err := json.Marshal(map[string]any{
		"resolution_token": token, "expires_at_unix_ms": expiresAt.UnixMilli(), "candidate": candidateJSON(candidate),
	})
	if err != nil {
		return nil, err
	}
	return canonicalHTTPBody(body)
}

func placeProblemBody(status int, code string, retryable bool, title, requestID string) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"type": "https://liveroute.invalid/problems/" + lowerASCII(code), "title": title,
		"status": status, "code": code, "request_id": requestID, "retryable": retryable,
	})
	if err != nil {
		return nil, err
	}
	return canonicalHTTPBody(body)
}
