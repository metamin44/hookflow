//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/accesskeys"
	"github.com/metamin44/hookflow/internal/platform/kek"
)

// mgmtFailWrap is a provider seam that can fail deterministically after the
// fixture has already created a secret.
type mgmtFailWrap struct {
	inner kek.Provider
	armed atomic.Bool
}

func (g *mgmtFailWrap) Wrap(ctx context.Context, dek []byte, c kek.Context) (kek.Wrapped, error) {
	if g.armed.Load() {
		return kek.Wrapped{}, errors.New("keyring /private/provider-detail token=not-a-real-token")
	}
	return g.inner.Wrap(ctx, dek, c)
}

func (g *mgmtFailWrap) Unwrap(ctx context.Context, w kek.Wrapped, c kek.Context) ([]byte, error) {
	return g.inner.Unwrap(ctx, w, c)
}

func (g *mgmtFailWrap) CurrentKID() string { return g.inner.CurrentKID() }

func setMgmtOrgStatus(t *testing.T, f *mgmtFixture, status string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`UPDATE organizations SET status = $2 WHERE id = $1::uuid`, f.orgID, status); err != nil {
		t.Fatalf("set organization status: %v", err)
	}
}

func countMgmtEndpoints(t *testing.T, f *mgmtFixture, liveOnly bool) int {
	t.Helper()
	query := `SELECT count(*) FROM endpoints WHERE organization_id = $1::uuid`
	if liveOnly {
		query += ` AND deleted_at IS NULL`
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), query, f.orgID).Scan(&count); err != nil {
		t.Fatalf("count endpoints: %v", err)
	}
	return count
}

func countMgmtSubscriptions(t *testing.T, f *mgmtFixture, endpointID string, activeOnly bool) int {
	t.Helper()
	query := `SELECT count(*) FROM endpoint_subscriptions
		WHERE organization_id = $1::uuid AND endpoint_id = $2::uuid`
	if activeOnly {
		query += ` AND is_active`
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), query, f.orgID, endpointID).Scan(&count); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	return count
}

func countOrgAPIKeys(t *testing.T, f *mgmtFixture) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM api_keys WHERE organization_id = $1::uuid`, f.orgID).Scan(&count); err != nil {
		t.Fatalf("count api keys: %v", err)
	}
	return count
}

func subscriptionActive(t *testing.T, f *mgmtFixture, subscriptionID string) bool {
	t.Helper()
	var active bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT is_active FROM endpoint_subscriptions WHERE organization_id = $1::uuid AND id = $2::uuid`,
		f.orgID, subscriptionID).Scan(&active); err != nil {
		t.Fatalf("read subscription state: %v", err)
	}
	return active
}

func endpointDeleted(t *testing.T, f *mgmtFixture, endpointID string) bool {
	t.Helper()
	var deleted bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT deleted_at IS NOT NULL FROM endpoints WHERE organization_id = $1::uuid AND id = $2::uuid`,
		f.orgID, endpointID).Scan(&deleted); err != nil {
		t.Fatalf("read endpoint deletion state: %v", err)
	}
	return deleted
}

func apiKeyRevokedAt(t *testing.T, f *mgmtFixture, keyID string) *time.Time {
	t.Helper()
	var revoked *time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT revoked_at FROM api_keys WHERE organization_id = $1::uuid AND id = $2::uuid`,
		f.orgID, keyID).Scan(&revoked); err != nil {
		t.Fatalf("read api key revocation state: %v", err)
	}
	return revoked
}

func subscriptionIDFromReply(t *testing.T, reply mgmtReply) string {
	t.Helper()
	return decodeMgmt[struct {
		ID string `json:"id"`
	}](t, reply).ID
}

// createEndpointQuiet creates an endpoint without ever echoing a response body
// that carries the one-time secret.
func (f *mgmtFixture) createEndpointQuiet(token, body string) string {
	f.t.Helper()
	reply := f.do(http.MethodPost, f.endpoints(), token, body)
	wantSensitiveStatus(f.t, reply, http.StatusCreated, "create endpoint")
	prefix := f.endpoints() + "/"
	location := reply.header.Get("Location")
	if !strings.HasPrefix(location, prefix) || len(location) == len(prefix) {
		f.t.Fatal("create response did not carry the endpoint Location")
	}
	return strings.TrimPrefix(location, prefix)
}

// TestManagementArchivedPolicyClosure proves every archived rejection leaves
// state and audit rows unchanged, including the previously untested
// subscription deactivation and API-key revocation paths.
func TestManagementArchivedPolicyClosure(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, keyID := f.createKey("full", "endpoints:write", "endpoints:secrets:rotate")
	createdID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	subscriptions := f.endpoint(createdID) + "/subscriptions"

	keep := f.do(http.MethodPost, subscriptions, token, `{"event_type":"order.created"}`)
	wantStatus(t, keep, http.StatusCreated, "seed active subscription")
	keepID := subscriptionIDFromReply(t, keep)
	deactivate := f.do(http.MethodPost, subscriptions, token, `{"event_type":"order.updated"}`)
	wantStatus(t, deactivate, http.StatusCreated, "seed second subscription")
	deactivateID := subscriptionIDFromReply(t, deactivate)

	setMgmtOrgStatus(t, f, "suspended")
	wantStatus(t, f.do(http.MethodDelete, subscriptions+"/"+deactivateID, token, ""),
		http.StatusOK, "suspended deactivation before archive")

	setMgmtOrgStatus(t, f, "archived")
	endpointsBefore := countMgmtEndpoints(t, f, false)
	liveBefore := countMgmtEndpoints(t, f, true)
	subsBefore := countMgmtSubscriptions(t, f, createdID, false)
	keysBefore := countOrgAPIKeys(t, f)
	createdAudit := f.auditCount("endpoint.created")
	deletedAudit := f.auditCount("endpoint.deleted")
	subCreatedAudit := f.auditCount("subscription.created")
	subReactivatedAudit := f.auditCount("subscription.reactivated")
	subDeactivatedAudit := f.auditCount("subscription.deactivated")
	keyCreatedAudit := f.auditCount("apikey.created")
	keyRevokedAudit := f.auditCount("apikey.revoked")

	wantProblem(t, f.do(http.MethodDelete, subscriptions+"/"+keepID, token, ""),
		http.StatusForbidden, "organization_archived", "")
	if !subscriptionActive(t, f, keepID) {
		t.Fatal("archived deactivation mutated the subscription")
	}
	if got := f.auditCount("subscription.deactivated"); got != subDeactivatedAudit {
		t.Fatalf("archived deactivation wrote an audit row (%d -> %d)", subDeactivatedAudit, got)
	}

	wantProblem(t, f.do(http.MethodPost, subscriptions, token, `{"event_type":"order.new"}`),
		http.StatusForbidden, "organization_archived", "")
	wantProblem(t, f.do(http.MethodPost, subscriptions, token, `{"event_type":"order.updated"}`),
		http.StatusForbidden, "organization_archived", "")
	if subscriptionActive(t, f, deactivateID) {
		t.Fatal("archived reactivation mutated the subscription")
	}
	if got := countMgmtSubscriptions(t, f, createdID, false); got != subsBefore {
		t.Fatalf("archived subscription writes changed row count (%d -> %d)", subsBefore, got)
	}
	if got := f.auditCount("subscription.created"); got != subCreatedAudit {
		t.Fatalf("archived subscription create wrote an audit row (%d -> %d)", subCreatedAudit, got)
	}
	if got := f.auditCount("subscription.reactivated"); got != subReactivatedAudit {
		t.Fatalf("archived reactivation wrote an audit row (%d -> %d)", subReactivatedAudit, got)
	}

	wantProblem(t, f.do(http.MethodDelete, f.endpoint(createdID), token, ""),
		http.StatusForbidden, "organization_archived", "")
	if endpointDeleted(t, f, createdID) {
		t.Fatal("archived soft delete mutated the endpoint")
	}
	if got := f.auditCount("endpoint.deleted"); got != deletedAudit {
		t.Fatalf("archived soft delete wrote an audit row (%d -> %d)", deletedAudit, got)
	}

	wantProblem(t, f.do(http.MethodPost, f.endpoints(), token,
		`{"name":"archived-new","url":"https://example.test/a"}`),
		http.StatusForbidden, "organization_archived", "")
	if got := countMgmtEndpoints(t, f, false); got != endpointsBefore {
		t.Fatalf("archived endpoint create changed row count (%d -> %d)", endpointsBefore, got)
	}
	if got := countMgmtEndpoints(t, f, true); got != liveBefore {
		t.Fatalf("archived endpoint create changed live count (%d -> %d)", liveBefore, got)
	}
	if got := f.auditCount("endpoint.created"); got != createdAudit {
		t.Fatalf("archived endpoint create wrote an audit row (%d -> %d)", createdAudit, got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, _, err := f.access.Create(ctx, f.orgID, "archived-blocked", []string{"events:write"}, nil); !errors.Is(err, accesskeys.ErrOrganizationArchived) {
		t.Fatalf("archived api key create = %v, want ErrOrganizationArchived", err)
	}
	if got := countOrgAPIKeys(t, f); got != keysBefore {
		t.Fatalf("archived api key create changed row count (%d -> %d)", keysBefore, got)
	}
	if got := f.auditCount("apikey.created"); got != keyCreatedAudit {
		t.Fatalf("archived api key create wrote an audit row (%d -> %d)", keyCreatedAudit, got)
	}

	// The existing key stays active: archived organizations only deny writes.
	if _, _, err := f.access.Revoke(ctx, f.orgID, keyID); !errors.Is(err, accesskeys.ErrOrganizationArchived) {
		t.Fatalf("archived api key revoke = %v, want ErrOrganizationArchived", err)
	}
	if revoked := apiKeyRevokedAt(t, f, keyID); revoked != nil {
		t.Fatal("archived api key revoke mutated the key")
	}
	if got := f.auditCount("apikey.revoked"); got != keyRevokedAudit {
		t.Fatalf("archived api key revoke wrote an audit row (%d -> %d)", keyRevokedAudit, got)
	}
	wantStatus(t, f.do(http.MethodGet, f.endpoint(createdID), token, ""),
		http.StatusOK, "archived read with the unrevoked key")

	if countMgmtSubscriptions(t, f, createdID, true) != 1 {
		t.Fatal("archived policy changed the active subscription set")
	}
}

// TestManagementSuspendedPolicyClosure proves the approved suspended
// permissions: endpoint soft-delete, subscription deactivation and API-key
// revocation, with audit actor/target metadata and immediate key invalidation.
func TestManagementSuspendedPolicyClosure(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, keyID := f.createKey("operator", "endpoints:write", "endpoints:secrets:rotate")
	revokedToken, revokedKeyID := f.createKey("revoked", "events:write")

	endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	subscriptions := f.endpoint(endpointID) + "/subscriptions"
	active := f.do(http.MethodPost, subscriptions, token, `{"event_type":"order.created"}`)
	wantStatus(t, active, http.StatusCreated, "seed active subscription")
	activeID := subscriptionIDFromReply(t, active)

	doomedID := f.createEndpointQuiet(token, `{"name":"doomed","url":"https://example.test/doomed"}`)
	doomedSub := f.do(http.MethodPost, f.endpoint(doomedID)+"/subscriptions", token, `{"event_type":"order.deleted"}`)
	wantStatus(t, doomedSub, http.StatusCreated, "seed doomed subscription")
	doomedSubID := subscriptionIDFromReply(t, doomedSub)
	doomedSecret := f.storedSecret(doomedID)

	setMgmtOrgStatus(t, f, "suspended")

	deactivated := f.do(http.MethodDelete, subscriptions+"/"+activeID, token, "")
	wantStatus(t, deactivated, http.StatusOK, "suspended subscription deactivation")
	if subscriptionActive(t, f, activeID) {
		t.Fatal("suspended deactivation left the subscription active")
	}
	deactivatedAudits := f.auditRows("subscription.deactivated")
	if len(deactivatedAudits) != 1 {
		t.Fatalf("subscription.deactivated audit rows = %d, want 1", len(deactivatedAudits))
	}
	audit := deactivatedAudits[0]
	if audit.ActorType != "api_key" || audit.ActorID == nil || *audit.ActorID != keyID {
		t.Fatalf("deactivation audit actor = %+v", audit)
	}
	if audit.TargetID == nil || *audit.TargetID != activeID {
		t.Fatalf("deactivation audit target = %v", audit.TargetID)
	}
	if audit.Metadata["event_type"] != "order.created" {
		t.Fatalf("deactivation audit metadata = %v", audit.Metadata)
	}

	// Suspended organizations may revoke; the key is unusable immediately.
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	_, changed, err := f.access.Revoke(ctx, f.orgID, revokedKeyID)
	if err != nil || !changed {
		t.Fatalf("suspended api key revoke = changed:%v error:%v", changed, err)
	}
	if revoked := apiKeyRevokedAt(t, f, revokedKeyID); revoked == nil {
		t.Fatal("suspended revoke did not persist revoked_at")
	}
	wantProblem(t, f.do(http.MethodGet, f.endpoints(), revokedToken, ""),
		http.StatusUnauthorized, "invalid_api_key", "")
	revokedAudits := f.auditRows("apikey.revoked")
	if len(revokedAudits) != 1 {
		t.Fatalf("apikey.revoked audit rows = %d, want 1", len(revokedAudits))
	}
	if revokedAudits[0].ActorType != "service" || revokedAudits[0].TargetID == nil ||
		*revokedAudits[0].TargetID != revokedKeyID {
		t.Fatalf("revocation audit = %+v", revokedAudits[0])
	}

	deleted := f.do(http.MethodDelete, f.endpoint(doomedID), token, "")
	wantStatus(t, deleted, http.StatusNoContent, "suspended endpoint soft delete")
	if !endpointDeleted(t, f, doomedID) {
		t.Fatal("suspended soft delete did not set deleted_at")
	}
	if f.storedSecret(doomedID) != doomedSecret {
		t.Fatal("soft delete changed the stored secret envelope")
	}
	if got := countMgmtSubscriptions(t, f, doomedID, false); got != 1 {
		t.Fatalf("soft delete changed subscription rows: %d, want 1", got)
	}
	if !subscriptionActive(t, f, doomedSubID) {
		t.Fatal("soft delete mutated the subscription state")
	}
	deletedAudits := f.auditRows("endpoint.deleted")
	if len(deletedAudits) != 1 {
		t.Fatalf("endpoint.deleted audit rows = %d, want 1", len(deletedAudits))
	}
	if deletedAudits[0].ActorType != "api_key" || deletedAudits[0].ActorID == nil ||
		*deletedAudits[0].ActorID != keyID {
		t.Fatalf("delete audit actor = %+v", deletedAudits[0])
	}
	if deletedAudits[0].TargetID == nil || *deletedAudits[0].TargetID != doomedID {
		t.Fatalf("delete audit target = %v", deletedAudits[0].TargetID)
	}
	wantProblem(t, f.do(http.MethodGet, f.endpoint(doomedID), token, ""), http.StatusNotFound, "not_found", "")
	// Suspended policy rejects subscription creation before endpoint lookup, so a
	// deleted endpoint still answers with the organization-state rejection. The
	// request must remain a no-op.
	rows := countMgmtSubscriptions(t, f, doomedID, false)
	createdAudits := f.auditCount("subscription.created")
	wantProblem(t, f.do(http.MethodPost, f.endpoint(doomedID)+"/subscriptions", token,
		`{"event_type":"order.after"}`), http.StatusForbidden, "organization_suspended", "")
	if got := countMgmtSubscriptions(t, f, doomedID, false); got != rows {
		t.Fatalf("rejected suspended subscription create changed rows (%d -> %d)", rows, got)
	}
	if got := f.auditCount("subscription.created"); got != createdAudits {
		t.Fatalf("rejected suspended subscription create wrote an audit row (%d -> %d)", createdAudits, got)
	}
}

func decodeSecretOnly(t *testing.T, reply mgmtReply) string {
	t.Helper()
	var document struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(reply.body, &document); err != nil {
		t.Fatal("secret response was not valid JSON")
	}
	return document.Secret
}

// wantSensitiveStatus never echoes a response body, which may contain a
// one-time secret.
func wantSensitiveStatus(t *testing.T, reply mgmtReply, status int, description string) {
	t.Helper()
	if reply.status != status {
		t.Fatalf("%s status = %d, want %d", description, reply.status, status)
	}
}

func assertNoProviderLeak(t *testing.T, reply mgmtReply, description string) {
	t.Helper()
	body := strings.ToLower(string(reply.body))
	for _, forbidden := range []string{"whsec_", "hfse1.", "keyring", "provider-detail", "/private", "not-a-real-token", "argon2", "salt"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("%s leaked secret or provider material", description)
		}
	}
}

// TestManagementSecretResponsesAreUncacheable covers the one-time create and
// rotate responses plus error paths, without ever printing a secret value.
func TestManagementSecretResponsesAreUncacheable(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write", "endpoints:secrets:rotate")

		created := f.do(http.MethodPost, f.endpoints(), token,
			`{"name":"orders","url":"https://example.test/hooks"}`)
		wantSensitiveStatus(t, created, http.StatusCreated, "create endpoint")
		if created.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("create Cache-Control = %q, want no-store", created.header.Get("Cache-Control"))
		}
		secret := decodeSecretOnly(t, created)
		if !strings.HasPrefix(secret, "whsec_") || len(secret) != len("whsec_")+44 {
			t.Fatal("create response did not carry a canonical one-time secret")
		}
		createdSecret := secret
		secret = ""
		location := created.header.Get("Location")
		prefix := f.endpoints() + "/"
		if !strings.HasPrefix(location, prefix) || len(location) == len(prefix) {
			t.Fatal("create response did not carry the endpoint Location")
		}
		endpointID := strings.TrimPrefix(location, prefix)

		rotated := f.do(http.MethodPost, f.endpoint(endpointID)+"/secret/rotate", token, "", "If-Match", "*")
		wantSensitiveStatus(t, rotated, http.StatusOK, "rotate secret")
		if rotated.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("rotate Cache-Control = %q, want no-store", rotated.header.Get("Cache-Control"))
		}
		rotatedSecret := decodeSecretOnly(t, rotated)
		if !strings.HasPrefix(rotatedSecret, "whsec_") || rotatedSecret == createdSecret {
			t.Fatal("rotation did not carry a fresh one-time secret")
		}
		createdSecret = ""
		rotatedSecret = ""
	})

	t.Run("errors", func(t *testing.T) {
		gate := &mgmtFailWrap{}
		f := newMgmtFixture(t, mgmtOptions{wrap: func(inner kek.Provider) kek.Provider {
			gate.inner = inner
			return gate
		}})
		token, _ := f.createKey("full", "endpoints:write", "endpoints:secrets:rotate")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)

		stale := f.do(http.MethodPost, f.endpoint(endpointID)+"/secret/rotate", token, "",
			"If-Match", `"hf1-stale"`)
		wantProblem(t, stale, http.StatusPreconditionFailed, "precondition_failed", "")
		if stale.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("error Cache-Control = %q, want no-store", stale.header.Get("Cache-Control"))
		}
		assertNoProviderLeak(t, stale, "precondition failure")

		before := f.storedSecret(endpointID)
		gate.armed.Store(true)
		failed := f.do(http.MethodPost, f.endpoint(endpointID)+"/secret/rotate", token, "",
			"If-Match", "*")
		wantProblem(t, failed, http.StatusInternalServerError, "internal_error", "")
		if failed.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("provider failure Cache-Control = %q, want no-store", failed.header.Get("Cache-Control"))
		}
		assertNoProviderLeak(t, failed, "provider failure")
		if f.storedSecret(endpointID) != before {
			t.Fatal("failed provider rotation changed the stored secret")
		}
		if got := f.auditCount("endpoint.secret_rotated"); got != 0 {
			t.Fatalf("failed provider rotation wrote %d audit rows", got)
		}
	})
}
