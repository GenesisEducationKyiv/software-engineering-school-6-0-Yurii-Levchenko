//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github-release-notifier/internal/orchestrator"
	"github-release-notifier/internal/outbox"
	"github-release-notifier/internal/subscription"
)

// stubConfirmationSender stands in for the gRPC client: it records calls and
// returns a configurable error, so we test the orchestrator's inline
// complete/compensate logic without a real notifier.
type stubConfirmationSender struct {
	err   error
	calls int
}

func (s *stubConfirmationSender) SendConfirmation(_ context.Context, _, _, _ string) error {
	s.calls++
	return s.err
}

// newGRPCOrchestrator wires a real orchestrator (real test DB) in gRPC mode by
// injecting the stub sender.
func newGRPCOrchestrator(sender *stubConfirmationSender) *orchestrator.Orchestrator {
	return orchestrator.New(
		testDB,
		subscription.NewStore(testDB),
		orchestrator.NewStore(testDB),
		outbox.NewStore(testDB),
		sender,
	)
}

// TestOrchestrator_StartConfirmation_GRPC_Success: the sync path calls the sender,
// completes the saga inline, and writes NO outbox row (the gRPC call replaces the
// relay+broker).
func TestOrchestrator_StartConfirmation_GRPC_Success(t *testing.T) {
	truncateSagaTables(t)
	sender := &stubConfirmationSender{}
	orch := newGRPCOrchestrator(sender)

	err := orch.StartConfirmation(context.Background(),
		"a@b.com", "x/y", "tok-g1", "http://t/api/confirm/tok-g1")
	if err != nil {
		t.Fatalf("StartConfirmation: %v", err)
	}

	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1", sender.calls)
	}
	assertCount(t, `SELECT COUNT(*) FROM subscriptions WHERE confirmed=false`, 1)
	assertCount(t, `SELECT COUNT(*) FROM saga WHERE state='completed'`, 1)
	assertCount(t, `SELECT COUNT(*) FROM outbox`, 0)
}

// TestOrchestrator_StartConfirmation_GRPC_Failure_Compensates: a send failure
// compensates inline (subscription -> failed, saga -> failed) and still returns
// nil, so the API contract matches the broker path (failure surfaces via status).
func TestOrchestrator_StartConfirmation_GRPC_Failure_Compensates(t *testing.T) {
	truncateSagaTables(t)
	sender := &stubConfirmationSender{err: errors.New("notifier down")}
	orch := newGRPCOrchestrator(sender)

	err := orch.StartConfirmation(context.Background(),
		"a@b.com", "x/y", "tok-g2", "http://t/api/confirm/tok-g2")
	if err != nil {
		t.Fatalf("StartConfirmation should return nil on send failure, got: %v", err)
	}

	assertCount(t, `SELECT COUNT(*) FROM subscriptions WHERE status='failed'`, 1)
	assertCount(t, `SELECT COUNT(*) FROM saga WHERE state='failed'`, 1)
	assertCount(t, `SELECT COUNT(*) FROM outbox`, 0)
}
