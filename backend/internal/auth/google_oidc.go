package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	googleIssuer       = "https://accounts.google.com"
	googleLegacyIssuer = "accounts.google.com"
	googleJWKSURL      = "https://www.googleapis.com/oauth2/v3/certs"
	maximumTokenBytes  = 16 * 1024
	maximumJWKSMaxAge  = 24 * time.Hour
)

var ErrInvalidGoogleToken = errors.New("google identity token is invalid")

type googleTokenValidationError struct {
	stage string
}

func (err *googleTokenValidationError) Error() string {
	return ErrInvalidGoogleToken.Error()
}

func (err *googleTokenValidationError) Unwrap() error {
	return ErrInvalidGoogleToken
}

func invalidGoogleToken(stage string) error {
	return &googleTokenValidationError{stage: stage}
}

// GoogleTokenValidationStage returns a non-sensitive validation-stage label
// suitable for operational logs. It never includes token or claim values.
func GoogleTokenValidationStage(err error) string {
	var validationError *googleTokenValidationError
	if errors.As(err, &validationError) {
		return validationError.stage
	}
	return "unknown"
}

// ExtractNonce reads only the unsigned JWT payload so the handler can locate
// the one stored nonce. The result is never trusted until Verify validates the
// complete signed token against Google's keys and configured audience.
func ExtractNonce(rawToken string) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 || parts[1] == "" {
		return "", ErrInvalidGoogleToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > maximumTokenBytes {
		return "", ErrInvalidGoogleToken
	}
	var claims struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Nonce == "" || len(claims.Nonce) > 512 {
		return "", ErrInvalidGoogleToken
	}
	return claims.Nonce, nil
}

// GoogleIdentity is the verified identity boundary consumed by persistence.
// Issuer is canonicalized so the database identity key never depends on which
// of Google's two accepted issuer spellings appeared in one token.
type GoogleIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type GoogleVerifier struct {
	clientID string
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	now      func() time.Time
}

// NewGoogleVerifier constructs one long-lived verifier. The remote key set is
// intentionally reused so normal requests do not perform provider I/O and an
// unknown key id receives the library's single bounded refresh behavior.
func NewGoogleVerifier(ctx context.Context, clientID string, client *http.Client) (*GoogleVerifier, error) {
	if ctx == nil || strings.TrimSpace(clientID) == "" || len(clientID) > 512 {
		return nil, errors.New("google OIDC context and web client id are required")
	}
	if client == nil || client.Timeout <= 0 {
		return nil, errors.New("google OIDC HTTP client with a timeout is required")
	}

	boundedClient := *client
	transport := boundedClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	boundedClient.Transport = cacheControlCapTransport{base: transport}
	keyContext := oidc.ClientContext(ctx, &boundedClient)
	keySet := oidc.NewRemoteKeySet(keyContext, googleJWKSURL)
	return newGoogleVerifier(clientID, keySet, &boundedClient, time.Now), nil
}

func newGoogleVerifier(
	clientID string,
	keySet oidc.KeySet,
	client *http.Client,
	now func() time.Time,
) *GoogleVerifier {
	configuration := &oidc.Config{
		ClientID:             clientID,
		SupportedSigningAlgs: []string{oidc.RS256},
		SkipIssuerCheck:      true,
		Now:                  now,
	}
	return &GoogleVerifier{
		clientID: clientID,
		verifier: oidc.NewVerifier(googleIssuer, keySet, configuration),
		client:   client,
		now:      now,
	}
}

// Verify validates the signed token and the browser nonce. It returns only a
// generic public error so provider claims and validation details cannot leak
// through an authentication response.
func (verifier *GoogleVerifier) Verify(ctx context.Context, rawToken, expectedNonce string) (GoogleIdentity, error) {
	if verifier == nil || verifier.verifier == nil || ctx == nil || expectedNonce == "" ||
		len(rawToken) == 0 || len(rawToken) > maximumTokenBytes {
		return GoogleIdentity{}, invalidGoogleToken("input")
	}
	if verifier.client != nil {
		ctx = oidc.ClientContext(ctx, verifier.client)
	}
	token, err := verifier.verifier.Verify(ctx, rawToken)
	if err != nil {
		return GoogleIdentity{}, invalidGoogleToken(signedTokenValidationStage(err))
	}
	if token.Issuer != googleIssuer && token.Issuer != googleLegacyIssuer {
		return GoogleIdentity{}, invalidGoogleToken("issuer")
	}
	if token.Subject == "" || len(token.Subject) > 255 {
		return GoogleIdentity{}, invalidGoogleToken("subject")
	}
	if token.IssuedAt.IsZero() || token.IssuedAt.After(verifier.now()) {
		return GoogleIdentity{}, invalidGoogleToken("issued_at")
	}
	if len(token.Nonce) != len(expectedNonce) ||
		subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(expectedNonce)) != 1 {
		return GoogleIdentity{}, invalidGoogleToken("nonce")
	}

	var claims struct {
		AuthorizedParty string `json:"azp"`
		Email           string `json:"email"`
		EmailVerified   bool   `json:"email_verified"`
		Name            string `json:"name"`
	}
	if err := token.Claims(&claims); err != nil {
		return GoogleIdentity{}, invalidGoogleToken("identity_claims")
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != verifier.clientID {
		return GoogleIdentity{}, invalidGoogleToken("authorized_party")
	}
	if len(claims.Email) > 320 || (claims.Email != "" && !claims.EmailVerified) || len(claims.Name) > 200 {
		return GoogleIdentity{}, invalidGoogleToken("profile_claims")
	}
	displayName := strings.TrimSpace(claims.Name)
	if displayName == "" {
		displayName = "LiveRoute user"
	}

	return GoogleIdentity{
		Issuer:        googleIssuer,
		Subject:       token.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		DisplayName:   displayName,
	}, nil
}

func signedTokenValidationStage(err error) string {
	var expired *oidc.TokenExpiredError
	if errors.As(err, &expired) {
		return "expired"
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "oidc: expected audience "):
		return "audience"
	case strings.HasPrefix(message, "oidc: current time "):
		return "not_before"
	case strings.Contains(message, "fetching keys context deadline exceeded"),
		strings.Contains(message, "fetching keys context canceled"):
		return "keys_timeout"
	case strings.Contains(message, "fetching keys "):
		return "keys_fetch"
	case strings.HasSuffix(message, "failed to verify id token signature"):
		return "signature"
	case strings.HasPrefix(message, "failed to verify signature: "):
		return "signature_or_keys"
	case strings.HasPrefix(message, "oidc: malformed jwt: "),
		strings.HasPrefix(message, "oidc: id token not signed"),
		strings.HasPrefix(message, "oidc: multiple signatures"):
		return "token_format"
	default:
		return "signed_token"
	}
}

type cacheControlCapTransport struct {
	base http.RoundTripper
}

func (transport cacheControlCapTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Header.Set("Cache-Control", capCacheControl(response.Header.Get("Cache-Control")))
	return response, nil
}

func capCacheControl(value string) string {
	parts := strings.Split(value, ",")
	for index, part := range parts {
		trimmed := strings.TrimSpace(part)
		parts[index] = trimmed
		name, rawSeconds, ok := strings.Cut(trimmed, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "max-age") {
			continue
		}
		seconds, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(rawSeconds), `"`), 10, 64)
		if err != nil || seconds < 0 {
			parts[index] = "max-age=0"
		} else if seconds > int64(maximumJWKSMaxAge/time.Second) {
			parts[index] = fmt.Sprintf("max-age=%d", int64(maximumJWKSMaxAge/time.Second))
		}
	}
	return strings.Join(parts, ", ")
}
