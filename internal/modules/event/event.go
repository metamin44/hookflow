package event

import "time"

// Record is one committed events row. Payload is only read when the row is
// inserted; replays read the frozen response columns.
type Record struct {
	ID               string
	OrganizationID   string
	Type             string
	Payload          []byte
	DestinationCount int
	ReceivedAt       time.Time
}

// Destination is one matched endpoint/subscription pair. It carries exactly the
// snapshot columns a Delivery copies (U33).
type Destination struct {
	SubscriptionID  string
	EndpointID      string
	URL             string
	Headers         string
	SecretEncrypted string
}
