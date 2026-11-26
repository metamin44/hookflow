package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	nethttptest "net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePinger struct {
	err     error
	calls   int
	lastCtx context.Context
	ping    func(context.Context) error
}

func (p *fakePinger) Ping(ctx context.Context) error {
	p.calls++
	p.lastCtx = ctx
	if p.ping != nil {
		return p.ping(ctx)
	}
	return p.err
}

func testLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func doRequest(handler nethttp.Handler, method, path string) *nethttptest.ResponseRecorder {
	req := nethttptest.NewRequest(method, path, nil)
	rec := nethttptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *nethttptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	return body
}

func assertHealthHeaders(t *testing.T, rec *nethttptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestLiveness(t *testing.T) {
	pinger := &fakePinger{err: errors.New("database must not be consulted")}
	handler := NewHandler(pinger, testLogger(io.Discard))

	rec := doRequest(handler, nethttp.MethodGet, livePath)

	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decodeJSON(t, rec)["status"]; got != "ok" {
		t.Fatalf("status body = %q", got)
	}
	if pinger.calls != 0 {
		t.Fatalf("liveness called the database %d time(s)", pinger.calls)
	}
	assertHealthHeaders(t, rec)
}

func TestReadinessSuccess(t *testing.T) {
	pinger := &fakePinger{}
	handler := NewHandler(pinger, testLogger(io.Discard))

	rec := doRequest(handler, nethttp.MethodGet, readyPath)

	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decodeJSON(t, rec)["status"]; got != "ready" {
		t.Fatalf("status body = %q", got)
	}
	if pinger.calls != 1 {
		t.Fatalf("readiness ping calls = %d, want 1", pinger.calls)
	}
	if pinger.lastCtx == nil {
		t.Fatal("readiness did not pass a request context")
	}
	if _, ok := pinger.lastCtx.Deadline(); !ok {
		t.Fatal("readiness ping context has no deadline")
	}
	assertHealthHeaders(t, rec)
}

func TestReadinessFailureDoesNotLeakDatabaseError(t *testing.T) {
	const secret = "unit-test-password"
	var logs bytes.Buffer
	pinger := &fakePinger{
		err: errors.New("ping failed: password=" + secret + " host=127.0.0.1 user=hookflow"),
	}
	handler := NewHandler(pinger, testLogger(&logs))

	rec := doRequest(handler, nethttp.MethodGet, readyPath)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := decodeJSON(t, rec)["status"]; got != "unavailable" {
		t.Fatalf("status body = %q", got)
	}
	if strings.Contains(rec.Body.String(), "ping failed") || strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("response leaked the database error: %s", rec.Body.String())
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("log leaked the database password: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "[REDACTED]") {
		t.Fatalf("log did not record a sanitized error: %s", logs.String())
	}
	assertHealthHeaders(t, rec)
}

func TestReadinessPropagatesRequestCancellation(t *testing.T) {
	pinger := &fakePinger{
		ping: func(ctx context.Context) error {
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("ping context error = %v, want context.Canceled", ctx.Err())
			}
			return ctx.Err()
		},
	}
	handler := NewHandler(pinger, testLogger(io.Discard))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := nethttptest.NewRequest(nethttp.MethodGet, readyPath, nil).WithContext(ctx)
	rec := nethttptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHealthRejectsNonGET(t *testing.T) {
	methods := []string{
		nethttp.MethodPost,
		nethttp.MethodPut,
		nethttp.MethodPatch,
		nethttp.MethodDelete,
		nethttp.MethodHead,
	}
	paths := []string{livePath, readyPath}

	for _, path := range paths {
		for _, method := range methods {
			t.Run(method+" "+path, func(t *testing.T) {
				pinger := &fakePinger{}
				handler := NewHandler(pinger, testLogger(io.Discard))

				rec := doRequest(handler, method, path)

				if rec.Code != nethttp.StatusMethodNotAllowed {
					t.Fatalf("status = %d, want 405", rec.Code)
				}
				if got := rec.Header().Get("Allow"); got != nethttp.MethodGet {
					t.Fatalf("Allow = %q, want GET", got)
				}
				if got := decodeJSON(t, rec)["error"]; got != "method_not_allowed" {
					t.Fatalf("error body = %q", got)
				}
				if pinger.calls != 0 {
					t.Fatalf("rejected request called the database %d time(s)", pinger.calls)
				}
				assertHealthHeaders(t, rec)
			})
		}
	}
}

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		secret string
	}{
		{
			name:   "password assignment",
			err:    errors.New("connect failed: password=unit-test-secret&sslmode=disable"),
			secret: "unit-test-secret",
		},
		{
			name:   "postgres dsn",
			err:    errors.New("connect failed: postgres://hookflow:unit-test-secret@127.0.0.1:5433/hookflow_dev"),
			secret: "unit-test-secret",
		},
		{
			name:   "authorization",
			err:    errors.New("request failed: Authorization: Bearer unit-test-token"),
			secret: "unit-test-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeError(tt.err)
			if strings.Contains(got, tt.secret) {
				t.Fatalf("sanitized error contains the secret: %s", got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("sanitized error has no redaction marker: %s", got)
			}
		})
	}
}

func TestNewServerSettings(t *testing.T) {
	handler := NewHandler(&fakePinger{}, testLogger(io.Discard))
	server := NewServer("127.0.0.1:1234", handler, testLogger(io.Discard))

	if server.Addr != "127.0.0.1:1234" {
		t.Fatalf("Addr = %q", server.Addr)
	}
	if server.Handler == nil {
		t.Fatal("Handler is nil")
	}
	if server.ReadHeaderTimeout != readHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v", server.ReadHeaderTimeout)
	}
	if server.IdleTimeout != idleTimeout {
		t.Fatalf("IdleTimeout = %v", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != maxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d", server.MaxHeaderBytes)
	}
	if server.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, want unset", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want unset", server.WriteTimeout)
	}
	if server.ErrorLog == nil {
		t.Fatal("ErrorLog is nil")
	}
}

func TestServerServesAndShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	handler := NewHandler(&fakePinger{}, testLogger(io.Discard))
	server := NewServer(listener.Addr().String(), handler, testLogger(io.Discard))
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	client := &nethttp.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + listener.Addr().String() + livePath)
	if err != nil {
		t.Fatalf("GET %s: %v", livePath, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("GET %s status = %d", livePath, resp.StatusCode)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	select {
	case err := <-serveErr:
		if !errors.Is(err, nethttp.ErrServerClosed) {
			t.Fatalf("Serve() error = %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after Shutdown()")
	}
}
