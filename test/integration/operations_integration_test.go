//go:build integration

package integration_test

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/modules/operations"
)

type operationsWindowDoc struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Seconds int64  `json:"seconds"`
}

type operationsCountsDoc struct {
	Total       int64    `json:"total"`
	Initial     int64    `json:"initial"`
	Retry       int64    `json:"retry"`
	Pending     int64    `json:"pending"`
	Retrying    int64    `json:"retrying"`
	Succeeded   int64    `json:"succeeded"`
	Failed      int64    `json:"failed"`
	Expired     int64    `json:"expired"`
	Terminal    int64    `json:"terminal"`
	SuccessRate *float64 `json:"success_rate"`
}

type operationsLatencyDoc struct {
	Attempts int64   `json:"attempts"`
	AvgMS    float64 `json:"avg_ms"`
	P95MS    float64 `json:"p95_ms"`
	Sampled  bool    `json:"sampled"`
}

type operationsRetriesDoc struct {
	Generations int64    `json:"generations"`
	Succeeded   int64    `json:"succeeded"`
	Failed      int64    `json:"failed"`
	Expired     int64    `json:"expired"`
	InProgress  int64    `json:"in_progress"`
	Terminal    int64    `json:"terminal"`
	SuccessRate *float64 `json:"success_rate"`
}

type operationsOverviewDoc struct {
	Window             operationsWindowDoc  `json:"window"`
	PreviousWindow     operationsWindowDoc  `json:"previous_window"`
	EventsAccepted     int64                `json:"events_accepted"`
	Deliveries         operationsCountsDoc  `json:"deliveries"`
	PreviousDeliveries operationsCountsDoc  `json:"previous_deliveries"`
	Latency            operationsLatencyDoc `json:"latency"`
	PreviousLatency    operationsLatencyDoc `json:"previous_latency"`
	Retries            operationsRetriesDoc `json:"retries"`
	ObservedAt         string               `json:"observed_at"`
}

type operationsEndpointDoc struct {
	EndpointID              string   `json:"endpoint_id"`
	Name                    string   `json:"name"`
	Status                  string   `json:"status"`
	Deliveries              int64    `json:"deliveries"`
	Succeeded               int64    `json:"succeeded"`
	Failed                  int64    `json:"failed"`
	Expired                 int64    `json:"expired"`
	InProgress              int64    `json:"in_progress"`
	Terminal                int64    `json:"terminal"`
	SuccessRate             *float64 `json:"success_rate"`
	ConsecutiveFailures     int64    `json:"consecutive_failures"`
	StreakCapped            bool     `json:"streak_capped"`
	LastSucceededAt         *string  `json:"last_succeeded_at"`
	LastFailedAt            *string  `json:"last_failed_at"`
	LastSucceededDeliveryID *string  `json:"last_succeeded_delivery_id"`
	LastFailedDeliveryID    *string  `json:"last_failed_delivery_id"`
}

type operationsEndpointsDoc struct {
	Window     operationsWindowDoc     `json:"window"`
	Endpoints  []operationsEndpointDoc `json:"endpoints"`
	Limit      int                     `json:"limit"`
	Truncated  bool                    `json:"truncated"`
	ObservedAt string                  `json:"observed_at"`
}

func (f *mgmtFixture) operationsToken() string {
	f.t.Helper()
	token, _ := f.createKey("operations-read", "operations:read")
	return token
}

func (f *mgmtFixture) overviewPath() string { return f.orgPath() + "/operations/overview" }
func (f *mgmtFixture) healthPath() string   { return f.orgPath() + "/operations/endpoints" }

func TestOperationalObservabilityAuthorizationAndTenancy(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	operationsToken := f.operationsToken()
	eventsToken, _ := f.createKey("events-write", "events:write")
	historyToken := f.historyToken()

	overview := f.overviewPath()
	endpoints := f.healthPath()
	if reply := f.do(http.MethodGet, overview, "", ""); reply.status != http.StatusUnauthorized {
		t.Fatalf("anonymous overview = %d, want 401", reply.status)
	}
	for _, path := range []string{overview, endpoints} {
		wantProblem(t, f.do(http.MethodGet, path, eventsToken, ""),
			http.StatusForbidden, "insufficient_scope", "")
		wantProblem(t, f.do(http.MethodGet, path, historyToken, ""),
			http.StatusForbidden, "insufficient_scope", "")
		reply := f.do(http.MethodGet, path, operationsToken, "")
		wantStatus(t, reply, http.StatusOK, "operations read "+path)
		if cache := reply.header.Get("Cache-Control"); cache != "no-store" {
			t.Fatalf("%s Cache-Control = %q", path, cache)
		}
	}

	wantProblem(t, f.do(http.MethodGet, overview+"?window=90m", operationsToken, ""),
		http.StatusBadRequest, "invalid_window", "window")
	wantProblem(t, f.do(http.MethodGet, overview+"?limit=10", operationsToken, ""),
		http.StatusBadRequest, "unknown_filter", "limit")
	wantProblem(t, f.do(http.MethodGet, endpoints+"?limit=0", operationsToken, ""),
		http.StatusBadRequest, "invalid_limit", "limit")
	wantProblem(t, f.do(http.MethodGet, endpoints+"?window=2h", operationsToken, ""),
		http.StatusBadRequest, "invalid_window", "window")
	wantProblem(t, f.do(http.MethodGet, endpoints+"?cursor=abc", operationsToken, ""),
		http.StatusBadRequest, "unknown_filter", "cursor")

	reply := f.do(http.MethodPost, overview, operationsToken, "")
	wantStatus(t, reply, http.StatusMethodNotAllowed, "operations POST")
	if allow := reply.header.Get("Allow"); allow != "GET" {
		t.Fatalf("Allow = %q, want GET", allow)
	}
	wantProblem(t, f.do(http.MethodGet, f.orgPath()+"/operations", operationsToken, ""),
		http.StatusNotFound, "not_found", "")

	otherOrg := mustID(t)
	f.cleanupSecondOrg(otherOrg)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		otherOrg, "operations-other-"+otherOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	otherToken := f.createKeyFor(otherOrg, "operations-other", "operations:read")
	wantProblem(t, f.do(http.MethodGet, "/v1/organizations/"+otherOrg+"/operations/overview", operationsToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, "/v1/organizations/"+otherOrg+"/operations/endpoints", operationsToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, overview, otherToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, endpoints, otherToken, ""),
		http.StatusNotFound, "not_found", "")
	wantStatus(t, f.do(http.MethodGet, "/v1/organizations/"+otherOrg+"/operations/overview", otherToken, ""),
		http.StatusOK, "other organization overview")
}

func TestOperationalOverviewAggregates(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token := f.operationsToken()
	endpointID := mustID(t)
	f.seedEndpoint(f.orgID, endpointID, "overview-endpoint")

	now := time.Now().UTC()
	current := func(minutesAgo int) time.Time { return now.Add(-time.Duration(minutesAgo) * time.Minute) }
	seedEvent := func(created time.Time) string {
		eventID := mustID(t)
		f.seedEvent(f.orgID, eventID, "ops.event", created, `{"secret":"PAYLOAD-SECRET"}`, 1)
		return eventID
	}
	seedRow := func(eventID, state string, generation int, created time.Time, completed *time.Time, parent *string) string {
		deliveryID := mustID(t)
		var next *time.Time
		if state == "pending" || state == "retrying" {
			next = ptr(created.Add(time.Minute))
		}
		f.seedDelivery(f.orgID, seedDelivery{ID: deliveryID, EventID: eventID, EndpointID: endpointID,
			State: state, CreatedAt: created, CompletedAt: completed, NextAttemptAt: next,
			Generation: generation, ParentID: parent})
		return deliveryID
	}

	// Five generation-1 deliveries in the current window, one Event each.
	succeededEvent := seedEvent(current(50))
	seedRow(succeededEvent, "succeeded", 1, current(50), ptr(current(49)), nil)
	failedEvent := seedEvent(current(45))
	failedID := seedRow(failedEvent, "failed", 1, current(45), ptr(current(44)), nil)
	expiredEvent := seedEvent(current(40))
	expiredID := seedRow(expiredEvent, "expired", 1, current(40), ptr(current(39)), nil)
	pendingEvent := seedEvent(current(35))
	pendingID := seedRow(pendingEvent, "pending", 1, current(35), nil, nil)
	retryingEvent := seedEvent(current(30))
	seedRow(retryingEvent, "retrying", 1, current(30), nil, nil)
	// Three retry generations reuse their parent Event, so no Event is counted
	// twice or created by a retry.
	seedRow(failedEvent, "succeeded", 2, current(25), ptr(current(24)), &failedID)
	seedRow(expiredEvent, "failed", 2, current(20), ptr(current(19)), &expiredID)
	seedRow(pendingEvent, "pending", 2, current(15), nil, &pendingID)
	_ = retryingEvent

	// Previous window: one accepted Event and one successful delivery.
	previousEvent := seedEvent(current(90))
	previousDelivery := seedRow(previousEvent, "succeeded", 1, current(90), ptr(current(89)), nil)

	// Four measured attempts on one current delivery, one in the previous window.
	attemptAt := current(48)
	attemptEvent := seedEvent(attemptAt)
	attemptDelivery := seedRow(attemptEvent, "succeeded", 1, attemptAt, ptr(attemptAt.Add(time.Second)), nil)
	for index, duration := range []int{100, 200, 300, 400} {
		f.seedAttempt(f.orgID, seedAttempt{DeliveryID: attemptDelivery, Number: index + 1,
			Outcome: "success", Duration: &duration, StartedAt: attemptAt.Add(time.Duration(index) * time.Second)})
	}
	previousDuration := 1000
	f.seedAttempt(f.orgID, seedAttempt{DeliveryID: previousDelivery, Number: 1,
		Outcome: "success", Duration: &previousDuration, StartedAt: current(89)})

	reply := f.do(http.MethodGet, f.overviewPath()+"?window=1h", token, "")
	wantStatus(t, reply, http.StatusOK, "overview")
	overview := decodeMgmt[operationsOverviewDoc](t, reply)

	if overview.Window.Seconds != 3600 || overview.PreviousWindow.Seconds != 3600 ||
		overview.Window.From != overview.PreviousWindow.To {
		t.Fatalf("windows = %+v / %+v", overview.Window, overview.PreviousWindow)
	}
	if overview.ObservedAt != overview.Window.To {
		t.Fatalf("observed_at = %q, window.to = %q", overview.ObservedAt, overview.Window.To)
	}
	// Six distinct Events were accepted in the window: five generation-1 events
	// plus one event for the attempted delivery. Retry generations add none.
	if overview.EventsAccepted != 6 {
		t.Fatalf("events_accepted = %d, want 6", overview.EventsAccepted)
	}
	assertCountsDoc(t, "deliveries", overview.Deliveries, operationsCountsDoc{
		Total: 9, Initial: 6, Retry: 3, Pending: 2, Retrying: 1,
		Succeeded: 3, Failed: 2, Expired: 1, Terminal: 6, SuccessRate: floatPtr(0.5),
	})
	assertCountsDoc(t, "previous deliveries", overview.PreviousDeliveries, operationsCountsDoc{
		Total: 1, Succeeded: 1, Terminal: 1, SuccessRate: floatPtr(1),
	})
	if overview.Retries.Generations != 3 || overview.Retries.Succeeded != 1 ||
		overview.Retries.Failed != 1 || overview.Retries.InProgress != 1 ||
		overview.Retries.Terminal != 2 || overview.Retries.SuccessRate == nil ||
		math.Abs(*overview.Retries.SuccessRate-0.5) > 1e-9 {
		t.Fatalf("retries = %+v", overview.Retries)
	}
	if overview.Latency.Attempts != 4 || overview.Latency.Sampled ||
		math.Abs(overview.Latency.AvgMS-250) > 1e-9 ||
		math.Abs(overview.Latency.P95MS-385) > 1e-9 {
		t.Fatalf("latency = %+v", overview.Latency)
	}
	if overview.PreviousLatency.Attempts != 1 ||
		math.Abs(overview.PreviousLatency.AvgMS-1000) > 1e-9 {
		t.Fatalf("previous latency = %+v", overview.PreviousLatency)
	}
	for _, secret := range []string{"PAYLOAD-SECRET", "EVENT-HEADER-SECRET", "ENVELOPE-SECRET", "receiver.invalid"} {
		if strings.Contains(string(reply.body), secret) {
			t.Fatalf("overview leaked %q: %s", secret, reply.body)
		}
	}
}

func TestOperationalEndpointHealth(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token := f.operationsToken()
	now := time.Now().UTC()
	at := func(minutesAgo int) time.Time { return now.Add(-time.Duration(minutesAgo) * time.Minute) }

	addEndpoint := func(name, status string) string {
		endpointID := mustID(t)
		f.seedEndpoint(f.orgID, endpointID, name)
		if status != "active" {
			if _, err := f.pool.Exec(context.Background(),
				`UPDATE endpoints SET status = $2 WHERE id = $1::uuid`, endpointID, status); err != nil {
				t.Fatalf("set endpoint status: %v", err)
			}
		}
		return endpointID
	}
	addDelivery := func(endpointID string, created time.Time, state string, completed *time.Time) string {
		eventID := mustID(t)
		f.seedEvent(f.orgID, eventID, "ops.health", created, `{"secret":"PAYLOAD-SECRET"}`, 1)
		deliveryID := mustID(t)
		var next *time.Time
		if state == "pending" || state == "retrying" {
			next = ptr(created.Add(time.Minute))
		}
		f.seedDelivery(f.orgID, seedDelivery{ID: deliveryID, EventID: eventID, EndpointID: endpointID,
			State: state, CreatedAt: created, CompletedAt: completed, NextAttemptAt: next})
		return deliveryID
	}

	alpha := addEndpoint("alpha", "active")
	addDelivery(alpha, at(40), "succeeded", ptr(at(39)))
	alphaLastSuccess := addDelivery(alpha, at(30), "succeeded", ptr(at(29)))
	addDelivery(alpha, at(20), "failed", ptr(at(19)))
	alphaLastFailure := addDelivery(alpha, at(10), "failed", ptr(at(9)))
	// The newest terminal delivery expired without reaching the receiver: it
	// counts against the endpoint but must not become last_failed_*.
	addDelivery(alpha, at(2), "expired", ptr(at(1)))

	bravo := addEndpoint("bravo", "active")
	bravoDelivery := addDelivery(bravo, at(5), "succeeded", ptr(at(4)))
	echo := addEndpoint("echo", "paused")
	echoDelivery := addDelivery(echo, at(8), "failed", ptr(at(7)))
	delta := addEndpoint("delta", "active")
	addDelivery(delta, at(4), "pending", nil)

	deleted := addEndpoint("charlie", "active")
	addDelivery(deleted, at(3), "failed", ptr(at(2)))
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE endpoints SET deleted_at = now() WHERE id = $1::uuid`, deleted); err != nil {
		t.Fatalf("soft-delete endpoint: %v", err)
	}
	previous := addEndpoint("november", "active")
	addDelivery(previous, at(90), "failed", ptr(at(89)))

	reply := f.do(http.MethodGet, f.healthPath()+"?window=1h", token, "")
	wantStatus(t, reply, http.StatusOK, "endpoint health")
	health := decodeMgmt[operationsEndpointsDoc](t, reply)
	if health.Limit != 50 || health.Truncated {
		t.Fatalf("paging = %d/%v", health.Limit, health.Truncated)
	}
	names := make([]string, 0, len(health.Endpoints))
	for _, endpoint := range health.Endpoints {
		names = append(names, endpoint.Name)
	}
	if strings.Join(names, ",") != "alpha,echo,bravo,delta" {
		t.Fatalf("ordering = %v", names)
	}
	alphaHealth := health.Endpoints[0]
	if alphaHealth.EndpointID != alpha || alphaHealth.Deliveries != 5 || alphaHealth.Succeeded != 2 ||
		alphaHealth.Failed != 2 || alphaHealth.Expired != 1 || alphaHealth.Terminal != 5 ||
		alphaHealth.SuccessRate == nil || math.Abs(*alphaHealth.SuccessRate-0.4) > 1e-9 {
		t.Fatalf("alpha = %+v", alphaHealth)
	}
	if alphaHealth.ConsecutiveFailures != 3 || alphaHealth.StreakCapped {
		t.Fatalf("alpha streak = %d/%v", alphaHealth.ConsecutiveFailures, alphaHealth.StreakCapped)
	}
	if alphaHealth.LastFailedDeliveryID == nil || *alphaHealth.LastFailedDeliveryID != alphaLastFailure {
		t.Fatalf("alpha last failure = %+v, want %s", alphaHealth.LastFailedDeliveryID, alphaLastFailure)
	}
	if alphaHealth.LastSucceededDeliveryID == nil || *alphaHealth.LastSucceededDeliveryID != alphaLastSuccess {
		t.Fatalf("alpha last success = %+v, want %s", alphaHealth.LastSucceededDeliveryID, alphaLastSuccess)
	}
	if alphaHealth.LastSucceededAt == nil || alphaHealth.LastFailedAt == nil {
		t.Fatalf("alpha timestamps = %+v", alphaHealth)
	}
	echoHealth := health.Endpoints[1]
	if echoHealth.EndpointID != echo || echoHealth.Status != "paused" || echoHealth.Terminal != 1 ||
		echoHealth.SuccessRate == nil || *echoHealth.SuccessRate != 0 ||
		echoHealth.ConsecutiveFailures != 1 ||
		echoHealth.LastFailedDeliveryID == nil || *echoHealth.LastFailedDeliveryID != echoDelivery {
		t.Fatalf("echo = %+v", echoHealth)
	}
	bravoHealth := health.Endpoints[2]
	if bravoHealth.EndpointID != bravo || bravoHealth.SuccessRate == nil || *bravoHealth.SuccessRate != 1 ||
		bravoHealth.ConsecutiveFailures != 0 ||
		bravoHealth.LastSucceededDeliveryID == nil || *bravoHealth.LastSucceededDeliveryID != bravoDelivery {
		t.Fatalf("bravo = %+v", bravoHealth)
	}
	deltaHealth := health.Endpoints[3]
	if deltaHealth.EndpointID != delta || deltaHealth.Terminal != 0 || deltaHealth.SuccessRate != nil ||
		deltaHealth.ConsecutiveFailures != 0 || deltaHealth.InProgress != 1 {
		t.Fatalf("delta = %+v", deltaHealth)
	}
	for _, excluded := range []string{deleted, previous} {
		if strings.Contains(string(reply.body), excluded) {
			t.Fatalf("endpoint %s must be excluded: %s", excluded, reply.body)
		}
	}
	for _, secret := range []string{"PAYLOAD-SECRET", "EVENT-HEADER-SECRET", "ENVELOPE-SECRET", "receiver.invalid"} {
		if strings.Contains(string(reply.body), secret) {
			t.Fatalf("endpoint health leaked %q: %s", secret, reply.body)
		}
	}

	limitedReply := f.do(http.MethodGet, f.healthPath()+"?window=1h&limit=1", token, "")
	wantStatus(t, limitedReply, http.StatusOK, "limited endpoint health")
	limited := decodeMgmt[operationsEndpointsDoc](t, limitedReply)
	if limited.Limit != 1 || !limited.Truncated || len(limited.Endpoints) != 1 ||
		limited.Endpoints[0].Name != "alpha" {
		t.Fatalf("limited page = %+v", limited)
	}
}

func TestOperationalWindowBoundariesAndIsolation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	endpointID := mustID(t)
	f.seedEndpoint(f.orgID, endpointID, "boundary-endpoint")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	window := operations.Window{From: base, To: base.Add(time.Hour)}
	seedAt := func(organizationID, endpoint, state string, created, completed time.Time) (string, string) {
		eventID := mustID(t)
		f.seedEvent(organizationID, eventID, "bounds.event", created, `{"ok":true}`, 1)
		deliveryID := mustID(t)
		f.seedDelivery(organizationID, seedDelivery{ID: deliveryID, EventID: eventID, EndpointID: endpoint,
			State: state, CreatedAt: created, CompletedAt: ptr(completed)})
		return eventID, deliveryID
	}
	seedAt(f.orgID, endpointID, "succeeded", base.Add(-time.Second), base.Add(-time.Second))
	_, inStart := seedAt(f.orgID, endpointID, "succeeded", base, base)
	_, inEnd := seedAt(f.orgID, endpointID, "failed", base.Add(59*time.Minute+59*time.Second), base.Add(time.Hour))
	_, atEnd := seedAt(f.orgID, endpointID, "succeeded", base.Add(time.Hour), base.Add(time.Hour))
	_ = atEnd

	otherOrg := mustID(t)
	f.cleanupSecondOrg(otherOrg)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		otherOrg, "bounds-other-"+otherOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	otherEndpoint := mustID(t)
	f.seedEndpoint(otherOrg, otherEndpoint, "other-endpoint")
	seedAt(otherOrg, otherEndpoint, "succeeded", base.Add(time.Minute), base.Add(time.Minute))

	for index, duration := range []int{100, 200} {
		f.seedAttempt(f.orgID, seedAttempt{DeliveryID: inStart, Number: index + 1,
			Outcome: "success", Duration: &duration, StartedAt: base.Add(time.Duration(index) * time.Second)})
	}
	excludedDuration := 500
	f.seedAttempt(f.orgID, seedAttempt{DeliveryID: inEnd, Number: 1,
		Outcome: "terminal_failure", Duration: &excludedDuration, StartedAt: base.Add(time.Hour)})

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	overview, err := operations.ReadOverview(ctx, f.pool, f.orgID, window)
	if err != nil {
		t.Fatalf("read overview: %v", err)
	}
	if overview.EventsAccepted != 2 {
		t.Fatalf("events_accepted = %d, want 2 (half-open window)", overview.EventsAccepted)
	}
	assertCounts(t, "boundary deliveries", overview.Deliveries, operationsCountsDoc{
		Total: 2, Initial: 2, Succeeded: 1, Failed: 1, Terminal: 2, SuccessRate: floatPtr(0.5)})
	if overview.PreviousDeliveries.Total != 1 || overview.PreviousDeliveries.Succeeded != 1 {
		t.Fatalf("previous = %+v", overview.PreviousDeliveries)
	}
	if overview.Latency.Attempts != 2 || math.Abs(overview.Latency.AvgMS-150) > 1e-9 {
		t.Fatalf("latency = %+v (attempt at the exclusive bound must be excluded)", overview.Latency)
	}

	health, truncated, err := operations.ReadEndpoints(ctx, f.pool, f.orgID, window, 10)
	if err != nil {
		t.Fatalf("read endpoint health: %v", err)
	}
	if truncated || len(health) != 1 || health[0].ID != endpointID ||
		health[0].Deliveries != 2 || health[0].Succeeded != 1 || health[0].Failed != 1 {
		t.Fatalf("health = %+v truncated=%v", health, truncated)
	}
	if health[0].LastSucceededDeliveryID == nil || *health[0].LastSucceededDeliveryID != inStart {
		t.Fatalf("last success within window = %+v", health[0].LastSucceededDeliveryID)
	}
	if health[0].LastFailedDeliveryID == nil || *health[0].LastFailedDeliveryID != inEnd {
		t.Fatalf("last failure within window = %+v", health[0].LastFailedDeliveryID)
	}

	emptyOrg := mustID(t)
	f.cleanupSecondOrg(emptyOrg)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		emptyOrg, "bounds-empty-"+emptyOrg); err != nil {
		t.Fatalf("seed empty organization: %v", err)
	}
	emptyOverview, err := operations.ReadOverview(ctx, f.pool, emptyOrg, window)
	if err != nil {
		t.Fatalf("read empty overview: %v", err)
	}
	if emptyOverview.EventsAccepted != 0 || emptyOverview.Deliveries.Total != 0 ||
		emptyOverview.Deliveries.SuccessRate() != nil || emptyOverview.Latency.Attempts != 0 ||
		emptyOverview.PreviousLatency.Attempts != 0 || emptyOverview.Retries.Generations != 0 {
		t.Fatalf("empty overview = %+v", emptyOverview)
	}
	emptyHealth, emptyTruncated, err := operations.ReadEndpoints(ctx, f.pool, emptyOrg, window, 10)
	if err != nil {
		t.Fatalf("read empty health: %v", err)
	}
	if len(emptyHealth) != 0 || emptyTruncated {
		t.Fatalf("empty health = %+v truncated=%v", emptyHealth, emptyTruncated)
	}
	emptyToken := f.createKeyFor(emptyOrg, "empty-operations", "operations:read")
	emptyReply := f.do(http.MethodGet, "/v1/organizations/"+emptyOrg+"/operations/overview?window=30d",
		emptyToken, "")
	wantStatus(t, emptyReply, http.StatusOK, "empty overview over HTTP")
	emptyDoc := decodeMgmt[operationsOverviewDoc](t, emptyReply)
	if emptyDoc.Deliveries.SuccessRate != nil || emptyDoc.EventsAccepted != 0 {
		t.Fatalf("empty HTTP overview = %+v", emptyDoc)
	}
	emptyHealthReply := f.do(http.MethodGet, "/v1/organizations/"+emptyOrg+"/operations/endpoints",
		f.createKeyFor(emptyOrg, "empty-health", "operations:read"), "")
	wantStatus(t, emptyHealthReply, http.StatusOK, "empty health over HTTP")
	if !strings.Contains(string(emptyHealthReply.body), `"endpoints":[]`) {
		t.Fatalf("empty endpoints must be an empty array: %s", emptyHealthReply.body)
	}
}

func TestOperationalLatencySampleIsBounded(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	endpointID := mustID(t)
	f.seedEndpoint(f.orgID, endpointID, "latency-endpoint")
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	eventID := mustID(t)
	f.seedEvent(f.orgID, eventID, "latency.event", base, `{"ok":true}`, 1)
	deliveryID := mustID(t)
	f.seedDelivery(f.orgID, seedDelivery{ID: deliveryID, EventID: eventID, EndpointID: endpointID,
		State: "succeeded", CreatedAt: base, CompletedAt: ptr(base.Add(time.Minute))})
	started := base.Add(2 * time.Minute)

	insertCtx, insertCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer insertCancel()
	if _, err := f.pool.Exec(insertCtx, `
		INSERT INTO delivery_attempts
		     (id, organization_id, delivery_id, attempt_number, request_method, request_url,
		      request_headers, outcome, duration_ms, started_at, finished_at)
		SELECT gen_random_uuid(), $1::uuid, $2::uuid, series,
		       'POST', 'https://receiver.invalid/hook', '{}'::jsonb, 'success', 100,
		       $3::timestamptz, $3::timestamptz + interval '1 second'
		  FROM generate_series(1, 10001) AS series`,
		f.orgID, deliveryID, started); err != nil {
		t.Fatalf("seed bounded attempts: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	window := operations.Window{From: base, To: base.Add(time.Hour)}
	overview, err := operations.ReadOverview(ctx, f.pool, f.orgID, window)
	if err != nil {
		t.Fatalf("read overview: %v", err)
	}
	if !overview.Latency.Sampled || overview.Latency.Attempts != 10000 ||
		math.Abs(overview.Latency.AvgMS-100) > 1e-9 ||
		math.Abs(overview.Latency.P95MS-100) > 1e-9 {
		t.Fatalf("bounded latency = %+v", overview.Latency)
	}
}

func assertCountsDoc(t *testing.T, label string, got operationsCountsDoc, want operationsCountsDoc) {
	t.Helper()
	if got.Total != want.Total || got.Initial != want.Initial || got.Retry != want.Retry ||
		got.Pending != want.Pending || got.Retrying != want.Retrying ||
		got.Succeeded != want.Succeeded || got.Failed != want.Failed || got.Expired != want.Expired ||
		got.Terminal != want.Terminal {
		t.Fatalf("%s = %+v, want %+v", label, got, want)
	}
	compareRate(t, label, got.SuccessRate, want.SuccessRate)
}

func assertCounts(t *testing.T, label string, got operations.DeliveryCounts, want operationsCountsDoc) {
	t.Helper()
	if got.Total != want.Total || got.Initial != want.Initial || got.Retry != want.Retry ||
		got.Pending != want.Pending || got.Retrying != want.Retrying ||
		got.Succeeded != want.Succeeded || got.Failed != want.Failed || got.Expired != want.Expired ||
		got.Terminal() != want.Terminal {
		t.Fatalf("%s = %+v, want %+v", label, got, want)
	}
	compareRate(t, label, got.SuccessRate(), want.SuccessRate)
}

func compareRate(t *testing.T, label string, got, want *float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s success rate = %v, want null", label, *got)
		}
		return
	}
	if got == nil || math.Abs(*got-*want) > 1e-9 {
		t.Fatalf("%s success rate = %v, want %v", label, got, *want)
	}
}

func floatPtr(value float64) *float64 { return &value }
