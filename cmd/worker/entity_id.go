// Command worker runs the delivery consumer, Recovery scheduler and idempotency sweep.
package main

import "github.com/metamin44/hookflow/internal/platform/id"

func newEntityID() (string, error) {
	return id.NewV7()
}
