//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github-release-notifier/internal/notification"
	"github-release-notifier/internal/orchestrator"
)

// TestSagaReply_CompletesSaga drives the reply half of the saga: T1 leaves the
// saga in subscription_created; a "sent" reply moves it to completed. A second
// (redelivered) reply must be a no-op.
func TestSagaReply_CompletesSaga(t *testing.T) {
	truncateSagaTables(t)
	ctx := context.Background()

	orch := newOrchestrator()
	if err := orch.StartConfirmation(ctx, "a@b.com", "x/y", "tok-1", "url1"); err != nil {
		t.Fatalf("StartConfirmation: %v", err)
	}

	var sagaID string
	if err := testDB.Get(&sagaID, `SELECT id FROM saga LIMIT 1`); err != nil {
		t.Fatalf("get saga id: %v", err)
	}

	rc := orchestrator.NewReplyConsumer(orchestrator.NewStore(testDB))
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

	// Idempotent: a redelivered reply for an already-completed saga is a no-op.
	if err := rc.HandleReply(ctx, body); err != nil {
		t.Errorf("second HandleReply should be a no-op, got %v", err)
	}
}

// TestSagaReply_UnknownSaga_NoOp: a reply referencing a saga that does not exist
// is acked (no error), not retried forever.
func TestSagaReply_UnknownSaga_NoOp(t *testing.T) {
	truncateSagaTables(t)

	rc := orchestrator.NewReplyConsumer(orchestrator.NewStore(testDB))
	body, _ := json.Marshal(notification.SagaReply{
		SagaID: "00000000-0000-0000-0000-000000000000",
		Status: notification.SagaStatusSent,
	})

	if err := rc.HandleReply(context.Background(), body); err != nil {
		t.Errorf("unknown-saga reply should be a no-op ack, got %v", err)
	}
}
