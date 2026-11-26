package destination

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Transport returns the Worker transport. It never honors proxy environment
// variables, never skips TLS verification, and pins every connection to the
// address that was just validated. Keep-alives are disabled so each delivery
// attempt resolves and validates again instead of reusing an earlier
// connection.
func Transport(resolver Resolver, handshake time.Duration) *http.Transport {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	d := &dialer{
		resolver: resolver,
		dial: (&net.Dialer{
			Timeout:   handshake,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &http.Transport{
		// Proxy must stay nil: HTTP_PROXY, HTTPS_PROXY, and NO_PROXY are not
		// part of the approved delivery contract.
		Proxy:                 nil,
		DialContext:           d.dialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   handshake,
		ResponseHeaderTimeout: handshake,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		IdleConnTimeout:       90 * time.Second,
	}
}

type dialer struct {
	resolver Resolver
	dial     func(ctx context.Context, network, address string) (net.Conn, error)
	// check is nil in production. Package tests may replace it to exercise the
	// dial path against loopback fixtures; no production path sets it.
	check func(netip.Addr) error
}

func (d *dialer) checkAddr(addr netip.Addr) error {
	if d.check != nil {
		return d.check(addr)
	}
	return CheckAddr(addr)
}

// dialContext resolves and validates the destination, then dials the literal
// validated address. The transport keeps the original hostname for TLS SNI
// and certificate verification; only the socket target is pinned.
func (d *dialer) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.dial == nil || d.resolver == nil {
		return nil, errors.New("destination dialer is not initialized")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &ProhibitedError{Reason: "invalid dial address"}
	}
	if port != HTTPSPort {
		return nil, &ProhibitedError{Reason: "non-443 port"}
	}
	var addr netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addr = literal
	} else {
		addr, err = resolveAllowed(ctx, d.resolver, host, d.checkAddr)
		if err != nil {
			return nil, err
		}
	}
	if err := d.checkAddr(addr); err != nil {
		return nil, err
	}
	addr = addr.Unmap()
	conn, err := d.dial(ctx, network, net.JoinHostPort(addr.String(), port))
	if err != nil {
		return nil, err
	}
	if err := verifyRemote(conn, addr); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// verifyRemote fails closed when the connected peer is not the address that
// was validated. A DNS answer changing between validation and connection can
// therefore never reach a prohibited address.
func verifyRemote(conn net.Conn, want netip.Addr) error {
	tcp, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return &ProhibitedError{Reason: "connected remote address is unavailable"}
	}
	got, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return &ProhibitedError{Reason: "connected remote address is invalid"}
	}
	if got.Unmap() != want {
		return &ProhibitedError{Reason: "connected remote address mismatch"}
	}
	return nil
}
