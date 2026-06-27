package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github-release-notifier/internal/notification"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakeSender records sends. failFirst makes the first N calls fail (transient);
// err (once failFirst is exhausted) makes every call fail (permanent).
type fakeSender struct {
	confirm      []notification.ConfirmationRequest
	release      []notification.ReleaseRequest
	err          error
	failFirst    int
	confirmCalls int
}

func (f *fakeSender) SendConfirmationEmail(to, confirmURL string) error {
	f.confirmCalls++
	if f.failFirst > 0 {
		f.failFirst--
		return errors.New("transient smtp")
	}
	if f.err != nil {
		return f.err
	}
	f.confirm = append(f.confirm, notification.ConfirmationRequest{To: to, ConfirmURL: confirmURL})
	return nil
}

func (f *fakeSender) SendReleaseNotification(to, repo, tag, unsubscribeURL string) error {
	if f.err != nil {
		return f.err
	}
	f.release = append(f.release, notification.ReleaseRequest{To: to, Repo: repo, Tag: tag, UnsubscribeURL: unsubscribeURL})
	return nil
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

// fakeReplier records (sagaID, status) of each reply.
type repliedItem struct{ sagaID, status string }
type fakeReplier struct {
	replied []repliedItem
	err     error
}

func (f *fakeReplier) ReplyConfirmation(_ context.Context, sagaID, status string) error {
	if f.err != nil {
		return f.err
	}
	f.replied = append(f.replied, repliedItem{sagaID, status})
	return nil
}

func delivery(key string, payload any) amqp.Delivery {
	body, _ := json.Marshal(payload)
	return amqp.Delivery{RoutingKey: key, Body: body}
}

// fastRetry disables the backoff sleep so failure tests don't wait.
func fastRetry(c *Consumer, attempts int) {
	c.maxAttempts = attempts
	c.backoff = 0
}

func TestHandle_Confirmation_Sends(t *testing.T) {
	f := &fakeSender{}
	c := NewConsumer(f, &fakeDedup{})

	err := c.handle(context.Background(), &fakeReplier{}, delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/confirm/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.confirm) != 1 || f.confirm[0].To != "a@b.com" {
		t.Errorf("confirm sends = %+v, want one to a@b.com", f.confirm)
	}
}

func TestHandle_Confirmation_WithSagaID_RepliesSent(t *testing.T) {
	f := &fakeSender{}
	rep := &fakeReplier{}
	c := NewConsumer(f, &fakeDedup{})

	err := c.handle(context.Background(), rep, delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{SagaID: "saga-1", To: "a@b.com", ConfirmURL: "http://x/confirm/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(rep.replied) != 1 || rep.replied[0] != (repliedItem{"saga-1", notification.SagaStatusSent}) {
		t.Errorf("replied = %+v, want one {saga-1, sent}", rep.replied)
	}
}

func TestHandle_Confirmation_NoSagaID_DoesNotReply(t *testing.T) {
	rep := &fakeReplier{}
	c := NewConsumer(&fakeSender{}, &fakeDedup{})

	err := c.handle(context.Background(), rep, delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/confirm/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(rep.replied) != 0 {
		t.Errorf("replied = %v, want none (no saga id in the command)", rep.replied)
	}
}

func TestHandle_Retry_SucceedsAfterTransient(t *testing.T) {
	f := &fakeSender{failFirst: 1} // first attempt fails, second succeeds
	rep := &fakeReplier{}
	c := NewConsumer(f, &fakeDedup{})
	fastRetry(c, 2)

	err := c.handle(context.Background(), rep, delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{SagaID: "saga-2", To: "a@b.com", ConfirmURL: "http://x/c/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if f.confirmCalls != 2 {
		t.Errorf("send attempts = %d, want 2 (one retry)", f.confirmCalls)
	}
	if len(rep.replied) != 1 || rep.replied[0].status != notification.SagaStatusSent {
		t.Errorf("replied = %+v, want one with status sent", rep.replied)
	}
}

func TestHandle_Confirmation_SendFails_WithSagaID_RepliesFailed(t *testing.T) {
	f := &fakeSender{err: errors.New("smtp down")}
	rep := &fakeReplier{}
	c := NewConsumer(f, &fakeDedup{})
	fastRetry(c, 2)

	err := c.handle(context.Background(), rep, delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{SagaID: "saga-x", To: "a@b.com", ConfirmURL: "http://x/c/t"}))

	// Saga command: we reported failure, so handle acks (returns nil).
	if err != nil {
		t.Fatalf("handle should ack a reported failure, got error: %v", err)
	}
	if f.confirmCalls != 2 {
		t.Errorf("send attempts = %d, want 2 (retried then gave up)", f.confirmCalls)
	}
	if len(rep.replied) != 1 || rep.replied[0] != (repliedItem{"saga-x", notification.SagaStatusFailed}) {
		t.Errorf("replied = %+v, want one {saga-x, failed}", rep.replied)
	}
}

func TestHandle_Release_Sends(t *testing.T) {
	f := &fakeSender{}
	rep := &fakeReplier{}
	c := NewConsumer(f, &fakeDedup{})

	err := c.handle(context.Background(), rep, delivery(notification.RoutingRelease,
		notification.ReleaseRequest{To: "a@b.com", Repo: "golang/go", Tag: "v1.22.0", UnsubscribeURL: "http://x/unsub/t"}))

	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.release) != 1 || f.release[0].Tag != "v1.22.0" {
		t.Errorf("release sends = %+v, want one with tag v1.22.0", f.release)
	}
	if len(rep.replied) != 0 {
		t.Errorf("release must not reply to the saga, got %v", rep.replied)
	}
}

func TestHandle_BadJSON_ReturnsError(t *testing.T) {
	c := NewConsumer(&fakeSender{}, &fakeDedup{})
	d := amqp.Delivery{RoutingKey: notification.RoutingConfirm, Body: []byte("not json")}
	if err := c.handle(context.Background(), &fakeReplier{}, d); err == nil {
		t.Fatal("want error for bad JSON (so the message dead-letters, not acked)")
	}
}

func TestHandle_UnknownRoutingKey_ReturnsError(t *testing.T) {
	c := NewConsumer(&fakeSender{}, &fakeDedup{})
	if err := c.handle(context.Background(), &fakeReplier{}, amqp.Delivery{RoutingKey: "mystery", Body: []byte("{}")}); err == nil {
		t.Fatal("want error for unknown routing key")
	}
}

func TestHandle_SendFails_NoSaga_ReturnsError(t *testing.T) {
	f := &fakeSender{err: errors.New("smtp down")}
	c := NewConsumer(f, &fakeDedup{})
	fastRetry(c, 1)

	// No saga id and the send fails -> no reply path -> surface error so Run DLQs it.
	err := c.handle(context.Background(), &fakeReplier{}, delivery(notification.RoutingConfirm,
		notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/c/t"}))

	if err == nil {
		t.Fatal("want error when a non-saga send fails, so Run nacks -> DLQ")
	}
}

func TestHandle_Duplicate_Skips(t *testing.T) {
	f := &fakeSender{}
	c := NewConsumer(f, &fakeDedup{seen: true})

	body, _ := json.Marshal(notification.ConfirmationRequest{To: "a@b.com", ConfirmURL: "http://x/c/t"})
	d := amqp.Delivery{RoutingKey: notification.RoutingConfirm, MessageId: "confirm:t", Body: body}

	if err := c.handle(context.Background(), &fakeReplier{}, d); err != nil {
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

	if err := c.handle(context.Background(), &fakeReplier{}, d); err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if len(f.confirm) != 1 {
		t.Errorf("sent %d, want 1", len(f.confirm))
	}
	if len(dd.marked) != 1 || dd.marked[0] != "confirm:t" {
		t.Errorf("marked = %v, want [confirm:t] (mark happens after a successful send)", dd.marked)
	}
}
