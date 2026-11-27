package management

import (
	"net/url"
	"testing"
	"time"
)

func TestDecodeOverviewQuery(t *testing.T) {
	window, appErr := decodeOverviewQuery(url.Values{})
	if appErr != nil {
		t.Fatalf("default query = %v", appErr)
	}
	if window != 24*time.Hour {
		t.Fatalf("default window = %s, want 24h", window)
	}

	valid := map[string]time.Duration{
		"1h":  time.Hour,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
	}
	for value, want := range valid {
		window, appErr := decodeOverviewQuery(url.Values{"window": {value}})
		if appErr != nil {
			t.Fatalf("window %q rejected: %v", value, appErr)
		}
		if window != want {
			t.Fatalf("window %q = %s, want %s", value, window, want)
		}
	}

	cases := []struct {
		name  string
		key   string
		value string
		code  string
		field string
	}{
		{"unknown filter", "since", "2026-01-01T00:00:00Z", "unknown_filter", "since"},
		{"limit is not accepted", "limit", "10", "unknown_filter", "limit"},
		{"empty window", "window", "", "invalid_window", "window"},
		{"unknown window", "window", "90m", "invalid_window", "window"},
		{"oversized window", "window", "31d", "invalid_window", "window"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, appErr := decodeOverviewQuery(url.Values{testCase.key: {testCase.value}})
			wantProblemCode(t, appErr, testCase.code, testCase.field)
		})
	}

	if _, appErr := decodeOverviewQuery(url.Values{"window": {"1h", "24h"}}); appErr == nil ||
		appErr.Code != "invalid_filter" {
		t.Fatalf("duplicate window = %+v", appErr)
	}
}

func TestDecodeEndpointHealthQuery(t *testing.T) {
	window, limit, appErr := decodeEndpointHealthQuery(url.Values{})
	if appErr != nil {
		t.Fatalf("default query = %v", appErr)
	}
	if window != 24*time.Hour || limit != 50 {
		t.Fatalf("defaults = %s/%d", window, limit)
	}

	for value, want := range map[string]int{"1": 1, "100": 100} {
		_, limit, appErr := decodeEndpointHealthQuery(url.Values{"limit": {value}})
		if appErr != nil {
			t.Fatalf("limit %q rejected: %v", value, appErr)
		}
		if limit != want {
			t.Fatalf("limit %q = %d, want %d", value, limit, want)
		}
	}

	cases := []struct {
		name  string
		key   string
		value string
		code  string
		field string
	}{
		{"zero limit", "limit", "0", "invalid_limit", "limit"},
		{"oversized limit", "limit", "101", "invalid_limit", "limit"},
		{"non numeric limit", "limit", "many", "invalid_limit", "limit"},
		{"invalid window", "window", "2h", "invalid_window", "window"},
		{"unknown filter", "cursor", "abc", "unknown_filter", "cursor"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, appErr := decodeEndpointHealthQuery(url.Values{testCase.key: {testCase.value}})
			wantProblemCode(t, appErr, testCase.code, testCase.field)
		})
	}
}

func wantProblemCode(t *testing.T, appErr *HTTPError, code, field string) {
	t.Helper()
	if appErr == nil {
		t.Fatalf("invalid query was accepted, want %s", code)
	}
	if appErr.Code != code || appErr.Field != field {
		t.Fatalf("problem = %s/%s, want %s/%s", appErr.Code, appErr.Field, code, field)
	}
	if appErr.Status != 400 {
		t.Fatalf("status = %d, want 400", appErr.Status)
	}
}
