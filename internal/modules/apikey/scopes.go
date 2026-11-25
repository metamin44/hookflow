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
)

// Set is a parsed scope grant. Unknown scopes and wildcards are ignored; an
// empty grant means events:write only.
type Set struct {
	eventsWrite    bool
	endpointsRead  bool
	endpointsWrite bool
	secretsRotate  bool
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
			}
		}
	}
	if !set.eventsWrite && !set.endpointsRead && !set.endpointsWrite && !set.secretsRotate {
		set.eventsWrite = true
	}
	return set
}

// AllowsEventsWrite reports the events:write capability.
func (s Set) AllowsEventsWrite() bool { return s.eventsWrite }

// AllowsEndpointRead reports the effective endpoints:read capability.
func (s Set) AllowsEndpointRead() bool { return s.endpointsRead || s.endpointsWrite }

// AllowsEndpointWrite reports the endpoints:write capability.
func (s Set) AllowsEndpointWrite() bool { return s.endpointsWrite }

// AllowsSecretRotate reports the endpoints:secrets:rotate capability.
func (s Set) AllowsSecretRotate() bool { return s.secretsRotate }

// CanonicalStrings renders the granted scopes in the approved storage form.
func (s Set) CanonicalStrings() []string {
	granted := make([]string, 0, 4)
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
	slices.Sort(granted)
	return granted
}
