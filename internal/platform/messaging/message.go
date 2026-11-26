// Package messaging implements HookFlow's RabbitMQ topology and identifiers-only
// dispatch message contract.
package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Dispatch topology names and the identifiers-only schema version are fixed V1 contracts.
const (
	DispatchExchange     = "hookflow.dispatch"
	DispatchRoutingKey   = "delivery"
	DispatchQueue        = "hookflow.dispatch.q"
	DeadLetterExchange   = "hookflow.dispatch.dlx"
	DeadLetterRoutingKey = "delivery.failed"
	DeadLetterQueue      = "hookflow.dispatch.dlq"
	PolicyName           = "hookflow-dispatch-dlq"
	SchemaVersion        = 1

	publisherAppID = "hookflow-publisher"
	maxMessageSize = 16 * 1024
)

const (
	headerSchemaVersion = "x-hookflow-schema-version"
	headerDeliveryID    = "x-hookflow-delivery-id"
	headerOutboxID      = "x-hookflow-outbox-id"
	headerSequenceKey   = "x-hookflow-sequence"
)

// Envelope is the complete broker message body. Payloads and secrets remain in
// PostgreSQL; the broker carries only stable identifiers and the dispatch
// generation number.
type Envelope struct {
	SchemaVersion  int    `json:"schema_version"`
	OrganizationID string `json:"organization_id"`
	DeliveryID     string `json:"delivery_id"`
	DispatchSeq    int    `json:"dispatch_seq"`
	OutboxID       string `json:"outbox_id"`
}

// ValidateEnvelope rejects unsupported schemas, non-positive dispatch sequences,
// and identifiers that are not lowercase canonical UUID text.
func ValidateEnvelope(envelope Envelope) error {
	if envelope.SchemaVersion != SchemaVersion {
		return errors.New("unsupported dispatch schema version")
	}
	if envelope.DispatchSeq <= 0 {
		return errors.New("dispatch sequence must be positive")
	}
	for _, identifier := range []struct {
		name  string
		value string
	}{
		{name: "organization_id", value: envelope.OrganizationID},
		{name: "delivery_id", value: envelope.DeliveryID},
		{name: "outbox_id", value: envelope.OutboxID},
	} {
		if !canonicalUUID(identifier.value) {
			return errors.New(identifier.name + " must be a canonical UUID")
		}
	}
	return nil
}

// EncodePublishing validates and encodes an envelope as a persistent JSON AMQP
// publishing. Routing and mandatory publication are publisher-channel concerns.
func EncodePublishing(envelope Envelope) (amqp.Publishing, error) {
	if err := ValidateEnvelope(envelope); err != nil {
		return amqp.Publishing{}, err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return amqp.Publishing{}, errors.New("encode dispatch envelope")
	}
	return amqp.Publishing{
		ContentType:   amqp.MimeApplicationJSON,
		DeliveryMode:  amqp.Persistent,
		MessageId:     envelope.OutboxID,
		CorrelationId: envelope.DeliveryID,
		AppId:         publisherAppID,
		Headers: amqp.Table{
			headerSchemaVersion: strconv.Itoa(SchemaVersion),
			headerDeliveryID:    envelope.DeliveryID,
			headerOutboxID:      envelope.OutboxID,
			headerSequenceKey:   strconv.Itoa(envelope.DispatchSeq),
		},
		Body: body,
	}, nil
}

// ParseDelivery decodes and validates a dispatch delivery, including the
// redundancy between its body, AMQP properties, routing metadata, and headers.
func ParseDelivery(delivery amqp.Delivery) (Envelope, error) {
	if len(delivery.Body) > maxMessageSize {
		return Envelope{}, errors.New("dispatch message exceeds 16 KiB")
	}
	envelope, err := decodeEnvelope(delivery.Body)
	if err != nil {
		return Envelope{}, err
	}
	if err := validateDeliveryProperties(delivery, envelope); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// BuildOutboxPayload returns the JSON body stored in an outbox row before a
// publisher claims it.
func BuildOutboxPayload(outboxID, organizationID, deliveryID string, sequence int) (string, error) {
	envelope := Envelope{
		SchemaVersion:  SchemaVersion,
		OrganizationID: organizationID,
		DeliveryID:     deliveryID,
		DispatchSeq:    sequence,
		OutboxID:       outboxID,
	}
	if err := ValidateEnvelope(envelope); err != nil {
		return "", err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", errors.New("encode outbox dispatch payload")
	}
	return string(body), nil
}

func decodeEnvelope(body []byte) (Envelope, error) {
	if len(body) == 0 {
		return Envelope{}, errors.New("dispatch message body is empty")
	}
	if !utf8.Valid(body) {
		return Envelope{}, errors.New("dispatch message body is not valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil {
		return Envelope{}, errors.New("dispatch message body is not valid JSON")
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return Envelope{}, errors.New("dispatch message body must be a JSON object")
	}

	var envelope Envelope
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Envelope{}, errors.New("dispatch message body is not valid JSON")
		}
		name, ok := token.(string)
		if !ok {
			return Envelope{}, errors.New("dispatch message field name is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return Envelope{}, errors.New("dispatch message contains a duplicate field")
		}
		seen[name] = struct{}{}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return Envelope{}, errors.New("dispatch message body is not valid JSON")
		}
		switch name {
		case "schema_version":
			err = json.Unmarshal(raw, &envelope.SchemaVersion)
		case "organization_id":
			err = json.Unmarshal(raw, &envelope.OrganizationID)
		case "delivery_id":
			err = json.Unmarshal(raw, &envelope.DeliveryID)
		case "dispatch_seq":
			err = json.Unmarshal(raw, &envelope.DispatchSeq)
		case "outbox_id":
			err = json.Unmarshal(raw, &envelope.OutboxID)
		default:
			return Envelope{}, errors.New("dispatch message contains an unknown field")
		}
		if err != nil {
			return Envelope{}, errors.New("dispatch message field has an invalid type")
		}
	}

	closing, err := decoder.Token()
	if err != nil {
		return Envelope{}, errors.New("dispatch message body is not valid JSON")
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return Envelope{}, errors.New("dispatch message body must be a JSON object")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return Envelope{}, errors.New("dispatch message body must contain exactly one JSON value")
	}
	if err := ValidateEnvelope(envelope); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func validateDeliveryProperties(delivery amqp.Delivery, envelope Envelope) error {
	if delivery.ContentType != amqp.MimeApplicationJSON {
		return errors.New("dispatch message content type must be application/json")
	}
	if delivery.ContentEncoding != "" || delivery.DeliveryMode != amqp.Persistent ||
		delivery.Priority != 0 || delivery.CorrelationId == "" || delivery.ReplyTo != "" ||
		delivery.Expiration != "" || delivery.MessageId == "" || !delivery.Timestamp.IsZero() ||
		delivery.Type != "" || delivery.UserId != "" || delivery.AppId != publisherAppID {
		return errors.New("dispatch message properties do not match the publishing contract")
	}
	if delivery.Exchange != DispatchExchange || delivery.RoutingKey != DispatchRoutingKey {
		return errors.New("dispatch message routing metadata does not match the topology")
	}
	if delivery.MessageId != envelope.OutboxID || delivery.CorrelationId != envelope.DeliveryID {
		return errors.New("dispatch message properties do not match the envelope identifiers")
	}
	if len(delivery.Headers) < 4 {
		return errors.New("dispatch message must contain the four contract headers")
	}
	for name := range delivery.Headers {
		switch name {
		case headerSchemaVersion, headerDeliveryID, headerOutboxID, headerSequenceKey,
			"x-acquired-count", "x-delivery-count":
			// Quorum queues add their own delivery counters on redelivery. They
			// carry no application data and are not part of the four-field contract.
		default:
			return errors.New("dispatch message contains an unexpected header")
		}
	}
	if !headerSchema(delivery.Headers[headerSchemaVersion]) {
		return errors.New("dispatch schema header does not match the envelope")
	}
	if header, ok := delivery.Headers[headerDeliveryID].(string); !ok || header != envelope.DeliveryID {
		return errors.New("dispatch delivery header does not match the envelope")
	}
	if header, ok := delivery.Headers[headerOutboxID].(string); !ok || header != envelope.OutboxID {
		return errors.New("dispatch outbox header does not match the envelope")
	}
	if !headerSequence(delivery.Headers[headerSequenceKey], envelope.DispatchSeq) {
		return errors.New("dispatch sequence header does not match the envelope")
	}
	return nil
}

func headerSchema(value any) bool {
	if text, ok := value.(string); ok {
		return text == strconv.Itoa(SchemaVersion)
	}
	integer, ok := headerInteger(value)
	return ok && integer == SchemaVersion
}

func headerSequence(value any, sequence int) bool {
	if text, ok := value.(string); ok {
		return text == strconv.Itoa(sequence)
	}
	integer, ok := headerInteger(value)
	return ok && integer > 0 && integer == int64(sequence)
}

func headerInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case byte:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	default:
		return 0, false
	}
}

func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if value[index] != '-' {
				return false
			}
			continue
		}
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
