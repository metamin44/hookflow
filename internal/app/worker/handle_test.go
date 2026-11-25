package worker

import (
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

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
