package notification

import (
	"context"
	"encoding/json"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakePublishCh captures what BrokerPublisher would send to RabbitMQ, so we can
// assert the publish logic (routing key, body, headers) without a real broker.
type fakePublishCh struct {
	keys []string
	msgs []amqp.Publishing
	err  error
}

func (f *fakePublishCh) PublishWithContext(_ context.Context, _, key string, _, _ bool, msg amqp.Publishing) error {
	if f.err != nil {
		return f.err
	}
	f.keys = append(f.keys, key)
	f.msgs = append(f.msgs, msg)
	return nil
}

func TestBrokerPublisher_SendConfirmation_PublishesRoutedMessage(t *testing.T) {
	f := &fakePublishCh{}
	p := &BrokerPublisher{ch: f}

	if err := p.SendConfirmationEmail("a@b.com", "http://x/api/confirm/tok-7"); err != nil {
		t.Fatalf("SendConfirmationEmail: %v", err)
	}

	if len(f.keys) != 1 || f.keys[0] != RoutingConfirm {
		t.Fatalf("routing key = %v, want %q", f.keys, RoutingConfirm)
	}
	msg := f.msgs[0]
	if msg.MessageId != "confirm:tok-7" {
		t.Errorf("MessageId = %q, want confirm:tok-7", msg.MessageId)
	}
	if msg.DeliveryMode != amqp.Persistent {
		t.Errorf("DeliveryMode = %d, want persistent(%d)", msg.DeliveryMode, amqp.Persistent)
	}
	var req ConfirmationRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if req.To != "a@b.com" || req.ConfirmURL != "http://x/api/confirm/tok-7" {
		t.Errorf("decoded body = %+v", req)
	}
}

func TestBrokerPublisher_SendRelease_PublishesRoutedMessage(t *testing.T) {
	f := &fakePublishCh{}
	p := &BrokerPublisher{ch: f}

	if err := p.SendReleaseNotification("a@b.com", "golang/go", "v1.22.0", "http://x/api/unsubscribe/tok-9"); err != nil {
		t.Fatalf("SendReleaseNotification: %v", err)
	}

	if f.keys[0] != RoutingRelease {
		t.Errorf("routing key = %q, want %q", f.keys[0], RoutingRelease)
	}
	msg := f.msgs[0]
	// Deterministic id on (repo, tag, sub token) so a re-publish dedups.
	if msg.MessageId != "release:golang/go:v1.22.0:tok-9" {
		t.Errorf("MessageId = %q, want release:golang/go:v1.22.0:tok-9", msg.MessageId)
	}
	var req ReleaseRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if req.Repo != "golang/go" || req.Tag != "v1.22.0" || req.UnsubscribeURL != "http://x/api/unsubscribe/tok-9" {
		t.Errorf("decoded body = %+v", req)
	}
}
