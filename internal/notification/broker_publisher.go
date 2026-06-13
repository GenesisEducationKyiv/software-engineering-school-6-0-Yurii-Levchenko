package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	ExchangeName   = "notifications"
	RoutingConfirm = "confirmation"
	RoutingRelease = "release"
)

// amqpPublisher is the slice of *amqp.Channel the publisher needs; an interface
// so tests can inject a fake without a real broker.
type amqpPublisher interface {
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
}

// BrokerPublisher implements EmailNotifier/ReleaseNotifier by publishing
// notification commands to RabbitMQ instead of POSTing to the notifier. It is
// the ACL: a domain call becomes a routed, persistent AMQP message.
type BrokerPublisher struct {
	conn *amqp.Connection
	ch   amqpPublisher
	mu   sync.Mutex // an amqp channel is not safe for concurrent publishers
}

// NewBrokerPublisher dials RabbitMQ and declares the notifications exchange.
func NewBrokerPublisher(url string) (*BrokerPublisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	// Declare here too (idempotent) so neither side depends on start order.
	if err := ch.ExchangeDeclare(ExchangeName, "direct", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare exchange: %w", err)
	}
	return &BrokerPublisher{conn: conn, ch: ch}, nil
}

// Close tears down the connection (which closes its channels).
func (p *BrokerPublisher) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

func (p *BrokerPublisher) SendConfirmationEmail(to, confirmURL string) error {
	return p.publish(RoutingConfirm, "confirm:"+path.Base(confirmURL),
		ConfirmationRequest{To: to, ConfirmURL: confirmURL})
}

func (p *BrokerPublisher) SendReleaseNotification(to, repo, tag, unsubscribeURL string) error {
	id := fmt.Sprintf("release:%s:%s:%s", repo, tag, path.Base(unsubscribeURL))
	return p.publish(RoutingRelease, id,
		ReleaseRequest{To: to, Repo: repo, Tag: tag, UnsubscribeURL: unsubscribeURL})
}

func (p *BrokerPublisher) publish(routingKey, messageID string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch.PublishWithContext(ctx, ExchangeName, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent, // persisted with the broker's volume
		MessageId:    messageID,       // deterministic -> consumer dedup key
		Body:         body,
	})
}
