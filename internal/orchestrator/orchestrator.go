package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github-release-notifier/internal/notification"
	"github-release-notifier/internal/outbox"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// subscriptionStore is the subscription-side operations the orchestrator needs:
// create/reactivate the row inside the saga's T1 transaction, and (for the
// synchronous gRPC path) mark it failed during inline compensation.
// *subscription.Store satisfies it. Narrow (primitive args) so the orchestrator
// does not import the subscription domain package.
type subscriptionStore interface {
	CreateInTx(ctx context.Context, ext sqlx.ExtContext, email, repo, token string) (int, error)
	ReactivateInTx(ctx context.Context, ext sqlx.ExtContext, id int, token string) error
	MarkFailed(ctx context.Context, id int) error
}

// confirmationSender sends the confirmation email synchronously (the gRPC
// transport, ADR-011). When wired (non-nil), the orchestrator drives the saga
// inline — call, then complete or compensate — instead of via the outbox →
// broker → async-reply path. Implemented by the monolith's gRPC client.
type confirmationSender interface {
	SendConfirmation(ctx context.Context, email, repo, confirmURL string) error
}

// Orchestrator drives the subscribe saga. In this step it runs T1
// transactionally; saga completion (reply handling) and compensation are added
// in later PRs.
type Orchestrator struct {
	db     *sqlx.DB
	subs   subscriptionStore
	sagas  *Store
	outbox *outbox.Store
	sender confirmationSender // nil ⇒ async broker transport (the default)
}

// New wires the orchestrator with the DB (for transactions), the subscription
// store, the saga store, and the outbox store. sender selects the confirmation
// transport: nil uses the async broker path (outbox → relay → RabbitMQ); a
// non-nil sender uses the synchronous gRPC path (ADR-011).
func New(db *sqlx.DB, subs subscriptionStore, sagas *Store, ob *outbox.Store, sender confirmationSender) *Orchestrator {
	return &Orchestrator{db: db, subs: subs, sagas: sagas, outbox: ob, sender: sender}
}

// StartConfirmation runs saga step T1: in a single local transaction it creates
// the pending subscription, records the saga, and writes the confirmation-email
// command to the outbox. The relay publishes the command afterwards. Committing
// all three together keeps "subscription created" and "email will be sent"
// atomic — no dual-write (ADR-010).
func (o *Orchestrator) StartConfirmation(ctx context.Context, email, repo, token, confirmURL string) error {
	if o.sender != nil {
		return o.startConfirmationSync(ctx, email, repo, token, confirmURL)
	}

	tx, err := o.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin saga transaction: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded, so it is always safe to defer.
	defer func() { _ = tx.Rollback() }()

	subID, err := o.subs.CreateInTx(ctx, tx, email, repo, token)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	if err := o.enqueueConfirmation(ctx, tx, subID, email, repo, token, confirmURL); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit saga transaction: %w", err)
	}
	return nil
}

// ReactivateConfirmation runs step T1 for an EXISTING (previously failed)
// subscription: it revives the row (fresh token, back to pending) and starts a
// new saga, all in one transaction. The old (failed) saga stays as history.
func (o *Orchestrator) ReactivateConfirmation(ctx context.Context, subscriptionID int, email, repo, token, confirmURL string) error {
	if o.sender != nil {
		return o.reactivateConfirmationSync(ctx, subscriptionID, email, repo, token, confirmURL)
	}

	tx, err := o.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin saga transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := o.subs.ReactivateInTx(ctx, tx, subscriptionID, token); err != nil {
		return fmt.Errorf("reactivate subscription: %w", err)
	}
	if err := o.enqueueConfirmation(ctx, tx, subscriptionID, email, repo, token, confirmURL); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit saga transaction: %w", err)
	}
	return nil
}

// enqueueConfirmation writes the saga row and the confirmation-email outbox
// command within the caller's transaction. Shared by StartConfirmation (new
// subscription) and ReactivateConfirmation (revived one).
func (o *Orchestrator) enqueueConfirmation(ctx context.Context, tx *sqlx.Tx, subID int, email, repo, token, confirmURL string) error {
	sagaID := uuid.NewString()

	payload, err := json.Marshal(notification.ConfirmationRequest{
		SagaID:     sagaID,
		To:         email,
		ConfirmURL: confirmURL,
	})
	if err != nil {
		return fmt.Errorf("marshal confirmation command: %w", err)
	}

	sg := &Saga{
		ID:             sagaID,
		SubscriptionID: subID,
		Email:          email,
		Repo:           repo,
		State:          StateSubscriptionCreated,
	}
	if err := o.sagas.CreateInTx(ctx, tx, sg); err != nil {
		return fmt.Errorf("create saga: %w", err)
	}

	if err := o.outbox.Enqueue(ctx, tx, sagaID, notification.RoutingConfirm, "confirm:"+token, payload); err != nil {
		return fmt.Errorf("enqueue confirmation command: %w", err)
	}
	return nil
}

// --- Synchronous gRPC transport (ADR-011) ---
// When a confirmationSender is wired, Subscribe drives the confirmation inline
// instead of via the outbox. T1 writes subscription + saga only (NO outbox row —
// otherwise the relay would ALSO publish it and the notifier would send twice).
// After committing T1, the orchestrator calls the notifier synchronously and
// advances the saga from the result.

func (o *Orchestrator) startConfirmationSync(ctx context.Context, email, repo, token, confirmURL string) error {
	subID, sagaID, err := o.createSubscriptionAndSaga(ctx, email, repo, token, 0)
	if err != nil {
		return err
	}
	return o.dispatchSync(ctx, sagaID, subID, email, repo, confirmURL)
}

func (o *Orchestrator) reactivateConfirmationSync(ctx context.Context, subscriptionID int, email, repo, token, confirmURL string) error {
	subID, sagaID, err := o.createSubscriptionAndSaga(ctx, email, repo, token, subscriptionID)
	if err != nil {
		return err
	}
	return o.dispatchSync(ctx, sagaID, subID, email, repo, confirmURL)
}

// createSubscriptionAndSaga runs the sync-path T1: create-or-reactivate the
// subscription and insert the saga row in one transaction — no outbox command.
// reactivateID == 0 means a new subscription; otherwise the existing row is
// revived (fresh token, back to pending).
func (o *Orchestrator) createSubscriptionAndSaga(ctx context.Context, email, repo, token string, reactivateID int) (subID int, sagaID string, err error) {
	tx, err := o.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, "", fmt.Errorf("begin saga transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if reactivateID == 0 {
		subID, err = o.subs.CreateInTx(ctx, tx, email, repo, token)
		if err != nil {
			return 0, "", fmt.Errorf("create subscription: %w", err)
		}
	} else {
		if err = o.subs.ReactivateInTx(ctx, tx, reactivateID, token); err != nil {
			return 0, "", fmt.Errorf("reactivate subscription: %w", err)
		}
		subID = reactivateID
	}

	sagaID = uuid.NewString()
	sg := &Saga{ID: sagaID, SubscriptionID: subID, Email: email, Repo: repo, State: StateSubscriptionCreated}
	if err = o.sagas.CreateInTx(ctx, tx, sg); err != nil {
		return 0, "", fmt.Errorf("create saga: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("commit saga transaction: %w", err)
	}
	return subID, sagaID, nil
}

// dispatchSync calls the notifier over gRPC and advances the saga from the
// outcome: completed on success, compensated on any error. It returns nil even
// when the send fails — the failure is reflected in the subscription status, so
// the API contract stays identical to the broker path (which also returns
// success and surfaces failure asynchronously via status).
func (o *Orchestrator) dispatchSync(ctx context.Context, sagaID string, subID int, email, repo, confirmURL string) error {
	if err := o.sender.SendConfirmation(ctx, email, repo, confirmURL); err != nil {
		slog.Error("Sync confirmation failed, compensating saga", "saga_id", sagaID, "err", err)
		return o.compensateSync(ctx, sagaID, subID)
	}
	if err := o.sagas.UpdateState(ctx, sagaID, StateCompleted, ""); err != nil {
		return fmt.Errorf("complete saga %s: %w", sagaID, err)
	}
	slog.Info("Saga completed (grpc)", "saga_id", sagaID)
	return nil
}

// compensateSync runs compensation C1 inline — mark the subscription failed —
// the same effect as the async "failed" reply on the broker path. It moves the
// saga through compensating → failed so a crash mid-way is still recognizable.
func (o *Orchestrator) compensateSync(ctx context.Context, sagaID string, subID int) error {
	const reason = "confirmation email failed (grpc)"
	if err := o.sagas.UpdateState(ctx, sagaID, StateCompensating, reason); err != nil {
		return fmt.Errorf("mark saga %s compensating: %w", sagaID, err)
	}
	if err := o.subs.MarkFailed(ctx, subID); err != nil {
		return fmt.Errorf("compensate saga %s: %w", sagaID, err)
	}
	if err := o.sagas.UpdateState(ctx, sagaID, StateFailed, reason); err != nil {
		return fmt.Errorf("fail saga %s: %w", sagaID, err)
	}
	slog.Info("Saga compensated (grpc)", "saga_id", sagaID)
	return nil
}
