package http

import (
	"log/slog"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 1 << 20
)

// NewServer builds the API HTTP server with conservative transport limits.
//
// ReadTimeout and WriteTimeout are intentionally left unset: HookFlow will
// eventually carry webhook traffic whose useful request duration is not known
// yet, so a global deadline now would be an unapproved constraint.
func NewServer(addr string, handler http.Handler, logger *slog.Logger) *http.Server {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	if logger != nil {
		server.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	}
	return server
}
