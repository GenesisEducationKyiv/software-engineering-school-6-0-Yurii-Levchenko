package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github-release-notifier/internal/notification"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakeSender records what the consumer asked to send, so we can test the
// message-handling logic without a real SMTP server.
type fakeSender struct {
	confirm []notification.ConfirmationRequest
	release []notification.ReleaseRequest
	err     error
}

func (f *fakeSender) SendConfirmationEmail(to, confirmURL string) error {
	f.confirm = append(f.confirm, notification.ConfirmationRequest{To: to, ConfirmURL: confirmURL})
	return f.err
}

func (f *fakeSender) SendReleaseNotification(to, repo, tag, unsubscribeURL string) error {
	f.release = append(f.release, notification.ReleaseRequest{To: to, Repo: repo, Tag: tag, UnsubscribeURL: unsubscribeURL})
	return f.err
}

// fakeDedup: `seen` controls whether AlreadyProcessed reports a duplicate;
// `marked` records ids passed to MarkProcessed.
type fakeDedup struct {
	seen   bool
	marked []string
}

func (f *fakeDedup) AlreadyProcessed(_ context.Context, _ string) (bool, error) { return f.seen, nil }
func (f *fakeDedup) MarkProcessed(_ context.Context, id string) error {
	f.marked = append(f.marked, id)
	return nil
}

func delivery(key string, payload any) amqp.Delivery {
	body, _ := json.Marshal(payload)
	return amqp.Delivery{RoutingKey: key, Body: body}
}

func TestHandle_Confirmation_Sends(t *testing.T) {
	f := &fakeSender{}
	c := NewConsumer(f, &fakeDedup{})

	err := c.handle(context.Background(), delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/confirm/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.confirm) != 1 || f.confirm[0].To != "a@b.com" {
		t.Errorf("confirm sends = %+v, want one to a@b.com", f.confirm)
	}
}

func TestHandle_Release_Sends(t *testing.T) {
	f := &fakeSender{}
	c := NewConsumer(f, &fakeDedup{})

	err := c.handle(context.Background(), delivery(notification.RoutingRelease,
		notification.ReleaseRequest{To: "a@b.com", Repo: "golang/go", Tag: "v1.22.0", UnsubscribeURL: "http://x/unsub/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.release) != 1 || f.release[0].Tag != "v1.22.0" {
		t.Errorf("release sends = %+v, want one with tag v1.22.0", f.release)
	}
}

func TestHandle_BadJSON_ReturnsError(t *testing.T) {
	c := NewConsumer(&fakeSender{}, &fakeDedup{})
	d := amqp.Delivery{RoutingKey: notification.RoutingConfirm, Body: []byte("not json")}
	if err := c.handle(context.Background(), d); err == nil {
		t.Fatal("want error for bad JSON (so the message dead-letters, not acked)")
	}
}

func TestHandle_UnknownRoutingKey_ReturnsError(t *testing.T) {
	c := NewConsumer(&fakeSender{}, &fakeDedup{})
	if err := c.handle(context.Background(), amqp.Delivery{RoutingKey: "mystery", Body: []byte("{}")}); err == nil {
		t.Fatal("want error for unknown routing key")
	}
}

func TestHandle_SendFails_ReturnsError(t *testing.T) {
	f := &fakeSender{err: errors.New("smtp down")}
	c := NewConsumer(f, &fakeDedup{})

	err := c.handle(context.Background(), delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/c/t"}))

	if err == nil {
		t.Fatal("want error when send fails, so Run nacks -> redeliver/DLQ instead of acking")
	}
}

func TestHandle_Duplicate_Skips(t *testing.T) {
	f := &fakeSender{}
	c := NewConsumer(f, &fakeDedup{seen: true})

	body, _ := json.Marshal(notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/c/t"})
	d := amqp.Delivery{RoutingKey: notification.RoutingConfirm, MessageId: "confirm:t", Body: body}

	if err := c.handle(context.Background(), d); err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.confirm) != 0 {
		t.Errorf("sent %d, want 0 — an already-processed message must be skipped", len(f.confirm))
	}
}

func TestHandle_FirstTime_MarksAfterSend(t *testing.T) {
	f := &fakeSender{}
	dd := &fakeDedup{seen: false}
	c := NewConsumer(f, dd)

	body, _ := json.Marshal(notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/c/t"})
	d := amqp.Delivery{RoutingKey: notification.RoutingConfirm, MessageId: "confirm:t", Body: body}

	if err := c.handle(context.Background(), d); err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.confirm) != 1 {
		t.Errorf("sent %d, want 1", len(f.confirm))
	}
	if len(dd.marked) != 1 || dd.marked[0] != "confirm:t" {
		t.Errorf("marked = %v, want [confirm:t] (mark happens after a successful send)", dd.marked)
	}
}
