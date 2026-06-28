package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github-release-notifier/internal/notification"
	"github-release-notifier/internal/outbox"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// subscriptionCreator creates the pending subscription row inside the saga's
// transaction (step T1). *subscription.Store satisfies it. It is a narrow
// interface (primitive args) so the orchestrator does not import the
// subscription domain package.
type subscriptionCreator interface {
	CreateInTx(ctx context.Context, ext sqlx.ExtContext, email, repo, token string) (int, error)
	ReactivateInTx(ctx context.Context, ext sqlx.ExtContext, id int, token string) error
}

// Orchestrator drives the subscribe saga. In this step it runs T1
// transactionally; saga completion (reply handling) and compensation are added
// in later PRs.
type Orchestrator struct {
	db     *sqlx.DB
	subs   subscriptionCreator
	sagas  *Store
	outbox *outbox.Store
}

// New wires the orchestrator with the DB (for transactions), the subscription
// creator, the saga store, and the outbox store.
func New(db *sqlx.DB, subs subscriptionCreator, sagas *Store, ob *outbox.Store) *Orchestrator {
	return &Orchestrator{db: db, subs: subs, sagas: sagas, outbox: ob}
}

// StartConfirmation runs saga step T1: in a single local transaction it creates
// the pending subscription, records the saga, and writes the confirmation-email
// command to the outbox. The relay publishes the command afterwards. Committing
// all three together keeps "subscription created" and "email will be sent"
// atomic — no dual-write (ADR-010).
func (o *Orchestrator) StartConfirmation(ctx context.Context, email, repo, token, confirmURL string) error {
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
