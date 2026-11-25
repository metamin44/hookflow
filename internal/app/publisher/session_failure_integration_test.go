//go:build integration

package publisher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

func integrationSession(t *testing.T) (*Session, config.RabbitMQ) {
	t.Helper()
	cfg, err := config.LoadRabbitMQ()
	if err != nil {
		t.Skipf("integration RabbitMQ configuration unavailable: %v", err)
	}
	session, err := NewSession(cfg, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	return session, cfg
}

func TestSessionChannelCloseBeforePublishIsUnknownIntegration(t *testing.T) {
	session, _ := integrationSession(t)
	if err := session.generation.channel.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := session.Publish(context.Background(), testClaim("44444444-4444-4444-8444-444444444444", 1))
	if result != ResultUnknown || !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("Publish() = %q, %v; want UNKNOWN after channel close", result, err)
	}
}

func TestSessionConnectionLossDuringPublishIsUnknownIntegration(t *testing.T) {
	session, cfg := integrationSession(t)
	claim := testClaim("55555555-5555-4555-8555-555555555555", 1)
	session.onPublished = func() { _ = session.conn.Close() }
	result, err := session.Publish(context.Background(), claim)
	if result != ResultUnknown || !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("Publish() = %q, %v; want UNKNOWN after connection loss", result, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := messaging.Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = channel.Close() }()
	deliveries, err := channel.Consume(messaging.DispatchQueue, "integration-cleanup-"+claim.MessageID, false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-deliveries:
		if message.MessageId != claim.MessageID {
			_ = message.Reject(true)
			t.Fatalf("queue contained message %q, want test-owned %q", message.MessageId, claim.MessageID)
		}
		if err := message.Ack(false); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("accepted unknown-outcome message was not available for cleanup")
	}
}
