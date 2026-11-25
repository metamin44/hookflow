package delivery

import "testing"

func TestPreHTTPReasonClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason    PreHTTPReason
		valid     bool
		transient bool
	}{
		{reason: ReasonSecretUnavailable, valid: true, transient: true},
		{reason: ReasonSecretMissing, valid: true},
		{reason: ReasonSecretInvalid, valid: true},
		{reason: ReasonProviderDisallowed, valid: true},
		{reason: ReasonSigningInternal, valid: true},
		{reason: ReasonDestinationDisallowed, valid: true},
		{reason: PreHTTPReason("unknown"), valid: false},
		{reason: PreHTTPReason(""), valid: false},
	}
	for _, tt := range tests {
		if got := tt.reason.Valid(); got != tt.valid {
			t.Errorf("%q Valid() = %v, want %v", tt.reason, got, tt.valid)
		}
		if got := tt.reason.Transient(); got != tt.transient {
			t.Errorf("%q Transient() = %v, want %v", tt.reason, got, tt.transient)
		}
	}
}
