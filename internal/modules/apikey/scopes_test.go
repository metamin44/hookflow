package apikey

import (
	"reflect"
	"testing"
)

func TestParseScopes(t *testing.T) {
	cases := []struct {
		name   string
		input  []string
		read   bool
		write  bool
		rotate bool
		events bool
		canon  []string
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
			if got := set.CanonicalStrings(); !reflect.DeepEqual(got, testCase.canon) {
				t.Fatalf("CanonicalStrings() = %v, want %v", got, testCase.canon)
			}
		})
	}
}
