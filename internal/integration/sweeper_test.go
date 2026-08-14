//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github-release-notifier/internal/orchestrator"
	"github-release-notifier/internal/outbox"
	"github-release-notifier/internal/subscription"
)

func newSweeper() *orchestrator.Sweeper {
	// interval is irrelevant — tests call Sweep directly. staleAfter=1m so a
	// backdated saga (1h old) is treated as stuck.
	return orchestrator.NewSweeper(
		orchestrator.NewStore(testDB),
		subscription.NewStore(testDB),
		outbox.NewStore(testDB),
		time.Minute, time.Minute,
	)
}

// backdateSaga makes a saga look stuck by moving its updated_at into the past.
func backdateSaga(t *testing.T, sagaID string) {
	t.Helper()
	if _, err := testDB.Exec(`UPDATE saga SET updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, sagaID); err != nil {
		t.Fatalf("backdate saga: %v", err)
	}
}

// TestSweeper_ResumesStuckSubscriptionCreated: a saga still waiting (its reply
// was lost) gets its outbox command re-queued for the relay to re-publish.
func TestSweeper_ResumesStuckSubscriptionCreated(t *testing.T) {
	truncateSagaTables(t)
	ctx := context.Background()
	sagaID, _ := startOneSaga(t, "a@b.com", "x/y", "tok-stuck")

	// Pretend the relay already published it, then the saga got stuck.
	if _, err := testDB.Exec(`UPDATE outbox SET published_at = NOW() WHERE saga_id = $1`, sagaID); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	backdateSaga(t, sagaID)

	if err := newSweeper().Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// The outbox command is unpublished again -> the relay will re-publish it.
	var unpublished int
	if err := testDB.Get(&unpublished,
		`SELECT COUNT(*) FROM outbox WHERE saga_id = $1 AND published_at IS NULL`, sagaID); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if unpublished != 1 {
		t.Errorf("unpublished outbox rows for saga = %d, want 1 (re-driven)", unpublished)
	}
}

// TestSweeper_FinishesStuckCompensating: a saga left mid-compensation (crash
// between marking compensating and failed) is finished — subscription failed,
// saga failed.
func TestSweeper_FinishesStuckCompensating(t *testing.T) {
	truncateSagaTables(t)
	ctx := context.Background()
	sagaID, subID := startOneSaga(t, "a@b.com", "x/y", "tok-comp")

	// Simulate a crash mid-compensation: saga is compensating, subscription not
	// yet marked, and it has been sitting there.
	if _, err := testDB.Exec(`UPDATE saga SET state = 'compensating' WHERE id = $1`, sagaID); err != nil {
		t.Fatalf("set compensating: %v", err)
	}
	backdateSaga(t, sagaID)

	if err := newSweeper().Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	var sagaState, subStatus string
	if err := testDB.Get(&sagaState, `SELECT state FROM saga WHERE id = $1`, sagaID); err != nil {
		t.Fatalf("query saga: %v", err)
	}
	if err := testDB.Get(&subStatus, `SELECT status FROM subscriptions WHERE id = $1`, subID); err != nil {
		t.Fatalf("query subscription: %v", err)
	}
	if sagaState != string(orchestrator.StateFailed) {
		t.Errorf("saga state = %q, want failed", sagaState)
	}
	if subStatus != "failed" {
		t.Errorf("subscription status = %q, want failed", subStatus)
	}
}

// TestSweeper_IgnoresFreshSagas: a saga that is still within the staleAfter
// window is left alone.
func TestSweeper_IgnoresFreshSagas(t *testing.T) {
	truncateSagaTables(t)
	ctx := context.Background()
	sagaID, _ := startOneSaga(t, "a@b.com", "x/y", "tok-fresh")
	// not backdated -> fresh

	if err := newSweeper().Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// Outbox row still in its original (unpublished) state, not touched by a sweep.
	var state string
	if err := testDB.Get(&state, `SELECT state FROM saga WHERE id = $1`, sagaID); err != nil {
		t.Fatalf("query saga: %v", err)
	}
	if state != string(orchestrator.StateSubscriptionCreated) {
		t.Errorf("fresh saga state = %q, want subscription_created (untouched)", state)
	}
}
