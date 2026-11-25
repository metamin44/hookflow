package eventapp

import "time"

// Result is the approved 202 response payload. It is populated only after the
// ingestion transaction commits; a replay reproduces the stored values.
type Result struct {
	EventID          string
	DestinationCount int
	ReceivedAt       time.Time
	Replayed         bool
}
