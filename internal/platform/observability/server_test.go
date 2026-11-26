package observability

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestStartServerRejectsWildcardBind(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := StartServer(context.Background(), "0.0.0.0:0", New(), logger)
	if err == nil {
		_ = server.Close(context.Background())
		t.Fatal("StartServer accepted an unspecified bind address")
	}
	if !strings.Contains(err.Error(), "unspecified address") {
		t.Fatalf("error = %v, want unsafe-bind rejection", err)
	}
}

func TestStartServerBindsLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := StartServer(ctx, "127.0.0.1:0", New(), logger)
	if err != nil {
		t.Fatalf("StartServer() error: %v", err)
	}
	host, _, err := net.SplitHostPort(server.Addr())
	if err != nil {
		_ = server.Close(context.Background())
		t.Fatalf("SplitHostPort(%q): %v", server.Addr(), err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		_ = server.Close(context.Background())
		t.Fatalf("bound host = %q, want loopback", host)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := server.Close(shutdownCtx); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}
