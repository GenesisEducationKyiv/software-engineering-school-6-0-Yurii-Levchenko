package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github-release-notifier/internal/notification"

	amqp "github.com/rabbitmq/amqp091-go"
)

const replyQueueName = "saga.replies"

// ReplyConsumer consumes saga replies from the notifier and advances the saga
// state (e.g. subscription_created -> completed when the confirmation email was
// sent). It is the orchestrator's half of the async command/reply exchange.
type ReplyConsumer struct {
	sagas *Store
}

// NewReplyConsumer creates a reply consumer backed by the saga store.
func NewReplyConsumer(sagas *Store) *ReplyConsumer {
	return &ReplyConsumer{sagas: sagas}
}

// Run connects to RabbitMQ, declares the reply queue bound to the notifications
// exchange, and consumes replies until ctx is canceled. It returns an error on
// any connection failure so the caller can reconnect.
func (rc *ReplyConsumer) Run(ctx context.Context, url string) error {
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

	if err := rc.declareTopology(ch); err != nil {
		return err
	}

	// Manual ack: ack only after the saga state is advanced.
	deliveries, err := ch.Consume(replyQueueName, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}

	slog.Info("Saga reply consumer started", "queue", replyQueueName)
	for {
		select {
		case <-ctx.Done():
			slog.Info("Saga reply consumer stopping")
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("deliveries channel closed")
			}
			if err := rc.HandleReply(ctx, d.Body); err != nil {
				slog.Error("Saga reply processing failed", "err", err)
				_ = d.Nack(false, false)
				continue
			}
			_ = d.Ack(false)
		}
	}
}

func (rc *ReplyConsumer) declareTopology(ch *amqp.Channel) error {
	// Idempotent: the exchange is also declared by the publisher and the notifier.
	if err := ch.ExchangeDeclare(notification.ExchangeName, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(replyQueueName, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare reply queue: %w", err)
	}
	if err := ch.QueueBind(replyQueueName, notification.RoutingSagaReply, notification.ExchangeName, false, nil); err != nil {
		return fmt.Errorf("bind reply queue: %w", err)
	}
	return ch.Qos(10, 0, false)
}

// HandleReply advances the saga based on one reply. It is idempotent: a reply for
// an already-advanced (or unknown) saga is a no-op, so a redelivered reply is safe.
func (rc *ReplyConsumer) HandleReply(ctx context.Context, body []byte) error {
	var rep notification.SagaReply
	if err := json.Unmarshal(body, &rep); err != nil {
		return fmt.Errorf("decode saga reply: %w", err)
	}

	sg, err := rc.sagas.GetByID(ctx, rep.SagaID)
	if err != nil {
		return fmt.Errorf("load saga %s: %w", rep.SagaID, err)
	}
	if sg == nil {
		slog.Warn("Saga reply for unknown saga, ignoring", "saga_id", rep.SagaID)
		return nil
	}

	// Only the email-sent reply advances the saga for now; failure/compensation
	// is handled in a later PR. The state guard makes re-delivery a no-op.
	if rep.Status == notification.SagaStatusSent && sg.State == StateSubscriptionCreated {
		if err := rc.sagas.UpdateState(ctx, rep.SagaID, StateCompleted, ""); err != nil {
			return fmt.Errorf("complete saga %s: %w", rep.SagaID, err)
		}
		slog.Info("Saga completed", "saga_id", rep.SagaID)
	}
	return nil
}
