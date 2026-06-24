//go:build integration

package integration

import (
	"context"
	"testing"

	"github-release-notifier/internal/orchestrator"
	"github-release-notifier/internal/outbox"
	"github-release-notifier/internal/subscription"
)

// newOrchestrator wires a real orchestrator against the shared test DB.
func newOrchestrator() *orchestrator.Orchestrator {
	return orchestrator.New(
		testDB,
		subscription.NewStore(testDB),
		orchestrator.NewStore(testDB),
		outbox.NewStore(testDB),
	)
}

func truncateSagaTables(t *testing.T) {
	t.Helper()
	if _, err := testDB.Exec(`TRUNCATE TABLE subscriptions, saga, outbox RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func assertCount(t *testing.T, query string, want int) {
	t.Helper()
	var got int
	if err := testDB.Get(&got, query); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Errorf("query %q: got %d, want %d", query, got, want)
	}
}

// TestOrchestrator_StartConfirmation_Atomic verifies T1 writes all three rows
// (subscription, saga, outbox command) in one go.
func TestOrchestrator_StartConfirmation_Atomic(t *testing.T) {
	truncateSagaTables(t)
	orch := newOrchestrator()

	err := orch.StartConfirmation(context.Background(),
		"a@b.com", "x/y", "tok-1", "http://t/api/confirm/tok-1")
	if err != nil {
		t.Fatalf("StartConfirmation: %v", err)
	}

	assertCount(t, `SELECT COUNT(*) FROM subscriptions WHERE confirmed=false`, 1)
	assertCount(t, `SELECT COUNT(*) FROM saga WHERE state='subscription_created'`, 1)
	assertCount(t, `SELECT COUNT(*) FROM outbox WHERE routing_key='confirmation' AND message_id='confirm:tok-1'`, 1)
}

// TestOrchestrator_StartConfirmation_RollsBackOnDuplicate verifies the whole T1
// transaction rolls back when one write fails: a second subscribe for the same
// (email, repo) violates UNIQUE(email, repo), so no saga or outbox row leaks.
func TestOrchestrator_StartConfirmation_RollsBackOnDuplicate(t *testing.T) {
	truncateSagaTables(t)
	orch := newOrchestrator()
	ctx := context.Background()

	if err := orch.StartConfirmation(ctx, "a@b.com", "x/y", "tok-1", "url1"); err != nil {
		t.Fatalf("first StartConfirmation: %v", err)
	}
	if err := orch.StartConfirmation(ctx, "a@b.com", "x/y", "tok-2", "url2"); err == nil {
		t.Fatal("expected duplicate StartConfirmation to fail on UNIQUE(email, repo)")
	}

	// Nothing from the failed second attempt persisted: still exactly one of each.
	assertCount(t, `SELECT COUNT(*) FROM subscriptions`, 1)
	assertCount(t, `SELECT COUNT(*) FROM saga`, 1)
	assertCount(t, `SELECT COUNT(*) FROM outbox`, 1)
}
