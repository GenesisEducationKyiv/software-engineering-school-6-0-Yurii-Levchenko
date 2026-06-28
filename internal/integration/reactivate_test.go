//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github-release-notifier/internal/notification"
)

// TestSubscribe_ReactivatesFailedSubscription drives the full failed -> retry
// lifecycle through the HTTP API: subscribe, force a compensation (email failed),
// then re-subscribe. The second subscribe must reactivate the same row (200, back
// to pending) rather than reject it as a duplicate (409), and the subscription
// row must not be duplicated.
func TestSubscribe_ReactivatesFailedSubscription(t *testing.T) {
	ta := newTestApp(t)
	ta.github.repos["golang/go"] = true

	// 1. First subscribe.
	status, _ := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com", "repo": "golang/go",
	})
	if status != http.StatusOK {
		t.Fatalf("first subscribe: got %d, want 200", status)
	}

	var subID int
	if err := ta.db.Get(&subID,
		`SELECT id FROM subscriptions WHERE email=$1 AND repo=$2`,
		"alice@example.com", "golang/go"); err != nil {
		t.Fatalf("get subscription id: %v", err)
	}
	var sagaID string
	if err := ta.db.Get(&sagaID, `SELECT id FROM saga WHERE subscription_id=$1`, subID); err != nil {
		t.Fatalf("get saga id: %v", err)
	}

	// 2. Force compensation: the confirmation email permanently failed.
	body, _ := json.Marshal(notification.SagaReply{SagaID: sagaID, Status: notification.SagaStatusFailed})
	if err := newReplyConsumer().HandleReply(context.Background(), body); err != nil {
		t.Fatalf("compensate: %v", err)
	}
	var st string
	if err := ta.db.Get(&st, `SELECT status FROM subscriptions WHERE id=$1`, subID); err != nil {
		t.Fatalf("status after compensation: %v", err)
	}
	if st != "failed" {
		t.Fatalf("status after compensation = %q, want failed", st)
	}

	// 3. Re-subscribe to the same repo: must reactivate (200), not 409.
	status, b := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com", "repo": "golang/go",
	})
	if status != http.StatusOK {
		t.Fatalf("re-subscribe: got %d, want 200 (reactivate, not 409); body=%v", status, b)
	}

	// Same row reused, back to pending — no duplicate subscription.
	var st2 string
	if err := ta.db.Get(&st2, `SELECT status FROM subscriptions WHERE id=$1`, subID); err != nil {
		t.Fatalf("status after reactivation: %v", err)
	}
	if st2 != "pending" {
		t.Errorf("status after reactivation = %q, want pending", st2)
	}
	var subCount int
	if err := ta.db.Get(&subCount,
		`SELECT COUNT(*) FROM subscriptions WHERE email=$1 AND repo=$2`,
		"alice@example.com", "golang/go"); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subCount != 1 {
		t.Errorf("subscription rows = %d, want 1 (reused, not duplicated)", subCount)
	}

	// Two sagas now for this subscription: the failed attempt + the reactivation.
	var sagaCount int
	if err := ta.db.Get(&sagaCount, `SELECT COUNT(*) FROM saga WHERE subscription_id=$1`, subID); err != nil {
		t.Fatalf("count sagas: %v", err)
	}
	if sagaCount != 2 {
		t.Errorf("saga rows = %d, want 2 (failed attempt + reactivation)", sagaCount)
	}
}
