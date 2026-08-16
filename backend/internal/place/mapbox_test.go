package place

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMapboxResolverUsesPermanentReverseRequestAndOfflineTimezone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("permanent") != "true" || query.Get("country") != "US" || query.Get("access_token") != "server-token" ||
			query.Get("latitude") != "41.824" || query.Get("longitude") != "-71.4128" {
			t.Errorf("unexpected query: %v", query)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-71.41,41.82]},"properties":{"full_address":"1 Main St, Providence, RI 02903, United States","context":{"country":{"country_code":"US"}}}}]}`))
	}))
	defer server.Close()
	resolver := newTestMapboxResolver(t, server.URL)
	candidate, err := resolver.Resolve(context.Background(), "user", Coordinate{Latitude: 41.824, Longitude: -71.4128}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Latitude != 41.82 || candidate.Longitude != -71.41 || candidate.TimeZoneName != "America/New_York" ||
		candidate.DisplayName != "1 Main St, Providence, RI 02903, United States" {
		t.Fatalf("candidate=%+v", candidate)
	}
}

func TestMapboxResolverMapsFailuresWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		status    int
		code      FailureCode
		retryable bool
		ready     bool
	}{
		{http.StatusBadRequest, FailurePlaceNotResolved, false, true},
		{http.StatusTooManyRequests, FailureProviderUnavailable, true, true},
		{http.StatusInternalServerError, FailureProviderUnavailable, true, true},
		{http.StatusUnauthorized, FailureProviderUnavailable, false, false},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls++
				writer.WriteHeader(test.status)
			}))
			defer server.Close()
			resolver := newTestMapboxResolver(t, server.URL)
			_, err := resolver.Resolve(context.Background(), "user", Coordinate{Latitude: 41, Longitude: -71}, func() error { return nil })
			var resolveErr *ResolveError
			if !errors.As(err, &resolveErr) || resolveErr.Code != test.code || resolveErr.Retryable != test.retryable {
				t.Fatalf("error=%#v", err)
			}
			if calls != 1 || resolver.Ready() != test.ready {
				t.Fatalf("calls=%d ready=%t", calls, resolver.Ready())
			}
		})
	}
}

func TestMapboxResolverFallsBackToCoordinateLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-71.41,41.82]},"properties":{"context":{"country":{"country_code":"US"}}}}]}`))
	}))
	defer server.Close()
	resolver := newTestMapboxResolver(t, server.URL)
	candidate, err := resolver.Resolve(context.Background(), "user", Coordinate{Latitude: 41, Longitude: -71}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if candidate.DisplayName != "41.820000, -71.410000" || candidate.FormattedAddress != "" {
		t.Fatalf("candidate=%+v", candidate)
	}
}

func TestMapboxResolverEnforcesFiveAttemptsPerMinute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	resolver := newTestMapboxResolver(t, server.URL)
	for index := 0; index < 5; index++ {
		_, _ = resolver.Resolve(context.Background(), "user", Coordinate{Latitude: 41, Longitude: -71}, func() error { return nil })
	}
	_, err := resolver.Resolve(context.Background(), "user", Coordinate{Latitude: 41, Longitude: -71}, func() error { return nil })
	var resolveErr *ResolveError
	if !errors.As(err, &resolveErr) || resolveErr.Code != FailureResourceExhausted || !resolveErr.Retryable {
		t.Fatalf("sixth error=%#v", err)
	}
}

func TestMapboxResolverRejectsOversizeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 129)))
	}))
	defer server.Close()
	resolver := newTestMapboxResolver(t, server.URL)
	resolver.config.MaxResponseBytes = 128
	_, err := resolver.Resolve(context.Background(), "user", Coordinate{Latitude: 41, Longitude: -71}, func() error { return nil })
	var resolveErr *ResolveError
	if !errors.As(err, &resolveErr) || resolveErr.Code != FailureProviderUnavailable || !resolveErr.Retryable {
		t.Fatalf("error=%#v", err)
	}
}

func newTestMapboxResolver(t *testing.T, endpoint string) *MapboxResolver {
	t.Helper()
	zone := &TimeZoneResolver{zones: []zoneGeometry{{name: "America/New_York", polygons: []polygon{{
		rings:  []ring{{{lon: -80, lat: 35}, {lon: -65, lat: 35}, {lon: -65, lat: 48}, {lon: -80, lat: 48}, {lon: -80, lat: 35}}},
		minLon: -80, maxLon: -65, minLat: 35, maxLat: 48,
	}}}}}
	resolver, err := NewMapboxResolver(MapboxConfig{
		Endpoint: endpoint, Token: "server-token", Client: &http.Client{Timeout: time.Second}, TimeZones: zone,
		MaxResponseBytes: 262144, GlobalConcurrency: 16, PerUserConcurrency: 1, AttemptsPerMinute: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}
