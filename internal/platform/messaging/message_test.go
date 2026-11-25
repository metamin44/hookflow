package messaging

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func testEnvelope() Envelope {
	return Envelope{
		SchemaVersion:  SchemaVersion,
		OrganizationID: "01934f5b-9c7a-7000-8000-000000000001",
		DeliveryID:     "01934f5b-9c7a-7000-8000-000000000002",
		DispatchSeq:    7,
		OutboxID:       "01934f5b-9c7a-7000-8000-000000000003",
	}
}

func deliveryFromPublishing(t *testing.T, publishing amqp.Publishing) amqp.Delivery {
	t.Helper()
	return amqp.Delivery{
		Headers:         publishing.Headers,
		ContentType:     publishing.ContentType,
		ContentEncoding: publishing.ContentEncoding,
		DeliveryMode:    publishing.DeliveryMode,
		Priority:        publishing.Priority,
		CorrelationId:   publishing.CorrelationId,
		ReplyTo:         publishing.ReplyTo,
		Expiration:      publishing.Expiration,
		MessageId:       publishing.MessageId,
		Timestamp:       publishing.Timestamp,
		Type:            publishing.Type,
		UserId:          publishing.UserId,
		AppId:           publishing.AppId,
		Exchange:        DispatchExchange,
		RoutingKey:      DispatchRoutingKey,
		Body:            publishing.Body,
	}
}

func validDelivery(t *testing.T) amqp.Delivery {
	t.Helper()
	publishing, err := EncodePublishing(testEnvelope())
	if err != nil {
		t.Fatalf("EncodePublishing() error = %v", err)
	}
	return deliveryFromPublishing(t, publishing)
}

func TestValidateEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{name: "wrong schema", mutate: func(e *Envelope) { e.SchemaVersion = 2 }},
		{name: "zero sequence", mutate: func(e *Envelope) { e.DispatchSeq = 0 }},
		{name: "negative sequence", mutate: func(e *Envelope) { e.DispatchSeq = -1 }},
		{name: "empty organization", mutate: func(e *Envelope) { e.OrganizationID = "" }},
		{name: "empty delivery", mutate: func(e *Envelope) { e.DeliveryID = "" }},
		{name: "empty outbox", mutate: func(e *Envelope) { e.OutboxID = "" }},
		{name: "uppercase UUID", mutate: func(e *Envelope) { e.OrganizationID = strings.ToUpper(e.OrganizationID) }},
		{name: "unhyphenated UUID", mutate: func(e *Envelope) { e.DeliveryID = strings.ReplaceAll(e.DeliveryID, "-", "") }},
		{name: "non-hex UUID", mutate: func(e *Envelope) { e.OutboxID = "g" + e.OutboxID[1:] }},
		{name: "UUID whitespace", mutate: func(e *Envelope) { e.OutboxID = " " + e.OutboxID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := testEnvelope()
			tt.mutate(&envelope)
			if err := ValidateEnvelope(envelope); err == nil {
				t.Fatal("ValidateEnvelope() accepted invalid envelope")
			}
		})
	}

	if err := ValidateEnvelope(testEnvelope()); err != nil {
		t.Fatalf("ValidateEnvelope() error = %v", err)
	}
}

func TestEncodePublishingAndBuildOutboxPayload(t *testing.T) {
	envelope := testEnvelope()
	publishing, err := EncodePublishing(envelope)
	if err != nil {
		t.Fatalf("EncodePublishing() error = %v", err)
	}
	wantBody, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !bytes.Equal(publishing.Body, wantBody) {
		t.Fatalf("body = %s, want %s", publishing.Body, wantBody)
	}
	if publishing.ContentType != "application/json" || publishing.DeliveryMode != amqp.Persistent {
		t.Fatalf("unexpected publishing properties: content type %q, mode %d", publishing.ContentType, publishing.DeliveryMode)
	}
	if publishing.MessageId != envelope.OutboxID || publishing.CorrelationId != envelope.DeliveryID || publishing.AppId != "hookflow-publisher" {
		t.Fatalf("unexpected correlation properties: %+v", publishing)
	}
	wantHeaders := amqp.Table{
		"x-hookflow-schema-version": "1",
		"x-hookflow-delivery-id":    envelope.DeliveryID,
		"x-hookflow-outbox-id":      envelope.OutboxID,
		"x-hookflow-sequence":       strconv.Itoa(envelope.DispatchSeq),
	}
	if !reflect.DeepEqual(publishing.Headers, wantHeaders) {
		t.Fatalf("headers = %#v, want %#v", publishing.Headers, wantHeaders)
	}

	payload, err := BuildOutboxPayload(envelope.OutboxID, envelope.OrganizationID, envelope.DeliveryID, envelope.DispatchSeq)
	if err != nil {
		t.Fatalf("BuildOutboxPayload() error = %v", err)
	}
	if payload != string(wantBody) {
		t.Fatalf("payload = %q, want %q", payload, wantBody)
	}
	if _, err := BuildOutboxPayload("bad", envelope.OrganizationID, envelope.DeliveryID, envelope.DispatchSeq); err == nil {
		t.Fatal("BuildOutboxPayload() accepted an invalid outbox ID")
	}
}

func TestParseDeliveryRoundTripAndScalarHeaders(t *testing.T) {
	envelope := testEnvelope()
	publishing, err := EncodePublishing(envelope)
	if err != nil {
		t.Fatalf("EncodePublishing() error = %v", err)
	}
	tests := []struct {
		name     string
		schema   any
		sequence any
	}{
		{name: "strings", schema: "1", sequence: "7"},
		{name: "int32", schema: int32(1), sequence: int32(7)},
		{name: "int64", schema: int64(1), sequence: int64(7)},
		{name: "uint32", schema: uint32(1), sequence: uint32(7)},
		{name: "native int", schema: int(1), sequence: int(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := publishing
			candidate.Headers = amqp.Table{
				"x-hookflow-schema-version": tt.schema,
				"x-hookflow-delivery-id":    envelope.DeliveryID,
				"x-hookflow-outbox-id":      envelope.OutboxID,
				"x-hookflow-sequence":       tt.sequence,
			}
			got, err := ParseDelivery(deliveryFromPublishing(t, candidate))
			if err != nil {
				t.Fatalf("ParseDelivery() error = %v", err)
			}
			if got != envelope {
				t.Fatalf("ParseDelivery() = %+v, want %+v", got, envelope)
			}
		})
	}
}

func TestParseDeliveryAllowsOnlyBrokerOwnedQuorumCounters(t *testing.T) {
	delivery := validDelivery(t)
	delivery.Headers["x-acquired-count"] = int64(1)
	delivery.Headers["x-delivery-count"] = int64(1)
	if _, err := ParseDelivery(delivery); err != nil {
		t.Fatalf("ParseDelivery() rejected broker-owned quorum counters: %v", err)
	}

	delivery.Headers["x-hookflow-payload"] = "secret"
	if _, err := ParseDelivery(delivery); err == nil {
		t.Fatal("ParseDelivery() accepted an additional application header")
	}
}

func TestParseDeliveryRejectsMalformedBodies(t *testing.T) {
	validBody := validDelivery(t).Body
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: nil},
		{name: "too large", body: bytes.Repeat([]byte(" "), 16*1024+1)},
		{name: "null", body: []byte("null")},
		{name: "array", body: []byte("[]")},
		{name: "multiple values", body: append(append([]byte(nil), validBody...), []byte(" {}")...)},
		{name: "trailing junk", body: append(append([]byte(nil), validBody...), []byte(" junk")...)},
		{name: "truncated", body: validBody[:len(validBody)-1]},
		{name: "unknown field", body: []byte(`{"schema_version":1,"organization_id":"01934f5b-9c7a-7000-8000-000000000001","delivery_id":"01934f5b-9c7a-7000-8000-000000000002","dispatch_seq":7,"outbox_id":"01934f5b-9c7a-7000-8000-000000000003","payload":"secret"}`)},
		{name: "duplicate field", body: []byte(`{"schema_version":1,"schema_version":1,"organization_id":"01934f5b-9c7a-7000-8000-000000000001","delivery_id":"01934f5b-9c7a-7000-8000-000000000002","dispatch_seq":7,"outbox_id":"01934f5b-9c7a-7000-8000-000000000003"}`)},
		{name: "escaped duplicate field", body: []byte(`{"schema_version":1,"organization_id":"01934f5b-9c7a-7000-8000-000000000001","\u006frganization_id":"01934f5b-9c7a-7000-8000-000000000001","delivery_id":"01934f5b-9c7a-7000-8000-000000000002","dispatch_seq":7,"outbox_id":"01934f5b-9c7a-7000-8000-000000000003"}`)},
		{name: "missing field", body: []byte(`{"schema_version":1,"organization_id":"01934f5b-9c7a-7000-8000-000000000001","delivery_id":"01934f5b-9c7a-7000-8000-000000000002","dispatch_seq":7}`)},
		{name: "fractional sequence", body: bytes.Replace(validBody, []byte(`"dispatch_seq":7`), []byte(`"dispatch_seq":7.5`), 1)},
		{name: "string sequence", body: bytes.Replace(validBody, []byte(`"dispatch_seq":7`), []byte(`"dispatch_seq":"7"`), 1)},
		{name: "zero sequence", body: bytes.Replace(validBody, []byte(`"dispatch_seq":7`), []byte(`"dispatch_seq":0`), 1)},
		{name: "wrong schema", body: bytes.Replace(validBody, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)},
		{name: "uppercase UUID", body: bytes.Replace(validBody, []byte("01934f5b-9c7a-7000-8000-000000000001"), []byte("01934F5B-9C7A-7000-8000-000000000001"), 1)},
		{name: "invalid UTF-8", body: append([]byte(`{"schema_version":1,"organization_id":"`), 0xff)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delivery := validDelivery(t)
			delivery.Body = tt.body
			if _, err := ParseDelivery(delivery); err == nil {
				t.Fatal("ParseDelivery() accepted malformed body")
			}
		})
	}

	t.Run("maximum size with whitespace", func(t *testing.T) {
		delivery := validDelivery(t)
		padding := 16*1024 - len(delivery.Body)
		delivery.Body = append(bytes.Repeat([]byte(" "), padding), delivery.Body...)
		if _, err := ParseDelivery(delivery); err != nil {
			t.Fatalf("ParseDelivery() rejected a 16 KiB body: %v", err)
		}
	})
}

func TestParseDeliveryRejectsPropertyAndHeaderMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*amqp.Delivery)
	}{
		{name: "content type", mutate: func(d *amqp.Delivery) { d.ContentType = "text/plain" }},
		{name: "content encoding", mutate: func(d *amqp.Delivery) { d.ContentEncoding = "gzip" }},
		{name: "delivery mode", mutate: func(d *amqp.Delivery) { d.DeliveryMode = amqp.Transient }},
		{name: "message ID", mutate: func(d *amqp.Delivery) { d.MessageId = "01934f5b-9c7a-7000-8000-000000000004" }},
		{name: "correlation ID", mutate: func(d *amqp.Delivery) { d.CorrelationId = "01934f5b-9c7a-7000-8000-000000000004" }},
		{name: "application ID", mutate: func(d *amqp.Delivery) { d.AppId = "other" }},
		{name: "reply to", mutate: func(d *amqp.Delivery) { d.ReplyTo = "secret" }},
		{name: "expiration", mutate: func(d *amqp.Delivery) { d.Expiration = "1000" }},
		{name: "priority", mutate: func(d *amqp.Delivery) { d.Priority = 1 }},
		{name: "timestamp", mutate: func(d *amqp.Delivery) { d.Timestamp = time.Unix(1, 0) }},
		{name: "type", mutate: func(d *amqp.Delivery) { d.Type = "other" }},
		{name: "user ID", mutate: func(d *amqp.Delivery) { d.UserId = "other" }},
		{name: "exchange", mutate: func(d *amqp.Delivery) { d.Exchange = "other" }},
		{name: "routing key", mutate: func(d *amqp.Delivery) { d.RoutingKey = "other" }},
		{name: "missing schema header", mutate: func(d *amqp.Delivery) { delete(d.Headers, "x-hookflow-schema-version") }},
		{name: "missing delivery header", mutate: func(d *amqp.Delivery) { delete(d.Headers, "x-hookflow-delivery-id") }},
		{name: "missing outbox header", mutate: func(d *amqp.Delivery) { delete(d.Headers, "x-hookflow-outbox-id") }},
		{name: "missing sequence header", mutate: func(d *amqp.Delivery) { delete(d.Headers, "x-hookflow-sequence") }},
		{name: "schema header mismatch", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-schema-version"] = "2" }},
		{name: "delivery header mismatch", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-delivery-id"] = "01934f5b-9c7a-7000-8000-000000000004" }},
		{name: "outbox header mismatch", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-outbox-id"] = "01934f5b-9c7a-7000-8000-000000000004" }},
		{name: "sequence header mismatch", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-sequence"] = "8" }},
		{name: "sequence noncanonical string", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-sequence"] = "07" }},
		{name: "sequence float", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-sequence"] = float64(7) }},
		{name: "schema bool", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-schema-version"] = true }},
		{name: "delivery bytes", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-delivery-id"] = []byte(testEnvelope().DeliveryID) }},
		{name: "outbox table", mutate: func(d *amqp.Delivery) { d.Headers["x-hookflow-outbox-id"] = amqp.Table{"id": testEnvelope().OutboxID} }},
		{name: "unexpected header", mutate: func(d *amqp.Delivery) { d.Headers["payload"] = "secret" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delivery := validDelivery(t)
			tt.mutate(&delivery)
			if _, err := ParseDelivery(delivery); err == nil {
				t.Fatal("ParseDelivery() accepted mismatched delivery")
			}
		})
	}
}
