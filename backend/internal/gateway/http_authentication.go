package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liveroute/liveroute/backend/internal/auth"
	"github.com/liveroute/liveroute/backend/internal/canonicaljson"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/place"
)

const (
	developmentSessionCookie = "liveroute_dev_session"
	productionSessionCookie  = "__Host-liveroute_session"
	developmentBindingCookie = "liveroute_dev_oidc_binding"
	productionBindingCookie  = "__Secure-liveroute_oidc_binding"
	maxHTTPBodyBytes         = 262144
)

type HTTPAuthConfig struct {
	Store             *persistence.HTTPAuthStore
	Trips             *persistence.SavedTripStore
	Places            *persistence.PlaceStore
	StartActivation   func(tripID, operationID string)
	StartDeactivation func(tripID, operationID string)
	GoogleVerifier    *auth.GoogleVerifier
	AllowedOrigins    []string
	SecureCookies     bool
	FrontendOrigin    string
}

type HTTPAuthHandler struct {
	config  HTTPAuthConfig
	origins map[string]struct{}
}

func NewHTTPAuthHandler(config HTTPAuthConfig) (*HTTPAuthHandler, error) {
	if config.Store == nil || len(config.AllowedOrigins) == 0 {
		return nil, errors.New("HTTP authentication store and allowed origins are required")
	}
	origins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || origin != parsed.Scheme+"://"+parsed.Host {
			return nil, errors.New("allowed origins must be exact scheme and host origins")
		}
		origins[origin] = struct{}{}
	}
	return &HTTPAuthHandler{config: config, origins: origins}, nil
}

func (handler *HTTPAuthHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := newHTTPID()
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodOptions {
		handler.handleOptions(writer, request)
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/api/v1")
	switch path {
	case "/auth/google/nonce":
		if request.Method != http.MethodPost {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.createNonce(writer, request)
	case "/auth/google":
		if request.Method != http.MethodPost {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.authenticateGoogle(writer, request)
	case "/session":
		if request.Method != http.MethodGet {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.getSession(writer, request)
	case "/trips":
		if request.Method == http.MethodPost {
			handler.createTrip(writer, request)
			return
		}
		if request.Method != http.MethodGet {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.listTrips(writer, request)
	case "/places/resolve":
		if request.Method != http.MethodPost {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.resolvePlace(writer, request)
	case "/places":
		if request.Method != http.MethodPost {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.createPlace(writer, request)
	case "/auth/logout":
		if request.Method != http.MethodPost {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.logout(writer, request)
	case "/auth/ws-ticket":
		if request.Method != http.MethodPost {
			handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			return
		}
		handler.createWebSocketTicket(writer, request)
	default:
		segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(segments) == 2 && segments[0] == "trips" {
			tripID := segments[1]
			switch request.Method {
			case http.MethodGet:
				handler.getTrip(writer, request, tripID)
			case http.MethodPatch:
				handler.updateTrip(writer, request, tripID)
			case http.MethodDelete:
				handler.deleteTrip(writer, request, tripID)
			default:
				handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			}
			return
		}
		if len(segments) == 3 && segments[0] == "trips" &&
			(segments[2] == "activate" || segments[2] == "deactivate") &&
			request.Method == http.MethodPost {
			if segments[2] == "activate" {
				handler.activateTrip(writer, request, segments[1])
			} else {
				handler.deactivateTrip(writer, request, segments[1])
			}
			return
		}
		if len(segments) == 3 && segments[0] == "trips" && segments[2] == "activities" && request.Method == http.MethodPost {
			handler.mutateTripActivity(writer, request, segments[1], "", "add")
			return
		}
		if len(segments) == 4 && segments[0] == "trips" && segments[2] == "activities" {
			switch request.Method {
			case http.MethodPatch:
				handler.mutateTripActivity(writer, request, segments[1], segments[3], "replace")
			case http.MethodDelete:
				handler.mutateTripActivity(writer, request, segments[1], segments[3], "delete")
			default:
				handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
			}
			return
		}
		handler.problem(writer, request, http.StatusNotFound, "NOT_FOUND", false, "resource not found")
	}
}

type activationCoordinateJSON struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type activateTripJSON struct {
	StartingLocation *activationCoordinateJSON `json:"starting_location"`
}

func (handler *HTTPAuthHandler) activateTrip(writer http.ResponseWriter, request *http.Request, tripID string) {
	session, key, revision, ifMatch, ok := handler.admitTripMutation(writer, request, tripID, true)
	if !ok {
		return
	}
	var body activateTripJSON
	if err := decodeJSON(request, &body); err != nil || body.StartingLocation == nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "starting_location is required")
		return
	}
	path := "/api/v1/trips/" + tripID + "/activate"
	_, digest, err := canonicalHTTPIdentity(http.MethodPost, path, ifMatch, body)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "activation request is invalid")
		return
	}
	result, err := handler.config.Trips.Activate(request.Context(), persistence.ActivateSavedTripRequest{
		UserID: session.User.ID, TripID: tripID, IdempotencyKey: key,
		ExpectedRevision: revision, RequestDigest: digest,
		StartingLatitude: body.StartingLocation.Latitude, StartingLongitude: body.StartingLocation.Longitude,
	})
	if handler.handleTripMutationError(writer, request, err) {
		return
	}
	if handler.config.StartActivation != nil &&
		(!result.Duplicate || result.Transition.Operation.State == "pending") {
		handler.config.StartActivation(tripID, result.Transition.Operation.OperationID)
	}
	writer.Header().Set("ETag", `"trip-revision-`+result.Transition.Trip.TripRevision+`"`)
	handler.writeJSON(writer, http.StatusAccepted, result.Transition)
}

func (handler *HTTPAuthHandler) deactivateTrip(writer http.ResponseWriter, request *http.Request, tripID string) {
	session, key, revision, ifMatch, ok := handler.admitTripMutation(writer, request, tripID, false)
	if !ok {
		return
	}
	if request.Body != nil {
		limited, err := io.ReadAll(io.LimitReader(request.Body, 1))
		if err != nil || len(limited) != 0 {
			handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "deactivation request body is not allowed")
			return
		}
	}
	path := "/api/v1/trips/" + tripID + "/deactivate"
	_, digest, err := canonicalHTTPIdentity(http.MethodPost, path, ifMatch, nil)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "deactivation request is invalid")
		return
	}
	result, err := handler.config.Trips.Deactivate(request.Context(), persistence.DeactivateSavedTripRequest{
		UserID: session.User.ID, TripID: tripID, IdempotencyKey: key,
		ExpectedRevision: revision, RequestDigest: digest,
	})
	if handler.handleTripMutationError(writer, request, err) {
		return
	}
	if handler.config.StartDeactivation != nil &&
		(!result.Duplicate || result.Transition.Operation.State == "pending") {
		handler.config.StartDeactivation(tripID, result.Transition.Operation.OperationID)
	}
	writer.Header().Set("ETag", `"trip-revision-`+result.Transition.Trip.TripRevision+`"`)
	handler.writeJSON(writer, http.StatusAccepted, result.Transition)
}

func (handler *HTTPAuthHandler) mutateTripActivity(writer http.ResponseWriter, request *http.Request, tripID, activityID, kind string) {
	requireJSON := kind != "delete"
	session, key, revision, ifMatch, ok := handler.admitTripMutation(writer, request, tripID, requireJSON)
	if !ok {
		return
	}
	if kind != "add" && !validHTTPUUID(activityID) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "activity id is invalid")
		return
	}
	path := "/api/v1/trips/" + tripID + "/activities"
	method := http.MethodPost
	var activity *persistence.SavedActivityInput
	var identityBody any
	if kind != "delete" {
		var body persistence.SavedActivityInput
		if err := decodeJSON(request, &body); err != nil {
			handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "activity is invalid")
			return
		}
		activity, identityBody = &body, body
	} else {
		if request.Body != nil {
			limited, err := io.ReadAll(io.LimitReader(request.Body, 1))
			if err != nil || len(limited) != 0 {
				handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "DELETE request body is not allowed")
				return
			}
		}
	}
	if kind != "add" {
		path += "/" + activityID
		if kind == "replace" {
			method = http.MethodPatch
		} else {
			method = http.MethodDelete
		}
	}
	_, digest, err := canonicalHTTPIdentity(method, path, ifMatch, identityBody)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "activity mutation is invalid")
		return
	}
	mutation := persistence.SavedActivityMutationRequest{
		UserID: session.User.ID, TripID: tripID, ActivityID: activityID, IdempotencyKey: key,
		ExpectedRevision: revision, RequestDigest: digest, Activity: activity,
	}
	var result persistence.MutatedSavedTrip
	if kind == "add" {
		result, err = handler.config.Trips.AddActivity(request.Context(), mutation)
	} else if kind == "replace" {
		result, err = handler.config.Trips.ReplaceActivity(request.Context(), mutation)
	} else {
		result, err = handler.config.Trips.DeleteActivity(request.Context(), mutation)
	}
	if handler.handleTripMutationError(writer, request, err) {
		return
	}
	writer.Header().Set("ETag", `"trip-revision-`+result.Trip.TripRevision+`"`)
	status := http.StatusOK
	if kind == "add" {
		status = http.StatusCreated
	}
	handler.writeJSON(writer, status, result.Trip)
}

func (handler *HTTPAuthHandler) updateTrip(writer http.ResponseWriter, request *http.Request, tripID string) {
	session, key, revision, ifMatch, ok := handler.admitTripMutation(writer, request, tripID, true)
	if !ok {
		return
	}
	var body persistence.UpdateSavedTripRequest
	if err := decodeJSON(request, &body); err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip update is invalid")
		return
	}
	_, digest, err := canonicalHTTPIdentity(http.MethodPatch, "/api/v1/trips/"+tripID, ifMatch, body)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip update is invalid")
		return
	}
	body.UserID, body.TripID, body.IdempotencyKey = session.User.ID, tripID, key
	body.ExpectedRevision, body.RequestDigest = revision, digest
	result, err := handler.config.Trips.Update(request.Context(), body)
	if handler.handleTripMutationError(writer, request, err) {
		return
	}
	writer.Header().Set("ETag", `"trip-revision-`+result.Trip.TripRevision+`"`)
	handler.writeJSON(writer, http.StatusOK, result.Trip)
}

func (handler *HTTPAuthHandler) deleteTrip(writer http.ResponseWriter, request *http.Request, tripID string) {
	session, key, revision, ifMatch, ok := handler.admitTripMutation(writer, request, tripID, false)
	if !ok {
		return
	}
	if request.Body != nil {
		limited, err := io.ReadAll(io.LimitReader(request.Body, 1))
		if err != nil || len(limited) != 0 {
			handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "DELETE request body is not allowed")
			return
		}
	}
	_, digest, err := canonicalHTTPIdentity(http.MethodDelete, "/api/v1/trips/"+tripID, ifMatch, nil)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip deletion is invalid")
		return
	}
	_, err = handler.config.Trips.Delete(request.Context(), persistence.DeleteSavedTripRequest{
		UserID: session.User.ID, TripID: tripID, IdempotencyKey: key,
		ExpectedRevision: revision, RequestDigest: digest,
	})
	if handler.handleTripMutationError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPAuthHandler) admitTripMutation(writer http.ResponseWriter, request *http.Request, tripID string, requireJSON bool) (persistence.Session, string, uint64, string, bool) {
	if !handler.checkOrigin(writer, request) {
		return persistence.Session{}, "", 0, "", false
	}
	session, ok := handler.authenticateSession(writer, request, true)
	if !ok {
		return persistence.Session{}, "", 0, "", false
	}
	if handler.config.Trips == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip storage is not configured")
		return persistence.Session{}, "", 0, "", false
	}
	if !validHTTPUUID(tripID) || strings.Contains(tripID, "/") {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip id is invalid")
		return persistence.Session{}, "", 0, "", false
	}
	if request.URL.RawQuery != "" {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "query parameters are not allowed")
		return persistence.Session{}, "", 0, "", false
	}
	key := request.Header.Get("Idempotency-Key")
	if !validHTTPUUID(key) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "Idempotency-Key is invalid")
		return persistence.Session{}, "", 0, "", false
	}
	ifMatch := request.Header.Get("If-Match")
	if ifMatch == "" {
		handler.problem(writer, request, http.StatusPreconditionRequired, "PRECONDITION_REQUIRED", false, "If-Match is required")
		return persistence.Session{}, "", 0, "", false
	}
	revision, valid := parseTripETag(ifMatch)
	if !valid {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "If-Match is invalid")
		return persistence.Session{}, "", 0, "", false
	}
	if requireJSON {
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "Content-Type must be application/json")
			return persistence.Session{}, "", 0, "", false
		}
	}
	return session, key, revision, ifMatch, true
}

func (handler *HTTPAuthHandler) handleTripMutationError(writer http.ResponseWriter, request *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, persistence.ErrSavedTripInput):
		handler.problem(writer, request, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", false, "trip mutation is invalid")
	case errors.Is(err, persistence.ErrTripNotFound):
		handler.problem(writer, request, http.StatusNotFound, "NOT_FOUND", false, "trip not found")
	case errors.Is(err, persistence.ErrSavedActivityNotFound):
		handler.problem(writer, request, http.StatusNotFound, "NOT_FOUND", false, "activity not found")
	case errors.Is(err, persistence.ErrSavedTripNotInactive):
		handler.problem(writer, request, http.StatusConflict, "CONFLICT", false, "trip must be inactive")
	case errors.Is(err, persistence.ErrExecutionTripConflict):
		handler.problem(writer, request, http.StatusConflict, "CONFLICT", false, "another trip is already executing")
	case errors.Is(err, persistence.ErrActivationUnscheduled), errors.Is(err, persistence.ErrActivationOutsideDay):
		handler.problem(writer, request, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", false, "trip cannot be activated")
	case errors.Is(err, persistence.ErrSavedTripNotExecuting):
		handler.problem(writer, request, http.StatusConflict, "CONFLICT", false, "trip is not executing")
	case errors.Is(err, persistence.ErrLeaseHeld):
		handler.problem(writer, request, http.StatusConflict, "CONFLICT", true, "runtime lease is still held")
	case errors.Is(err, persistence.ErrTripRevisionStale):
		handler.problem(writer, request, http.StatusPreconditionFailed, "PRECONDITION_FAILED", false, "trip revision does not match")
	case errors.Is(err, persistence.ErrHTTPIdempotencyReused):
		handler.problem(writer, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", false, "Idempotency-Key was already used for a different request")
	case errors.Is(err, persistence.ErrHTTPMutationPending):
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip mutation is still pending")
	default:
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip mutation could not be saved")
	}
	return true
}

func parseTripETag(value string) (uint64, bool) {
	const prefix = `"trip-revision-`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	if raw == "" || len(raw) > 20 || len(raw) > 1 && raw[0] == '0' {
		return 0, false
	}
	result, err := strconv.ParseUint(raw, 10, 64)
	return result, err == nil
}

func (handler *HTTPAuthHandler) resolvePlace(writer http.ResponseWriter, request *http.Request) {
	session, idempotencyKey, ok := handler.admitPlaceMutation(writer, request)
	if !ok {
		return
	}
	var body struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if err := decodeJSON(request, &body); err != nil || body.Latitude == nil || body.Longitude == nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "place coordinate is invalid")
		return
	}
	coordinate := place.Coordinate{Latitude: *body.Latitude, Longitude: *body.Longitude}
	if !place.ValidCoordinate(coordinate) {
		handler.problem(writer, request, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", false, "place coordinate is invalid")
		return
	}
	identity, _, err := canonicalHTTPIdentity(http.MethodPost, "/api/v1/places/resolve", "", map[string]any{
		"latitude": *body.Latitude, "longitude": *body.Longitude,
	})
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "place coordinate is invalid")
		return
	}
	result, err := handler.config.Places.Resolve(request.Context(), persistence.ResolvePlaceInput{
		UserID: session.User.ID, IdempotencyKey: idempotencyKey, RequestIdentity: identity,
		RequestID:  writer.Header().Get("X-Request-ID"),
		Coordinate: coordinate,
	})
	if errors.Is(err, persistence.ErrHTTPIdempotencyReused) {
		handler.problem(writer, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", false, "Idempotency-Key was already used for a different request")
		return
	}
	if errors.Is(err, persistence.ErrHTTPMutationPending) {
		handler.problem(writer, request, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE", true, "place resolution outcome is unavailable; use a new key for another explicit attempt")
		return
	}
	if errors.Is(err, persistence.ErrPlaceInput) {
		handler.problem(writer, request, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", false, "place coordinate is invalid")
		return
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "place resolution could not be recorded")
		return
	}
	handler.writeStoredHTTPResult(writer, result)
}

func (handler *HTTPAuthHandler) createPlace(writer http.ResponseWriter, request *http.Request) {
	session, idempotencyKey, ok := handler.admitPlaceMutation(writer, request)
	if !ok {
		return
	}
	var body struct {
		ResolutionToken string `json:"resolution_token"`
	}
	if err := decodeJSON(request, &body); err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "place request is invalid")
		return
	}
	_, digest, err := canonicalHTTPIdentity(http.MethodPost, "/api/v1/places", "", map[string]any{"resolution_token": body.ResolutionToken})
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "place request is invalid")
		return
	}
	result, err := handler.config.Places.Create(request.Context(), persistence.CreatePlaceInput{
		UserID: session.User.ID, IdempotencyKey: idempotencyKey, RequestDigest: digest,
		ResolutionToken: body.ResolutionToken, RequestID: writer.Header().Get("X-Request-ID"),
	})
	if errors.Is(err, persistence.ErrHTTPIdempotencyReused) {
		handler.problem(writer, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", false, "Idempotency-Key was already used for a different request")
		return
	}
	if errors.Is(err, persistence.ErrHTTPMutationPending) {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "place creation is still pending")
		return
	}
	if errors.Is(err, persistence.ErrPlaceResolutionGone) {
		handler.problem(writer, request, http.StatusGone, "NOT_FOUND", false, "place resolution has expired or was consumed")
		return
	}
	if errors.Is(err, persistence.ErrPlaceInput) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "place request is invalid")
		return
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "place could not be saved")
		return
	}
	handler.writeStoredHTTPResult(writer, result)
}

func (handler *HTTPAuthHandler) admitPlaceMutation(writer http.ResponseWriter, request *http.Request) (persistence.Session, string, bool) {
	if !handler.checkOrigin(writer, request) {
		return persistence.Session{}, "", false
	}
	session, ok := handler.authenticateSession(writer, request, true)
	if !ok {
		return persistence.Session{}, "", false
	}
	if handler.config.Places == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE", true, "place resolution is not configured")
		return persistence.Session{}, "", false
	}
	if request.URL.RawQuery != "" {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "query parameters are not allowed")
		return persistence.Session{}, "", false
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if !validHTTPUUID(idempotencyKey) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "Idempotency-Key is invalid")
		return persistence.Session{}, "", false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "Content-Type must be application/json")
		return persistence.Session{}, "", false
	}
	return session, idempotencyKey, true
}

func canonicalHTTPIdentity(method, path, ifMatch string, body any) ([]byte, [sha256.Size]byte, error) {
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	raw, err := json.Marshal(struct {
		Method      string          `json:"method"`
		Path        string          `json:"path"`
		IfMatch     string          `json:"if_match"`
		ContentType string          `json:"content_type"`
		Body        json.RawMessage `json:"body"`
	}{Method: method, Path: path, IfMatch: ifMatch, ContentType: "application/json", Body: rawBody})
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	canonical, err := canonicaljson.Marshal(raw)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	return canonical, sha256.Sum256(canonical), nil
}

func (handler *HTTPAuthHandler) writeStoredHTTPResult(writer http.ResponseWriter, result persistence.PlaceHTTPResult) {
	writer.Header().Set("Content-Type", result.ContentType)
	writer.WriteHeader(result.Status)
	_, _ = writer.Write(result.Body)
}

func (handler *HTTPAuthHandler) createTrip(writer http.ResponseWriter, request *http.Request) {
	if !handler.checkOrigin(writer, request) {
		return
	}
	session, ok := handler.authenticateSession(writer, request, true)
	if !ok {
		return
	}
	if handler.config.Trips == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip storage is not configured")
		return
	}
	if request.URL.RawQuery != "" {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "query parameters are not allowed")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if !validHTTPUUID(idempotencyKey) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "Idempotency-Key is invalid")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "Content-Type must be application/json")
		return
	}
	var input persistence.CreateSavedTripRequest
	if err := decodeJSON(request, &input); err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip request is invalid")
		return
	}
	body, err := json.Marshal(input)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip request is invalid")
		return
	}
	digestInput, err := json.Marshal(struct {
		Method      string          `json:"method"`
		Path        string          `json:"path"`
		IfMatch     string          `json:"if_match"`
		ContentType string          `json:"content_type"`
		Body        json.RawMessage `json:"body"`
	}{Method: http.MethodPost, Path: "/api/v1/trips", ContentType: "application/json", Body: body})
	if err != nil {
		handler.problem(writer, request, http.StatusInternalServerError, "INTERNAL", false, "request identity could not be created")
		return
	}
	canonical, err := canonicaljson.Marshal(digestInput)
	if err != nil {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip request is invalid")
		return
	}
	input.UserID = session.User.ID
	input.IdempotencyKey = idempotencyKey
	input.RequestDigest = sha256.Sum256(canonical)
	created, err := handler.config.Trips.Create(request.Context(), input)
	if errors.Is(err, persistence.ErrSavedTripInput) {
		handler.problem(writer, request, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", false, "trip request is invalid")
		return
	}
	if errors.Is(err, persistence.ErrHTTPIdempotencyReused) {
		handler.problem(writer, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", false, "Idempotency-Key was already used for a different request")
		return
	}
	if errors.Is(err, persistence.ErrHTTPMutationPending) {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip creation is still pending")
		return
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip could not be saved")
		return
	}
	writer.Header().Set("ETag", `"trip-revision-`+created.Trip.TripRevision+`"`)
	handler.writeJSON(writer, http.StatusCreated, created.Trip)
}

func (handler *HTTPAuthHandler) createNonce(writer http.ResponseWriter, request *http.Request) {
	if !handler.checkOrigin(writer, request) {
		return
	}
	nonce, err := handler.config.Store.CreateLoginNonce(request.Context())
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "authentication is temporarily unavailable")
		return
	}
	http.SetCookie(writer, handler.bindingCookie(nonce.Binding, int(time.Until(nonce.ExpiresAt).Seconds())))
	handler.writeJSON(writer, http.StatusCreated, map[string]any{
		"nonce": nonce.Nonce, "expires_at_unix_ms": nonce.ExpiresAt.UnixMilli(),
	})
}

func (handler *HTTPAuthHandler) authenticateGoogle(writer http.ResponseWriter, request *http.Request) {
	if !handler.checkOrigin(writer, request) {
		return
	}
	if handler.config.GoogleVerifier == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "Google authentication is not configured")
		return
	}
	var input struct {
		Credential          string `json:"credential"`
		DefaultTimeZoneName string `json:"default_time_zone_name"`
	}
	if err := decodeJSON(request, &input); err != nil || strings.TrimSpace(input.Credential) == "" || !validHTTPTimeZone(input.DefaultTimeZoneName) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "login request is invalid")
		return
	}
	bindingCookie, err := request.Cookie(handler.bindingCookieName())
	if err != nil || bindingCookie.Value == "" {
		handler.logGoogleAuthenticationRejection(writer, "binding_cookie")
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	nonce, err := auth.ExtractNonce(input.Credential)
	if err != nil {
		handler.logGoogleAuthenticationRejection(writer, "credential_nonce")
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	identity, err := handler.config.GoogleVerifier.Verify(request.Context(), input.Credential, nonce)
	if err != nil {
		handler.logGoogleAuthenticationRejection(writer, "token_"+auth.GoogleTokenValidationStage(err))
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	session, err := handler.config.Store.CreateSessionForGoogle(request.Context(), nonce, bindingCookie.Value, persistence.GoogleIdentity{
		Issuer: identity.Issuer, Subject: identity.Subject, Email: identity.Email, EmailVerified: identity.EmailVerified, DisplayName: identity.DisplayName,
	}, input.DefaultTimeZoneName)
	if errors.Is(err, persistence.ErrNonceNotFound) || errors.Is(err, persistence.ErrAuthenticationInput) {
		stage := "nonce_binding"
		if errors.Is(err, persistence.ErrAuthenticationInput) {
			stage = "identity_input"
		}
		handler.logGoogleAuthenticationRejection(writer, stage)
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "authentication is temporarily unavailable")
		return
	}
	http.SetCookie(writer, handler.sessionCookie(session.Token, int(time.Until(session.AbsoluteExpiresAt).Seconds())))
	http.SetCookie(writer, handler.clearBindingCookie())
	handler.writeJSON(writer, http.StatusOK, sessionJSON(session))
}

func (handler *HTTPAuthHandler) logGoogleAuthenticationRejection(writer http.ResponseWriter, stage string) {
	slog.Warn("Google authentication rejected", "request_id", writer.Header().Get("X-Request-ID"), "stage", stage)
}

func (handler *HTTPAuthHandler) getSession(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") != "" && !handler.checkOrigin(writer, request) {
		return
	}
	session, ok := handler.authenticateSession(writer, request, false)
	if !ok {
		return
	}
	if session.Rotated {
		http.SetCookie(writer, handler.sessionCookie(session.Token, int(time.Until(session.AbsoluteExpiresAt).Seconds())))
		writer.Header().Set("X-LiveRoute-CSRF-Token", session.CSRFToken)
	}
	handler.writeJSON(writer, http.StatusOK, sessionJSON(session))
}

func (handler *HTTPAuthHandler) listTrips(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") != "" && !handler.checkOrigin(writer, request) {
		return
	}
	session, ok := handler.authenticateSession(writer, request, false)
	if !ok {
		return
	}
	if handler.config.Trips == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip storage is not configured")
		return
	}
	trips, err := handler.config.Trips.List(request.Context(), session.User.ID)
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trips are temporarily unavailable")
		return
	}
	handler.writeJSON(writer, http.StatusOK, trips)
}

func (handler *HTTPAuthHandler) getTrip(writer http.ResponseWriter, request *http.Request, tripID string) {
	if request.Header.Get("Origin") != "" && !handler.checkOrigin(writer, request) {
		return
	}
	session, ok := handler.authenticateSession(writer, request, false)
	if !ok {
		return
	}
	if handler.config.Trips == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip storage is not configured")
		return
	}
	if !validHTTPUUID(tripID) {
		handler.problem(writer, request, http.StatusBadRequest, "INVALID_ARGUMENT", false, "trip id is invalid")
		return
	}
	trip, err := handler.config.Trips.Get(request.Context(), session.User.ID, tripID)
	if errors.Is(err, persistence.ErrTripNotFound) {
		handler.problem(writer, request, http.StatusNotFound, "NOT_FOUND", false, "trip not found")
		return
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "trip is temporarily unavailable")
		return
	}
	writer.Header().Set("ETag", `"trip-revision-`+trip.TripRevision+`"`)
	handler.writeJSON(writer, http.StatusOK, trip)
}

func (handler *HTTPAuthHandler) createWebSocketTicket(writer http.ResponseWriter, request *http.Request) {
	if !handler.checkOrigin(writer, request) {
		return
	}
	session, ok := handler.authenticateSession(writer, request, true)
	if !ok {
		return
	}
	ticket, err := handler.config.Store.IssueWebSocketTicket(request.Context(), session)
	if errors.Is(err, persistence.ErrSessionNotFound) {
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "authentication is temporarily unavailable")
		return
	}
	handler.writeJSON(writer, http.StatusCreated, map[string]any{"ticket": ticket.Token, "expires_at_unix_ms": ticket.ExpiresAt.UnixMilli()})
}

func (handler *HTTPAuthHandler) logout(writer http.ResponseWriter, request *http.Request) {
	if !handler.checkOrigin(writer, request) {
		return
	}
	session, ok := handler.authenticateSession(writer, request, true)
	if !ok {
		return
	}
	if err := handler.config.Store.Logout(request.Context(), session); err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "logout is temporarily unavailable")
		return
	}
	http.SetCookie(writer, handler.clearSessionCookie())
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPAuthHandler) authenticateSession(writer http.ResponseWriter, request *http.Request, requireCSRF bool) (persistence.Session, bool) {
	cookie, err := request.Cookie(handler.sessionCookieName())
	if err != nil || cookie.Value == "" {
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication required")
		return persistence.Session{}, false
	}
	session, err := handler.config.Store.AuthenticateSession(request.Context(), cookie.Value)
	if errors.Is(err, persistence.ErrSessionNotFound) {
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication required")
		return persistence.Session{}, false
	}
	if err != nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "DURABILITY_UNAVAILABLE", true, "authentication is temporarily unavailable")
		return persistence.Session{}, false
	}
	if requireCSRF && !handler.config.Store.VerifyCSRF(session, request.Header.Get("X-CSRF-Token")) {
		handler.problem(writer, request, http.StatusForbidden, "PERMISSION_DENIED", false, "CSRF validation failed")
		return persistence.Session{}, false
	}
	if session.Rotated {
		http.SetCookie(writer, handler.sessionCookie(session.Token, int(time.Until(session.AbsoluteExpiresAt).Seconds())))
		writer.Header().Set("X-LiveRoute-CSRF-Token", session.CSRFToken)
	}
	return session, true
}

func (handler *HTTPAuthHandler) checkOrigin(writer http.ResponseWriter, request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if _, ok := handler.origins[origin]; !ok {
		handler.problem(writer, request, http.StatusForbidden, "PERMISSION_DENIED", false, "origin is not allowed")
		return false
	}
	if handler.config.FrontendOrigin != "" && origin == handler.config.FrontendOrigin {
		writer.Header().Set("Access-Control-Allow-Origin", origin)
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
		writer.Header().Add("Vary", "Origin")
	}
	return true
}

func (handler *HTTPAuthHandler) handleOptions(writer http.ResponseWriter, request *http.Request) {
	if !handler.checkOrigin(writer, request) {
		return
	}
	writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, If-Match, Idempotency-Key, X-CSRF-Token")
	writer.Header().Set("Access-Control-Expose-Headers", "ETag, X-Request-ID, X-LiveRoute-CSRF-Token")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPAuthHandler) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (handler *HTTPAuthHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool, detail string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type": "https://liveroute.invalid/problems/" + strings.ToLower(code), "title": detail, "status": status,
		"code": code, "request_id": writer.Header().Get("X-Request-ID"), "retryable": retryable,
	})
}

func decodeJSON(request *http.Request, value any) error {
	if request.ContentLength > maxHTTPBodyBytes {
		return errors.New("request body is too large")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxHTTPBodyBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request contains more than one JSON value")
	}
	return nil
}

func sessionJSON(session persistence.Session) map[string]any {
	user := map[string]any{"user_id": session.User.ID, "display_name": session.User.DisplayName, "default_time_zone_name": session.User.DefaultTimeZoneName}
	if session.User.Email != "" {
		user["email"] = session.User.Email
	}
	return map[string]any{"user": user, "csrf_token": session.CSRFToken, "idle_expires_at_unix_ms": session.IdleExpiresAt.UnixMilli(), "absolute_expires_at_unix_ms": session.AbsoluteExpiresAt.UnixMilli()}
}

func (handler *HTTPAuthHandler) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: handler.sessionCookieName(), Value: value, Path: "/", HttpOnly: true, Secure: handler.config.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func (handler *HTTPAuthHandler) clearSessionCookie() *http.Cookie {
	cookie := handler.sessionCookie("", -1)
	cookie.Expires = time.Unix(1, 0)
	return cookie
}

func (handler *HTTPAuthHandler) bindingCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: handler.bindingCookieName(), Value: value, Path: "/api/v1/auth/google", HttpOnly: true, Secure: handler.config.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func (handler *HTTPAuthHandler) clearBindingCookie() *http.Cookie {
	cookie := handler.bindingCookie("", -1)
	cookie.Expires = time.Unix(1, 0)
	return cookie
}

func (handler *HTTPAuthHandler) sessionCookieName() string {
	if handler.config.SecureCookies {
		return productionSessionCookie
	}
	return developmentSessionCookie
}

func (handler *HTTPAuthHandler) bindingCookieName() string {
	if handler.config.SecureCookies {
		return productionBindingCookie
	}
	return developmentBindingCookie
}

func validHTTPTimeZone(value string) bool {
	if len(value) == 0 || len(value) > 64 || !strings.Contains(value, "/") {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

func validHTTPUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || strings.ToLower(value) != value {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return value[14] >= '1' && value[14] <= '5' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}

func newHTTPID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
