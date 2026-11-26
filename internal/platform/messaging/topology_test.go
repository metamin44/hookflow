package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
)

type managementRequest struct {
	method string
	path   string
	authOK bool
}

type managementFixture struct {
	mu            sync.Mutex
	existing      *managementPolicy
	namedStatus   int
	policies      []managementPolicy
	queues        []managementQueue
	exchanges     []managementExchange
	putStatus     int
	requests      []managementRequest
	putBody       []byte
	failurePath   string
	failureStatus int
	failureBody   string
}

func validManagementState() *managementFixture {
	policy := requiredDeadLetterPolicy()
	policy.Name = PolicyName
	return &managementFixture{
		existing:    &policy,
		policies:    []managementPolicy{cloneManagementPolicy(policy)},
		queues:      validManagementQueues(),
		exchanges:   validManagementExchanges(),
		namedStatus: http.StatusOK,
	}
}

func validManagementQueues() []managementQueue {
	return []managementQueue{
		{Name: DispatchQueue, Durable: true, Type: "quorum", Arguments: map[string]any{"x-queue-type": "quorum"}},
		{Name: DeadLetterQueue, Durable: true, Type: "quorum", Arguments: map[string]any{"x-queue-type": "quorum"}},
	}
}

func validManagementExchanges() []managementExchange {
	return []managementExchange{
		{Name: DispatchExchange, Durable: true, Type: "topic"},
		{Name: DeadLetterExchange, Durable: true, Type: "direct"},
	}
}

func cloneManagementPolicy(policy managementPolicy) managementPolicy {
	clone := policy
	clone.Definition = make(map[string]any, len(policy.Definition))
	for key, value := range policy.Definition {
		clone.Definition[key] = value
	}
	return clone
}

func newManagementServer(t *testing.T, state *managementFixture) *httptest.Server {
	t.Helper()
	vhostPath := url.PathEscape("team/blue")
	policyPath := "/api/policies/" + vhostPath
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, authOK := r.BasicAuth()
		authOK = authOK && username == testAMQPUsername && password == testAMQPPassword
		state.mu.Lock()
		state.requests = append(state.requests, managementRequest{method: r.Method, path: r.URL.EscapedPath(), authOK: authOK})
		if state.failurePath != "" && r.URL.EscapedPath() == state.failurePath {
			status := state.failureStatus
			body := state.failureBody
			state.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		state.mu.Unlock()
		if !authOK {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == policyPath+"/"+PolicyName:
			state.mu.Lock()
			status := state.namedStatus
			if status == 0 {
				status = http.StatusNotFound
				if state.existing != nil {
					status = http.StatusOK
				}
			}
			var existing managementPolicy
			if state.existing != nil {
				existing = *state.existing
			}
			state.mu.Unlock()
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			writeManagementJSON(t, w, status, existing)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == policyPath:
			state.mu.Lock()
			policies := state.policies
			state.mu.Unlock()
			writeManagementJSON(t, w, http.StatusOK, policies)
		case r.Method == http.MethodPut && r.URL.EscapedPath() == policyPath+"/"+PolicyName:
			body, err := io.ReadAll(io.LimitReader(r.Body, maxManagementResponseBytes+1))
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.putBody = body
			status := state.putStatus
			if status == 0 {
				status = http.StatusCreated
			}
			state.mu.Unlock()
			w.WriteHeader(status)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/queues/"+vhostPath:
			state.mu.Lock()
			queues := state.queues
			state.mu.Unlock()
			writeManagementJSON(t, w, http.StatusOK, queues)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/exchanges/"+vhostPath:
			state.mu.Lock()
			exchanges := state.exchanges
			state.mu.Unlock()
			writeManagementJSON(t, w, http.StatusOK, exchanges)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeManagementJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode management response: %v", err)
	}
}

func policyTestConfig(serverURL string) config.RabbitMQ {
	cfg := approvedMessagingConfig("amqp://"+testAMQPUsername+":"+testAMQPPassword+"@broker.example.com/team%2Fblue", serverURL)
	cfg.VHost = "/team/blue"
	return cfg
}

func TestEnsurePolicyCreatesExactPolicyAndEscapesVHost(t *testing.T) {
	state := validManagementState()
	state.existing = nil
	state.namedStatus = http.StatusNotFound
	state.policies = nil
	server := newManagementServer(t, state)

	if err := EnsurePolicy(context.Background(), policyTestConfig(server.URL)); err != nil {
		t.Fatalf("EnsurePolicy() error = %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.putBody) == 0 {
		t.Fatal("EnsurePolicy() did not create the missing policy")
	}
	wantBody, err := json.Marshal(requiredDeadLetterPolicy())
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !bytes.Equal(state.putBody, wantBody) {
		t.Fatalf("PUT body = %s, want %s", state.putBody, wantBody)
	}
	wantPaths := []string{
		"/api/policies/team%2Fblue/" + PolicyName,
		"/api/policies/team%2Fblue",
		"/api/policies/team%2Fblue/" + PolicyName,
		"/api/queues/team%2Fblue",
		"/api/exchanges/team%2Fblue",
	}
	if len(state.requests) != len(wantPaths) {
		t.Fatalf("requests = %+v, want %d requests", state.requests, len(wantPaths))
	}
	for index, request := range state.requests {
		if request.path != wantPaths[index] || !request.authOK {
			t.Errorf("request %d = %+v, want path %q with BasicAuth", index, request, wantPaths[index])
		}
	}
	if state.requests[2].method != http.MethodPut {
		t.Fatalf("policy creation method = %q, want PUT", state.requests[2].method)
	}
}

func TestEnsurePolicyAcceptsExactExistingPolicy(t *testing.T) {
	state := validManagementState()
	server := newManagementServer(t, state)
	if err := EnsurePolicy(context.Background(), policyTestConfig(server.URL)); err != nil {
		t.Fatalf("EnsurePolicy() error = %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.putBody) != 0 {
		t.Fatal("EnsurePolicy() overwrote an exact existing policy")
	}
}

func TestEnsurePolicySupportsManagementAPIPathPrefix(t *testing.T) {
	state := validManagementState()
	server := newManagementServer(t, state)
	if err := EnsurePolicy(context.Background(), policyTestConfig(server.URL+"/api")); err != nil {
		t.Fatalf("EnsurePolicy() with /api management URL error = %v", err)
	}
}

func TestEnsurePolicyRejectsSameNameMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*managementPolicy)
	}{
		{name: "pattern", mutate: func(p *managementPolicy) { p.Pattern = ".*" }},
		{name: "apply to", mutate: func(p *managementPolicy) { p.ApplyTo = "all" }},
		{name: "priority", mutate: func(p *managementPolicy) { p.Priority = json.Number("1") }},
		{name: "exchange", mutate: func(p *managementPolicy) { p.Definition["dead-letter-exchange"] = "other" }},
		{name: "routing key", mutate: func(p *managementPolicy) { p.Definition["dead-letter-routing-key"] = "other" }},
		{name: "strategy", mutate: func(p *managementPolicy) { p.Definition["dead-letter-strategy"] = "at-least-once" }},
		{name: "delivery limit", mutate: func(p *managementPolicy) { p.Definition["delivery-limit"] = 30 }},
		{name: "extra definition", mutate: func(p *managementPolicy) { p.Definition["message-ttl"] = 1000 }},
		{name: "missing definition", mutate: func(p *managementPolicy) { delete(p.Definition, "delivery-limit") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := validManagementState()
			policy := cloneManagementPolicy(*state.existing)
			tt.mutate(&policy)
			state.existing = &policy
			state.policies = []managementPolicy{cloneManagementPolicy(policy)}
			server := newManagementServer(t, state)
			err := EnsurePolicy(context.Background(), policyTestConfig(server.URL))
			if err == nil {
				t.Fatal("EnsurePolicy() accepted a mismatched same-name policy")
			}
			assertNoSecret(t, err, testAMQPUsername, testAMQPPassword)
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.putBody) != 0 {
				t.Fatal("EnsurePolicy() overwrote a mismatched same-name policy")
			}
		})
	}
}

func TestEnsurePolicyRejectsConflictingPolicies(t *testing.T) {
	for _, key := range controlledPolicyKeys {
		t.Run(key, func(t *testing.T) {
			state := validManagementState()
			state.existing = nil
			state.namedStatus = http.StatusNotFound
			state.policies = []managementPolicy{{
				Name:       "another-policy",
				Pattern:    policyDefinitionPattern,
				ApplyTo:    policyDefinitionApplyTo,
				Definition: map[string]any{key: "conflicting-value"},
				Priority:   json.Number("1"),
			}}
			server := newManagementServer(t, state)
			err := EnsurePolicy(context.Background(), policyTestConfig(server.URL))
			if err == nil {
				t.Fatal("EnsurePolicy() accepted a conflicting policy")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.putBody) != 0 {
				t.Fatal("EnsurePolicy() created a policy despite a conflict")
			}
		})
	}
}

func TestEnsurePolicyIgnoresNonConflictingPolicies(t *testing.T) {
	tests := []struct {
		name   string
		policy managementPolicy
	}{
		{name: "different queue", policy: managementPolicy{Name: "other", Pattern: `^hookflow\.dispatch\.dlq$`, ApplyTo: "queues", Definition: map[string]any{"delivery-limit": 30}}},
		{name: "exchange target", policy: managementPolicy{Name: "other", Pattern: policyDefinitionPattern, ApplyTo: "exchanges", Definition: map[string]any{"delivery-limit": 30}}},
		{name: "uncontrolled key", policy: managementPolicy{Name: "other", Pattern: policyDefinitionPattern, ApplyTo: "queues", Definition: map[string]any{"message-ttl": 1000}}},
		{name: "all target nonmatching", policy: managementPolicy{Name: "other", Pattern: `^other$`, ApplyTo: "all", Definition: map[string]any{"dead-letter-exchange": "other"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := validManagementState()
			state.existing = nil
			state.namedStatus = http.StatusNotFound
			state.policies = []managementPolicy{tt.policy}
			server := newManagementServer(t, state)
			if err := EnsurePolicy(context.Background(), policyTestConfig(server.URL)); err != nil {
				t.Fatalf("EnsurePolicy() rejected a non-conflicting policy: %v", err)
			}
		})
	}
}

func TestEnsurePolicyRejectsTopologyMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*managementFixture)
	}{
		{name: "work queue not durable", mutate: func(s *managementFixture) { s.queues[0].Durable = false }},
		{name: "DLQ not durable", mutate: func(s *managementFixture) { s.queues[1].Durable = false }},
		{name: "work queue classic", mutate: func(s *managementFixture) { s.queues[0].Type = "classic" }},
		{name: "DLQ classic", mutate: func(s *managementFixture) { s.queues[1].Type = "classic" }},
		{name: "missing queue type argument", mutate: func(s *managementFixture) { s.queues[0].Arguments = nil }},
		{name: "wrong queue type argument", mutate: func(s *managementFixture) { s.queues[0].Arguments["x-queue-type"] = "classic" }},
		{name: "unexpected queue argument", mutate: func(s *managementFixture) { s.queues[0].Arguments["x-message-ttl"] = 1000 }},
		{name: "auto delete queue", mutate: func(s *managementFixture) { s.queues[0].AutoDelete = true }},
		{name: "missing work queue", mutate: func(s *managementFixture) { s.queues = s.queues[1:] }},
		{name: "missing DLQ", mutate: func(s *managementFixture) { s.queues = s.queues[:1] }},
		{name: "dispatch exchange type", mutate: func(s *managementFixture) { s.exchanges[0].Type = "direct" }},
		{name: "DLX type", mutate: func(s *managementFixture) { s.exchanges[1].Type = "topic" }},
		{name: "dispatch exchange not durable", mutate: func(s *managementFixture) { s.exchanges[0].Durable = false }},
		{name: "DLX not durable", mutate: func(s *managementFixture) { s.exchanges[1].Durable = false }},
		{name: "internal exchange", mutate: func(s *managementFixture) { s.exchanges[0].Internal = true }},
		{name: "unexpected exchange argument", mutate: func(s *managementFixture) { s.exchanges[0].Arguments = map[string]any{"alternate-exchange": "other"} }},
		{name: "missing exchange", mutate: func(s *managementFixture) { s.exchanges = s.exchanges[1:] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := validManagementState()
			tt.mutate(state)
			server := newManagementServer(t, state)
			if err := EnsurePolicy(context.Background(), policyTestConfig(server.URL)); err == nil {
				t.Fatal("EnsurePolicy() accepted mismatched management topology")
			}
		})
	}
}

func TestEnsurePolicyManagementErrorsAreSecretSafe(t *testing.T) {
	secretBody := `{"error":"` + testAMQPUsername + ` ` + testAMQPPassword + `"}`
	tests := []struct {
		name   string
		mutate func(*managementFixture)
	}{
		{name: "server error", mutate: func(s *managementFixture) {
			s.failurePath = "/api/policies/team%2Fblue/" + PolicyName
			s.failureStatus = http.StatusInternalServerError
			s.failureBody = secretBody
		}},
		{name: "malformed JSON", mutate: func(s *managementFixture) {
			s.failurePath = "/api/policies/team%2Fblue/" + PolicyName
			s.failureStatus = http.StatusOK
			s.failureBody = "not-json"
		}},
		{name: "multiple JSON values", mutate: func(s *managementFixture) {
			s.failurePath = "/api/policies/team%2Fblue/" + PolicyName
			s.failureStatus = http.StatusOK
			s.failureBody = "{} {}"
		}},
		{name: "oversized response", mutate: func(s *managementFixture) {
			s.failurePath = "/api/policies/team%2Fblue/" + PolicyName
			s.failureStatus = http.StatusOK
			s.failureBody = `"` + strings.Repeat("x", maxManagementResponseBytes) + `"`
		}},
		{name: "creation not found", mutate: func(s *managementFixture) {
			s.existing = nil
			s.namedStatus = http.StatusNotFound
			s.policies = nil
			s.putStatus = http.StatusNotFound
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := validManagementState()
			tt.mutate(state)
			server := newManagementServer(t, state)
			err := EnsurePolicy(context.Background(), policyTestConfig(server.URL))
			if err == nil {
				t.Fatal("EnsurePolicy() accepted a management failure")
			}
			assertNoSecret(t, err, testAMQPUsername, testAMQPPassword, secretBody)
		})
	}
}

func TestEnsurePolicyRejectsInvalidEndpoints(t *testing.T) {
	validURI := "amqp://" + testAMQPUsername + ":" + testAMQPPassword + "@broker.example.com/team%2Fblue"
	managementURLs := []string{
		"",
		" http://management.example.com",
		"/relative",
		"ftp://management.example.com",
		"https://" + testAMQPUsername + ":" + testAMQPPassword + "@management.example.com",
		"http://management.example.com?query=1",
		"http://management.example.com#fragment",
		"http://management.example.com:0",
		"http://management.example.com:65536",
	}
	for _, managementURL := range managementURLs {
		t.Run(managementURL, func(t *testing.T) {
			cfg := approvedMessagingConfig(validURI, managementURL)
			err := EnsurePolicy(context.Background(), cfg)
			if err == nil {
				t.Fatal("EnsurePolicy() accepted an invalid management URL")
			}
			assertNoSecret(t, err, validURI, testAMQPUsername, testAMQPPassword)
		})
	}

	cfg := approvedMessagingConfig("http://"+testAMQPUsername+":"+testAMQPPassword+"@broker.example.com", "http://management.example.com")
	err := EnsurePolicy(context.Background(), cfg)
	if err == nil {
		t.Fatal("EnsurePolicy() accepted an invalid AMQP URI")
	}
	assertNoSecret(t, err, cfg.URI, testAMQPUsername, testAMQPPassword)
}

func TestEnsurePolicyHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := EnsurePolicy(ctx, policyTestConfig(server.URL))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("EnsurePolicy() error = %v, want context deadline exceeded", err)
	}
}

func TestEnsureTopologyRejectsUnavailableConnection(t *testing.T) {
	cfg := policyTestConfig("http://management.example.com")
	if err := EnsureTopology(context.Background(), nil, cfg); err == nil {
		t.Fatal("EnsureTopology() accepted a nil connection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := EnsureTopology(ctx, nil, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureTopology() error = %v, want context canceled", err)
	}
}
