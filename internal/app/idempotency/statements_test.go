package idempotency

import (
	"strings"
	"testing"
)

// TestClaimStatementsMatchApprovedContract guards the exact SQL shapes the
// Owner approved: claim-first insert, 72-hour database-time window, locked
// second read and in-place expired takeover.
func TestClaimStatementsMatchApprovedContract(t *testing.T) {
	for _, fragment := range []string{
		"ON CONFLICT (organization_id, idempotency_key) DO NOTHING",
		"RETURNING id::text",
		"'sha256'",
		"now() + interval '72 hours'",
	} {
		if !strings.Contains(claimSQL, fragment) {
			t.Errorf("claimSQL lacks %q", fragment)
		}
	}
	for _, fragment := range []string{
		"COALESCE(event_id::text, '')",
		"expires_at <= now()",
		"FOR UPDATE",
	} {
		if !strings.Contains(resolveSQL, fragment) {
			t.Errorf("resolveSQL lacks %q", fragment)
		}
	}
	for _, fragment := range []string{
		"SET request_hash = $2",
		"event_id = NULL",
		"created_at = now()",
		"expires_at = now() + interval '72 hours'",
	} {
		if !strings.Contains(takeoverSQL, fragment) {
			t.Errorf("takeoverSQL lacks %q", fragment)
		}
	}
	if maxClaimAttempts != 2 {
		t.Errorf("maxClaimAttempts = %d, want the single bounded retry (2)", maxClaimAttempts)
	}
}
