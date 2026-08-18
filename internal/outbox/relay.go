package outbox

import (
	"context"
	"log/slog"
	"time"
)

// Publisher is what the Relay needs to push a stored message to the broker.
// *notification.BrokerPublisher satisfies it.
type Publisher interface {
	Publish(ctx context.Context, routingKey, messageID string, body []byte) error
}

// relayStore is the subset of *Store the Relay depends on (kept small for
// testability — the unit test injects a fake).
type relayStore interface {
	FetchUnpublished(ctx context.Context, limit int) ([]Message, error)
	MarkPublished(ctx context.Context, id int64) error
}

// Relay periodically drains the outbox: it reads unpublished messages, publishes
// them to the broker, and marks them published. This is the "message relay" half
// of the transactional-outbox pattern — it turns a row committed in the local DB
// into a reliably delivered broker message, decoupled from the request that wrote it.
type Relay struct {
	store    relayStore
	pub      Publisher
	interval time.Duration
	batch    int
}

// markPublishedTimeout bounds the post-publish bookkeeping write, which runs
// with cancellation detached from the relay's context.
const markPublishedTimeout = 5 * time.Second

// NewRelay creates a Relay that polls every interval.
func NewRelay(store relayStore, pub Publisher, interval time.Duration) *Relay {
	return &Relay{store: store, pub: pub, interval: interval, batch: 100}
}

// Run drains immediately, then on every tick, until ctx is canceled (graceful
// shutdown). Same goroutine+ticker shape as the release scanner.
func (r *Relay) Run(ctx context.Context) {
	slog.Info("Outbox relay started", "interval", r.interval)
	r.drain(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Outbox relay stopped")
			return
		case <-ticker.C:
			r.drain(ctx)
		}
	}
}

// drain publishes one batch of unpublished messages, oldest first.
//
// Order is publish-then-mark: a crash in between re-publishes the message on the
// next tick (at-least-once) — the consumer dedups on MessageId, so a duplicate is
// harmless. On a publish error we stop the batch (the broker is likely down) and
// retry on the next tick; a mark error is logged but non-fatal (the row simply
// re-publishes later and the consumer dedups).
func (r *Relay) drain(ctx context.Context) {
	msgs, err := r.store.FetchUnpublished(ctx, r.batch)
	if err != nil {
		slog.Error("Outbox relay failed to fetch messages", "err", err)
		return
	}
	for _, m := range msgs {
		if err := r.pub.Publish(ctx, m.RoutingKey, m.MessageID, m.Payload); err != nil {
			slog.Error("Outbox relay failed to publish, will retry next tick",
				"outbox_id", m.ID, "message_id", m.MessageID, "err", err)
			return
		}
		if err := r.markPublished(ctx, m); err != nil {
			slog.Warn("Outbox relay published but failed to mark, may re-publish",
				"outbox_id", m.ID, "message_id", m.MessageID, "err", err)
		}
	}
}

// markPublished stamps published_at for a message already handed to the broker.
//
// It deliberately does NOT inherit cancellation from ctx: Publish has already
// succeeded, so a shutdown must not stop us from recording that. Otherwise every
// restart leaves ghost rows (published_at NULL for messages that were in fact
// published) which the relay re-publishes on the next boot and which are
// indistinguishable from a genuine failure. It keeps its own short deadline so a
// hung database can't block shutdown either.
func (r *Relay) markPublished(ctx context.Context, m Message) error {
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markPublishedTimeout)
	defer cancel()
	return r.store.MarkPublished(markCtx, m.ID)
}
