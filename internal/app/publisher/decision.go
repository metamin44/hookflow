package publisher

import (
	"errors"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/platform/messaging"
)

// publishDecision correlates one outstanding publish. It contains no timers:
// silence is never treated as proof that a mandatory return is absent.
type publishDecision struct {
	messageID string
	tag       uint64
	sent      bool
	returned  bool
	confirm   *bool
}

func (d *publishDecision) observeReturn(value amqp.Return) error {
	if !d.sent {
		return errors.New("return arrived before publish registration")
	}
	if d.returned {
		return errors.New("duplicate publisher return")
	}
	if value.MessageId == "" || value.MessageId != d.messageID {
		return errors.New("unmatched publisher return")
	}
	if value.Exchange != messaging.DispatchExchange || value.RoutingKey != messaging.DispatchRoutingKey {
		return errors.New("publisher return routing mismatch")
	}
	d.returned = true
	return nil
}

func (d *publishDecision) observeConfirmation(value amqp.Confirmation) error {
	if !d.sent || value.DeliveryTag != d.tag {
		return errors.New("unmatched publisher confirmation")
	}
	if d.confirm != nil {
		return errors.New("duplicate publisher confirmation")
	}
	ack := value.Ack
	d.confirm = &ack
	return nil
}

func (d *publishDecision) result() (PublishResult, error) {
	if d.confirm == nil {
		return ResultUnknown, errors.New("publisher confirmation is still outstanding")
	}
	if !*d.confirm {
		return ResultUnknown, errors.New("publisher received a negative confirmation")
	}
	if d.returned {
		return ResultReturned, nil
	}
	return ResultConfirmed, nil
}
