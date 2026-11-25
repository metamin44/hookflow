// Command outbox-publisher relays committed PostgreSQL outbox rows to RabbitMQ.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, defaultDependencies()); err != nil {
		fmt.Fprintf(os.Stderr, "outbox-publisher: %v\n", err)
		os.Exit(1)
	}
}
