package persistence

import (
	"strings"
	"testing"
)

func TestHMACKeyRingRequiresDistinctUsableKeys(t *testing.T) {
	key := strings.Repeat("k", 32)
	if _, err := NewHMACKeyRing(HMACKey{ID: "current", Value: []byte(key)}, []HMACKey{{ID: "current", Value: []byte(key)}}); err == nil {
		t.Fatal("duplicate current/previous key id was accepted")
	}
	ring, err := NewHMACKeyRing(HMACKey{ID: "current", Value: []byte(key)}, []HMACKey{{ID: "previous", Value: []byte(strings.Repeat("p", 32))}})
	if err != nil {
		t.Fatal(err)
	}
	if ring.CurrentID() != "current" {
		t.Fatalf("current key id=%q", ring.CurrentID())
	}
}

func TestCSRFUsesPresentedSessionTokenAndKeyIdentifier(t *testing.T) {
	ring, err := NewHMACKeyRing(HMACKey{ID: "current", Value: []byte(strings.Repeat("k", 32))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &HTTPAuthStore{keys: ring}
	session := Session{CSRFKeyID: "current", PresentedToken: strings.Repeat("A", 43)}
	session.CSRFToken = store.CSRF(session.PresentedToken, session.CSRFKeyID)
	session.PresentedCSRFToken = session.CSRFToken
	if !store.VerifyCSRF(session, session.CSRFToken) {
		t.Fatal("valid CSRF token was rejected")
	}
	if store.VerifyCSRF(session, store.CSRF(strings.Repeat("B", 43), session.CSRFKeyID)) {
		t.Fatal("CSRF token for another session token was accepted")
	}
}

func TestOpaqueTokenValidationRejectsNonCanonicalValues(t *testing.T) {
	valid := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if !validOpaqueToken(valid) {
		t.Fatal("canonical opaque token rejected")
	}
	for _, value := range []string{"", valid[:42], valid + "=", strings.Repeat("!", 43)} {
		if validOpaqueToken(value) {
			t.Fatalf("invalid opaque token accepted: %q", value)
		}
	}
}

func TestSavedTripListRejectsInvalidUserBeforeDatabaseAccess(t *testing.T) {
	store := &SavedTripStore{}
	if _, err := store.List(nil, "not-a-uuid"); err == nil {
		t.Fatal("invalid user id was accepted")
	}
}
