package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github-release-notifier/internal/outbox"
)

// Sweeper periodically recovers sagas left stuck by a crash or a lost message —
// the "resume on restart" half of the saga: it re-drives sagas still waiting and
// finishes interrupted compensations.
type Sweeper struct {
	sagas      *Store
	subs       subscriptionCompensator
	outbox     *outbox.Store
	interval   time.Duration
	staleAfter time.Duration
}

// NewSweeper wires the sweeper with the saga store, the subscription compensator,
// the outbox store, its poll interval, and how long a saga may sit non-terminal
// before it is treated as stuck.
func NewSweeper(sagas *Store, subs subscriptionCompensator, ob *outbox.Store, interval, staleAfter time.Duration) *Sweeper {
	return &Sweeper{sagas: sagas, subs: subs, outbox: ob, interval: interval, staleAfter: staleAfter}
}

// Run sweeps immediately, then on every tick, until ctx is canceled.
func (sw *Sweeper) Run(ctx context.Context) {
	slog.Info("Saga sweeper started", "interval", sw.interval, "stale_after", sw.staleAfter)
	if err := sw.Sweep(ctx); err != nil {
		slog.Error("Saga sweep failed", "err", err)
	}

	ticker := time.NewTicker(sw.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Saga sweeper stopping")
			return
		case <-ticker.C:
			if err := sw.Sweep(ctx); err != nil {
				slog.Error("Saga sweep failed", "err", err)
			}
		}
	}
}

// Sweep runs one recovery pass over stuck sagas. Each saga is handled
// independently: a failure on one is logged and does not stop the others.
func (sw *Sweeper) Sweep(ctx context.Context) error {
	before := time.Now().Add(-sw.staleAfter)
	sagas, err := sw.sagas.FindResumable(ctx, before)
	if err != nil {
		return err
	}
	for _, sg := range sagas {
		switch sg.State {
		case StateSubscriptionCreated:
			// Resume forward: re-publish the confirmation command. The consumer
			// dedups (no second email) and re-replies, completing the saga.
			if err := sw.outbox.Requeue(ctx, sg.ID); err != nil {
				slog.Error("Sweeper failed to re-drive saga", "saga_id", sg.ID, "err", err)
				continue
			}
			slog.Info("Sweeper re-driving stuck saga", "saga_id", sg.ID)
		case StateCompensating:
			// Finish an interrupted compensation (idempotent).
			if err := sw.subs.MarkFailed(ctx, sg.SubscriptionID); err != nil {
				slog.Error("Sweeper failed to compensate saga", "saga_id", sg.ID, "err", err)
				continue
			}
			if err := sw.sagas.UpdateState(ctx, sg.ID, StateFailed, "compensation finished by sweeper"); err != nil {
				slog.Error("Sweeper failed to fail saga", "saga_id", sg.ID, "err", err)
				continue
			}
			slog.Info("Sweeper finished compensation", "saga_id", sg.ID)
		}
	}
	return nil
}
