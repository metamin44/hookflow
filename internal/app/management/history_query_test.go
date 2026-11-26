package management

import (
	"net/url"
	"testing"
)

func TestDecodeEventListQuery(t *testing.T) {
	query, appErr := decodeEventListQuery(url.Values{})
	if appErr != nil {
		t.Fatalf("default query = %v", appErr)
	}
	if query.PageSize != 50 || query.Type != "" || query.Cursor != "" {
		t.Fatalf("defaults = %+v", query)
	}

	valid := url.Values{
		"type":          {"order.created"},
		"endpoint_id":   {"0190f5b0-0000-7000-8000-000000000001"},
		"received_from": {"2026-01-01T00:00:00Z"},
		"received_to":   {"2026-01-02T00:00:00.123Z"},
		"page_size":     {"100"},
	}
	query, appErr = decodeEventListQuery(valid)
	if appErr != nil {
		t.Fatalf("valid query = %v", appErr)
	}
	if query.Type != "order.created" || query.PageSize != 100 ||
		query.ReceivedFrom == nil || query.ReceivedTo == nil {
		t.Fatalf("valid query = %+v", query)
	}

	cases := []struct {
		name  string
		key   string
		value string
		code  string
		field string
	}{
		{"unknown filter", "payload", "x", "unknown_filter", "payload"},
		{"invalid type", "type", "Order.Created", "invalid_event_type", "type"},
		{"invalid endpoint", "endpoint_id", "not-a-uuid", "invalid_endpoint_id", "endpoint_id"},
		{"uppercase endpoint", "endpoint_id", "0190F5B0-0000-7000-8000-000000000001", "invalid_endpoint_id", "endpoint_id"},
		{"invalid time", "received_from", "yesterday", "invalid_time_window", "received_from"},
		{"empty time", "received_to", "", "invalid_time_window", "received_to"},
		{"reversed window", "received_from", "2026-01-02T00:00:00Z", "invalid_time_window", "received_to"},
		{"zero page", "page_size", "0", "invalid_page_size", "page_size"},
		{"oversized page", "page_size", "101", "invalid_page_size", "page_size"},
		{"non numeric page", "page_size", "many", "invalid_page_size", "page_size"},
		{"empty cursor", "cursor", "", "invalid_cursor", "cursor"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := url.Values{testCase.key: {testCase.value}}
			if testCase.name == "reversed window" {
				values.Set("received_to", "2026-01-01T00:00:00Z")
			}
			_, appErr := decodeEventListQuery(values)
			if appErr == nil {
				t.Fatal("invalid query was accepted")
			}
			if appErr.Code != testCase.code || appErr.Field != testCase.field {
				t.Fatalf("rejection = %s/%s, want %s/%s", appErr.Code, appErr.Field, testCase.code, testCase.field)
			}
		})
	}

	if _, appErr := decodeEventListQuery(url.Values{"type": {"a", "b"}}); appErr == nil ||
		appErr.Code != "invalid_filter" {
		t.Fatalf("duplicate filter = %v", appErr)
	}
	if _, appErr := decodeEventListQuery(url.Values{"cursor": {string(make([]byte, 2049))}}); appErr == nil ||
		appErr.Code != "invalid_cursor" {
		t.Fatalf("oversized cursor = %v", appErr)
	}
}

func TestDecodeDeliveryListQuery(t *testing.T) {
	query, appErr := decodeDeliveryListQuery(url.Values{})
	if appErr != nil {
		t.Fatalf("default query = %v", appErr)
	}
	if query.PageSize != 50 {
		t.Fatalf("default page size = %d", query.PageSize)
	}
	query, appErr = decodeDeliveryListQuery(url.Values{
		"state":        {"retrying"},
		"created_from": {"2026-01-01T00:00:00Z"},
		"created_to":   {"2026-01-03T00:00:00Z"},
		"endpoint_id":  {"0190f5b0-0000-7000-8000-000000000001"},
		"page_size":    {"2"},
	})
	if appErr != nil || query.State != "retrying" || query.PageSize != 2 {
		t.Fatalf("query = %+v, err = %v", query, appErr)
	}
	if _, appErr := decodeDeliveryListQuery(url.Values{"state": {"leased"}}); appErr == nil ||
		appErr.Code != "invalid_state" {
		t.Fatalf("leased state accepted: %v", appErr)
	}
	if _, appErr := decodeDeliveryListQuery(url.Values{"received_from": {"2026-01-01T00:00:00Z"}}); appErr == nil ||
		appErr.Code != "unknown_filter" {
		t.Fatalf("event-only filter accepted: %v", appErr)
	}
}

func TestDecodeAttemptListQuery(t *testing.T) {
	cursor, pageSize, appErr := decodeAttemptListQuery(url.Values{})
	if appErr != nil || cursor != "" || pageSize != 50 {
		t.Fatalf("defaults = %q/%d/%v", cursor, pageSize, appErr)
	}
	if _, _, appErr := decodeAttemptListQuery(url.Values{"state": {"failed"}}); appErr == nil ||
		appErr.Code != "unknown_filter" {
		t.Fatalf("attempt filter accepted: %v", appErr)
	}
}

func TestPageSizeOf(t *testing.T) {
	if size, appErr := pageSizeOf(0); appErr != nil || size != 50 {
		t.Fatalf("pageSizeOf(0) = %d/%v", size, appErr)
	}
	if size, appErr := pageSizeOf(100); appErr != nil || size != 100 {
		t.Fatalf("pageSizeOf(100) = %d/%v", size, appErr)
	}
	for _, value := range []int{-1, 101} {
		if _, appErr := pageSizeOf(value); appErr == nil || appErr.Code != "invalid_page_size" {
			t.Fatalf("pageSizeOf(%d) = %v", value, appErr)
		}
	}
}

func TestCanonicalUUID(t *testing.T) {
	if !canonicalUUID("0190f5b0-0000-7000-8000-00000000000a") {
		t.Fatal("canonical UUID rejected")
	}
	for _, value := range []string{
		"",
		"0190F5B0-0000-7000-8000-00000000000A",
		"0190f5b0-0000-7000-8000-00000000000",
		"{0190f5b0-0000-7000-8000-00000000000a}",
	} {
		if canonicalUUID(value) {
			t.Fatalf("non-canonical UUID %q accepted", value)
		}
	}
}
