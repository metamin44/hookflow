package destination

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// PreflightTimeout is the approved advisory DNS preflight budget at endpoint
// creation and update.
const PreflightTimeout = 2 * time.Second

// Resolver is the DNS surface the policy needs. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ResolveAllowed resolves every A and AAAA answer for host and returns one
// allowed address. Any prohibited answer rejects the whole set, which also
// rejects mixed public/private answer sets. IPv4-mapped answers are unwrapped
// before classification. A resolution failure is returned unchanged so the
// Worker keeps the existing transient dns classification.
func ResolveAllowed(ctx context.Context, resolver Resolver, host string) (netip.Addr, error) {
	return resolveAllowed(ctx, resolver, host, CheckAddr)
}

func resolveAllowed(ctx context.Context, resolver Resolver, host string, check func(netip.Addr) error) (netip.Addr, error) {
	if resolver == nil {
		return netip.Addr{}, errors.New("destination resolver is required")
	}
	if check == nil {
		check = CheckAddr
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	var selected netip.Addr
	for _, addr := range addrs {
		if err := check(addr); err != nil {
			return netip.Addr{}, err
		}
		if !selected.IsValid() {
			selected = addr.Unmap()
		}
	}
	return selected, nil
}

// Preflight performs the advisory endpoint create/update DNS check. A
// prohibited answer is returned; any DNS failure allows the request because
// delivery-time enforcement remains authoritative.
func Preflight(ctx context.Context, resolver Resolver, raw string) error {
	target, err := ParseURL(raw)
	if err != nil {
		return err
	}
	if target.Literal {
		return nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()
	if _, err := ResolveAllowed(lookupCtx, resolver, target.Host); err != nil {
		if errors.Is(err, ErrProhibited) {
			return err
		}
		return nil
	}
	return nil
}
