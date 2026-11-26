package subscription

import (
	"strings"
	"testing"
)

func TestValidateEventType(t *testing.T) {
	valid := []string{
		"order.created",
		"order_created",
		"order-created",
		"order.created.v2",
		"a",
		"0",
		strings.Repeat("a", MaxEventTypeLength),
	}
	for _, eventType := range valid {
		if !ValidateEventType(eventType) {
			t.Fatalf("ValidateEventType(%q) = false, want true", eventType)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", MaxEventTypeLength+1),
		"order.*",
		"order.created*",
		"order created",
		"Order.Created",
		"order/created",
		"order.created!",
		"order.created\n",
		"#",
		"*",
	}
	for _, eventType := range invalid {
		if ValidateEventType(eventType) {
			t.Fatalf("ValidateEventType(%q) = true, want false", eventType)
		}
	}
}
