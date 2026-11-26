package destination

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The success fixture replaces only the resolved address and the policy
// verdict for the loopback test server. Production construction sets neither.
func TestTransportDeliversToValidatedHTTPSDestination(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	var requests atomic.Int32
	var serverName atomic.Value
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		serverName.Store(r.TLS.ServerName)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())

	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	d := newTestDialer(resolver, &dialRecorder{}, func(netip.Addr) error { return nil })
	d.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}

	transport := Transport(resolver, time.Second)
	transport.DialContext = d.dialContext
	transport.TLSClientConfig.RootCAs = pool
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	request, err := http.NewRequest(http.MethodPost, "https://example.com/hook", strings.NewReader(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if got, _ := serverName.Load().(string); got != "example.com" {
		t.Fatalf("TLS SNI = %q, want the original hostname", got)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("transport enabled InsecureSkipVerify")
	}
}

func TestTransportIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")

	transport := Transport(nil, time.Second)
	if transport.Proxy != nil {
		t.Fatal("transport honors a proxy function")
	}
	if !transport.DisableKeepAlives {
		t.Fatal("transport reuses connections, which would skip per-attempt revalidation")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("transport does not require verified TLS")
	}
	if transport.DialContext == nil {
		t.Fatal("transport has no pinned dialer")
	}
}

func TestTransportRejectsProhibitedAnswer(t *testing.T) {
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("10.0.0.1")}}
	transport := Transport(resolver, time.Second)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("https://example.com/hook")
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, ErrProhibited) {
		t.Fatalf("error = %v, want prohibited", err)
	}
}
