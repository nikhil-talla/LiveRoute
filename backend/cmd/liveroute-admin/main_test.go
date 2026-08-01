package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTokenDigestDoesNotAcceptSecretFormatting(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := readTokenDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest == ([32]byte{}) {
		t.Fatal("valid token produced empty digest")
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTokenDigest(path); err == nil {
		t.Fatal("newline-terminated token was accepted")
	}
}

func TestSeedConfigRequiresCanonicalUserIdentity(t *testing.T) {
	config := seedConfig{databaseURL: "postgres://example", tokenPath: "/token", displayName: "Dev", timeZone: "America/New_York"}
	if err := config.validate(); err == nil {
		t.Fatal("missing user id was accepted")
	}
	config.userID = "11111111-1111-4111-8111-111111111111"
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
}
