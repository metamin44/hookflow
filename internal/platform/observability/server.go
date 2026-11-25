package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server exposes process-local metrics. It has no entity-ID labels.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// StartServer binds and serves metrics. A bind failure is returned rather than
// silently disabling observability.
func StartServer(ctx context.Context, address string, metrics *Metrics, logger *slog.Logger) (*Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen metrics: %w", err)
	}
	server := &http.Server{
		Handler: metrics.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	result := &Server{http: server, ln: listener}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "error_class", "internal")
		}
	}()
	//nolint:gosec // Shutdown must outlive the canceled startup/run context.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("metrics server listening", "addr", listener.Addr().String())
	return result, nil
}

// Close gracefully stops the metrics listener.
func (s *Server) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}
