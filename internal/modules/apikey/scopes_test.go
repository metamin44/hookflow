package apikey

import (
	"reflect"
	"testing"
)

func TestParseScopes(t *testing.T) {
	cases := []struct {
		name         string
		input        []string
		read         bool
		write        bool
		rotate       bool
		events       bool
		billingRead  bool
		billingWrite bool
		deliveries   bool
		canon        []string
	}{
		{
			name:   "empty means events write only",
			input:  nil,
			events: true,
			canon:  []string{ScopeEventsWrite},
		},
		{
			name:   "blank entries mean events write only",
			input:  []string{" ", ""},
			events: true,
			canon:  []string{ScopeEventsWrite},
		},
		{
			name:  "read only",
			input: []string{ScopeEndpointsRead},
			read:  true,
			canon: []string{ScopeEndpointsRead},
		},
		{
			name:  "write implies read",
			input: []string{ScopeEndpointsWrite},
			read:  true,
			write: true,
			canon: []string{ScopeEndpointsRead, ScopeEndpointsWrite},
		},
		{
			name:   "rotate does not imply metadata",
			input:  []string{ScopeSecretsRotate},
			rotate: true,
			canon:  []string{ScopeSecretsRotate},
		},
		{
			name:   "rotate with read",
			input:  []string{ScopeSecretsRotate, ScopeEndpointsRead},
			read:   true,
			rotate: true,
			canon:  []string{ScopeEndpointsRead, ScopeSecretsRotate},
		},
		{
			name:  "unknown scopes are ignored",
			input: []string{"admin", "endpoints:*", ScopeEndpointsRead},
			read:  true,
			canon: []string{ScopeEndpointsRead},
		},
		{
			name:   "comma separated values",
			input:  []string{"events:write, endpoints:read"},
			read:   true,
			events: true,
			canon:  []string{ScopeEndpointsRead, ScopeEventsWrite},
		},
		{
			name:   "wildcard is not a scope",
			input:  []string{"*"},
			events: true,
			canon:  []string{ScopeEventsWrite},
		},
		{
			name:         "billing write implies billing read",
			input:        []string{ScopeBillingWrite},
			billingRead:  true,
			billingWrite: true,
			canon:        []string{ScopeBillingRead, ScopeBillingWrite},
		},
		{
			name:        "billing read only",
			input:       []string{ScopeBillingRead},
			billingRead: true,
			canon:       []string{ScopeBillingRead},
		},
		{
			name:       "deliveries read is independent",
			input:      []string{ScopeDeliveriesRead},
			deliveries: true,
			canon:      []string{ScopeDeliveriesRead},
		},
		{
			name:         "deliveries read does not imply billing or endpoint scopes",
			input:        []string{"deliveries:read,billing:write"},
			billingRead:  true,
			billingWrite: true,
			deliveries:   true,
			canon:        []string{ScopeBillingRead, ScopeBillingWrite, ScopeDeliveriesRead},
		},
		{
			name:   "delivery wildcard is not a scope",
			input:  []string{"deliveries:*"},
			events: true,
			canon:  []string{ScopeEventsWrite},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			set := Parse(testCase.input)
			if set.AllowsEndpointRead() != testCase.read {
				t.Fatalf("AllowsEndpointRead() = %v, want %v", set.AllowsEndpointRead(), testCase.read)
			}
			if set.AllowsEndpointWrite() != testCase.write {
				t.Fatalf("AllowsEndpointWrite() = %v, want %v", set.AllowsEndpointWrite(), testCase.write)
			}
			if set.AllowsEventsWrite() != testCase.events {
				t.Fatalf("AllowsEventsWrite() = %v, want %v", set.AllowsEventsWrite(), testCase.events)
			}
			if set.AllowsSecretRotate() != testCase.rotate {
				t.Fatalf("AllowsSecretRotate() = %v, want %v", set.AllowsSecretRotate(), testCase.rotate)
			}
			if set.AllowsBillingRead() != testCase.billingRead {
				t.Fatalf("AllowsBillingRead() = %v, want %v", set.AllowsBillingRead(), testCase.billingRead)
			}
			if set.AllowsBillingWrite() != testCase.billingWrite {
				t.Fatalf("AllowsBillingWrite() = %v, want %v", set.AllowsBillingWrite(), testCase.billingWrite)
			}
			if set.AllowsDeliveriesRead() != testCase.deliveries {
				t.Fatalf("AllowsDeliveriesRead() = %v, want %v", set.AllowsDeliveriesRead(), testCase.deliveries)
			}
			if got := set.CanonicalStrings(); !reflect.DeepEqual(got, testCase.canon) {
				t.Fatalf("CanonicalStrings() = %v, want %v", got, testCase.canon)
			}
		})
	}
}
