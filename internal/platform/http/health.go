// Package http contains the API HTTP server and health endpoints.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

const (
	livePath  = "/health/live"
	readyPath = "/health/ready"

	readinessTimeout = 2 * time.Second
)

const (
	liveBody             = "{\"status\":\"ok\"}\n"
	readyBody            = "{\"status\":\"ready\"}\n"
	unavailableBody      = "{\"status\":\"unavailable\"}\n"
	methodNotAllowedBody = "{\"error\":\"method_not_allowed\"}\n"
)

var (
	passwordPattern      = regexp.MustCompile(`(?i)(password\s*=\s*)[^\s&;]+`)
	dsnCredentialPattern = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:\s/]+:)[^@\s]+@`)
	authorizationPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*)[^\r\n]+`)
)

// Pinger is the health-check dependency satisfied by the database pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

type handler struct {
	pinger Pinger
	logger *slog.Logger
}

// NewHandler builds the health endpoint handler.
func NewHandler(pinger Pinger, logger *slog.Logger) http.Handler {
	if pinger == nil {
		panic("platform/http: nil Pinger")
	}
	if logger == nil {
		logger = slog.Default()
	}

	h := &handler{pinger: pinger, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc(livePath, h.live)
	mux.HandleFunc(readyPath, h.ready)
	return mux
}

func (h *handler) live(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, liveBody)
}

func (h *handler) ready(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	if err := h.pinger.Ping(ctx); err != nil {
		h.logger.WarnContext(r.Context(), "readiness database ping failed", "error", sanitizeError(err))
		writeJSON(w, http.StatusServiceUnavailable, unavailableBody)
		return
	}
	writeJSON(w, http.StatusOK, readyBody)
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJSON(w, http.StatusMethodNotAllowed, methodNotAllowedBody)
	return false
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	headers := w.Header()
	headers.Set("Content-Type", "application/json; charset=utf-8")
	headers.Set("Cache-Control", "no-store")
	headers.Set("X-Content-Type-Options", "nosniff")

	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()
	message = passwordPattern.ReplaceAllString(message, "${1}[REDACTED]")
	message = dsnCredentialPattern.ReplaceAllString(message, "${1}[REDACTED]@")
	message = authorizationPattern.ReplaceAllString(message, "${1}[REDACTED]")
	return message
}
