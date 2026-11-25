package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

type blockingProcessor struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (p *blockingProcessor) Process(context.Context, *Handle) disposition.Decision {
	close(p.started)
	<-p.release
	close(p.done)
	return disposition.Decision{Action: disposition.ActionHold}
}

func TestRunGenerationBoundsInFlightWaitOnChannelClose(t *testing.T) {
	processor := &blockingProcessor{
		started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}),
	}
	consumer := &Consumer{
		prefetch: 1, workBudget: 25 * time.Millisecond, processor: processor,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: observability.New(),
	}
	deliveries := make(chan amqp.Delivery, 1)
	deliveries <- amqp.Delivery{Body: []byte(`{}`)}
	close(deliveries)
	generation := &channelGeneration{}
	returned := make(chan bool, 1)
	go func() { returned <- consumer.runGeneration(context.Background(), generation, deliveries) }()

	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}
	select {
	case graceful := <-returned:
		if graceful {
			t.Fatal("channel loss was reported as a graceful shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight wait exceeded the configured work budget")
	}
	close(processor.release)
	select {
	case <-processor.done:
	case <-time.After(time.Second):
		t.Fatal("processor did not finish after release")
	}
}
