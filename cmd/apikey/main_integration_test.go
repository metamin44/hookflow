//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/modules/apikey"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

type cliFixture struct {
	cfg   config.Config
	pool  *pgxpool.Pool
	orgID string
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("connect integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	orgID, err := id.NewV7()
	if err != nil {
		t.Fatalf("generate organization id: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx,
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		orgID, "cli-"+orgID); err != nil {
		t.Fatalf("seed CLI organization: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Raw().Exec(cleanupCtx, `DELETE FROM organizations WHERE id = $1::uuid`, orgID); err != nil {
			t.Errorf("cleanup CLI organization: %v", err)
		}
	})
	return &cliFixture{cfg: cfg, pool: pool.Raw(), orgID: orgID}
}

func (f *cliFixture) run(t *testing.T, now time.Time, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := dependencies{
		loadConfig: func() (config.Config, error) { return f.cfg, nil },
		newPool:    database.NewPool,
		random:     secrets.DefaultReader(),
		now:        func() time.Time { return now },
	}
	err := run(context.Background(), args, &stdout, &stderr, deps)
	return stdout.String(), stderr.String(), err
}

func outputValue(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("output is missing %q", prefix)
	return ""
}

func assertNoCLILeak(t *testing.T, description string, values ...string) {
	t.Helper()
	for _, value := range values {
		lower := strings.ToLower(value)
		for _, forbidden := range []string{"$argon2", "salt", "verifier", "whsec_"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s exposed sensitive material", description)
			}
		}
	}
}

type cliKeyState struct {
	prefix    string
	hash      string
	algorithm string
	scopes    []string
	expires   *time.Time
	revoked   *time.Time
	rowJSON   string
}

func (f *cliFixture) keyState(t *testing.T, organizationID, keyID string) cliKeyState {
	t.Helper()
	var state cliKeyState
	if err := f.pool.QueryRow(context.Background(),
		`SELECT key_prefix, secret_hash, hash_algorithm, scopes, expires_at, revoked_at, to_jsonb(k)::text
		   FROM api_keys k WHERE organization_id = $1::uuid AND id = $2::uuid`,
		organizationID, keyID).Scan(&state.prefix, &state.hash, &state.algorithm, &state.scopes,
		&state.expires, &state.revoked, &state.rowJSON); err != nil {
		t.Fatalf("read CLI api key state: %v", err)
	}
	return state
}

type cliAudit struct {
	actorType string
	action    string
	targetID  string
	metadata  json.RawMessage
}

func (f *cliFixture) audit(t *testing.T, action, targetID string) []cliAudit {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT actor_type, action, target_id::text, metadata::text
		   FROM audit_logs WHERE organization_id = $1::uuid AND action = $2
		   ORDER BY occurred_at, id`, f.orgID, action)
	if err != nil {
		t.Fatalf("read CLI audit rows: %v", err)
	}
	defer rows.Close()
	var result []cliAudit
	for rows.Next() {
		var row cliAudit
		var target *string
		if err := rows.Scan(&row.actorType, &row.action, &target, &row.metadata); err != nil {
			t.Fatalf("scan CLI audit row: %v", err)
		}
		if target != nil {
			row.targetID = *target
		}
		if targetID == "" || row.targetID == targetID {
			result = append(result, row)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate CLI audit rows: %v", err)
	}
	return result
}

func (f *cliFixture) createKey(t *testing.T, now time.Time, name string, extra ...string) (string, string, string) {
	t.Helper()
	args := append([]string{"create", "-organization", f.orgID, "-name", name}, extra...)
	stdout, stderr, err := f.run(t, now, args...)
	if err != nil {
		t.Fatalf("CLI create failed: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatal("CLI create wrote to stderr")
	}
	token := outputValue(t, stdout, "api_key: ")
	keyID := outputValue(t, stdout, "id: ")
	if _, err := apikey.ParseToken(token); err != nil {
		t.Fatal("CLI create printed a malformed raw key")
	}
	if strings.Count(stdout, token) != 1 {
		t.Fatal("CLI create did not print the raw key exactly once")
	}
	return token, keyID, stdout
}

func TestAPIKeyCLIEndToEnd(t *testing.T) {
	f := newCLIFixture(t)
	fixed := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)

	token, keyID, createOutput := f.createKey(t, fixed, "cli-key",
		"-scopes", "events:write,endpoints:read", "-expires", "2h")
	prefix := outputValue(t, createOutput, "prefix: ")
	if prefix != token[:apikey.PrefixLength] {
		t.Fatal("CLI prefix does not match the raw key prefix")
	}
	if outputValue(t, createOutput, "scopes: ") != "endpoints:read,events:write" {
		t.Fatal("CLI did not print canonical scopes")
	}
	if outputValue(t, createOutput, "expires_at: ") != fixed.Add(2*time.Hour).Format(time.RFC3339) {
		t.Fatal("CLI did not print the fixed expiry")
	}

	state := f.keyState(t, f.orgID, keyID)
	if state.prefix != token[:apikey.PrefixLength] {
		t.Fatal("stored prefix differs from the printed prefix")
	}
	if state.algorithm != "argon2id" || !strings.HasPrefix(state.hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatal("stored verifier is not the approved Argon2id PHC form")
	}
	if strings.Contains(state.hash, token) || strings.Contains(state.rowJSON, token) {
		t.Fatal("the raw API key was persisted")
	}
	if len(state.scopes) != 2 || state.scopes[0] != "endpoints:read" || state.scopes[1] != "events:write" {
		t.Fatalf("stored scopes = %v, want canonical endpoints:read,events:write", state.scopes)
	}
	if state.expires == nil || !state.expires.Equal(fixed.Add(2*time.Hour)) {
		t.Fatal("stored expiry does not match -expires from the fixed clock")
	}
	if state.revoked != nil {
		t.Fatal("new CLI key is already revoked")
	}

	listOutput, listErr, err := f.run(t, fixed, "list", "-organization", f.orgID)
	if err != nil {
		t.Fatalf("CLI list failed: %v", err)
	}
	if strings.TrimSpace(listErr) != "" {
		t.Fatal("CLI list wrote to stderr")
	}
	if !strings.Contains(listOutput, "cli-key") || !strings.Contains(listOutput, prefix) {
		t.Fatal("CLI list omitted the key name or prefix")
	}
	if strings.Contains(listOutput, token) || strings.Contains(listOutput, state.hash) ||
		strings.Contains(listOutput, "$argon2id$") {
		t.Fatal("CLI list printed the raw key or verifier")
	}
	assertNoCLILeak(t, "CLI list output", listOutput)

	createdAudit := f.audit(t, "apikey.created", keyID)
	if len(createdAudit) != 1 {
		t.Fatalf("apikey.created audit rows = %d, want 1", len(createdAudit))
	}
	var metadata struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(createdAudit[0].metadata, &metadata); err != nil {
		t.Fatalf("decode create audit metadata: %v", err)
	}
	if createdAudit[0].actorType != "service" || metadata.Name != "cli-key" ||
		len(metadata.Scopes) != 2 {
		t.Fatal("apikey.created audit actor or metadata is incorrect")
	}

	revokeOutput, revokeErr, err := f.run(t, fixed, "revoke", "-organization", f.orgID, "-id", keyID)
	if err != nil {
		t.Fatalf("CLI revoke failed: %v", err)
	}
	if strings.TrimSpace(revokeErr) != "" {
		t.Fatal("CLI revoke wrote to stderr")
	}
	if strings.TrimSpace(revokeOutput) != "revoked api key "+keyID {
		t.Fatal("CLI revoke did not report the revoked key id")
	}
	if state = f.keyState(t, f.orgID, keyID); state.revoked == nil {
		t.Fatal("CLI revoke did not persist revoked_at")
	}
	revokedAudit := f.audit(t, "apikey.revoked", keyID)
	if len(revokedAudit) != 1 || revokedAudit[0].actorType != "service" {
		t.Fatalf("apikey.revoked audit rows = %+v, want one service actor", revokedAudit)
	}

	secondOutput, _, err := f.run(t, fixed, "revoke", "-organization", f.orgID, "-id", keyID)
	if err != nil {
		t.Fatalf("repeat CLI revoke failed: %v", err)
	}
	if !strings.Contains(secondOutput, "already revoked") {
		t.Fatal("repeat CLI revoke did not report an already-revoked key")
	}
	if got := len(f.audit(t, "apikey.revoked", keyID)); got != 1 {
		t.Fatalf("repeat revoke wrote an audit row (count=%d)", got)
	}
}

func TestAPIKeyCLIOrganizationPolicyAndOwnership(t *testing.T) {
	f := newCLIFixture(t)
	fixed := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)

	_, suspendedKey, _ := f.createKey(t, fixed, "suspended-key", "-scopes", "events:write")
	_, archivedKey, _ := f.createKey(t, fixed, "archived-key", "-scopes", "events:write")

	otherOrg, err := id.NewV7()
	if err != nil {
		t.Fatalf("generate second organization id: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		otherOrg, "other-"+otherOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1::uuid`, otherOrg); err != nil {
			t.Errorf("cleanup second CLI organization: %v", err)
		}
	})
	otherOutput, _, err := f.run(t, fixed, "create", "-organization", otherOrg,
		"-name", "other-key", "-scopes", "events:write")
	if err != nil {
		t.Fatalf("CLI create for second organization failed: %v", err)
	}
	otherToken := outputValue(t, otherOutput, "api_key: ")
	otherKey := outputValue(t, otherOutput, "id: ")

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE organizations SET status = 'suspended' WHERE id = $1::uuid`, f.orgID); err != nil {
		t.Fatalf("suspend CLI organization: %v", err)
	}
	revokeOutput, _, err := f.run(t, fixed, "revoke", "-organization", f.orgID, "-id", suspendedKey)
	if err != nil {
		t.Fatalf("suspended CLI revoke failed: %v", err)
	}
	if !strings.Contains(revokeOutput, "revoked api key "+suspendedKey) {
		t.Fatal("suspended CLI revoke did not succeed")
	}
	if state := f.keyState(t, f.orgID, suspendedKey); state.revoked == nil {
		t.Fatal("suspended CLI revoke did not persist revoked_at")
	}

	createOutput, _, createErr := f.run(t, fixed, "create", "-organization", f.orgID,
		"-name", "suspended-blocked", "-scopes", "events:write")
	if createErr == nil {
		t.Fatal("suspended CLI create succeeded")
	}
	if strings.TrimSpace(createOutput) != "" {
		t.Fatal("suspended CLI create printed output")
	}
	if !strings.Contains(createErr.Error(), "suspended") {
		t.Fatalf("suspended CLI create error = %v", createErr)
	}
	assertNoCLILeak(t, "suspended create error", createErr.Error())

	crossOutput, _, crossErr := f.run(t, fixed, "revoke", "-organization", f.orgID, "-id", otherKey)
	if crossErr == nil {
		t.Fatal("cross-tenant CLI revoke succeeded")
	}
	if strings.TrimSpace(crossOutput) != "" {
		t.Fatal("cross-tenant CLI revoke printed output")
	}
	if !strings.Contains(strings.ToLower(crossErr.Error()), "not found") {
		t.Fatalf("cross-tenant revoke error = %v", crossErr)
	}
	assertNoCLILeak(t, "cross-tenant revoke error", crossErr.Error(), otherToken)
	if state := f.keyState(t, otherOrg, otherKey); state.revoked != nil {
		t.Fatal("cross-tenant CLI revoke mutated the other organization's key")
	}

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE organizations SET status = 'archived' WHERE id = $1::uuid`, f.orgID); err != nil {
		t.Fatalf("archive CLI organization: %v", err)
	}
	before := f.keyState(t, f.orgID, archivedKey)
	archivedOutput, _, archivedErr := f.run(t, fixed, "revoke", "-organization", f.orgID, "-id", archivedKey)
	if archivedErr == nil {
		t.Fatal("archived CLI revoke succeeded")
	}
	if strings.TrimSpace(archivedOutput) != "" {
		t.Fatal("archived CLI revoke printed output")
	}
	if !strings.Contains(archivedErr.Error(), "archived") {
		t.Fatalf("archived CLI revoke error = %v", archivedErr)
	}
	assertNoCLILeak(t, "archived revoke error", archivedErr.Error())
	after := f.keyState(t, f.orgID, archivedKey)
	if after.revoked != nil || before.revoked != nil {
		t.Fatal("archived CLI revoke mutated the key")
	}
	if got := len(f.audit(t, "apikey.revoked", archivedKey)); got != 0 {
		t.Fatalf("archived CLI revoke wrote %d audit rows", got)
	}

	if _, _, err := f.run(t, fixed, "revoke", "-organization", f.orgID, "-id", "not-a-uuid"); err == nil {
		t.Fatal("CLI revoke accepted a malformed key id")
	} else {
		assertNoCLILeak(t, "malformed key error", err.Error())
	}
	if _, _, err := f.run(t, fixed, "create", "-organization", "not-a-uuid", "-name", "x"); err == nil {
		t.Fatal("CLI create accepted a malformed organization id")
	} else {
		assertNoCLILeak(t, "malformed organization error", err.Error())
	}
}
