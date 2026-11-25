//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/app/accesskeys"
	eventapp "github.com/metamin44/hookflow/internal/app/event"
	"github.com/metamin44/hookflow/internal/app/management"
	"github.com/metamin44/hookflow/internal/modules/apikey"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// mgmtWrapGate blocks provider Wrap calls after being armed so tests can prove
// that no database lock is held during provider I/O.
type mgmtWrapGate struct {
	inner   kek.Provider
	entered chan struct{}
	release chan struct{}
	armed   atomic.Bool
	once    sync.Once
}

func (g *mgmtWrapGate) Wrap(ctx context.Context, dek []byte, c kek.Context) (kek.Wrapped, error) {
	if !g.armed.Load() {
		return g.inner.Wrap(ctx, dek, c)
	}
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return kek.Wrapped{}, ctx.Err()
	}
	return g.inner.Wrap(ctx, dek, c)
}

func (g *mgmtWrapGate) Unwrap(ctx context.Context, w kek.Wrapped, c kek.Context) ([]byte, error) {
	return g.inner.Unwrap(ctx, w, c)
}

func (g *mgmtWrapGate) CurrentKID() string { return g.inner.CurrentKID() }

type mgmtOptions struct {
	maxActive int
	wrap      func(kek.Provider) kek.Provider
}

type mgmtFixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	raw        kek.Provider
	provider   kek.Provider
	service    *management.Service
	access     *accesskeys.Service
	server     *httptest.Server
	client     *http.Client
	orgID      string
	keyCounter atomic.Int32
}

func mgmtLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newMgmtFixture(t *testing.T, opts mgmtOptions) *mgmtFixture {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	pool, err := database.NewPool(ctx, cfg.Database)
	cancel()
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	if opts.maxActive == 0 {
		opts.maxActive = 8
	}
	ringPath := filepath.Join(t.TempDir(), "keyring.json")
	ringBody := fmt.Sprintf(`{"current":"dev-1","keys":{"dev-1":%q}}`,
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, secrets.DEKSize)))
	if err := os.WriteFile(ringPath, []byte(ringBody), 0o600); err != nil {
		t.Fatalf("write test keyring: %v", err)
	}
	provider, err := kek.NewFileProvider(ringPath, "dev-1", kek.FileOptions{})
	if err != nil {
		t.Fatalf("open test keyring: %v", err)
	}
	effective := kek.Provider(provider)
	if opts.wrap != nil {
		effective = opts.wrap(provider)
	}
	service, err := management.NewService(pool.Raw(), effective, opts.maxActive, mgmtLogger(), secrets.DefaultReader())
	if err != nil {
		t.Fatalf("build management service: %v", err)
	}
	auth, err := management.NewAuthenticator(pool.Raw(), apikey.NewVerifier())
	if err != nil {
		t.Fatalf("build authenticator: %v", err)
	}
	ingester, err := eventapp.NewService(pool.Raw(), eventapp.Options{})
	if err != nil {
		t.Fatalf("build ingestion service: %v", err)
	}
	handler, err := management.NewHandler(service, auth, ingester, mgmtLogger())
	if err != nil {
		t.Fatalf("build management handler: %v", err)
	}
	access, err := accesskeys.NewService(pool.Raw(), secrets.DefaultReader())
	if err != nil {
		t.Fatalf("build access key service: %v", err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	orgID := mustID(t)
	if _, err := pool.Raw().Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		orgID, "mgmt-"+orgID,
	); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		if _, err := pool.Raw().Exec(ctx, `DELETE FROM organizations WHERE id = $1::uuid`, orgID); err != nil {
			t.Errorf("cleanup organization: %v", err)
		}
	})
	return &mgmtFixture{
		t:        t,
		pool:     pool.Raw(),
		raw:      provider,
		provider: effective,
		service:  service,
		access:   access,
		server:   server,
		client:   &http.Client{Timeout: 10 * time.Second},
		orgID:    orgID,
	}
}

func (f *mgmtFixture) createKey(name string, scopes ...string) (string, string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	token, record, err := f.access.Create(ctx, f.orgID, fmt.Sprintf("%s-%d", name, f.keyCounter.Add(1)), scopes, nil)
	if err != nil {
		f.t.Fatalf("create api key: %v", err)
	}
	return token, record.ID
}

func (f *mgmtFixture) createKeyFor(orgID, name string, scopes ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	token, _, err := f.access.Create(ctx, orgID, name, scopes, nil)
	if err != nil {
		f.t.Fatalf("create api key for %s: %v", orgID, err)
	}
	return token
}

func (f *mgmtFixture) orgPath() string   { return "/v1/organizations/" + f.orgID }
func (f *mgmtFixture) endpoints() string { return f.orgPath() + "/endpoints" }
func (f *mgmtFixture) endpoint(id string) string {
	return f.endpoints() + "/" + id
}

type mgmtReply struct {
	status int
	header http.Header
	body   []byte
}

type outcome struct {
	reply mgmtReply
	err   error
}

func (r mgmtReply) etag() string { return r.header.Get("ETag") }

func (f *mgmtFixture) roundTrip(method, path, token, body string, headers ...string) (mgmtReply, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		return mgmtReply{}, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response, err := f.client.Do(request)
	if err != nil {
		return mgmtReply{}, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return mgmtReply{}, err
	}
	return mgmtReply{status: response.StatusCode, header: response.Header, body: data}, nil
}

func (f *mgmtFixture) do(method, path, token, body string, headers ...string) mgmtReply {
	f.t.Helper()
	reply, err := f.roundTrip(method, path, token, body, headers...)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	return reply
}

// status is the goroutine-safe variant used by concurrency tests.
func (f *mgmtFixture) status(method, path, token, body string, headers ...string) int {
	reply, err := f.roundTrip(method, path, token, body, headers...)
	if err != nil {
		f.t.Errorf("%s %s: %v", method, path, err)
		return 0
	}
	return reply.status
}

type endpointDoc struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   *string           `json:"description"`
	URL           string            `json:"url"`
	Status        string            `json:"status"`
	SigningScheme string            `json:"signing_scheme"`
	HTTPHeaders   map[string]string `json:"http_headers"`
	Secret        string            `json:"secret"`
}

type problemDoc struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
	Field  string `json:"field"`
}

func decodeMgmt[T any](t *testing.T, reply mgmtReply) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(reply.body, &value); err != nil {
		t.Fatalf("decode response %q: %v", string(reply.body), err)
	}
	return value
}

func (f *mgmtFixture) createEndpoint(token, body string, headers ...string) endpointDoc {
	f.t.Helper()
	reply := f.do(http.MethodPost, f.endpoints(), token, body, headers...)
	if reply.status != http.StatusCreated {
		f.t.Fatalf("create endpoint = %d %s, want 201", reply.status, reply.body)
	}
	return decodeMgmt[endpointDoc](f.t, reply)
}

func (f *mgmtFixture) storedSecret(endpointID string) string {
	f.t.Helper()
	var stored string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT secret_encrypted FROM endpoints WHERE id = $1::uuid`, endpointID,
	).Scan(&stored); err != nil {
		f.t.Fatalf("read stored secret: %v", err)
	}
	return stored
}

func (f *mgmtFixture) openSecret(endpointID string) []byte {
	f.t.Helper()
	envelope, err := secrets.Decode(f.storedSecret(endpointID))
	if err != nil {
		f.t.Fatalf("decode stored envelope: %v", err)
	}
	orgUUID, err := secrets.ParseUUID(f.orgID)
	if err != nil {
		f.t.Fatalf("parse organization id: %v", err)
	}
	endpointUUID, err := secrets.ParseUUID(endpointID)
	if err != nil {
		f.t.Fatalf("parse endpoint id: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	dek, err := f.raw.Unwrap(ctx, kek.Wrapped{KID: envelope.KID, Provider: envelope.Provider, Blob: envelope.WrappedDEK},
		kek.Context{OrganizationID: [16]byte(orgUUID), EndpointID: [16]byte(endpointUUID)})
	if err != nil {
		f.t.Fatalf("unwrap dek: %v", err)
	}
	defer clearBytes(dek)
	aad, err := envelope.BuildAAD(orgUUID, endpointUUID)
	if err != nil {
		f.t.Fatalf("build aad: %v", err)
	}
	plaintext, err := secrets.Open(dek, envelope.Nonce, envelope.Ciphertext, envelope.Tag, aad[:])
	if err != nil {
		f.t.Fatalf("open envelope: %v", err)
	}
	return plaintext
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func tokenPayload(t *testing.T, token string) []byte {
	t.Helper()
	if !strings.HasPrefix(token, "whsec_") {
		t.Fatal("secret does not carry the whsec_ prefix")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "whsec_"))
	if err != nil || len(raw) != secrets.DEKSize {
		t.Fatalf("secret payload is not 32 base64 bytes")
	}
	return raw
}

type auditRow struct {
	Action    string
	ActorType string
	ActorID   *string
	TargetID  *string
	IP        *string
	UserAgent *string
	Metadata  map[string]any
}

func (f *mgmtFixture) auditRows(action string) []auditRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT action, actor_type, actor_id::text, target_id::text, host(ip), user_agent, metadata
		 FROM audit_logs WHERE organization_id = $1::uuid AND action = $2
		 ORDER BY occurred_at, id`, f.orgID, action)
	if err != nil {
		f.t.Fatalf("read audit rows: %v", err)
	}
	defer rows.Close()
	result := make([]auditRow, 0, 4)
	for rows.Next() {
		var row auditRow
		if err := rows.Scan(&row.Action, &row.ActorType, &row.ActorID, &row.TargetID,
			&row.IP, &row.UserAgent, &row.Metadata); err != nil {
			f.t.Fatalf("scan audit row: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("iterate audit rows: %v", err)
	}
	return result
}

func (f *mgmtFixture) auditCount(action string) int { return len(f.auditRows(action)) }

func metadataFields(t *testing.T, metadata map[string]any) []string {
	t.Helper()
	raw, ok := metadata["fields"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("audit metadata fields = %T, want an array", raw)
	}
	fields := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("audit metadata field = %T, want a string", item)
		}
		fields = append(fields, text)
	}
	return fields
}

func wantStatus(t *testing.T, reply mgmtReply, status int, context string) {
	t.Helper()
	if reply.status != status {
		t.Fatalf("%s = %d %s, want %d", context, reply.status, reply.body, status)
	}
}

func wantProblem(t *testing.T, reply mgmtReply, status int, code, field string) {
	t.Helper()
	wantStatus(t, reply, status, "problem response")
	contentType := reply.header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/problem+json") {
		t.Fatalf("problem content type = %q", contentType)
	}
	problem := decodeMgmt[problemDoc](t, reply)
	if problem.Type != "about:blank" || problem.Status != status || problem.Code != code {
		t.Fatalf("problem = %+v, want about:blank/%d/%s", problem, status, code)
	}
	if field != "" && problem.Field != field {
		t.Fatalf("problem field = %q, want %q", problem.Field, field)
	}
}

func TestManagementAuthenticationAndScopes(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	writeToken, _ := f.createKey("write", "endpoints:write")
	readToken, _ := f.createKey("read", "endpoints:read")
	eventsToken, _ := f.createKey("events", "events:write")

	wantProblem(t, f.do(http.MethodGet, f.endpoints(), "", ""), http.StatusUnauthorized, "invalid_api_key", "")
	wantProblem(t, f.do(http.MethodGet, f.endpoints(), "abc", ""), http.StatusUnauthorized, "invalid_api_key", "")
	wantProblem(t, f.do(http.MethodGet, f.endpoints(), "", "", "Authorization", "Basic abc"), http.StatusUnauthorized, "invalid_api_key", "")
	unknownToken, _, err := apikey.Generate(nil)
	if err != nil {
		t.Fatalf("generate probe token: %v", err)
	}
	wantProblem(t, f.do(http.MethodGet, f.endpoints(), unknownToken, ""), http.StatusUnauthorized, "invalid_api_key", "")

	list := f.do(http.MethodGet, f.endpoints(), readToken, "")
	wantStatus(t, list, http.StatusOK, "read-only list")
	if list.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("success Cache-Control = %q, want no-store", list.header.Get("Cache-Control"))
	}
	var listed struct {
		Endpoints []endpointDoc `json:"endpoints"`
	}
	if err := json.Unmarshal(list.body, &listed); err != nil || listed.Endpoints == nil {
		t.Fatalf("list body = %s, want an endpoints array", list.body)
	}

	wantProblem(t, f.do(http.MethodPost, f.endpoints(), readToken, `{"name":"a","url":"https://example.test/h"}`),
		http.StatusForbidden, "insufficient_scope", "")
	wantProblem(t, f.do(http.MethodGet, f.endpoints(), eventsToken, ""),
		http.StatusForbidden, "insufficient_scope", "")

	// endpoints:write implies read.
	created := f.createEndpoint(writeToken, `{"name":"scope-check","url":"https://example.test/h"}`)
	wantStatus(t, f.do(http.MethodGet, f.endpoint(created.ID), writeToken, ""), http.StatusOK, "write key read")
	wantStatus(t, f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", writeToken, "",
		"If-Match", "*"), http.StatusForbidden, "rotation without rotate scope")
}

func TestManagementAPIKeyCreateReturnsPersistedCreatedAt(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	token, record, err := f.access.Create(ctx, f.orgID, "created-at", []string{apikey.ScopeEventsWrite}, nil)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	prefix, err := apikey.ParseToken(token)
	if err != nil {
		t.Fatalf("raw token is malformed: %v", err)
	}
	if record.ID == "" || record.OrganizationID != f.orgID || record.KeyPrefix != prefix ||
		record.HashAlgorithm != apikey.Algorithm() {
		t.Fatalf("create metadata = %+v", record)
	}
	if record.CreatedAt.IsZero() {
		t.Fatal("Create() returned a zero CreatedAt")
	}
	var persisted time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT created_at FROM api_keys WHERE organization_id = $1::uuid AND id = $2::uuid`,
		f.orgID, record.ID).Scan(&persisted); err != nil {
		t.Fatalf("read persisted created_at: %v", err)
	}
	if !record.CreatedAt.Equal(persisted) {
		t.Fatalf("Create() CreatedAt = %v, want persisted %v", record.CreatedAt, persisted)
	}
	rendered := fmt.Sprintf("%+v", record)
	if strings.Contains(rendered, "$argon2") || strings.Contains(rendered, token) {
		t.Fatal("Create() metadata exposed verifier or raw token material")
	}
}

func TestManagementEndpointLifecycle(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, keyID := f.createKey("full", "endpoints:write")

	reply := f.do(http.MethodPost, f.endpoints(), token,
		`{"name":"orders","description":"primary","url":"https://example.test/hooks",
		  "http_headers":{"X-Tenant":"acme"}}`,
		"User-Agent", "hookflow-integration")
	wantStatus(t, reply, http.StatusCreated, "create endpoint")
	created := decodeMgmt[endpointDoc](t, reply)
	if created.ID == "" || created.Name != "orders" || created.Status != "active" ||
		created.SigningScheme != "hmac_sha256" {
		t.Fatalf("created endpoint = %+v", created)
	}
	if created.Description == nil || *created.Description != "primary" {
		t.Fatalf("created description = %v", created.Description)
	}
	if created.HTTPHeaders["x-tenant"] != "acme" {
		t.Fatalf("created headers = %v, want canonical lowercase", created.HTTPHeaders)
	}
	if !strings.HasPrefix(created.Secret, "whsec_") {
		t.Fatal("create response did not carry a one-time whsec_ secret")
	}
	if bytes.Contains(reply.body, []byte("hfse1.")) || bytes.Contains(reply.body, []byte("organization_id")) ||
		bytes.Contains(reply.body, []byte("secret_encrypted")) {
		t.Fatalf("create response leaked envelope or internal fields: %s", reply.body)
	}
	if location := reply.header.Get("Location"); location != f.endpoint(created.ID) {
		t.Fatalf("Location = %q, want %q", location, f.endpoint(created.ID))
	}
	tag := reply.etag()
	if !strings.HasPrefix(tag, `"hf1-`) || !strings.HasSuffix(tag, `"`) {
		t.Fatalf("create ETag = %q", tag)
	}
	stored := f.storedSecret(created.ID)
	if !strings.HasPrefix(stored, "hfse1.") {
		t.Fatalf("stored secret does not use the v1 envelope")
	}
	if !bytes.Equal(tokenPayload(t, created.Secret), f.openSecret(created.ID)) {
		t.Fatal("stored envelope does not decrypt to the delivered secret")
	}

	createAudits := f.auditRows("endpoint.created")
	if len(createAudits) != 1 {
		t.Fatalf("endpoint.created audit rows = %d, want 1", len(createAudits))
	}
	if createAudits[0].ActorType != "api_key" || createAudits[0].ActorID == nil || *createAudits[0].ActorID != keyID {
		t.Fatalf("created audit actor = %+v", createAudits[0])
	}
	if createAudits[0].TargetID == nil || *createAudits[0].TargetID != created.ID {
		t.Fatalf("created audit target = %v", createAudits[0].TargetID)
	}
	if createAudits[0].UserAgent == nil || *createAudits[0].UserAgent != "hookflow-integration" {
		t.Fatalf("created audit user agent = %v", createAudits[0].UserAgent)
	}

	get := f.do(http.MethodGet, f.endpoint(created.ID), token, "")
	wantStatus(t, get, http.StatusOK, "get endpoint")
	if get.etag() != tag {
		t.Fatalf("GET ETag = %q, want create ETag %q", get.etag(), tag)
	}
	if got := decodeMgmt[endpointDoc](t, get); got.Secret != "" {
		t.Fatal("GET returned a secret")
	}

	wantProblem(t, f.do(http.MethodPost, f.endpoints(), token,
		`{"name":"orders","url":"https://example.test/other"}`), http.StatusConflict, "endpoint_name_conflict", "")

	invalid := []struct {
		body  string
		code  string
		field string
	}{
		{`{"name":"","url":"https://example.test/h"}`, "invalid_name", "name"},
		{`{"name":"a","url":"http://example.test/h"}`, "invalid_url", "url"},
		{`{"name":"a","url":"https://user@example.test/h"}`, "invalid_url", "url"},
		{`{"name":"a","url":"https://example.test/h","http_headers":{"Authorization":"x"}}`, "invalid_headers", "http_headers"},
		{`{"name":"a","url":"https://example.test/h","timeout_ms":5000}`, "forbidden_field", "timeout_ms"},
		{`{"name":"a","url":"https://example.test/h","signing_scheme":"hmac_sha256"}`, "forbidden_field", "signing_scheme"},
		{`{"name":"a","url":"https://example.test/h","status":"active"}`, "unknown_field", "status"},
		{`{"name":"a","url":"https://example.test/h","extra":true}`, "unknown_field", "extra"},
		{`[]`, "invalid_body", ""},
		{``, "invalid_body", ""},
	}
	for _, testCase := range invalid {
		wantProblem(t, f.do(http.MethodPost, f.endpoints(), token, testCase.body),
			http.StatusBadRequest, testCase.code, testCase.field)
	}

	wantStatus(t, f.do(http.MethodDelete, f.endpoint(created.ID), token, ""), http.StatusNoContent, "delete endpoint")
	wantProblem(t, f.do(http.MethodGet, f.endpoint(created.ID), token, ""), http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodDelete, f.endpoint(created.ID), token, ""), http.StatusNotFound, "not_found", "")
	if got := f.auditCount("endpoint.deleted"); got != 1 {
		t.Fatalf("endpoint.deleted audit rows = %d, want 1", got)
	}
}

func TestManagementPatchContract(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	createdTag := f.do(http.MethodGet, f.endpoint(created.ID), token, "").etag()

	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"x"}`),
		http.StatusPreconditionRequired, "precondition_required", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"x"}`, "If-Match", "not-a-tag"),
		http.StatusBadRequest, "invalid_if_match", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"x"}`, "If-Match", `"hf1-stale"`),
		http.StatusPreconditionFailed, "precondition_failed", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"x"}`, "If-Match", `W/`+createdTag),
		http.StatusPreconditionFailed, "precondition_failed", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{}`, "If-Match", createdTag),
		http.StatusBadRequest, "empty_patch", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"status":"deleted"}`, "If-Match", createdTag),
		http.StatusBadRequest, "invalid_status", "status")

	stale := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"x"}`, "If-Match", `"hf1-stale"`)
	if stale.etag() != createdTag {
		t.Fatalf("412 ETag = %q, want current %q", stale.etag(), createdTag)
	}

	auditsBefore := f.auditCount("endpoint.updated")
	noop := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"orders"}`, "If-Match", createdTag)
	wantStatus(t, noop, http.StatusOK, "no-op patch")
	if noop.etag() != createdTag {
		t.Fatalf("no-op ETag = %q, want %q", noop.etag(), createdTag)
	}
	if got := f.auditCount("endpoint.updated"); got != auditsBefore {
		t.Fatalf("no-op patch wrote an audit row (%d -> %d)", auditsBefore, got)
	}

	renamed := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"orders-v2"}`, "If-Match", createdTag)
	wantStatus(t, renamed, http.StatusOK, "rename patch")
	renamedTag := renamed.etag()
	if renamedTag == createdTag {
		t.Fatal("rename did not change the ETag")
	}
	updated := f.auditRows("endpoint.updated")
	if len(updated) != auditsBefore+1 {
		t.Fatalf("endpoint.updated audit rows = %d, want %d", len(updated), auditsBefore+1)
	}
	fields := metadataFields(t, updated[len(updated)-1].Metadata)
	if len(fields) != 1 || fields[0] != "name" {
		t.Fatalf("rename audit fields = %v, want [name]", fields)
	}

	reverted := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"orders"}`, "If-Match", renamedTag)
	wantStatus(t, reverted, http.StatusOK, "revert rename")
	if reverted.etag() != createdTag {
		t.Fatalf("A->B->A ETag = %q, want original %q", reverted.etag(), createdTag)
	}

	paused := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"status":"paused"}`, "If-Match", createdTag)
	wantStatus(t, paused, http.StatusOK, "pause patch")
	if decodeMgmt[endpointDoc](t, paused).Status != "paused" {
		t.Fatal("pause patch did not persist the status")
	}
	if got := f.auditCount("endpoint.paused"); got != 1 {
		t.Fatalf("endpoint.paused audit rows = %d, want 1", got)
	}
	activated := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"status":"active"}`, "If-Match", "*")
	wantStatus(t, activated, http.StatusOK, "activate patch")
	if got := f.auditCount("endpoint.activated"); got != 1 {
		t.Fatalf("endpoint.activated audit rows = %d, want 1", got)
	}

	// "*" is unconditional and matches even a stale cached tag scenario.
	byStar := f.do(http.MethodPatch, f.endpoint(created.ID), token,
		`{"description":null}`, "If-Match", "*")
	wantStatus(t, byStar, http.StatusOK, "star patch")
	if decodeMgmt[endpointDoc](t, byStar).Description != nil {
		t.Fatal("explicit null description was not applied")
	}
}

func TestManagementEndpointCapAndConcurrentActivation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{maxActive: 2})
	token, _ := f.createKey("full", "endpoints:write")
	first := f.createEndpoint(token, `{"name":"one","url":"https://example.test/one"}`)
	second := f.createEndpoint(token, `{"name":"two","url":"https://example.test/two"}`)
	wantProblem(t, f.do(http.MethodPost, f.endpoints(), token,
		`{"name":"three","url":"https://example.test/three"}`), http.StatusConflict, "active_endpoint_cap", "")

	for _, endpointID := range []string{first.ID, second.ID} {
		wantStatus(t, f.do(http.MethodPatch, f.endpoint(endpointID), token, `{"status":"paused"}`, "If-Match", "*"),
			http.StatusOK, "pause for cap test")
	}
	f.createEndpoint(token, `{"name":"three","url":"https://example.test/three"}`)

	// one is active, two is paused: activating two reaches the cap, and the
	// next activation is a conflict.
	wantStatus(t, f.do(http.MethodPatch, f.endpoint(second.ID), token, `{"status":"active"}`, "If-Match", "*"),
		http.StatusOK, "activate to the cap")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(first.ID), token, `{"status":"active"}`, "If-Match", "*"),
		http.StatusConflict, "active_endpoint_cap", "")
	wantStatus(t, f.do(http.MethodPatch, f.endpoint(second.ID), token, `{"status":"paused"}`, "If-Match", "*"),
		http.StatusOK, "pause second again")
	// Active: three. Paused: one, two. Two concurrent paused -> active
	// transitions at cap-1 must serialize under the organization lock: exactly
	// one wins, one is capped.
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, endpointID := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			statuses <- f.status(http.MethodPatch, f.endpoint(id), token, `{"status":"active"}`, "If-Match", "*")
		}(endpointID)
	}
	wg.Wait()
	close(statuses)
	results := make([]int, 0, 2)
	for status := range statuses {
		results = append(results, status)
	}
	sort.Ints(results)
	if results[0] != http.StatusOK || results[1] != http.StatusConflict {
		t.Fatalf("concurrent activation statuses = %v, want [200 409]", results)
	}
}

func TestManagementOrganizationPolicy(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write", "endpoints:secrets:rotate")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	subscription := f.do(http.MethodPost, f.endpoint(created.ID)+"/subscriptions", token, `{"event_type":"order.created"}`)
	wantStatus(t, subscription, http.StatusCreated, "seed subscription before suspension")

	setStatus := func(status string) {
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE organizations SET status = $2 WHERE id = $1::uuid`, f.orgID, status); err != nil {
			t.Fatalf("set organization status: %v", err)
		}
	}
	pause := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"status":"paused"}`, "If-Match", "*")
	wantStatus(t, pause, http.StatusOK, "pause before suspension")

	setStatus("suspended")

	wantStatus(t, f.do(http.MethodGet, f.endpoints(), token, ""), http.StatusOK, "suspended list")
	wantStatus(t, f.do(http.MethodGet, f.endpoint(created.ID), token, ""), http.StatusOK, "suspended get")
	wantStatus(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"orders-v2"}`, "If-Match", "*"),
		http.StatusOK, "suspended metadata patch")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token,
		`{"url":"https://example.test/other"}`, "If-Match", "*"), http.StatusForbidden, "organization_suspended", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token,
		`{"http_headers":{"x-a":"1"}}`, "If-Match", "*"), http.StatusForbidden, "organization_suspended", "")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"status":"active"}`, "If-Match", "*"),
		http.StatusForbidden, "organization_suspended", "")
	wantProblem(t, f.do(http.MethodPost, f.endpoints(), token,
		`{"name":"other","url":"https://example.test/o"}`), http.StatusForbidden, "organization_suspended", "")
	wantProblem(t, f.do(http.MethodPost, f.endpoint(created.ID)+"/subscriptions", token,
		`{"event_type":"order.updated"}`), http.StatusForbidden, "organization_suspended", "")
	if _, _, err := f.access.Create(context.Background(), f.orgID, "blocked", []string{"events:write"}, nil); !errors.Is(err, accesskeys.ErrOrganizationSuspended) {
		t.Fatalf("suspended api key create = %v, want ErrOrganizationSuspended", err)
	}
	// Suspension denies url, headers and activation only; rotation and
	// deactivation stay available.
	rotated := f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, "", "If-Match", "*")
	wantStatus(t, rotated, http.StatusOK, "suspended rotation")
	wantStatus(t, f.do(http.MethodDelete,
		f.endpoint(created.ID)+"/subscriptions/"+decodeMgmt[struct {
			ID string `json:"id"`
		}](t, subscription).ID, token, ""), http.StatusOK, "suspended subscription deactivation")

	setStatus("archived")
	wantStatus(t, f.do(http.MethodGet, f.endpoint(created.ID), token, ""), http.StatusOK, "archived get")
	wantProblem(t, f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"archived"}`, "If-Match", "*"),
		http.StatusForbidden, "organization_archived", "")
	wantProblem(t, f.do(http.MethodDelete, f.endpoint(created.ID), token, ""),
		http.StatusForbidden, "organization_archived", "")
	wantProblem(t, f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, "", "If-Match", "*"),
		http.StatusForbidden, "organization_archived", "")
	wantProblem(t, f.do(http.MethodPost, f.endpoints(), token,
		`{"name":"archived","url":"https://example.test/a"}`), http.StatusForbidden, "organization_archived", "")
	if _, _, err := f.access.Create(context.Background(), f.orgID, "blocked2", []string{"events:write"}, nil); !errors.Is(err, accesskeys.ErrOrganizationArchived) {
		t.Fatalf("archived api key create = %v, want ErrOrganizationArchived", err)
	}
}

func TestManagementSecretRotation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write", "endpoints:secrets:rotate")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	originalSecret := f.storedSecret(created.ID)
	originalTag := f.do(http.MethodGet, f.endpoint(created.ID), token, "").etag()

	wantProblem(t, f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, ""),
		http.StatusPreconditionRequired, "precondition_required", "")
	wantStatus(t, f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, "", "If-Match", `"hf1-stale"`),
		http.StatusPreconditionFailed, "stale rotation")
	if f.storedSecret(created.ID) != originalSecret {
		t.Fatal("stale rotation changed the stored secret")
	}

	reply := f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, "", "If-Match", originalTag)
	wantStatus(t, reply, http.StatusOK, "rotate secret")
	rotated := decodeMgmt[struct {
		Secret string `json:"secret"`
	}](t, reply)
	if !bytes.Equal(tokenPayload(t, rotated.Secret), f.openSecret(created.ID)) {
		t.Fatal("rotated envelope does not decrypt to the delivered secret")
	}
	if f.storedSecret(created.ID) == originalSecret {
		t.Fatal("rotation did not replace the stored envelope")
	}
	if reply.etag() == originalTag || reply.etag() == "" {
		t.Fatalf("rotation ETag = %q, want a new strong tag", reply.etag())
	}
	if got := f.auditCount("endpoint.secret_rotated"); got != 1 {
		t.Fatalf("endpoint.secret_rotated audit rows = %d, want 1", got)
	}
	if bytes.Contains(reply.body, []byte("hfse1.")) {
		t.Fatal("rotation response leaked the envelope")
	}
}

func TestManagementRotationReleasesLocksDuringProviderIO(t *testing.T) {
	gate := &mgmtWrapGate{entered: make(chan struct{}), release: make(chan struct{})}
	f := newMgmtFixture(t, mgmtOptions{wrap: func(inner kek.Provider) kek.Provider {
		gate.inner = inner
		return gate
	}})
	token, _ := f.createKey("full", "endpoints:write", "endpoints:secrets:rotate")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	originalSecret := f.storedSecret(created.ID)
	originalTag := f.do(http.MethodGet, f.endpoint(created.ID), token, "").etag()

	gate.armed.Store(true)
	rotation := make(chan outcome, 1)
	go func() {
		reply, err := f.roundTrip(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, "", "If-Match", originalTag)
		rotation <- outcome{reply: reply, err: err}
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("rotation never reached provider Wrap")
	}

	// No management lock may be held while the provider is blocked.
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock probe transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM organizations WHERE id = $1::uuid FOR NO KEY UPDATE NOWAIT`, f.orgID,
	).Scan(&status); err != nil {
		t.Fatalf("organization row was locked during provider I/O: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT status FROM endpoints WHERE id = $1::uuid FOR NO KEY UPDATE NOWAIT`, created.ID,
	).Scan(&status); err != nil {
		t.Fatalf("endpoint row was locked during provider I/O: %v", err)
	}
	_ = tx.Rollback(ctx)

	// A concurrent representation change must be accepted while the provider
	// is blocked, proving the rotation transaction has not started.
	changed := f.do(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"orders-v2"}`, "If-Match", originalTag)
	wantStatus(t, changed, http.StatusOK, "concurrent patch during provider I/O")

	close(gate.release)
	var finished outcome
	select {
	case finished = <-rotation:
	case <-time.After(5 * time.Second):
		t.Fatal("rotation did not finish after releasing the provider")
	}
	if finished.err != nil {
		t.Fatalf("rotation request: %v", finished.err)
	}
	reply := finished.reply
	wantProblem(t, reply, http.StatusPreconditionFailed, "precondition_failed", "")
	if reply.etag() == "" {
		t.Fatal("revalidation failure did not return the current ETag")
	}
	if f.storedSecret(created.ID) != originalSecret {
		t.Fatal("failed rotation replaced the stored secret")
	}
	if got := f.auditCount("endpoint.secret_rotated"); got != 0 {
		t.Fatalf("failed rotation wrote %d audit rows, want 0", got)
	}

	// Retrying with the fresh tag succeeds.
	retry := f.do(http.MethodPost, f.endpoint(created.ID)+"/secret/rotate", token, "", "If-Match", reply.etag())
	wantStatus(t, retry, http.StatusOK, "rotation retry")
	if got := f.auditCount("endpoint.secret_rotated"); got != 1 {
		t.Fatalf("endpoint.secret_rotated audit rows = %d, want 1", got)
	}
}

func TestManagementSubscriptions(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	path := f.endpoint(created.ID) + "/subscriptions"

	reply := f.do(http.MethodPost, path, token, `{"event_type":"order.created"}`)
	wantStatus(t, reply, http.StatusCreated, "create subscription")
	if location := reply.header.Get("Location"); !strings.HasSuffix(location, "/subscriptions/"+decodeMgmt[struct {
		ID string `json:"id"`
	}](t, reply).ID) {
		t.Fatalf("subscription Location = %q", location)
	}
	subscriptionID := decodeMgmt[struct {
		ID       string `json:"id"`
		Active   bool   `json:"is_active"`
		Endpoint string `json:"endpoint_id"`
	}](t, reply).ID

	wantProblem(t, f.do(http.MethodPost, path, token, `{"event_type":"order.created"}`),
		http.StatusConflict, "subscription_exists", "")
	wantProblem(t, f.do(http.MethodPost, path, token, `{"event_type":"order.*"}`),
		http.StatusBadRequest, "invalid_event_type", "event_type")
	wantProblem(t, f.do(http.MethodPost, path, token, `{"event_type":"Order.Created"}`),
		http.StatusBadRequest, "invalid_event_type", "event_type")
	wantProblem(t, f.do(http.MethodPost, path, token, `{"event_type":"a","extra":1}`),
		http.StatusBadRequest, "unknown_field", "extra")

	list := f.do(http.MethodGet, path, token, "")
	wantStatus(t, list, http.StatusOK, "list subscriptions")
	listBody := decodeMgmt[struct {
		Subscriptions []struct {
			ID        string `json:"id"`
			EventType string `json:"event_type"`
			IsActive  bool   `json:"is_active"`
		} `json:"subscriptions"`
	}](t, list)
	if len(listBody.Subscriptions) != 1 || !listBody.Subscriptions[0].IsActive {
		t.Fatalf("subscriptions = %+v", listBody.Subscriptions)
	}

	deactivatePath := path + "/" + subscriptionID
	deactivated := f.do(http.MethodDelete, deactivatePath, token, "")
	wantStatus(t, deactivated, http.StatusOK, "deactivate subscription")
	if decodeMgmt[struct {
		IsActive bool `json:"is_active"`
	}](t, deactivated).IsActive {
		t.Fatal("deactivation did not clear is_active")
	}
	if got := f.auditCount("subscription.deactivated"); got != 1 {
		t.Fatalf("subscription.deactivated audit rows = %d, want 1", got)
	}
	wantStatus(t, f.do(http.MethodDelete, deactivatePath, token, ""), http.StatusOK, "repeat deactivation")
	if got := f.auditCount("subscription.deactivated"); got != 1 {
		t.Fatalf("repeat deactivation wrote an audit row (%d)", got)
	}
	wantProblem(t, f.do(http.MethodDelete, path+"/"+mustID(t), token, ""), http.StatusNotFound, "not_found", "")

	reactivated := f.do(http.MethodPost, path, token, `{"event_type":"order.created"}`)
	wantStatus(t, reactivated, http.StatusOK, "reactivate subscription")
	if !decodeMgmt[struct {
		IsActive bool `json:"is_active"`
	}](t, reactivated).IsActive {
		t.Fatal("reactivation did not set is_active")
	}
	if got := f.auditCount("subscription.reactivated"); got != 1 {
		t.Fatalf("subscription.reactivated audit rows = %d, want 1", got)
	}

	// A soft-deleted endpoint keeps its subscription rows.
	wantStatus(t, f.do(http.MethodDelete, f.endpoint(created.ID), token, ""), http.StatusNoContent, "delete endpoint with subscription")
	var remaining int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM endpoint_subscriptions WHERE endpoint_id = $1::uuid`, created.ID).Scan(&remaining); err != nil {
		t.Fatalf("count preserved subscriptions: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("subscription rows after soft delete = %d, want 1", remaining)
	}
	wantProblem(t, f.do(http.MethodGet, path, token, ""), http.StatusNotFound, "not_found", "")
}

func TestManagementConcurrentSubscriptionCreate(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	path := f.endpoint(created.ID) + "/subscriptions"
	reply := f.do(http.MethodPost, path, token, `{"event_type":"order.created"}`)
	wantStatus(t, reply, http.StatusCreated, "seed subscription")
	subscriptionID := decodeMgmt[struct {
		ID string `json:"id"`
	}](t, reply).ID
	wantStatus(t, f.do(http.MethodDelete, path+"/"+subscriptionID, token, ""), http.StatusOK, "deactivate seed subscription")

	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- f.status(http.MethodPost, path, token, `{"event_type":"order.created"}`)
		}()
	}
	wg.Wait()
	close(statuses)
	results := make([]int, 0, 2)
	for status := range statuses {
		results = append(results, status)
	}
	sort.Ints(results)
	if results[0] != http.StatusOK || results[1] != http.StatusConflict {
		t.Fatalf("concurrent subscription create statuses = %v, want [200 409]", results)
	}
	if got := f.auditCount("subscription.reactivated"); got != 1 {
		t.Fatalf("subscription.reactivated audit rows = %d, want 1", got)
	}
}

func TestManagementTenantIsolation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)

	otherOrg := mustID(t)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`, otherOrg, "other-"+otherOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		if _, err := f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1::uuid`, otherOrg); err != nil {
			t.Errorf("cleanup second organization: %v", err)
		}
	})
	otherToken := f.createKeyFor(otherOrg, "other-key", "endpoints:write")

	crossPath := "/v1/organizations/" + otherOrg + "/endpoints"
	wantProblem(t, f.do(http.MethodGet, crossPath, token, ""), http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodPost, crossPath, token, `{"name":"a","url":"https://example.test/a"}`),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, "/v1/organizations/"+f.orgID+"/endpoints/"+created.ID, otherToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, "/v1/organizations/not-a-uuid/endpoints", token, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, f.endpoints()+"/not-a-uuid", token, ""),
		http.StatusNotFound, "not_found", "")
	// A syntactically valid id owned by nobody is a distinct 404 surface.
	wantProblem(t, f.do(http.MethodGet, f.endpoint(mustID(t)), token, ""), http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, f.endpoints(), otherToken, ""), http.StatusNotFound, "not_found", "")
	wantStatus(t, f.do(http.MethodGet, "/v1/organizations/"+otherOrg+"/endpoints", otherToken, ""),
		http.StatusOK, "second tenant list")
}

func TestManagementKeyShareCompatibility(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin key-share transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM organizations WHERE id = $1::uuid FOR KEY SHARE`, f.orgID).Scan(&id); err != nil {
		t.Fatalf("take organization key share: %v", err)
	}

	// The management FOR NO KEY UPDATE lock is compatible with the KEY SHARE
	// locks the delivery pipeline's FK checks take, so the patch must not block.
	done := make(chan outcome, 1)
	go func() {
		reply, err := f.roundTrip(http.MethodPatch, f.endpoint(created.ID), token, `{"name":"orders-v2"}`, "If-Match", "*")
		done <- outcome{reply: reply, err: err}
	}()
	select {
	case finished := <-done:
		if finished.err != nil {
			t.Fatalf("patch under key share: %v", finished.err)
		}
		wantStatus(t, finished.reply, http.StatusOK, "patch under concurrent key share")
	case <-time.After(5 * time.Second):
		t.Fatal("management write blocked behind a KEY SHARE lock")
	}
}

func TestManagementRoutingAndLimits(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	created := f.createEndpoint(token, `{"name":"orders","url":"https://example.test/hooks"}`)

	method := f.do(http.MethodPut, f.endpoint(created.ID), token, `{}`)
	wantStatus(t, method, http.StatusMethodNotAllowed, "unsupported method")
	if allow := method.header.Get("Allow"); allow != "GET, PATCH, DELETE" {
		t.Fatalf("Allow = %q", allow)
	}
	wantProblem(t, f.do(http.MethodGet, "/v1/unknown", token, ""), http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodPost, f.endpoints(), token, strings.Repeat("a", maxManagementBodyProbe)),
		http.StatusRequestEntityTooLarge, "payload_too_large", "")
}

const maxManagementBodyProbe = 1<<20 + 1
