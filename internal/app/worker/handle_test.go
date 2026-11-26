package worker

import (
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type blockingGenerationChannel struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingGenerationChannel) Ack(uint64, bool) error {
	close(c.started)
	<-c.release
	return nil
}

func (c *blockingGenerationChannel) Reject(uint64, bool) error {
	close(c.started)
	<-c.release
	return nil
}

func (*blockingGenerationChannel) IsClosed() bool { return false }

func (*blockingGenerationChannel) Consume(string, string, bool, bool, bool, bool, amqp.Table) (<-chan amqp.Delivery, error) {
	return nil, nil
}

func TestHandleRejectsClosedGeneration(t *testing.T) {
	generation := &channelGeneration{}
	handle := newHandle(generation, amqp.Delivery{DeliveryTag: 3, Body: []byte("pointer")})
	generation.close()
	if err := handle.Ack(); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("Ack() error = %v, want ErrStaleHandle", err)
	}
	if err := handle.RejectRequeue(); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("RejectRequeue() error = %v, want ErrStaleHandle", err)
	}
}

func TestGenerationCloseDoesNotWaitForBlockedSettlement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		settle func(*Handle) error
	}{
		{name: "ack", settle: (*Handle).Ack},
		{name: "reject", settle: (*Handle).RejectRequeue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &blockingGenerationChannel{started: make(chan struct{}), release: make(chan struct{})}
			closeCalled := make(chan struct{})
			generation := &channelGeneration{
				channel: channel, closeBudget: time.Second,
				closeChannel: func() error { close(closeCalled); return nil },
			}
			handle := newHandle(generation, amqp.Delivery{DeliveryTag: 3})
			settled := make(chan error, 1)
			go func() { settled <- tc.settle(handle) }()
			select {
			case <-channel.started:
			case <-time.After(time.Second):
				t.Fatal("broker settlement did not start")
			}
			closed := make(chan struct{})
			go func() { generation.close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("generation close waited for blocked broker I/O")
			}
			select {
			case err := <-settled:
				t.Fatalf("settlement returned before its controlled release: %v", err)
			default:
			}
			close(channel.release)
			if err := <-settled; err != nil {
				t.Fatalf("settlement error = %v", err)
			}
		})
	}
}

func TestHandleBodyIsCopied(t *testing.T) {
	generation := &channelGeneration{}
	body := []byte("original")
	handle := newHandle(generation, amqp.Delivery{Body: body})
	body[0] = 'X'
	got := handle.Body()
	if string(got) != "original" {
		t.Fatalf("Body() = %q", got)
	}
	got[0] = 'Y'
	if string(handle.Body()) != "original" {
		t.Fatal("Body() exposed mutable internal storage")
	}
}

func TestNilHandleFailsClosed(t *testing.T) {
	var handle *Handle
	if err := handle.Ack(); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("nil Ack() error = %v", err)
	}
	if err := handle.RejectRequeue(); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("nil RejectRequeue() error = %v", err)
	}
}
