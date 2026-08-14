package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github-release-notifier/internal/notification"

	amqp "github.com/rabbitmq/amqp091-go"
)

const replyQueueName = "saga.replies"

// subscriptionCompensator runs compensation C1: mark the subscription failed
// (no delete). *subscription.Store satisfies it.
type subscriptionCompensator interface {
	MarkFailed(ctx context.Context, id int) error
}

// ReplyConsumer consumes saga replies from the notifier and advances the saga
// state: subscription_created -> completed on "sent", or -> failed (compensating)
// on "failed". It is the orchestrator's half of the async command/reply exchange.
type ReplyConsumer struct {
	sagas *Store
	subs  subscriptionCompensator
}

// NewReplyConsumer creates a reply consumer backed by the saga store and the
// subscription compensator.
func NewReplyConsumer(sagas *Store, subs subscriptionCompensator) *ReplyConsumer {
	return &ReplyConsumer{sagas: sagas, subs: subs}
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
			if err := rc.handleWithRetry(ctx, d.Body); err != nil {
				slog.Error("Saga reply processing failed after retries, dropping", "err", err)
				_ = d.Nack(false, false)
				continue
			}
			_ = d.Ack(false)
		}
	}
}

// handleWithRetry retries HandleReply a few times before giving up, so a
// transient DB blip doesn't drop a reply. HandleReply is idempotent (state-guarded),
// so retrying is safe (review: k1llzers).
func (rc *ReplyConsumer) handleWithRetry(ctx context.Context, body []byte) error {
	const attempts = 3
	var err error
	for i := 1; i <= attempts; i++ {
		if err = rc.HandleReply(ctx, body); err == nil {
			return nil
		}
		if i == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return err
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

	// The state guards make a re-delivered reply a no-op (idempotent).
	switch rep.Status {
	case notification.SagaStatusSent:
		if sg.State == StateSubscriptionCreated {
			if err := rc.sagas.UpdateState(ctx, sg.ID, StateCompleted, ""); err != nil {
				return fmt.Errorf("complete saga %s: %w", sg.ID, err)
			}
			slog.Info("Saga completed", "saga_id", sg.ID)
		}
	case notification.SagaStatusFailed:
		// Compensate (C1): mark the subscription failed. compensating is an
		// intermediate state so a crash mid-compensation is resumable later.
		if sg.State == StateSubscriptionCreated || sg.State == StateCompensating {
			const reason = "confirmation email failed"
			if err := rc.sagas.UpdateState(ctx, sg.ID, StateCompensating, reason); err != nil {
				return fmt.Errorf("mark saga %s compensating: %w", sg.ID, err)
			}
			if err := rc.subs.MarkFailed(ctx, sg.SubscriptionID); err != nil {
				return fmt.Errorf("compensate saga %s: %w", sg.ID, err)
			}
			if err := rc.sagas.UpdateState(ctx, sg.ID, StateFailed, reason); err != nil {
				return fmt.Errorf("fail saga %s: %w", sg.ID, err)
			}
			slog.Info("Saga compensated (failed)", "saga_id", sg.ID)
		}
	}
	return nil
}
