package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liveroute/liveroute/backend/internal/auth"
	"github.com/liveroute/liveroute/backend/internal/canonicaljson"
	"github.com/liveroute/liveroute/backend/internal/persistence"
)

const (
	developmentSessionCookie = "liveroute_dev_session"
	productionSessionCookie  = "__Host-liveroute_session"
	developmentBindingCookie = "liveroute_dev_oidc_binding"
	productionBindingCookie  = "__Secure-liveroute_oidc_binding"
	maxHTTPBodyBytes         = 262144
)

type HTTPAuthConfig struct {
	Store          *persistence.HTTPAuthStore
	Trips          *persistence.SavedTripStore
	GoogleVerifier *auth.GoogleVerifier
	AllowedOrigins []string
	SecureCookies  bool
	FrontendOrigin string
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
		if strings.HasPrefix(path, "/trips/") {
			if request.Method != http.MethodGet {
				handler.problem(writer, request, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", false, "method not allowed")
				return
			}
			handler.getTrip(writer, request, strings.TrimPrefix(path, "/trips/"))
			return
		}
		handler.problem(writer, request, http.StatusNotFound, "NOT_FOUND", false, "resource not found")
	}
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
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	nonce, err := auth.ExtractNonce(input.Credential)
	if err != nil {
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	identity, err := handler.config.GoogleVerifier.Verify(request.Context(), input.Credential, nonce)
	if err != nil {
		handler.problem(writer, request, http.StatusUnauthorized, "UNAUTHENTICATED", false, "authentication failed")
		return
	}
	session, err := handler.config.Store.CreateSessionForGoogle(request.Context(), nonce, bindingCookie.Value, persistence.GoogleIdentity{
		Issuer: identity.Issuer, Subject: identity.Subject, Email: identity.Email, EmailVerified: identity.EmailVerified, DisplayName: identity.DisplayName,
	}, input.DefaultTimeZoneName)
	if errors.Is(err, persistence.ErrNonceNotFound) || errors.Is(err, persistence.ErrAuthenticationInput) {
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
