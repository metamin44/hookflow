//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type organizationSubscriptionDoc struct {
	ID                 string     `json:"id"`
	Plan               string     `json:"plan"`
	Status             string     `json:"status"`
	CurrentPeriodStart *time.Time `json:"current_period_start"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end"`
	TrialEndsAt        *time.Time `json:"trial_ends_at"`
	CanceledAt         *time.Time `json:"canceled_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (f *mgmtFixture) subscriptionPath() string {
	return f.orgPath() + "/subscription"
}

func (f *mgmtFixture) subscriptionHistoryPath() string {
	return f.orgPath() + "/subscriptions"
}

func (f *mgmtFixture) subscriptionHistory(token string) []organizationSubscriptionDoc {
	f.t.Helper()
	reply := f.do(http.MethodGet, f.subscriptionHistoryPath(), token, "")
	wantStatus(f.t, reply, http.StatusOK, "list organization subscriptions")
	return decodeMgmt[struct {
		Subscriptions []organizationSubscriptionDoc `json:"subscriptions"`
	}](f.t, reply).Subscriptions
}

func (f *mgmtFixture) subscriptionRowState(id string) (string, bool) {
	f.t.Helper()
	var status string
	var canceled bool
	err := f.pool.QueryRow(context.Background(),
		`SELECT status, canceled_at IS NOT NULL
		 FROM organization_subscriptions
		 WHERE organization_id = $1::uuid AND id = $2::uuid`, f.orgID, id).Scan(&status, &canceled)
	if err != nil {
		f.t.Fatalf("read subscription row: %v", err)
	}
	return status, canceled
}

func TestManagementOrganizationSubscriptionLifecycle(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("billing", "billing:write")
	path := f.subscriptionPath()

	wantProblem(t, f.do(http.MethodGet, path, token, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`, "If-Match", "*"),
		http.StatusPreconditionFailed, "precondition_failed", "")

	created := f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`)
	wantStatus(t, created, http.StatusCreated, "create organization subscription")
	if location := created.header.Get("Location"); location != path {
		t.Fatalf("Location = %q, want %q", location, path)
	}
	if cache := created.header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q", cache)
	}
	first := decodeMgmt[organizationSubscriptionDoc](t, created)
	if first.ID == "" || first.Plan != "free" || first.Status != "active" {
		t.Fatalf("created subscription = %+v", first)
	}
	if first.CurrentPeriodStart != nil || first.CurrentPeriodEnd != nil || first.CanceledAt != nil {
		t.Fatalf("created optional fields = %+v", first)
	}
	firstETag := created.header.Get("ETag")
	if firstETag == "" {
		t.Fatal("create response lacks an ETag")
	}

	fetched := f.do(http.MethodGet, path, token, "")
	wantStatus(t, fetched, http.StatusOK, "get organization subscription")
	if fetched.header.Get("ETag") != firstETag {
		t.Fatalf("GET ETag = %q, want %q", fetched.header.Get("ETag"), firstETag)
	}
	if got := decodeMgmt[organizationSubscriptionDoc](t, fetched); got.ID != first.ID {
		t.Fatalf("GET id = %q, want %q", got.ID, first.ID)
	}

	history := f.subscriptionHistory(token)
	if len(history) != 1 || history[0].ID != first.ID {
		t.Fatalf("history after create = %+v", history)
	}

	wantProblem(t, f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`),
		http.StatusPreconditionRequired, "precondition_required", "")
	wantProblem(t, f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`,
		"If-Match", `"hf1-stale"`), http.StatusPreconditionFailed, "precondition_failed", "")

	noop := f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`, "If-Match", firstETag)
	wantStatus(t, noop, http.StatusOK, "no-op replacement")
	if got := decodeMgmt[organizationSubscriptionDoc](t, noop); got.ID != first.ID {
		t.Fatalf("no-op changed the id to %q", got.ID)
	}
	if noop.header.Get("ETag") != firstETag {
		t.Fatalf("no-op ETag = %q, want %q", noop.header.Get("ETag"), firstETag)
	}
	if count := f.auditCount("organization_subscription.updated"); count != 0 {
		t.Fatalf("no-op wrote %d update audit rows", count)
	}

	statusChanged := f.do(http.MethodPut, path, token, `{"plan":"free","status":"past_due"}`, "If-Match", firstETag)
	wantStatus(t, statusChanged, http.StatusOK, "status transition")
	second := decodeMgmt[organizationSubscriptionDoc](t, statusChanged)
	if second.ID != first.ID || second.Status != "past_due" {
		t.Fatalf("status transition = %+v", second)
	}
	secondETag := statusChanged.header.Get("ETag")
	if secondETag == firstETag {
		t.Fatal("status change did not move the ETag")
	}
	if count := f.auditCount("organization_subscription.updated"); count != 1 {
		t.Fatalf("status change wrote %d update audit rows", count)
	}

	replacedBody := `{"plan":"pro","status":"active","current_period_start":"2026-01-01T00:00:00Z",` +
		`"current_period_end":"2026-02-01T00:00:00Z","trial_ends_at":"2026-01-08T00:00:00Z"}`
	replaced := f.do(http.MethodPut, path, token, replacedBody, "If-Match", secondETag)
	wantStatus(t, replaced, http.StatusOK, "plan change")
	third := decodeMgmt[organizationSubscriptionDoc](t, replaced)
	if third.ID == first.ID || third.Plan != "pro" || third.Status != "active" {
		t.Fatalf("plan change = %+v", third)
	}
	if third.CurrentPeriodStart == nil || third.CurrentPeriodEnd == nil || third.TrialEndsAt == nil {
		t.Fatalf("plan change timestamps = %+v", third)
	}
	if third.CurrentPeriodStart.Location() != time.UTC {
		t.Fatalf("period start location = %v, want UTC", third.CurrentPeriodStart.Location())
	}
	if count := f.auditCount("organization_subscription.plan_changed"); count != 1 {
		t.Fatalf("plan change wrote %d plan_changed rows", count)
	}
	if status, canceled := f.subscriptionRowState(first.ID); status != "canceled" || !canceled {
		t.Fatalf("previous period = %s/canceled=%v, want canceled/true", status, canceled)
	}
	if history := f.subscriptionHistory(token); len(history) != 2 {
		t.Fatalf("history after plan change has %d rows", len(history))
	}

	invalid := []struct {
		name  string
		body  string
		code  string
		field string
	}{
		{"canceled status", `{"plan":"pro","status":"canceled"}`, "invalid_status", "status"},
		{"unknown plan", `{"plan":"business","status":"active"}`, "invalid_plan", "plan"},
		{"end without start", `{"plan":"pro","status":"active","current_period_end":"2026-02-01T00:00:00Z"}`,
			"invalid_period", "current_period_end"},
		{"end before start", `{"plan":"pro","status":"active","current_period_start":"2026-02-01T00:00:00Z",` +
			`"current_period_end":"2026-01-01T00:00:00Z"}`, "invalid_period", "current_period_end"},
		{"unknown field", `{"plan":"pro","status":"active","price":"free"}`, "unknown_field", "price"},
		{"forbidden canceled_at", `{"plan":"pro","status":"active","canceled_at":"2026-01-01T00:00:00Z"}`,
			"forbidden_field", "canceled_at"},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			wantProblem(t, f.do(http.MethodPut, path, token, testCase.body),
				http.StatusBadRequest, testCase.code, testCase.field)
		})
	}

	canceled := f.do(http.MethodDelete, path, token, "")
	wantStatus(t, canceled, http.StatusOK, "cancel organization subscription")
	canceledDoc := decodeMgmt[organizationSubscriptionDoc](t, canceled)
	if canceledDoc.ID != third.ID || canceledDoc.Status != "canceled" || canceledDoc.CanceledAt == nil {
		t.Fatalf("canceled subscription = %+v", canceledDoc)
	}
	if count := f.auditCount("organization_subscription.canceled"); count != 1 {
		t.Fatalf("cancel wrote %d audit rows", count)
	}
	wantProblem(t, f.do(http.MethodGet, path, token, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodDelete, path, token, ""),
		http.StatusNotFound, "not_found", "")

	recreated := f.do(http.MethodPut, path, token, `{"plan":"enterprise","status":"incomplete"}`)
	wantStatus(t, recreated, http.StatusCreated, "recreate after cancellation")
	if got := decodeMgmt[organizationSubscriptionDoc](t, recreated); got.ID == third.ID || got.Plan != "enterprise" {
		t.Fatalf("recreated subscription = %+v", got)
	}
	if history := f.subscriptionHistory(token); len(history) != 3 {
		t.Fatalf("history after recreation has %d rows", len(history))
	}
}

func TestManagementOrganizationSubscriptionAuthorizationAndIsolation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	writeToken, _ := f.createKey("billing-write", "billing:write")
	readToken, _ := f.createKey("billing-read", "billing:read")
	endpointsToken, _ := f.createKey("endpoints", "endpoints:write")
	eventsToken, _ := f.createKey("events", "events:write")
	path := f.subscriptionPath()

	wantStatus(t, f.do(http.MethodPut, path, writeToken, `{"plan":"free","status":"active"}`),
		http.StatusCreated, "seed subscription")

	if reply := f.do(http.MethodGet, path, "", ""); reply.status != http.StatusUnauthorized {
		t.Fatalf("anonymous GET = %d", reply.status)
	}
	wantStatus(t, f.do(http.MethodGet, path, readToken, ""), http.StatusOK, "read scope GET")
	wantStatus(t, f.do(http.MethodGet, f.subscriptionHistoryPath(), readToken, ""), http.StatusOK, "read scope history")
	wantProblem(t, f.do(http.MethodPut, path, readToken, `{"plan":"free","status":"active"}`),
		http.StatusForbidden, "insufficient_scope", "")
	wantProblem(t, f.do(http.MethodDelete, path, readToken, ""),
		http.StatusForbidden, "insufficient_scope", "")
	for _, token := range []string{endpointsToken, eventsToken} {
		wantProblem(t, f.do(http.MethodGet, path, token, ""),
			http.StatusForbidden, "insufficient_scope", "")
	}

	otherOrg := mustID(t)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		otherOrg, "billing-other-"+otherOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(),
			`DELETE FROM organizations WHERE id = $1::uuid`, otherOrg); err != nil {
			t.Errorf("cleanup second organization: %v", err)
		}
	})
	otherToken := f.createKeyFor(otherOrg, "billing-other", "billing:write")

	wantProblem(t, f.do(http.MethodGet, "/v1/organizations/"+otherOrg+"/subscription", writeToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, path, otherToken, ""),
		http.StatusNotFound, "not_found", "")
	wantStatus(t, f.do(http.MethodGet, "/v1/organizations/"+otherOrg+"/subscription", otherToken, ""),
		http.StatusNotFound, "second tenant has no subscription")
	wantProblem(t, f.do(http.MethodGet, "/v1/organizations/not-a-uuid/subscription", writeToken, ""),
		http.StatusNotFound, "not_found", "")

	wantStatus(t, f.do(http.MethodGet, f.subscriptionHistoryPath(), writeToken, ""),
		http.StatusOK, "history")
	reply := f.do(http.MethodPost, path, writeToken, `{"plan":"free","status":"active"}`)
	wantStatus(t, reply, http.StatusMethodNotAllowed, "subscription method restriction")
	if allow := reply.header.Get("Allow"); allow != "GET, PUT, DELETE" {
		t.Fatalf("Allow = %q", allow)
	}
	reply = f.do(http.MethodPut, f.subscriptionHistoryPath(), writeToken, `{"plan":"free","status":"active"}`)
	wantStatus(t, reply, http.StatusMethodNotAllowed, "history method restriction")
	if allow := reply.header.Get("Allow"); allow != "GET" {
		t.Fatalf("history Allow = %q", allow)
	}
}

func TestManagementOrganizationSubscriptionStatusPolicy(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("billing", "billing:write")
	path := f.subscriptionPath()

	wantStatus(t, f.do(http.MethodPut, path, token, `{"plan":"pro","status":"active"}`),
		http.StatusCreated, "seed subscription")

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE organizations SET status = 'suspended' WHERE id = $1::uuid`, f.orgID); err != nil {
		t.Fatalf("suspend organization: %v", err)
	}
	wantStatus(t, f.do(http.MethodGet, path, token, ""), http.StatusOK, "suspended GET")
	wantProblem(t, f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`),
		http.StatusForbidden, "organization_suspended", "")
	wantStatus(t, f.do(http.MethodDelete, path, token, ""), http.StatusOK, "suspended cancel")

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE organizations SET status = 'active' WHERE id = $1::uuid`, f.orgID); err != nil {
		t.Fatalf("reactivate organization: %v", err)
	}
	wantStatus(t, f.do(http.MethodPut, path, token, `{"plan":"pro","status":"active"}`),
		http.StatusCreated, "reseed after suspension cancel")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE organizations SET status = 'archived' WHERE id = $1::uuid`, f.orgID); err != nil {
		t.Fatalf("archive organization: %v", err)
	}
	wantStatus(t, f.do(http.MethodGet, path, token, ""), http.StatusOK, "archived GET")
	wantProblem(t, f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`),
		http.StatusForbidden, "organization_archived", "")
	wantProblem(t, f.do(http.MethodDelete, path, token, ""),
		http.StatusForbidden, "organization_archived", "")
}

func TestManagementOrganizationSubscriptionConcurrency(t *testing.T) {
	t.Run("concurrent create", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("billing", "billing:write")
		path := f.subscriptionPath()

		statuses := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses <- f.status(http.MethodPut, path, token, `{"plan":"free","status":"active"}`)
			}()
		}
		wg.Wait()
		close(statuses)
		got := make([]int, 0, 2)
		for status := range statuses {
			got = append(got, status)
		}
		created, rejected := 0, 0
		for _, status := range got {
			switch status {
			case http.StatusCreated:
				created++
			case http.StatusPreconditionRequired, http.StatusConflict:
				rejected++
			}
		}
		if created != 1 || rejected != 1 {
			t.Fatalf("concurrent create statuses = %v, want one 201 and one 428/409", got)
		}
		if history := f.subscriptionHistory(token); len(history) != 1 {
			t.Fatalf("concurrent create left %d history rows", len(history))
		}
	})

	t.Run("concurrent replace with one validator", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("billing", "billing:write")
		path := f.subscriptionPath()
		created := f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`)
		wantStatus(t, created, http.StatusCreated, "seed subscription")
		etag := created.header.Get("ETag")

		statuses := make(chan int, 2)
		var wg sync.WaitGroup
		for _, body := range []string{
			`{"plan":"pro","status":"active"}`,
			`{"plan":"enterprise","status":"active"}`,
		} {
			body := body
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses <- f.status(http.MethodPut, path, token, body, "If-Match", etag)
			}()
		}
		wg.Wait()
		close(statuses)
		accepted, stale := 0, 0
		for status := range statuses {
			switch status {
			case http.StatusOK:
				accepted++
			case http.StatusPreconditionFailed:
				stale++
			}
		}
		if accepted != 1 || stale != 1 {
			t.Fatalf("concurrent replace accepted=%d stale=%d, want one each", accepted, stale)
		}
		if history := f.subscriptionHistory(token); len(history) != 2 {
			t.Fatalf("concurrent replace left %d history rows", len(history))
		}
	})

	t.Run("concurrent cancel", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("billing", "billing:write")
		path := f.subscriptionPath()
		wantStatus(t, f.do(http.MethodPut, path, token, `{"plan":"free","status":"active"}`),
			http.StatusCreated, "seed subscription")

		statuses := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses <- f.status(http.MethodDelete, path, token, "")
			}()
		}
		wg.Wait()
		close(statuses)
		ok, missing := 0, 0
		for status := range statuses {
			switch status {
			case http.StatusOK:
				ok++
			case http.StatusNotFound:
				missing++
			}
		}
		if ok != 1 || missing != 1 {
			t.Fatalf("concurrent cancel ok=%d missing=%d, want one each", ok, missing)
		}
		if count := f.auditCount("organization_subscription.canceled"); count != 1 {
			t.Fatalf("concurrent cancel wrote %d audit rows", count)
		}
	})
}
