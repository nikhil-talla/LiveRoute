package place

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FailureCode string

const (
	FailureProviderUnavailable FailureCode = "PROVIDER_UNAVAILABLE"
	FailureResourceExhausted   FailureCode = "RESOURCE_EXHAUSTED"
	FailurePlaceNotResolved    FailureCode = "PLACE_NOT_RESOLVED"
)

type ResolveError struct {
	Code      FailureCode
	Retryable bool
	Auth      bool
}

func (err *ResolveError) Error() string { return string(err.Code) }

type Coordinate struct {
	Latitude  float64
	Longitude float64
}

type Candidate struct {
	Latitude         float64
	Longitude        float64
	FormattedAddress string
	DisplayName      string
	TimeZoneName     string
}

type Provider interface {
	Resolve(context.Context, string, Coordinate, func() error) (Candidate, error)
}

type MapboxConfig struct {
	Endpoint           string
	Token              string
	Client             *http.Client
	TimeZones          *TimeZoneResolver
	MaxResponseBytes   int64
	GlobalConcurrency  int
	PerUserConcurrency int
	AttemptsPerMinute  int
	Now                func() time.Time
}

type MapboxResolver struct {
	config MapboxConfig
	global chan struct{}
	mu     sync.Mutex
	users  map[string]*userLimit
	ready  bool
}

type userLimit struct {
	inFlight int
	attempts []time.Time
}

func NewMapboxResolver(config MapboxConfig) (*MapboxResolver, error) {
	if parsed, err := url.Parse(config.Endpoint); err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, errors.New("Mapbox endpoint is invalid")
	}
	if strings.TrimSpace(config.Token) == "" || config.Client == nil || config.TimeZones == nil || config.MaxResponseBytes <= 0 ||
		config.GlobalConcurrency <= 0 || config.PerUserConcurrency != 1 || config.AttemptsPerMinute <= 0 {
		return nil, errors.New("Mapbox resolver configuration is incomplete")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &MapboxResolver{config: config, global: make(chan struct{}, config.GlobalConcurrency), users: make(map[string]*userLimit), ready: true}, nil
}

func (resolver *MapboxResolver) Ready() bool {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.ready
}

func (resolver *MapboxResolver) Resolve(ctx context.Context, userID string, coordinate Coordinate, beforeRequest func() error) (Candidate, error) {
	if ctx == nil || userID == "" || !ValidCoordinate(coordinate) {
		return Candidate{}, &ResolveError{Code: FailurePlaceNotResolved}
	}
	if !resolver.admitUser(userID) {
		return Candidate{}, &ResolveError{Code: FailureResourceExhausted, Retryable: true}
	}
	defer resolver.releaseUser(userID)
	select {
	case resolver.global <- struct{}{}:
		defer func() { <-resolver.global }()
	default:
		return Candidate{}, &ResolveError{Code: FailureResourceExhausted, Retryable: true}
	}
	if beforeRequest == nil || beforeRequest() != nil {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	}

	endpoint, err := url.Parse(resolver.config.Endpoint)
	if err != nil {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable}
	}
	query := endpoint.Query()
	query.Set("longitude", strconv.FormatFloat(coordinate.Longitude, 'f', -1, 64))
	query.Set("latitude", strconv.FormatFloat(coordinate.Latitude, 'f', -1, 64))
	query.Set("permanent", "true")
	query.Set("country", "US")
	query.Set("access_token", resolver.config.Token)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable}
	}
	response, err := resolver.config.Client.Do(request)
	if err != nil {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	}
	defer response.Body.Close()
	if mapped := resolver.mapStatus(response.StatusCode); mapped != nil {
		return Candidate{}, mapped
	}
	limited := io.LimitReader(response.Body, resolver.config.MaxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil || int64(len(raw)) > resolver.config.MaxResponseBytes {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	}
	var payload struct {
		Type     string `json:"type"`
		Features []struct {
			Type     string `json:"type"`
			Geometry struct {
				Type        string    `json:"type"`
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				FullAddress string `json:"full_address"`
				Context     struct {
					Country struct {
						CountryCode string `json:"country_code"`
					} `json:"country"`
				} `json:"context"`
			} `json:"properties"`
		} `json:"features"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decodeErr := decoder.Decode(&payload)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if decodeErr != nil || trailingErr != io.EOF || payload.Type != "FeatureCollection" || len(payload.Features) == 0 {
		if decodeErr == nil && trailingErr == io.EOF && payload.Type == "FeatureCollection" && len(payload.Features) == 0 {
			return Candidate{}, &ResolveError{Code: FailurePlaceNotResolved}
		}
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	}
	feature := payload.Features[0]
	if feature.Type != "Feature" || feature.Geometry.Type != "Point" || len(feature.Geometry.Coordinates) < 2 ||
		!finiteCoordinate(feature.Geometry.Coordinates[1], feature.Geometry.Coordinates[0]) {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	}
	if !strings.EqualFold(feature.Properties.Context.Country.CountryCode, "US") {
		return Candidate{}, &ResolveError{Code: FailurePlaceNotResolved}
	}
	latitude, longitude := feature.Geometry.Coordinates[1], feature.Geometry.Coordinates[0]
	zone, ok := resolver.config.TimeZones.Resolve(latitude, longitude)
	if !ok {
		return Candidate{}, &ResolveError{Code: FailurePlaceNotResolved}
	}
	address := strings.TrimSpace(feature.Properties.FullAddress)
	if len(address) > 500 {
		return Candidate{}, &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	}
	display := address
	if display == "" {
		display = fmt.Sprintf("%.6f, %.6f", latitude, longitude)
	}
	return Candidate{Latitude: latitude, Longitude: longitude, FormattedAddress: address, DisplayName: display, TimeZoneName: zone}, nil
}

func ValidCoordinate(value Coordinate) bool {
	return finiteCoordinate(value.Latitude, value.Longitude)
}

func (resolver *MapboxResolver) mapStatus(status int) *ResolveError {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
		return &ResolveError{Code: FailurePlaceNotResolved}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		resolver.mu.Lock()
		resolver.ready = false
		resolver.mu.Unlock()
		return &ResolveError{Code: FailureProviderUnavailable, Auth: true}
	case status == http.StatusTooManyRequests || status >= 500:
		return &ResolveError{Code: FailureProviderUnavailable, Retryable: true}
	default:
		return &ResolveError{Code: FailureProviderUnavailable}
	}
}

func (resolver *MapboxResolver) admitUser(userID string) bool {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	now := resolver.config.Now()
	limit := resolver.users[userID]
	if limit == nil {
		limit = &userLimit{}
		resolver.users[userID] = limit
	}
	cutoff := now.Add(-time.Minute)
	kept := limit.attempts[:0]
	for _, attempt := range limit.attempts {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	limit.attempts = kept
	if limit.inFlight >= resolver.config.PerUserConcurrency || len(limit.attempts) >= resolver.config.AttemptsPerMinute {
		return false
	}
	limit.inFlight++
	limit.attempts = append(limit.attempts, now)
	return true
}

func (resolver *MapboxResolver) releaseUser(userID string) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if limit := resolver.users[userID]; limit != nil && limit.inFlight > 0 {
		limit.inFlight--
	}
}
