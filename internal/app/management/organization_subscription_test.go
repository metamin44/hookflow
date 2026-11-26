package management

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/modules/orgsubscription"
)

func TestValidateOrganizationSubscription(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	before := start.Add(-time.Hour)

	cases := []struct {
		name  string
		input OrganizationSubscriptionInput
		field string
		code  string
	}{
		{
			name:  "valid free without period",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanFree, Status: orgsubscription.StatusActive},
		},
		{
			name: "valid pro with period and trial",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanPro, Status: orgsubscription.StatusTrialing,
				CurrentPeriodStart: &start, CurrentPeriodEnd: &end, TrialEndsAt: &end},
		},
		{
			name:  "unknown plan",
			input: OrganizationSubscriptionInput{Plan: "business", Status: orgsubscription.StatusActive},
			field: "plan", code: "invalid_plan",
		},
		{
			name:  "empty plan",
			input: OrganizationSubscriptionInput{Status: orgsubscription.StatusActive},
			field: "plan", code: "invalid_plan",
		},
		{
			name:  "canceled is not a replacement status",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanPro, Status: orgsubscription.StatusCanceled},
			field: "status", code: "invalid_status",
		},
		{
			name:  "unknown status",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanPro, Status: "paused"},
			field: "status", code: "invalid_status",
		},
		{
			name: "end without start",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanPro, Status: orgsubscription.StatusActive,
				CurrentPeriodEnd: &end},
			field: "current_period_end", code: "invalid_period",
		},
		{
			name: "end equals start",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanPro, Status: orgsubscription.StatusActive,
				CurrentPeriodStart: &start, CurrentPeriodEnd: &start},
			field: "current_period_end", code: "invalid_period",
		},
		{
			name: "end before start",
			input: OrganizationSubscriptionInput{Plan: orgsubscription.PlanPro, Status: orgsubscription.StatusActive,
				CurrentPeriodStart: &start, CurrentPeriodEnd: &before},
			field: "current_period_end", code: "invalid_period",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			appErr := validateOrganizationSubscription(testCase.input)
			if testCase.code == "" {
				if appErr != nil {
					t.Fatalf("unexpected rejection: %+v", appErr)
				}
				return
			}
			if appErr == nil {
				t.Fatal("request was accepted")
			}
			if appErr.Field != testCase.field || appErr.Code != testCase.code {
				t.Fatalf("rejection = %s/%s, want %s/%s", appErr.Field, appErr.Code, testCase.field, testCase.code)
			}
		})
	}
}

func TestChangedSubscriptionFields(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	trial := end.Add(24 * time.Hour)

	base := orgsubscription.Record{
		Plan:               orgsubscription.PlanPro,
		Status:             orgsubscription.StatusActive,
		CurrentPeriodStart: &start,
		CurrentPeriodEnd:   &end,
		TrialEndsAt:        &trial,
	}
	if fields := changedSubscriptionFields(base, base); len(fields) != 0 {
		t.Fatalf("identical records changed %v", fields)
	}

	next := base
	next.Plan = orgsubscription.PlanEnterprise
	next.Status = orgsubscription.StatusPastDue
	next.CurrentPeriodEnd = nil
	next.TrialEndsAt = nil
	want := []string{"current_period_end", "plan", "status", "trial_ends_at"}
	if fields := changedSubscriptionFields(base, next); !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}

	sameInstant := start.In(time.FixedZone("UTC+3", 3*60*60))
	next = base
	next.CurrentPeriodStart = &sameInstant
	if fields := changedSubscriptionFields(base, next); len(fields) != 0 {
		t.Fatalf("equal instant changed %v", fields)
	}
}

func TestDecodeOrganizationSubscription(t *testing.T) {
	document := map[string]json.RawMessage{
		"plan":                 json.RawMessage(`"pro"`),
		"status":               json.RawMessage(`"active"`),
		"current_period_start": json.RawMessage(`"2026-01-01T00:00:00+02:00"`),
		"current_period_end":   json.RawMessage(`"2026-02-01T00:00:00Z"`),
		"trial_ends_at":        json.RawMessage(`null`),
	}
	decoded, appErr := decodeOrganizationSubscription(document)
	if appErr != nil {
		t.Fatalf("decode: %+v", appErr)
	}
	if decoded.Plan != orgsubscription.PlanPro || decoded.Status != orgsubscription.StatusActive {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.CurrentPeriodStart == nil || decoded.CurrentPeriodStart.Location() != time.UTC {
		t.Fatalf("period start = %v, want UTC", decoded.CurrentPeriodStart)
	}
	if decoded.TrialEndsAt != nil {
		t.Fatalf("trial ends = %v, want nil", decoded.TrialEndsAt)
	}

	precise, appErr := decodeOrganizationSubscription(map[string]json.RawMessage{
		"plan":                 json.RawMessage(`"pro"`),
		"status":               json.RawMessage(`"active"`),
		"current_period_start": json.RawMessage(`"2026-01-01T00:00:00.000000500Z"`),
	})
	if appErr != nil {
		t.Fatalf("decode sub-microsecond timestamp: %+v", appErr)
	}
	if precise.CurrentPeriodStart.Nanosecond() != 0 {
		t.Fatalf("period start nanoseconds = %d, want database precision 0",
			precise.CurrentPeriodStart.Nanosecond())
	}

	cases := []struct {
		name  string
		body  string
		field string
		code  string
	}{
		{"missing plan", `{"status":"active"}`, "plan", "invalid_plan"},
		{"missing status", `{"plan":"pro"}`, "status", "invalid_status"},
		{"unknown field", `{"plan":"pro","status":"active","price":1}`, "price", "unknown_field"},
		{"forbidden id", `{"id":"x","plan":"pro","status":"active"}`, "id", "forbidden_field"},
		{"bad timestamp", `{"plan":"pro","status":"active","trial_ends_at":"tomorrow"}`, "trial_ends_at", "invalid_trial_ends_at"},
		{"null plan", `{"plan":null,"status":"active"}`, "plan", "invalid_plan"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(testCase.body), &raw); err != nil {
				t.Fatalf("test body: %v", err)
			}
			_, appErr := decodeOrganizationSubscription(raw)
			if appErr == nil {
				t.Fatal("document was accepted")
			}
			if appErr.Field != testCase.field || appErr.Code != testCase.code {
				t.Fatalf("rejection = %s/%s, want %s/%s", appErr.Field, appErr.Code, testCase.field, testCase.code)
			}
		})
	}
}
