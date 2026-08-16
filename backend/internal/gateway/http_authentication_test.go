package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

func TestHTTPAuthHandlerRejectsWildcardAndPathOrigins(t *testing.T) {
	for _, origin := range []string{"*", "http://localhost:5173/path", "http://localhost:5173?query=1"} {
		_, err := NewHTTPAuthHandler(HTTPAuthConfig{
			Store: &persistence.HTTPAuthStore{}, AllowedOrigins: []string{origin},
		})
		if err == nil {
			t.Fatalf("invalid origin accepted: %q", origin)
		}
	}
}

func TestHTTPAuthHandlerAddsLocalCORSForAllowedOrigin(t *testing.T) {
	handler, err := NewHTTPAuthHandler(HTTPAuthConfig{
		Store: &persistence.HTTPAuthStore{}, AllowedOrigins: []string{"http://localhost:5173"}, FrontendOrigin: "http://localhost:5173",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/v1/session", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("missing local CORS headers: %v", response.Header())
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want unauthorized", response.Code)
	}
}

func TestHTTPAuthHandlerProtectsTripListWithSessionAuthentication(t *testing.T) {
	handler, err := NewHTTPAuthHandler(HTTPAuthConfig{
		Store: &persistence.HTTPAuthStore{}, Trips: &persistence.SavedTripStore{}, AllowedOrigins: []string{"http://localhost:5173"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/v1/trips", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want unauthorized", response.Code)
	}
}

func TestParseTripETagIsExact(t *testing.T) {
	for raw, expected := range map[string]uint64{
		`"trip-revision-0"`:                    0,
		`"trip-revision-1"`:                    1,
		`"trip-revision-18446744073709551615"`: ^uint64(0),
	} {
		actual, ok := parseTripETag(raw)
		if !ok || actual != expected {
			t.Fatalf("parseTripETag(%q)=(%d,%t), want (%d,true)", raw, actual, ok, expected)
		}
	}
	for _, raw := range []string{
		"", "trip-revision-1", `"trip-revision-01"`, `"trip-revision--1"`,
		`"trip-revision-18446744073709551616"`, `W/"trip-revision-1"`,
	} {
		if _, ok := parseTripETag(raw); ok {
			t.Fatalf("invalid ETag accepted: %q", raw)
		}
	}
}
