package persistence

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authTokenBytes      = 32
	oidcNonceLifetime   = 5 * time.Minute
	sessionIdleLifetime = 7 * 24 * time.Hour
	sessionAbsoluteLife = 30 * 24 * time.Hour
	sessionRotationAge  = 24 * time.Hour
	predecessorGrace    = time.Minute
	idleTouchInterval   = 5 * time.Minute
	webSocketTicketLife = time.Minute
	csrfContext         = "liveroute.csrf.v1"
	maximumOpaqueToken  = 43
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrNonceNotFound   = errors.New("login nonce not found")
	ErrTicketNotFound  = errors.New("websocket ticket not found")
	ErrInvalidOpaque   = errors.New("opaque token is invalid")
	ianaZonePattern    = regexp.MustCompile(`^[A-Za-z_+-]+(?:/[A-Za-z0-9_+.-]+)+$`)
)

// HMACKey is a server-held key. The identifier is safe to persist; Value is
// never serialized into a response or log.
type HMACKey struct {
	ID    string
	Value []byte
}

type HMACKeyRing struct {
	current  HMACKey
	previous map[string][]byte
}

func NewHMACKeyRing(current HMACKey, previous []HMACKey) (HMACKeyRing, error) {
	if strings.TrimSpace(current.ID) == "" || len(current.Value) < sha256.Size {
		return HMACKeyRing{}, errors.New("current HMAC key id and value are required")
	}
	result := HMACKeyRing{current: HMACKey{ID: current.ID, Value: append([]byte(nil), current.Value...)}, previous: make(map[string][]byte)}
	for _, key := range previous {
		if strings.TrimSpace(key.ID) == "" || len(key.Value) < sha256.Size || key.ID == current.ID {
			return HMACKeyRing{}, errors.New("HMAC key ids must be unique and values must be at least 32 bytes")
		}
		if _, exists := result.previous[key.ID]; exists {
			return HMACKeyRing{}, errors.New("HMAC key ids must be unique")
		}
		result.previous[key.ID] = append([]byte(nil), key.Value...)
	}
	return result, nil
}

func (keys HMACKeyRing) CurrentID() string { return keys.current.ID }

func (keys HMACKeyRing) key(id string) ([]byte, bool) {
	if id == keys.current.ID {
		return keys.current.Value, true
	}
	value, ok := keys.previous[id]
	return value, ok
}

type AuthUser struct {
	ID                  string
	DisplayName         string
	Email               string
	DefaultTimeZoneName string
}

type Session struct {
	ID                     string
	FamilyID               string
	PresentedToken         string
	Token                  string
	User                   AuthUser
	CSRFKeyID              string
	CSRFToken              string
	PresentedCSRFToken     string
	IdleExpiresAt          time.Time
	AbsoluteExpiresAt      time.Time
	Rotated                bool
	CanMintWebSocketTicket bool
}

type LoginNonce struct {
	Nonce     string
	Binding   string
	ExpiresAt time.Time
}

type WebSocketTicket struct {
	Token     string
	ExpiresAt time.Time
}

type GoogleIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type HTTPAuthStore struct {
	pool *pgxpool.Pool
	keys HMACKeyRing
}

func NewHTTPAuthStore(pool *pgxpool.Pool, keys HMACKeyRing) (*HTTPAuthStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	if keys.current.ID == "" {
		return nil, errors.New("HMAC keys are required")
	}
	return &HTTPAuthStore{pool: pool, keys: keys}, nil
}

func (store *HTTPAuthStore) CreateLoginNonce(ctx context.Context) (LoginNonce, error) {
	if ctx == nil {
		return LoginNonce{}, errors.New("context is required")
	}
	nonce, err := newOpaqueToken()
	if err != nil {
		return LoginNonce{}, err
	}
	binding, err := newOpaqueToken()
	if err != nil {
		return LoginNonce{}, err
	}
	id, err := newAuthUUID()
	if err != nil {
		return LoginNonce{}, err
	}
	var expiresAt time.Time
	err = store.pool.QueryRow(ctx, `
		WITH current_time AS (SELECT clock_timestamp() AS value)
		INSERT INTO oidc_login_nonces (id, nonce_sha256, browser_binding_sha256, created_at, expires_at)
		SELECT $1, $2, $3, value, value + $4::interval FROM current_time
		RETURNING expires_at
	`, id, sha256Bytes(nonce), sha256Bytes(binding), oidcNonceLifetime.String()).Scan(&expiresAt)
	if err != nil {
		return LoginNonce{}, fmt.Errorf("create login nonce: %w", err)
	}
	return LoginNonce{Nonce: nonce, Binding: binding, ExpiresAt: expiresAt}, nil
}

// CreateSessionForGoogle consumes the nonce, updates/creates the external
// identity, and creates the LiveRoute session in one PostgreSQL transaction.
func (store *HTTPAuthStore) CreateSessionForGoogle(ctx context.Context, nonce, binding string, identity GoogleIdentity, defaultTimeZoneName string) (Session, error) {
	if ctx == nil || !validOpaqueToken(nonce) || !validOpaqueToken(binding) || !validGoogleIdentity(identity) || !validTimeZoneName(defaultTimeZoneName) {
		return Session{}, ErrAuthenticationInput
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin Google session transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nonceID string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM oidc_login_nonces
		WHERE nonce_sha256 = $1 AND browser_binding_sha256 = $2
		  AND consumed_at IS NULL AND expires_at > clock_timestamp()
		FOR UPDATE
	`, sha256Bytes(nonce), sha256Bytes(binding)).Scan(&nonceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNonceNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("lock login nonce: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE oidc_login_nonces SET consumed_at = clock_timestamp() WHERE id = $1`, nonceID); err != nil {
		return Session{}, fmt.Errorf("consume login nonce: %w", err)
	}

	user, err := upsertExternalIdentity(ctx, tx, identity, defaultTimeZoneName)
	if err != nil {
		return Session{}, err
	}
	result, err := insertSession(ctx, tx, user, store.keys.CurrentID())
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit Google session: %w", err)
	}
	result.CSRFToken = store.CSRF(result.Token, result.CSRFKeyID)
	return result, nil
}

func (store *HTTPAuthStore) AuthenticateSession(ctx context.Context, token string) (Session, error) {
	if ctx == nil || !validOpaqueToken(token) {
		return Session{}, ErrSessionNotFound
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin session lookup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var session Session
	var predecessorID *string
	err = tx.QueryRow(ctx, `
		SELECT s.id::text, s.session_family_id::text, s.user_id::text,
		       u.display_name, COALESCE(e.email, ''), u.default_time_zone_name,
		       s.csrf_key_id, s.idle_expires_at, s.absolute_expires_at,
		       s.replaced_by_session_id::text
		FROM user_sessions s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN LATERAL (
			SELECT email FROM external_identities
			WHERE user_id = s.user_id AND provider = 'google'
			ORDER BY last_authenticated_at DESC LIMIT 1
		) e ON true
		WHERE s.token_sha256 = $1 AND s.revoked_at IS NULL
		  AND s.absolute_expires_at > clock_timestamp()
		  AND (s.idle_expires_at > clock_timestamp()
		       OR (s.replaced_by_session_id IS NOT NULL AND s.predecessor_valid_until > clock_timestamp()))
		FOR UPDATE OF s
	`, sha256Bytes(token)).Scan(&session.ID, &session.FamilyID, &session.User.ID, &session.User.DisplayName, &session.User.Email, &session.User.DefaultTimeZoneName, &session.CSRFKeyID, &session.IdleExpiresAt, &session.AbsoluteExpiresAt, &predecessorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("lookup session: %w", err)
	}
	session.PresentedToken = token
	session.Token = token
	session.CanMintWebSocketTicket = predecessorID == nil
	if predecessorID == nil {
		if sessionShouldRotate(ctx, tx, session.ID) {
			rotated, rotateErr := rotateSession(ctx, tx, session, store.keys.CurrentID())
			if rotateErr != nil {
				return Session{}, rotateErr
			}
			session = rotated
		} else if _, err := tx.Exec(ctx, `UPDATE user_sessions SET last_seen_at = CASE WHEN clock_timestamp() - last_seen_at >= $2::interval THEN LEAST(clock_timestamp(), idle_expires_at) ELSE last_seen_at END WHERE id = $1`, session.ID, idleTouchInterval.String()); err != nil {
			return Session{}, fmt.Errorf("touch session: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit session lookup: %w", err)
	}
	session.CSRFToken = store.CSRF(session.Token, session.CSRFKeyID)
	session.PresentedCSRFToken = store.CSRF(session.PresentedToken, session.CSRFKeyID)
	return session, nil
}

func sessionShouldRotate(ctx context.Context, tx pgx.Tx, sessionID string) bool {
	var result bool
	_ = tx.QueryRow(ctx, `SELECT rotate_after <= clock_timestamp() FROM user_sessions WHERE id = $1`, sessionID).Scan(&result)
	return result
}

func (store *HTTPAuthStore) VerifyCSRF(session Session, token string) bool {
	key, ok := store.keys.key(session.CSRFKeyID)
	if !ok || token == "" || !validOpaqueToken(session.PresentedToken) {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(session.PresentedToken))
	_, _ = mac.Write([]byte(csrfContext))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	actual := session.PresentedCSRFToken
	if actual == "" {
		actual = session.CSRFToken
	}
	return hmac.Equal([]byte(expected), []byte(token)) && hmac.Equal([]byte(actual), []byte(token))
}

func (store *HTTPAuthStore) CSRF(token, keyID string) string {
	key, ok := store.keys.key(keyID)
	if !ok {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(token))
	_, _ = mac.Write([]byte(csrfContext))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (store *HTTPAuthStore) IssueWebSocketTicket(ctx context.Context, session Session) (WebSocketTicket, error) {
	if ctx == nil || !session.CanMintWebSocketTicket || !validOpaqueToken(session.PresentedToken) {
		return WebSocketTicket{}, ErrSessionNotFound
	}
	token, err := newOpaqueToken()
	if err != nil {
		return WebSocketTicket{}, err
	}
	id, err := newAuthUUID()
	if err != nil {
		return WebSocketTicket{}, err
	}
	var expiresAt time.Time
	err = store.pool.QueryRow(ctx, `
		WITH current_time AS (SELECT clock_timestamp() AS value)
		INSERT INTO websocket_auth_tickets (id, session_id, user_id, token_sha256, created_at, expires_at)
		SELECT $1, s.id, s.user_id, $2, value, value + $3::interval
		FROM user_sessions s, current_time
		WHERE s.token_sha256 = $4 AND s.revoked_at IS NULL
		  AND s.replaced_by_session_id IS NULL AND s.idle_expires_at > value AND s.absolute_expires_at > value
		RETURNING expires_at
	`, id, sha256Bytes(token), webSocketTicketLife.String(), sha256Bytes(session.Token)).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebSocketTicket{}, ErrSessionNotFound
	}
	if err != nil {
		return WebSocketTicket{}, fmt.Errorf("issue websocket ticket: %w", err)
	}
	return WebSocketTicket{Token: token, ExpiresAt: expiresAt}, nil
}

func (store *HTTPAuthStore) Logout(ctx context.Context, session Session) error {
	if ctx == nil || session.FamilyID == "" {
		return ErrSessionNotFound
	}
	_, err := store.pool.Exec(ctx, `UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, clock_timestamp()), revocation_reason = CASE WHEN revoked_at IS NULL THEN 'logout' ELSE revocation_reason END WHERE session_family_id = $1`, session.FamilyID)
	if err != nil {
		return fmt.Errorf("revoke session family: %w", err)
	}
	return nil
}

func (store *HTTPAuthStore) AuthenticateWebSocket(ctx context.Context, token string) (string, error) {
	if ctx == nil || !validOpaqueToken(token) {
		return "", ErrAuthenticationInput
	}
	var userID string
	err := store.pool.QueryRow(ctx, `
		WITH consumed AS (
			UPDATE websocket_auth_tickets t SET consumed_at = clock_timestamp()
			FROM user_sessions s
			WHERE t.token_sha256 = $1 AND t.consumed_at IS NULL AND t.expires_at > clock_timestamp()
			  AND s.id = t.session_id AND s.user_id = t.user_id AND s.revoked_at IS NULL
			  AND s.replaced_by_session_id IS NULL AND s.idle_expires_at > clock_timestamp()
			  AND s.absolute_expires_at > clock_timestamp()
			RETURNING t.user_id
		)
		SELECT user_id::text FROM consumed
	`, sha256Bytes(token)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAuthenticationFailed
	}
	if err != nil {
		return "", fmt.Errorf("consume websocket ticket: %w", err)
	}
	return userID, nil
}

func insertSession(ctx context.Context, tx pgx.Tx, user AuthUser, keyID string) (Session, error) {
	token, err := newOpaqueToken()
	if err != nil {
		return Session{}, err
	}
	familyID, err := newAuthUUID()
	if err != nil {
		return Session{}, err
	}
	id, err := newAuthUUID()
	if err != nil {
		return Session{}, err
	}
	var result Session
	result.User = user
	result.ID, result.FamilyID, result.CSRFKeyID = id, familyID, keyID
	result.Token, result.PresentedToken = token, token
	err = tx.QueryRow(ctx, `
		WITH current_time AS (SELECT clock_timestamp() AS value)
		INSERT INTO user_sessions (id, session_family_id, user_id, token_sha256, csrf_key_id, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_after)
		SELECT $1, $2, $3, $4, $5, value, value, value + $6::interval, value + $7::interval, value + $8::interval FROM current_time
		RETURNING idle_expires_at, absolute_expires_at
	`, id, familyID, user.ID, sha256Bytes(token), keyID, sessionIdleLifetime.String(), sessionAbsoluteLife.String(), sessionRotationAge.String()).Scan(&result.IdleExpiresAt, &result.AbsoluteExpiresAt)
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	result.CanMintWebSocketTicket = true
	return result, nil
}

func rotateSession(ctx context.Context, tx pgx.Tx, old Session, keyID string) (Session, error) {
	result := old
	result.Rotated = true
	result.CSRFKeyID = keyID
	var err error
	result.Token, err = newOpaqueToken()
	if err != nil {
		return Session{}, err
	}
	result.ID, err = newAuthUUID()
	if err != nil {
		return Session{}, err
	}
	err = tx.QueryRow(ctx, `
		WITH current_time AS (SELECT clock_timestamp() AS value)
		INSERT INTO user_sessions (id, session_family_id, user_id, token_sha256, csrf_key_id, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_after)
		SELECT $1, session_family_id, user_id, $2, $3, value, value, LEAST(value + $4::interval, absolute_expires_at), absolute_expires_at, LEAST(value + $5::interval, absolute_expires_at)
		FROM user_sessions, current_time WHERE id = $6
		RETURNING idle_expires_at, absolute_expires_at
	`, result.ID, sha256Bytes(result.Token), keyID, sessionIdleLifetime.String(), sessionRotationAge.String(), old.ID).Scan(&result.IdleExpiresAt, &result.AbsoluteExpiresAt)
	if err != nil {
		return Session{}, fmt.Errorf("insert rotated session: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE user_sessions SET replaced_by_session_id = $1, predecessor_valid_until = LEAST(clock_timestamp() + $2::interval, absolute_expires_at) WHERE id = $3`, result.ID, predecessorGrace.String(), old.ID); err != nil {
		return Session{}, fmt.Errorf("link rotated session: %w", err)
	}
	result.PresentedToken = old.PresentedToken
	result.CanMintWebSocketTicket = true
	return result, nil
}

func upsertExternalIdentity(ctx context.Context, tx pgx.Tx, identity GoogleIdentity, defaultTimeZoneName string) (AuthUser, error) {
	var user AuthUser
	err := tx.QueryRow(ctx, `
		SELECT u.id::text, u.display_name, COALESCE(e.email, ''), u.default_time_zone_name
		FROM external_identities e JOIN users u ON u.id = e.user_id
		WHERE e.provider = 'google' AND e.issuer = $1 AND e.subject = $2
		FOR UPDATE OF e, u
	`, identity.Issuer, identity.Subject).Scan(&user.ID, &user.DisplayName, &user.Email, &user.DefaultTimeZoneName)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE external_identities SET email = $1, email_verified = $2, display_name = $3, last_authenticated_at = clock_timestamp() WHERE provider = 'google' AND issuer = $4 AND subject = $5`, nullableString(identity.Email), identity.EmailVerified, identity.DisplayName, identity.Issuer, identity.Subject)
		if err != nil {
			return AuthUser{}, fmt.Errorf("update external identity: %w", err)
		}
		return user, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AuthUser{}, fmt.Errorf("lookup external identity: %w", err)
	}
	user.ID, err = newAuthUUID()
	if err != nil {
		return AuthUser{}, err
	}
	user.DisplayName, user.Email, user.DefaultTimeZoneName = identity.DisplayName, identity.Email, defaultTimeZoneName
	if _, err := tx.Exec(ctx, `INSERT INTO users (id, display_name, default_time_zone_name) VALUES ($1, $2, $3)`, user.ID, user.DisplayName, user.DefaultTimeZoneName); err != nil {
		return AuthUser{}, fmt.Errorf("insert Google user: %w", err)
	}
	identityID, err := newAuthUUID()
	if err != nil {
		return AuthUser{}, err
	}
	inserted, err := tx.Exec(ctx, `
		INSERT INTO external_identities (id, user_id, provider, issuer, subject, email, email_verified, display_name)
		VALUES ($1, $2, 'google', $3, $4, $5, $6, $7)
		ON CONFLICT (provider, issuer, subject) DO NOTHING
	`, identityID, user.ID, identity.Issuer, identity.Subject, nullableString(identity.Email), identity.EmailVerified, identity.DisplayName)
	if err != nil {
		return AuthUser{}, fmt.Errorf("insert external identity: %w", err)
	}
	if inserted.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			return AuthUser{}, fmt.Errorf("discard racing Google user: %w", err)
		}
		return upsertExternalIdentity(ctx, tx, identity, defaultTimeZoneName)
	}
	return user, nil
}

func validGoogleIdentity(identity GoogleIdentity) bool {
	return identity.Issuer == "https://accounts.google.com" && identity.Subject != "" && len(identity.Subject) <= 255 && len(identity.Email) <= 320 && (identity.Email == "" || identity.EmailVerified)
}

func validTimeZoneName(value string) bool {
	if len(value) < 1 || len(value) > 64 || !ianaZonePattern.MatchString(value) {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func newOpaqueToken() (string, error) {
	raw := make([]byte, authTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate opaque token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validOpaqueToken(value string) bool {
	if len(value) != maximumOpaqueToken {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == authTokenBytes && base64.RawURLEncoding.EncodeToString(raw) == value
}

func sha256Bytes(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func newAuthUUID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate auth id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
