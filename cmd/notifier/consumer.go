package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github-release-notifier/internal/notification"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	queueName = "notifications"
	dlxName   = "notifications.dlx"
	dlqName   = "notifications.dlq"
)

// emailSender is what the consumer needs from the SMTP layer; an interface so
// the message-handling logic is testable without a real mail server.
type emailSender interface {
	SendConfirmationEmail(to, confirmURL string) error
	SendReleaseNotification(to, repo, tag, unsubscribeURL string) error
}

// Consumer reads notification commands from RabbitMQ and sends the emails.
type Consumer struct {
	sender emailSender
}

func NewConsumer(sender emailSender) *Consumer {
	return &Consumer{sender: sender}
}

// Run connects to RabbitMQ, declares the topology (exchange, work queue with a
// dead-letter route, and the DLQ), then consumes until ctx is canceled.
func (c *Consumer) Run(ctx context.Context, url string) error {
	conn, err := amqp.Dial(url)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer ch.Close()

	if err := c.declareTopology(ch); err != nil {
		return err
	}

	// Manual ack (autoAck=false): we ack only after the email is sent, so a
	// crash mid-send redelivers the message instead of losing it.
	deliveries, err := ch.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}

	slog.Info("Notifier consumer started", "queue", queueName)
	for {
		select {
		case <-ctx.Done():
			slog.Info("Notifier consumer stopping")
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("deliveries channel closed")
			}
			if err := c.handle(d); err != nil {
				slog.Error("Notifier failed to process message, dead-lettering",
					"routing_key", d.RoutingKey, "message_id", d.MessageId, "err", err)
				_ = d.Nack(false, false) // requeue=false -> routed to the DLQ
				continue
			}
			_ = d.Ack(false)
		}
	}
}

func (c *Consumer) declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(notification.ExchangeName, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}
	// Dead-letter exchange + queue for messages that fail processing.
	if err := ch.ExchangeDeclare(dlxName, "fanout", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dlx: %w", err)
	}
	if _, err := ch.QueueDeclare(dlqName, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dlq: %w", err)
	}
	if err := ch.QueueBind(dlqName, "", dlxName, false, nil); err != nil {
		return fmt.Errorf("bind dlq: %w", err)
	}
	// Work queue: failed messages are dead-lettered to the DLX.
	if _, err := ch.QueueDeclare(queueName, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange": dlxName,
	}); err != nil {
		return fmt.Errorf("declare queue: %w", err)
	}
	for _, key := range []string{notification.RoutingConfirm, notification.RoutingRelease} {
		if err := ch.QueueBind(queueName, key, notification.ExchangeName, false, nil); err != nil {
			return fmt.Errorf("bind queue %s: %w", key, err)
		}
	}
	return ch.Qos(10, 0, false) // at most 10 unacked messages in flight
}

// handle decodes one delivery by its routing key and sends the email. A
// returned error tells Run to dead-letter the message; nil means ack.
func (c *Consumer) handle(d amqp.Delivery) error {
	switch d.RoutingKey {
	case notification.RoutingConfirm:
		var req notification.ConfirmationRequest
		if err := json.Unmarshal(d.Body, &req); err != nil {
			return fmt.Errorf("decode confirmation: %w", err)
		}
		return c.sender.SendConfirmationEmail(req.To, req.ConfirmURL)
	case notification.RoutingRelease:
		var req notification.ReleaseRequest
		if err := json.Unmarshal(d.Body, &req); err != nil {
			return fmt.Errorf("decode release: %w", err)
		}
		return c.sender.SendReleaseNotification(req.To, req.Repo, req.Tag, req.UnsubscribeURL)
	default:
		return fmt.Errorf("unknown routing key %q", d.RoutingKey)
	}
}
