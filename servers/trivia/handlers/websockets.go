package handlers

import (
	"fmt"
	"github.com/streadway/amqp"
)

const (
	// name of rabbitmq queue to use for services
	qName = "services"
)

// ConnectQueue connects to the RabbitMQ service at the address defined in the addr variable
// and creates a channel and queue to send messages to. It returns the go channel
// which contains messages living on the RabbitMQ queue. Errors are returned if the
// connection fails
func (ctx *TriviaContext) ConnectQueue(addr string) (<-chan amqp.Delivery, error) {
	con, err := amqp.Dial("amqp://" + addr)
	if err != nil {
		return nil, fmt.Errorf("Unable to connect to MQ, %v", err)
	}

	chann, err := con.Channel()
	if err != nil {
		return nil, fmt.Errorf("error creating channel, %v", err)
	}

	ctx.Channel = chann

	queue, err := chann.QueueDeclare(qName, true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("error declaring queue, %v", err)
	}

	events, err := chann.Consume(queue.Name, "", false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("error retreiving messages, %v", err)
	}
	return events, nil
}
