package destination

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type staticResolver struct {
	addrs  []netip.Addr
	err    error
	calls  int
	onCall func(int)
}

func (r *staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	if r.onCall != nil {
		r.onCall(r.calls)
	}
	return r.addrs, r.err
}

func TestResolveAllowedSelectsFirstAllowedAnswer(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("2606:4700:4700::1111"),
	}}
	addr, err := ResolveAllowed(context.Background(), resolver, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if addr != netip.MustParseAddr("1.1.1.1") {
		t.Fatalf("selected %s", addr)
	}
}

func TestResolveAllowedRejectsMixedAnswers(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("10.0.0.1"),
	}}
	if _, err := ResolveAllowed(context.Background(), resolver, "example.test"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("mixed answers = %v, want prohibited", err)
	}
}

func TestResolveAllowedRejectsSiteLocalAnswers(t *testing.T) {
	t.Parallel()
	for _, addrs := range [][]netip.Addr{
		{netip.MustParseAddr("fec0::1")},
		{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("fec0::1")},
		{netip.MustParseAddr("fec0::1"), netip.MustParseAddr("1.1.1.1")},
	} {
		resolver := &staticResolver{addrs: addrs}
		if _, err := ResolveAllowed(context.Background(), resolver, "example.test"); !errors.Is(err, ErrProhibited) {
			t.Fatalf("site-local answers %v = %v, want prohibited", addrs, err)
		}
	}
}

func TestResolveAllowedAcceptsPublicIPv6Answer(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}}
	addr, err := ResolveAllowed(context.Background(), resolver, "example.test")
	if err != nil || addr != netip.MustParseAddr("2606:4700:4700::1111") {
		t.Fatalf("public IPv6 answer = %v, %v", addr, err)
	}
}

func TestResolveAllowedRejectsMappedPrivateAnswer(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("::ffff:192.168.0.1"),
	}}
	if _, err := ResolveAllowed(context.Background(), resolver, "example.test"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("mapped private answer = %v, want prohibited", err)
	}
}

func TestResolveAllowedRejectsZonedAnswer(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("fe80::1%eth0"),
	}}
	if _, err := ResolveAllowed(context.Background(), resolver, "example.test"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("zoned answer = %v, want prohibited", err)
	}
}

func TestResolveAllowedKeepsDNSErrorTransient(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{err: &net.DNSError{Err: "temporary failure", Name: "example.test", IsTemporary: true}}
	_, err := ResolveAllowed(context.Background(), resolver, "example.test")
	if err == nil || errors.Is(err, ErrProhibited) {
		t.Fatalf("DNS failure = %v, want a non-prohibited error", err)
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("DNS failure lost its type: %v", err)
	}
}

func TestResolveAllowedEmptyAnswerIsTemporary(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{}
	_, err := ResolveAllowed(context.Background(), resolver, "example.test")
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || errors.Is(err, ErrProhibited) {
		t.Fatalf("empty answer = %v, want a DNS error", err)
	}
}

func TestPreflightAllowsDNSFailure(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{err: errors.New("lookup failed")}
	if err := Preflight(context.Background(), resolver, "https://example.test/hook"); err != nil {
		t.Fatalf("Preflight = %v, want nil on DNS failure", err)
	}
}

func TestPreflightRejectsProhibitedAnswer(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	if err := Preflight(context.Background(), resolver, "https://example.test/hook"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("Preflight = %v, want prohibited", err)
	}
}

func TestPreflightSkipsLiteralLookup(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	if err := Preflight(context.Background(), resolver, "https://1.1.1.1/hook"); err != nil {
		t.Fatalf("Preflight = %v", err)
	}
	if resolver.calls != 0 {
		t.Fatalf("literal URL performed %d DNS lookups", resolver.calls)
	}
}

type fakeConn struct {
	remote net.Addr
	closed bool
}

func (c *fakeConn) Read([]byte) (int, error)         { return 0, errors.New("unused") }
func (c *fakeConn) Write([]byte) (int, error)        { return 0, errors.New("unused") }
func (c *fakeConn) Close() error                     { c.closed = true; return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *fakeConn) RemoteAddr() net.Addr             { return c.remote }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type dialRecorder struct {
	addresses []string
	remote    netip.Addr
	conns     []*fakeConn
	err       error
}

func (r *dialRecorder) dial(_ context.Context, _ string, address string) (net.Conn, error) {
	r.addresses = append(r.addresses, address)
	if r.err != nil {
		return nil, r.err
	}
	remote := r.remote
	if !remote.IsValid() {
		host, _, _ := net.SplitHostPort(address)
		remote = netip.MustParseAddr(host)
	}
	conn := &fakeConn{remote: &net.TCPAddr{IP: net.IP(remote.AsSlice())}}
	r.conns = append(r.conns, conn)
	return conn, nil
}

func newTestDialer(resolver Resolver, recorder *dialRecorder, check func(netip.Addr) error) *dialer {
	return &dialer{resolver: resolver, dial: recorder.dial, check: check}
}

func TestDialPinsValidatedAddress(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	recorder := &dialRecorder{}
	d := newTestDialer(resolver, recorder, nil)
	conn, err := d.dialContext(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	if conn == nil || len(recorder.addresses) != 1 || recorder.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dialed %v", recorder.addresses)
	}
}

func TestDialRejectsProhibitedLiteralWithoutDialing(t *testing.T) {
	t.Parallel()
	recorder := &dialRecorder{}
	d := newTestDialer(&staticResolver{}, recorder, nil)
	for _, address := range []string{"10.0.0.1:443", "127.0.0.1:443", "[::1]:443", "[fe80::1]:443", "[fec0::1]:443"} {
		if _, err := d.dialContext(context.Background(), "tcp", address); !errors.Is(err, ErrProhibited) {
			t.Fatalf("%s = %v, want prohibited", address, err)
		}
	}
	if len(recorder.addresses) != 0 {
		t.Fatalf("prohibited literals dialed %v", recorder.addresses)
	}
}

func TestDialRejectsProhibitedResolutionWithoutDialing(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("10.0.0.1")}}
	recorder := &dialRecorder{}
	d := newTestDialer(resolver, recorder, nil)
	if _, err := d.dialContext(context.Background(), "tcp", "internal.example.test:443"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("error = %v, want prohibited", err)
	}
	if len(recorder.addresses) != 0 {
		t.Fatalf("prohibited answer was dialed %v", recorder.addresses)
	}
}

func TestDialRejectsMixedSiteLocalResolutionWithoutDialing(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("2606:4700:4700::1111"),
		netip.MustParseAddr("fec0::1"),
	}}
	recorder := &dialRecorder{}
	d := newTestDialer(resolver, recorder, nil)
	if _, err := d.dialContext(context.Background(), "tcp", "internal.example.test:443"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("mixed site-local resolution = %v, want prohibited", err)
	}
	if len(recorder.addresses) != 0 {
		t.Fatalf("mixed site-local answers were dialed: %v", recorder.addresses)
	}
}

func TestDialRejectsNon443Port(t *testing.T) {
	t.Parallel()
	recorder := &dialRecorder{}
	d := newTestDialer(&staticResolver{}, recorder, nil)
	if _, err := d.dialContext(context.Background(), "tcp", "example.test:8443"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("error = %v, want prohibited", err)
	}
	if len(recorder.addresses) != 0 {
		t.Fatal("non-443 port was dialed")
	}
}

func TestDialRemoteMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	recorder := &dialRecorder{remote: netip.MustParseAddr("10.0.0.1")}
	d := newTestDialer(resolver, recorder, nil)
	_, err := d.dialContext(context.Background(), "tcp", "example.test:443")
	if !errors.Is(err, ErrProhibited) {
		t.Fatalf("error = %v, want prohibited", err)
	}
	if len(recorder.conns) != 1 || !recorder.conns[0].closed {
		t.Fatal("mismatched connection was not closed")
	}
}

// TestDialRevalidatesEveryAttempt simulates DNS rebinding: the first lookup is
// public, the second is private. The prohibited address must never be dialed.
func TestDialRevalidatesEveryAttempt(t *testing.T) {
	t.Parallel()
	resolver := &staticResolver{}
	resolver.onCall = func(call int) {
		if call == 1 {
			resolver.addrs = []netip.Addr{netip.MustParseAddr("93.184.216.34")}
			return
		}
		resolver.addrs = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	}
	recorder := &dialRecorder{}
	d := newTestDialer(resolver, recorder, nil)

	if _, err := d.dialContext(context.Background(), "tcp", "example.test:443"); err != nil {
		t.Fatalf("first attempt = %v", err)
	}
	if _, err := d.dialContext(context.Background(), "tcp", "example.test:443"); !errors.Is(err, ErrProhibited) {
		t.Fatalf("rebound attempt = %v, want prohibited", err)
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls = %d, want 2", resolver.calls)
	}
	if len(recorder.addresses) != 1 || recorder.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dialed addresses = %v, want only the public answer", recorder.addresses)
	}
}

func TestDialErrorNeverContainsAddress(t *testing.T) {
	t.Parallel()
	recorder := &dialRecorder{}
	d := newTestDialer(&staticResolver{}, recorder, nil)
	_, err := d.dialContext(context.Background(), "tcp", "10.11.12.13:443")
	if err == nil || strings.Contains(err.Error(), "10.11.12.13") {
		t.Fatalf("diagnostic = %v, want an address-free error", err)
	}
}
