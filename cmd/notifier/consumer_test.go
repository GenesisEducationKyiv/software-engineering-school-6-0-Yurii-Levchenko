package main

import (
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

func delivery(key string, payload any) amqp.Delivery {
	body, _ := json.Marshal(payload)
	return amqp.Delivery{RoutingKey: key, Body: body}
}

func TestHandle_Confirmation_Sends(t *testing.T) {
	f := &fakeSender{}
	c := NewConsumer(f)

	err := c.handle(delivery(notification.RoutingConfirm,
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
	c := NewConsumer(f)

	err := c.handle(delivery(notification.RoutingRelease,
		notification.ReleaseRequest{To: "a@b.com", Repo: "golang/go", Tag: "v1.22.0", UnsubscribeURL: "http://x/unsub/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.release) != 1 || f.release[0].Tag != "v1.22.0" {
		t.Errorf("release sends = %+v, want one with tag v1.22.0", f.release)
	}
}

func TestHandle_BadJSON_ReturnsError(t *testing.T) {
	c := NewConsumer(&fakeSender{})
	d := amqp.Delivery{RoutingKey: notification.RoutingConfirm, Body: []byte("not json")}
	if err := c.handle(d); err == nil {
		t.Fatal("want error for bad JSON (so the message dead-letters, not acked)")
	}
}

func TestHandle_UnknownRoutingKey_ReturnsError(t *testing.T) {
	c := NewConsumer(&fakeSender{})
	if err := c.handle(amqp.Delivery{RoutingKey: "mystery", Body: []byte("{}")}); err == nil {
		t.Fatal("want error for unknown routing key")
	}
}

func TestHandle_SendFails_ReturnsError(t *testing.T) {
	f := &fakeSender{err: errors.New("smtp down")}
	c := NewConsumer(f)

	err := c.handle(delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/c/t"}))

	if err == nil {
		t.Fatal("want error when send fails, so Run nacks -> redeliver/DLQ instead of acking")
	}
}
