//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
)

// TestManagementRejectsProhibitedDestinationsIntegration proves the OD-4
// literal-address policy at the public management API boundary: creation and
// update fail with a field error, nothing is persisted, and a rejected PATCH
// leaves the stored snapshot unchanged.
func TestManagementRejectsProhibitedDestinationsIntegration(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("ssrf", "endpoints:write")

	rejected := map[string]string{
		"ipv4 loopback":      "https://127.0.0.1/hook",
		"ipv4 private":       "https://10.0.0.5/hook",
		"ipv4 metadata":      "https://169.254.169.254/latest/meta-data",
		"ipv4 documentation": "https://192.0.2.10/hook",
		"ipv6 loopback":      "https://[::1]/hook",
		"ipv6 unique local":  "https://[fd00::1]/hook",
		"ipv6 link local":    "https://[fe80::1]/hook",
		"ipv6 site local":    "https://[fec0::1]/hook",
		"mapped ipv4":        "https://[::ffff:127.0.0.1]/hook",
		"ipv6 zone":          "https://[fe80::1%25eth0]/hook",
		"plain http":         "http://1.1.1.1/hook",
		"non-443 port":       "https://1.1.1.1:8443/hook",
		"empty port":         "https://1.1.1.1:/hook",
		"userinfo":           "https://user:pass@1.1.1.1/hook",
		"fragment":           "https://1.1.1.1/hook#frag",
		"relative":           "/hook",
	}
	for name, destination := range rejected {
		t.Run(name, func(t *testing.T) {
			reply := f.do(http.MethodPost, f.endpoints(), token,
				`{"name":"blocked","url":"`+destination+`"}`)
			wantProblem(t, reply, http.StatusBadRequest, "invalid_url", "url")
			requireEndpointCount(t, f, 0)
		})
	}

	// A public literal address needs no DNS resolution and is accepted.
	created := f.do(http.MethodPost, f.endpoints(), token, `{"name":"public","url":"https://1.1.1.1/hook"}`)
	if created.status != http.StatusCreated {
		t.Fatalf("create public endpoint = %d body=%s, want 201", created.status, created.body)
	}
	document := decodeMgmt[endpointDoc](t, created)
	requireEndpointCount(t, f, 1)

	// PATCH applies the same policy after the strong ETag precondition passes.
	reply := f.do(http.MethodPatch, f.endpoint(document.ID), token,
		`{"url":"https://169.254.169.254/latest/meta-data"}`,
		"If-Match", created.etag())
	wantProblem(t, reply, http.StatusBadRequest, "invalid_url", "url")

	var stored string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT url FROM endpoints WHERE organization_id = $1::uuid AND id = $2::uuid`,
		f.orgID, document.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored endpoint url: %v", err)
	}
	if stored != "https://1.1.1.1/hook" {
		t.Fatalf("stored url = %q, want the accepted value unchanged", stored)
	}
}

func requireEndpointCount(t *testing.T, f *mgmtFixture, want int) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM endpoints WHERE organization_id = $1::uuid`, f.orgID).Scan(&count); err != nil {
		t.Fatalf("count endpoints: %v", err)
	}
	if count != want {
		t.Fatalf("endpoint count = %d, want %d", count, want)
	}
}
