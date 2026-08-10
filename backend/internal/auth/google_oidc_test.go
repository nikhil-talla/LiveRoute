package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const testClientID = "web-client.apps.googleusercontent.com"

func TestGoogleVerifierAcceptsCanonicalIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := generateKey(t)
	verifier := testVerifier(key, now)
	claims := validClaims(now)
	claims["iss"] = googleLegacyIssuer
	claims["name"] = ""

	identity, err := verifier.Verify(context.Background(), signToken(t, key, claims), "browser-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer != googleIssuer || identity.Subject != "google-subject" ||
		identity.Email != "person@example.com" || !identity.EmailVerified ||
		identity.DisplayName != "LiveRoute user" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestGoogleVerifierRejectsInvalidSecurityClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := generateKey(t)
	otherKey := generateKey(t)
	verifier := testVerifier(key, now)
	tests := []struct {
		name          string
		mutate        func(map[string]any)
		signingKey    *rsa.PrivateKey
		expectedNonce string
	}{
		{name: "issuer", mutate: func(claims map[string]any) { claims["iss"] = "https://attacker.example" }, signingKey: key, expectedNonce: "browser-nonce"},
		{name: "audience", mutate: func(claims map[string]any) { claims["aud"] = "other-client" }, signingKey: key, expectedNonce: "browser-nonce"},
		{name: "signature", mutate: func(map[string]any) {}, signingKey: otherKey, expectedNonce: "browser-nonce"},
		{name: "expiry", mutate: func(claims map[string]any) { claims["exp"] = now.Add(-time.Second).Unix() }, signingKey: key, expectedNonce: "browser-nonce"},
		{name: "future issued-at", mutate: func(claims map[string]any) { claims["iat"] = now.Add(time.Second).Unix() }, signingKey: key, expectedNonce: "browser-nonce"},
		{name: "nonce", mutate: func(map[string]any) {}, signingKey: key, expectedNonce: "different-nonce"},
		{name: "authorized party", mutate: func(claims map[string]any) { claims["azp"] = "other-client" }, signingKey: key, expectedNonce: "browser-nonce"},
		{name: "unverified retained email", mutate: func(claims map[string]any) { claims["email_verified"] = false }, signingKey: key, expectedNonce: "browser-nonce"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := validClaims(now)
			test.mutate(claims)
			_, err := verifier.Verify(context.Background(), signToken(t, test.signingKey, claims), test.expectedNonce)
			if !errors.Is(err, ErrInvalidGoogleToken) {
				t.Fatalf("Verify error = %v, want generic invalid-token error", err)
			}
		})
	}
}

func TestGoogleVerifierRejectsOversizedCredential(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	verifier := testVerifier(generateKey(t), now)
	_, err := verifier.Verify(context.Background(), strings.Repeat("x", maximumTokenBytes+1), "browser-nonce")
	if !errors.Is(err, ErrInvalidGoogleToken) {
		t.Fatalf("Verify error = %v, want invalid-token error", err)
	}
}

func TestCapCacheControl(t *testing.T) {
	for input, expected := range map[string]string{
		"public, max-age=172800": "public, max-age=86400",
		"public, max-age=3600":   "public, max-age=3600",
		"public":                 "public",
		"max-age=invalid":        "max-age=0",
	} {
		if actual := capCacheControl(input); actual != expected {
			t.Errorf("capCacheControl(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func testVerifier(key *rsa.PrivateKey, now time.Time) *GoogleVerifier {
	return newGoogleVerifier(
		testClientID,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}},
		nil,
		func() time.Time { return now },
	)
}

func validClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":            googleIssuer,
		"sub":            "google-subject",
		"aud":            testClientID,
		"azp":            testClientID,
		"exp":            now.Add(5 * time.Minute).Unix(),
		"iat":            now.Add(-time.Minute).Unix(),
		"nonce":          "browser-nonce",
		"email":          "person@example.com",
		"email_verified": true,
		"name":           "Test Person",
	}
}

func generateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
}
