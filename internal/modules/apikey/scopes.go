package apikey

import (
	"slices"
	"strings"
)

// Approved scope names.
const (
	ScopeEventsWrite    = "events:write"
	ScopeEndpointsRead  = "endpoints:read"
	ScopeEndpointsWrite = "endpoints:write"
	ScopeSecretsRotate  = "endpoints:secrets:rotate"
	ScopeBillingRead    = "billing:read"
	ScopeBillingWrite   = "billing:write"
	ScopeDeliveriesRead = "deliveries:read"
	// ScopeDeliveriesRetry grants manual creation of a new delivery generation.
	ScopeDeliveriesRetry = "deliveries:retry"
	// ScopeOperationsRead grants the derived operational overview and
	// endpoint-health routes. It is independent of delivery history.
	ScopeOperationsRead = "operations:read"
)

// Set is a parsed scope grant. Unknown scopes and wildcards are ignored; an
// empty grant means events:write only.
type Set struct {
	eventsWrite     bool
	endpointsRead   bool
	endpointsWrite  bool
	secretsRotate   bool
	billingRead     bool
	billingWrite    bool
	deliveriesRead  bool
	deliveriesRetry bool
	operationsRead  bool
}

// Parse interprets stored or operator-supplied scope values.
func Parse(values []string) Set {
	var set Set
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			switch strings.TrimSpace(part) {
			case ScopeEventsWrite:
				set.eventsWrite = true
			case ScopeEndpointsRead:
				set.endpointsRead = true
			case ScopeEndpointsWrite:
				set.endpointsWrite = true
			case ScopeSecretsRotate:
				set.secretsRotate = true
			case ScopeBillingRead:
				set.billingRead = true
			case ScopeBillingWrite:
				set.billingWrite = true
			case ScopeDeliveriesRead:
				set.deliveriesRead = true
			case ScopeDeliveriesRetry:
				set.deliveriesRetry = true
			case ScopeOperationsRead:
				set.operationsRead = true
			}
		}
	}
	if !set.any() {
		set.eventsWrite = true
	}
	return set
}

func (s Set) any() bool {
	return s.eventsWrite || s.endpointsRead || s.endpointsWrite ||
		s.secretsRotate || s.billingRead || s.billingWrite || s.deliveriesRead ||
		s.deliveriesRetry || s.operationsRead
}

// AllowsEventsWrite reports the events:write capability.
func (s Set) AllowsEventsWrite() bool { return s.eventsWrite }

// AllowsEndpointRead reports the effective endpoints:read capability.
func (s Set) AllowsEndpointRead() bool { return s.endpointsRead || s.endpointsWrite }

// AllowsEndpointWrite reports the endpoints:write capability.
func (s Set) AllowsEndpointWrite() bool { return s.endpointsWrite }

// AllowsSecretRotate reports the endpoints:secrets:rotate capability.
func (s Set) AllowsSecretRotate() bool { return s.secretsRotate }

// AllowsBillingRead reports the effective billing:read capability.
func (s Set) AllowsBillingRead() bool { return s.billingRead || s.billingWrite }

// AllowsBillingWrite reports the billing:write capability.
func (s Set) AllowsBillingWrite() bool { return s.billingWrite }

// AllowsDeliveriesRead reports the deliveries:read capability. It is an
// independent scope; no other scope implies it and it implies no other scope.
func (s Set) AllowsDeliveriesRead() bool { return s.deliveriesRead }

// AllowsDeliveriesRetry reports the deliveries:retry capability. It is
// independent of deliveries:read: retrying does not grant history visibility.
func (s Set) AllowsDeliveriesRetry() bool { return s.deliveriesRetry }

// AllowsOperationsRead reports the operations:read capability. It is
// independent: no other scope implies it and it implies no other scope.
func (s Set) AllowsOperationsRead() bool { return s.operationsRead }

// CanonicalStrings renders the granted scopes in the approved storage form.
func (s Set) CanonicalStrings() []string {
	granted := make([]string, 0, 9)
	if s.eventsWrite {
		granted = append(granted, ScopeEventsWrite)
	}
	if s.endpointsRead || s.endpointsWrite {
		granted = append(granted, ScopeEndpointsRead)
	}
	if s.endpointsWrite {
		granted = append(granted, ScopeEndpointsWrite)
	}
	if s.secretsRotate {
		granted = append(granted, ScopeSecretsRotate)
	}
	if s.billingRead || s.billingWrite {
		granted = append(granted, ScopeBillingRead)
	}
	if s.billingWrite {
		granted = append(granted, ScopeBillingWrite)
	}
	if s.deliveriesRead {
		granted = append(granted, ScopeDeliveriesRead)
	}
	if s.deliveriesRetry {
		granted = append(granted, ScopeDeliveriesRetry)
	}
	if s.operationsRead {
		granted = append(granted, ScopeOperationsRead)
	}
	slices.Sort(granted)
	return granted
}
