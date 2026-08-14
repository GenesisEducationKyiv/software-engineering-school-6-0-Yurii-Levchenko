//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github-release-notifier/internal/notification"
	"github-release-notifier/internal/orchestrator"
	"github-release-notifier/internal/subscription"
)

func newReplyConsumer() *orchestrator.ReplyConsumer {
	return orchestrator.NewReplyConsumer(orchestrator.NewStore(testDB), subscription.NewStore(testDB))
}

// startOneSaga runs T1 and returns the new saga id and subscription id.
func startOneSaga(t *testing.T, email, repo, token string) (sagaID string, subID int) {
	t.Helper()
	if err := newOrchestrator().StartConfirmation(context.Background(), email, repo, token, "http://t/api/confirm/"+token); err != nil {
		t.Fatalf("StartConfirmation: %v", err)
	}
	if err := testDB.Get(&sagaID, `SELECT id FROM saga WHERE email=$1`, email); err != nil {
		t.Fatalf("get saga id: %v", err)
	}
	if err := testDB.Get(&subID, `SELECT subscription_id FROM saga WHERE email=$1`, email); err != nil {
		t.Fatalf("get subscription id: %v", err)
	}
	return sagaID, subID
}

// TestSagaReply_CompletesSaga: a "sent" reply moves the saga to completed; a
// redelivered reply is a no-op.
func TestSagaReply_CompletesSaga(t *testing.T) {
	truncateSagaTables(t)
	ctx := context.Background()
	sagaID, _ := startOneSaga(t, "a@b.com", "x/y", "tok-1")

	rc := newReplyConsumer()
	body, _ := json.Marshal(notification.SagaReply{SagaID: sagaID, Status: notification.SagaStatusSent})

	if err := rc.HandleReply(ctx, body); err != nil {
		t.Fatalf("HandleReply: %v", err)
	}

	var state string
	if err := testDB.Get(&state, `SELECT state FROM saga WHERE id=$1`, sagaID); err != nil {
		t.Fatalf("get state: %v", err)
	}
	if state != string(orchestrator.StateCompleted) {
		t.Errorf("saga state = %q, want %q", state, orchestrator.StateCompleted)
	}

	if err := rc.HandleReply(ctx, body); err != nil {
		t.Errorf("second HandleReply should be a no-op, got %v", err)
	}
}

// TestSagaReply_FailedCompensates: a "failed" reply compensates — the saga goes
// to failed and the subscription is marked failed (not deleted). Idempotent on
// redelivery.
func TestSagaReply_FailedCompensates(t *testing.T) {
	truncateSagaTables(t)
	ctx := context.Background()
	sagaID, subID := startOneSaga(t, "a@b.com", "x/y", "tok-f")

	rc := newReplyConsumer()
	body, _ := json.Marshal(notification.SagaReply{SagaID: sagaID, Status: notification.SagaStatusFailed})

	if err := rc.HandleReply(ctx, body); err != nil {
		t.Fatalf("HandleReply: %v", err)
	}

	var sagaState, subStatus string
	if err := testDB.Get(&sagaState, `SELECT state FROM saga WHERE id=$1`, sagaID); err != nil {
		t.Fatalf("get saga state: %v", err)
	}
	if err := testDB.Get(&subStatus, `SELECT status FROM subscriptions WHERE id=$1`, subID); err != nil {
		t.Fatalf("get subscription status: %v", err)
	}
	if sagaState != string(orchestrator.StateFailed) {
		t.Errorf("saga state = %q, want failed", sagaState)
	}
	if subStatus != "failed" {
		t.Errorf("subscription status = %q, want failed (marked, not deleted)", subStatus)
	}
	// Subscription row must still exist (compensation marks, never deletes).
	var cnt int
	if err := testDB.Get(&cnt, `SELECT COUNT(*) FROM subscriptions WHERE id=$1`, subID); err != nil {
		t.Fatalf("count subscription: %v", err)
	}
	if cnt != 1 {
		t.Errorf("subscription rows = %d, want 1 (marked failed, not deleted)", cnt)
	}

	if err := rc.HandleReply(ctx, body); err != nil {
		t.Errorf("second HandleReply (already failed) should be a no-op, got %v", err)
	}
}

// TestSagaReply_UnknownSaga_NoOp: a reply for a saga that does not exist is
// acked (no error), not retried forever.
func TestSagaReply_UnknownSaga_NoOp(t *testing.T) {
	truncateSagaTables(t)
	rc := newReplyConsumer()
	body, _ := json.Marshal(notification.SagaReply{
		SagaID: "00000000-0000-0000-0000-000000000000",
		Status: notification.SagaStatusSent,
	})
	if err := rc.HandleReply(context.Background(), body); err != nil {
		t.Errorf("unknown-saga reply should be a no-op ack, got %v", err)
	}
}
