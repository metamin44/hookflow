// Package destination enforces the approved webhook destination policy:
// HTTPS-only URLs, port 443, and address-level SSRF protection for every
// delivery attempt.
//
// The policy is fixed in code. It is not configurable and has no environment
// override, so a deployment cannot disable address validation.
package destination

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// HTTPSPort is the only port a webhook destination may use.
const HTTPSPort = "443"

// ErrProhibited is the sentinel for every policy rejection. Callers must treat
// a wrapped ErrProhibited as a permanent pre-HTTP failure, never as a
// retryable network error.
var ErrProhibited = errors.New("destination is prohibited by policy")

// ProhibitedError reports why an address or URL was rejected. Addr is retained
// for tests and classification; Error never includes the address, host, or URL
// so the value is safe for bounded diagnostics.
type ProhibitedError struct {
	Reason string
	Addr   netip.Addr
}

// Error returns a bounded, destination-free message.
func (e *ProhibitedError) Error() string { return "destination is prohibited by policy" }

// Unwrap exposes the sentinel.
func (e *ProhibitedError) Unwrap() error { return ErrProhibited }

// Target is a syntactically valid destination URL.
type Target struct {
	Host    string
	Port    string
	Literal bool
	Address netip.Addr
}

var prohibitedIPv4 = mustPrefixes([]string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.31.196.0/24",
	"192.52.193.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"192.175.48.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
})

var prohibitedIPv6 = mustPrefixes([]string{
	"::/128",
	"::1/128",
	"::/96",
	"::ffff:0:0/96",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"3fff::/20",
	"5f00::/16",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
})

func mustPrefixes(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for i, value := range values {
		prefixes[i] = netip.MustParsePrefix(value)
	}
	return prefixes
}

// containsWhitespaceOrControl reports an ASCII space, control byte, or DEL.
// It matches the management input contract exactly.
func containsWhitespaceOrControl(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] <= 0x20 || raw[i] == 0x7f {
			return true
		}
	}
	return false
}

// CheckAddr applies the approved range tables and the standard netip
// classification to one address. IPv4-mapped IPv6 addresses are unwrapped
// first, so the IPv4 policy always governs them.
func CheckAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return &ProhibitedError{Reason: "invalid address"}
	}
	if addr.Zone() != "" {
		return &ProhibitedError{Reason: "zone identifier", Addr: addr}
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	prefixes := prohibitedIPv6
	if addr.Is4() {
		prefixes = prohibitedIPv4
	}
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return &ProhibitedError{Reason: "prohibited range", Addr: addr}
		}
	}
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() ||
		addr.IsInterfaceLocalMulticast() {
		return &ProhibitedError{Reason: "prohibited address class", Addr: addr}
	}
	return nil
}

// ParseURL applies the approved URL restrictions: HTTPS, a present host, no
// userinfo, no fragment, no IPv6 zone, port 443 only, no ASCII whitespace or
// control byte, and a literal IP host is classified immediately. The
// whitespace and control rejection duplicates the management input contract
// (and the database CHECK) so the delivery path enforces the same boundary
// even for a stored value that predates it.
func ParseURL(raw string) (Target, error) {
	if containsWhitespaceOrControl(raw) {
		return Target{}, fmt.Errorf("destination URL must not contain whitespace or control characters: %w", ErrProhibited)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return Target{}, fmt.Errorf("destination URL must be absolute https with a host: %w", ErrProhibited)
	}
	if parsed.User != nil {
		return Target{}, fmt.Errorf("destination URL must not contain user information: %w", ErrProhibited)
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(raw, "#") {
		return Target{}, fmt.Errorf("destination URL must not contain a fragment: %w", ErrProhibited)
	}
	host := parsed.Hostname()
	if host == "" {
		return Target{}, fmt.Errorf("destination URL must contain a host: %w", ErrProhibited)
	}
	if strings.Contains(host, "%") {
		return Target{}, fmt.Errorf("destination URL must not contain an IPv6 zone identifier: %w", ErrProhibited)
	}
	port := parsed.Port()
	if port == "" && strings.HasSuffix(parsed.Host, ":") {
		return Target{}, fmt.Errorf("destination URL must not contain an empty port: %w", ErrProhibited)
	}
	if port != "" && port != HTTPSPort {
		return Target{}, fmt.Errorf("destination URL must use port 443: %w", ErrProhibited)
	}
	if port == "" {
		port = HTTPSPort
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if err := CheckAddr(addr); err != nil {
			return Target{}, err
		}
		return Target{Host: host, Port: port, Literal: true, Address: addr.Unmap()}, nil
	}
	return Target{Host: host, Port: port}, nil
}
