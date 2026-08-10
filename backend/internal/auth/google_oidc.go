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
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}
	if verifier.client != nil {
		ctx = oidc.ClientContext(ctx, verifier.client)
	}
	token, err := verifier.verifier.Verify(ctx, rawToken)
	if err != nil {
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}
	if token.Issuer != googleIssuer && token.Issuer != googleLegacyIssuer {
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}
	if token.Subject == "" || len(token.Subject) > 255 || token.IssuedAt.IsZero() ||
		token.IssuedAt.After(verifier.now()) ||
		len(token.Nonce) != len(expectedNonce) ||
		subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(expectedNonce)) != 1 {
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}

	var claims struct {
		AuthorizedParty string `json:"azp"`
		Email           string `json:"email"`
		EmailVerified   bool   `json:"email_verified"`
		Name            string `json:"name"`
	}
	if err := token.Claims(&claims); err != nil {
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != verifier.clientID {
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}
	if len(claims.Email) > 320 || (claims.Email != "" && !claims.EmailVerified) || len(claims.Name) > 200 {
		return GoogleIdentity{}, ErrInvalidGoogleToken
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
