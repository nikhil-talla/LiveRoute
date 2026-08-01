package persistence

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrAuthenticationFailed = errors.New("authentication failed")
	ErrAuthenticationInput  = errors.New("authentication input is invalid")
)

// DevelopmentAuthenticator performs the database-backed local V1 token
// lookup. It deliberately returns one generic authentication failure for
// absent, revoked, and expired credentials so the gateway cannot disclose
// token or account state.
type DevelopmentAuthenticator struct {
	pool *pgxpool.Pool
}

func NewDevelopmentAuthenticator(pool *pgxpool.Pool) (*DevelopmentAuthenticator, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &DevelopmentAuthenticator{pool: pool}, nil
}

func (authenticator *DevelopmentAuthenticator) Authenticate(ctx context.Context, token string) (string, error) {
	if ctx == nil || !validDevelopmentToken(token) {
		return "", ErrAuthenticationInput
	}
	digest := sha256.Sum256([]byte(token))
	var userID string
	var storedDigest []byte
	err := authenticator.pool.QueryRow(ctx, `
		SELECT user_id::text, token_sha256
		FROM development_auth_tokens
		WHERE token_sha256 = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > clock_timestamp())
	`, digest[:]).Scan(&userID, &storedDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAuthenticationFailed
	}
	if err != nil {
		return "", fmt.Errorf("authenticate development token: %w", err)
	}
	if len(storedDigest) != sha256.Size || subtle.ConstantTimeCompare(digest[:], storedDigest) != 1 {
		return "", ErrAuthenticationFailed
	}
	return userID, nil
}

func validDevelopmentToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}
