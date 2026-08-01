package persistence

import (
	"context"
	"testing"
)

func TestDevelopmentTokenValidationIsFixedLengthCanonicalBase64URL(t *testing.T) {
	valid := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if !validDevelopmentToken(valid) {
		t.Fatal("valid development token rejected")
	}
	for _, invalid := range []string{
		"", valid[:42] + "=", valid + "\n", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA-",
	} {
		if validDevelopmentToken(invalid) {
			t.Fatalf("invalid development token accepted: %q", invalid)
		}
	}
	authenticator := &DevelopmentAuthenticator{}
	if _, err := authenticator.Authenticate(context.Background(), "short"); err != ErrAuthenticationInput {
		t.Fatalf("invalid token returned %v, want ErrAuthenticationInput", err)
	}
}
