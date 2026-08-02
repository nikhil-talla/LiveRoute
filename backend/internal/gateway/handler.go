package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	statusOK                = "OK"
	statusResourceExhausted = "RESOURCE_EXHAUSTED"
	protocolVersion         = "liveroute.v1"
	maxResyncIDsDefault     = 128
)

type Authenticator interface {
	Authenticate(context.Context, string) (string, error)
}

type AuthenticateFunc func(context.Context, string) (string, error)

func (f AuthenticateFunc) Authenticate(ctx context.Context, token string) (string, error) {
	return f(ctx, token)
}

type AuthenticatedMessage struct {
	UserID  string
	Message map[string]any
	Raw     []byte
	Sink    MessageSink
}

type MessageSink interface {
	PublishServerEnvelope([]byte) error
}

var ErrConnectionCapacity = errors.New("connection capacity exhausted")

type Config struct {
	Validator                   *EnvelopeValidator
	Authenticator               Authenticator
	BackendInstanceID           string
	FrameLimit                  int64
	DecodedMessageLimit         int64
	InboundQueueCapacity        int
	OutboundQueueCapacity       int
	HeartbeatInterval           time.Duration
	IdleTimeout                 time.Duration
	AuthenticationTimeout       time.Duration
	AllowOriginlessLocalClients bool
	AllowedOrigins              []string
	MaxOutstandingResyncIDs     int
	OnMessage                   func(context.Context, AuthenticatedMessage)
	Now                         func() time.Time
}

func (c Config) validate() error {
	if c.Validator == nil || c.Authenticator == nil {
		return fmt.Errorf("validator and authenticator are required")
	}
	if !canonicalUUID(c.BackendInstanceID) {
		return fmt.Errorf("backend instance id must be a canonical UUID")
	}
	if c.FrameLimit <= 0 || c.FrameLimit > 262144 || c.DecodedMessageLimit <= 0 || c.DecodedMessageLimit > 262144 {
		return fmt.Errorf("websocket limits must be positive and no larger than 262144")
	}
	if c.InboundQueueCapacity <= 0 || c.OutboundQueueCapacity <= 0 || c.HeartbeatInterval <= 0 || c.IdleTimeout <= 0 || c.AuthenticationTimeout <= 0 {
		return fmt.Errorf("websocket capacities and timeouts must be positive")
	}
	if c.MaxOutstandingResyncIDs <= 0 || c.MaxOutstandingResyncIDs > maxResyncIDsDefault {
		return fmt.Errorf("max outstanding resynchronization ids must be between 1 and %d", maxResyncIDsDefault)
	}
	return nil
}

type Handler struct {
	config          Config
	sessionsMu      sync.Mutex
	sessions        map[*session]struct{}
	onSessionClosed func(MessageSink)
	shuttingDown    bool
}

func NewHandler(config Config) (*Handler, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Handler{config: config, sessions: make(map[*session]struct{})}, nil
}

// SetOnMessage configures the authenticated-message callback before ServeHTTP
// accepts connections. It exists so the runnable backend can assemble its
// bounded trip admission layer after constructing the handler.
func (h *Handler) SetOnMessage(callback func(context.Context, AuthenticatedMessage)) {
	if h != nil {
		h.config.OnMessage = callback
	}
}

// SetOnSessionClosed configures cleanup for transport-owned session indexes.
// It must be called before ServeHTTP starts accepting connections.
func (h *Handler) SetOnSessionClosed(callback func(MessageSink)) {
	if h != nil {
		h.onSessionClosed = callback
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.sessionsMu.Lock()
	shuttingDown := h.shuttingDown
	h.sessionsMu.Unlock()
	if shuttingDown {
		http.Error(writer, "backend is shutting down", http.StatusServiceUnavailable)
		return
	}
	if !h.originAllowed(request) {
		connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err == nil {
			_ = connection.Close(websocket.StatusPolicyViolation, "origin denied")
		}
		return
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	connection.SetReadLimit(h.config.FrameLimit)
	session := newSession(h.config, connection)
	if !h.addSession(session) {
		session.close(websocket.StatusGoingAway, "backend is shutting down")
		return
	}
	defer h.removeSession(session)
	session.run(request.Context())
}

func (h *Handler) addSession(session *session) bool {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if h.shuttingDown {
		return false
	}
	h.sessions[session] = struct{}{}
	return true
}

func (h *Handler) removeSession(session *session) {
	h.sessionsMu.Lock()
	delete(h.sessions, session)
	callback := h.onSessionClosed
	h.sessionsMu.Unlock()
	if callback != nil {
		callback(session)
	}
}

// Shutdown stops new WebSocket upgrades and closes existing sessions with the
// retryable restart code. It does not own the net/http listener; the caller
// should shut that down after this method returns.
func (h *Handler) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown context is required")
	}
	h.sessionsMu.Lock()
	h.shuttingDown = true
	sessions := make([]*session, 0, len(h.sessions))
	for session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessionsMu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, session := range sessions {
			session.close(websocket.StatusGoingAway, "backend restart")
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) originAllowed(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin != "" {
		for _, allowed := range h.config.AllowedOrigins {
			if origin == allowed {
				return true
			}
		}
		return false
	}
	if !h.config.AllowOriginlessLocalClients {
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type session struct {
	config       Config
	connection   *websocket.Conn
	outbound     chan []byte
	inbound      chan AuthenticatedMessage
	closed       chan struct{}
	closeOnce    sync.Once
	lastActivity atomic.Int64
	userID       string
}

func newSession(config Config, connection *websocket.Conn) *session {
	session := &session{
		config: config, connection: connection,
		outbound: make(chan []byte, config.OutboundQueueCapacity),
		inbound:  make(chan AuthenticatedMessage, config.InboundQueueCapacity),
		closed:   make(chan struct{}),
	}
	session.lastActivity.Store(config.Now().UnixNano())
	return session
}

func (s *session) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go s.writeLoop(ctx)
	go s.heartbeatLoop(ctx)
	go s.messageLoop(ctx)

	authenticated := false
	for {
		readContext := ctx
		timeout := s.config.IdleTimeout
		if !authenticated {
			timeout = s.config.AuthenticationTimeout
		}
		readContext, readCancel := context.WithTimeout(ctx, timeout)
		messageType, raw, err := s.connection.Read(readContext)
		readCancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if authenticated {
					s.close(websocket.StatusCode(4008), "idle timeout")
				} else {
					s.close(websocket.StatusCode(4008), "authentication timeout")
				}
			} else {
				s.close(readCloseCode(err), "connection read failed")
			}
			return
		}
		s.lastActivity.Store(s.config.Now().UnixNano())
		if messageType != websocket.MessageText {
			s.close(websocket.StatusProtocolError, "text frames required")
			return
		}
		if int64(len(raw)) > s.config.DecodedMessageLimit {
			s.close(websocket.StatusMessageTooBig, "message too large")
			return
		}
		envelope, err := s.config.Validator.ValidateClient(raw)
		if err != nil {
			s.close(websocket.StatusProtocolError, "invalid message")
			return
		}
		kind, _ := envelope["kind"].(string)
		if kind == "ping" || kind == "pong" {
			if kind == "ping" {
				s.sendConnection("pong", statusOK, true, envelope["message_id"].(string), pingResponse(s.config.Now(), envelope["payload"].(map[string]any)))
			}
			continue
		}
		if !authenticated {
			if kind != "authenticate" {
				s.close(websocket.StatusCode(4001), "authentication required")
				return
			}
			payload, ok := envelope["payload"].(map[string]any)
			if !ok {
				s.close(websocket.StatusCode(4001), "authentication failed")
				return
			}
			token, ok := payload["token"].(string)
			if !ok {
				s.close(websocket.StatusCode(4001), "authentication failed")
				return
			}
			authContext, authCancel := context.WithTimeout(ctx, s.config.AuthenticationTimeout)
			userID, err := s.config.Authenticator.Authenticate(authContext, token)
			authCancel()
			if err != nil || !canonicalUUID(userID) {
				s.close(websocket.StatusCode(4001), "authentication failed")
				return
			}
			s.userID = userID
			authenticated = true
			s.sendConnection("connection_ready", statusOK, false, "", map[string]any{
				"user_id": userID, "backend_instance_id": s.config.BackendInstanceID,
				"heartbeat_interval_ms":      s.config.HeartbeatInterval.Milliseconds(),
				"idle_timeout_ms":            s.config.IdleTimeout.Milliseconds(),
				"max_frame_bytes":            s.config.FrameLimit,
				"max_outstanding_resync_ids": s.config.MaxOutstandingResyncIDs,
			})
			continue
		}
		if kind == "authenticate" {
			s.close(websocket.StatusProtocolError, "duplicate authentication")
			return
		}
		if s.config.OnMessage != nil {
			message := AuthenticatedMessage{
				UserID: s.userID, Message: envelope, Raw: append([]byte(nil), raw...), Sink: s,
			}
			select {
			case s.inbound <- message:
			default:
				s.sendConnection("error", statusResourceExhausted, true, "", map[string]any{
					"safe_message": "connection capacity exhausted",
				})
				s.close(websocket.StatusCode(4029), "connection capacity exhausted")
				return
			}
		}
	}
}

func (s *session) messageLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case message := <-s.inbound:
			s.config.OnMessage(ctx, message)
		}
	}
}

func (s *session) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case raw := <-s.outbound:
			writeContext, cancel := context.WithTimeout(ctx, s.config.IdleTimeout)
			err := s.connection.Write(writeContext, websocket.MessageText, raw)
			cancel()
			if err != nil {
				s.close(websocket.StatusInternalError, "connection write failed")
				return
			}
		}
	}
}

func (s *session) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case now := <-ticker.C:
			last := time.Unix(0, s.lastActivity.Load())
			if now.Sub(last) >= s.config.IdleTimeout {
				s.close(websocket.StatusCode(4008), "idle timeout")
				return
			}
			s.sendConnection("ping", statusOK, true, "", map[string]any{
				"nonce": newUUID(), "sent_at_unix_ms": now.UnixMilli(),
			})
		}
	}
}

func (s *session) sendConnection(kind, status string, retryable bool, inReply string, payload map[string]any) {
	envelope := map[string]any{
		"protocol_version": protocolVersion, "server_message_id": newUUID(),
		"kind": kind, "status": status, "retryable": retryable, "payload": payload,
	}
	if inReply != "" {
		envelope["in_reply_to_message_id"] = inReply
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	if err := s.PublishServerEnvelope(raw); err != nil {
		if errors.Is(err, ErrConnectionCapacity) {
			s.close(websocket.StatusCode(4029), "connection capacity exhausted")
		} else {
			s.close(websocket.StatusInternalError, "invalid server message")
		}
	}
}

func (s *session) PublishServerEnvelope(raw []byte) error {
	if err := s.config.Validator.ValidateServer(raw); err != nil {
		return fmt.Errorf("invalid server envelope: %w", err)
	}
	if !s.enqueue(raw) {
		return ErrConnectionCapacity
	}
	return nil
}

func (s *session) enqueue(raw []byte) bool {
	select {
	case <-s.closed:
		return false
	default:
	}
	select {
	case s.outbound <- raw:
		return true
	default:
		s.close(websocket.StatusCode(4029), "connection capacity exhausted")
		return false
	}
}

func (s *session) close(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.connection.Close(code, reason)
	})
}

func pingResponse(now time.Time, payload map[string]any) map[string]any {
	nonce, _ := payload["nonce"].(string)
	return map[string]any{"nonce": nonce, "received_at_unix_ms": now.UnixMilli()}
}

func readCloseCode(err error) websocket.StatusCode {
	if code := websocket.CloseStatus(err); code >= 0 {
		return code
	}
	return websocket.StatusInternalError
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	var decoded [16]byte
	if _, err := hex.Decode(decoded[:], []byte(strings.ReplaceAll(value, "-", ""))); err != nil {
		return false
	}
	return decoded[6]&0xf0 >= 0x10 && decoded[6]&0xf0 <= 0x50 && decoded[8]&0xc0 == 0x80 && strings.ToLower(value) == value
}

func newUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// NewUUID exposes the gateway's canonical UUID generator to bounded backend
// adapters that create non-durable event identities.
func NewUUID() string { return newUUID() }
