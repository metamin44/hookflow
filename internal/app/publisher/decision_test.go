package publisher

import (
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/platform/messaging"
)

func amqpReturn(messageID string) amqp.Return {
	return amqp.Return{MessageId: messageID, Exchange: messaging.DispatchExchange, RoutingKey: messaging.DispatchRoutingKey}
}

func amqpConfirmation(tag uint64, ack bool) amqp.Confirmation {
	return amqp.Confirmation{DeliveryTag: tag, Ack: ack}
}
