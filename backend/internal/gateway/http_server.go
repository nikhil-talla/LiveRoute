package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
)

type HTTPServer struct {
	server  *http.Server
	handler *Handler
}

func NewHTTPServer(
	address string,
	websocketHandler *Handler,
	healthHandler *HealthHandler,
) (*HTTPServer, error) {
	if address == "" || websocketHandler == nil || healthHandler == nil {
		return nil, errors.New("HTTP server address and handlers are required")
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", websocketHandler)
	mux.HandleFunc("/healthz", healthHandler.Healthz)
	mux.HandleFunc("/readyz", healthHandler.Readyz)
	return &HTTPServer{
		server:  &http.Server{Addr: address, Handler: mux},
		handler: websocketHandler,
	}, nil
}

// Serve runs the HTTP event loop on the supplied listener and closes WebSocket
// sessions with the gateway's retryable restart code when ctx is cancelled.
func (server *HTTPServer) Serve(ctx context.Context, listener net.Listener) error {
	if ctx == nil || listener == nil {
		return errors.New("HTTP server context and listener are required")
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.server.Serve(listener) }()
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownResult := server.handler.Shutdown(ctx)
		if err := server.server.Shutdown(ctx); err != nil && shutdownResult == nil {
			shutdownResult = err
		}
		if shutdownResult != nil {
			return shutdownResult
		}
		return ctx.Err()
	}
}
