package subscription

import (
	"context"
	"fmt"
	"regexp"

	"github-release-notifier/internal/repospec"

	"github.com/google/uuid"
)

// SubscriptionStore covers per-subscription operations used by the
// subscription lifecycle (subscribe, confirm, unsubscribe, list-for-email)
// We split this from a previous fat RepositoryStore interface (ISP):
// each consumer now depends only on the methods it actually calls
type SubscriptionStore interface {
	CreateSubscription(sub *Subscription) error
	GetSubscriptionByToken(token string) (*Subscription, error)
	GetSubscriptionByEmailAndRepo(email, repo string) (*Subscription, error)
	ConfirmSubscription(token string) error
	DeleteSubscription(token string) error
	GetSubscriptionsByEmail(email string) ([]Subscription, error)
	GetActiveRepos() ([]string, error)
	GetSubscribersByRepo(repo string) ([]Subscription, error)
}

// RepoTracker is the minimum interface service needs from the tracking
// store: it only registers a repo for scanning after confirmation
// Scanner has its own broader interface (ReleaseTrackingStore) because
// it also reads tracking state; service never reads, only registers
type RepoTracker interface {
	RegisterRepo(repo string) error
}

// GitHubClient defines the interface for GitHub API operations
type GitHubClient interface {
	CheckRepoExists(ctx context.Context, owner, repo string) (bool, error)
}

// sagaStarter begins the subscribe saga: it transactionally creates the pending
// subscription and enqueues the confirmation-email command. Implemented by the
// orchestrator. The service depends on this interface (not the orchestrator
// concretely) so the domain layer stays free of transaction/outbox details.
type sagaStarter interface {
	StartConfirmation(ctx context.Context, email, repo, token, confirmURL string) error
	ReactivateConfirmation(ctx context.Context, subscriptionID int, email, repo, token, confirmURL string) error
}

// Service contains all business logic for the subscription system
// validation, orchestration, and rules live in this layer
// Handlers call Service methods; Service calls Repository and external clients
type Service struct {
	subs    SubscriptionStore
	tracker RepoTracker
	github  GitHubClient
	saga    sagaStarter
	baseURL string
}

// New creates a Service wired to its stores, the GitHub client and the saga
// starter. baseURL is used to build the confirm/unsubscribe links.
func New(subs SubscriptionStore, tracker RepoTracker, github GitHubClient, saga sagaStarter, baseURL string) *Service {
	return &Service{
		subs:    subs,
		tracker: tracker,
		github:  github,
		saga:    saga,
		baseURL: baseURL,
	}
}

// email validation pattern
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// ValidateEmail reports whether email is syntactically acceptable. It is a
// pragmatic check, not full RFC 5322 validation.
func ValidateEmail(email string) bool {
	return emailRegex.MatchString(email)
}

// Subscribe handles the subscription creation logic
// The ctx is propagated to the GitHub API call so the request can be canceled
// if the originating HTTP client disconnects or the server is shutting down
func (s *Service) Subscribe(ctx context.Context, email, repoStr string) error {
	// 1. Validate email
	if !ValidateEmail(email) {
		return ErrInvalidEmail
	}

	// 2. Validate repo format
	spec, err := repospec.ParseRepoSpec(repoStr)
	if err != nil {
		// Translate the model-layer parsing error into the service domain
		// sentinel, preserving the original cause via %w for logs/debug
		return fmt.Errorf("%w: %w", ErrInvalidRepoFormat, err)
	}

	// 3. Check if repo exists on GitHub
	exists, err := s.github.CheckRepoExists(ctx, spec.Owner, spec.Name)
	// if GitHub responds 200 then repo exists so returns true
	if err != nil {
		return fmt.Errorf("failed to check repository: %w", err)
	}
	if !exists {
		return ErrRepoNotFound
	}

	// 4. Look up any existing subscription for this (email, repo).
	existing, err := s.subs.GetSubscriptionByEmailAndRepo(email, repoStr)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	token := uuid.New().String()
	confirmURL := fmt.Sprintf("%s/api/confirm/%s", s.baseURL, token)

	// 5. Start (or reactivate) the subscribe saga. Step T1 transactionally writes
	// the subscription, the saga, and the confirmation-email command to the outbox
	// (the relay publishes it). See ADR-010.
	if existing != nil {
		// A pending or confirmed subscription is a real duplicate -> reject. A
		// previously failed one is revived (same row, fresh saga), not blocked.
		if existing.Status != StatusFailed {
			return ErrAlreadySubscribed
		}
		if err := s.saga.ReactivateConfirmation(ctx, existing.ID, email, repoStr, token, confirmURL); err != nil {
			return fmt.Errorf("failed to reactivate subscription saga: %w", err)
		}
		return nil
	}

	if err := s.saga.StartConfirmation(ctx, email, repoStr, token, confirmURL); err != nil {
		return fmt.Errorf("failed to start subscription saga: %w", err)
	}
	return nil
}

// Confirm handles the email confirmation logic
func (s *Service) Confirm(token string) error {
	sub, err := s.subs.GetSubscriptionByToken(token)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}
	if sub == nil {
		return ErrTokenNotFound
	}

	// Idempotent: re-confirming is not an error
	if !sub.Confirmed {
		if err := s.subs.ConfirmSubscription(token); err != nil {
			return fmt.Errorf("failed to confirm subscription: %w", err)
		}
	}

	// Ensure repo is being tracked. Runs on repeat confirms too (RegisterRepo
	// is idempotent), so retrying the link heals a failed registration.
	if err := s.tracker.RegisterRepo(sub.Repo); err != nil {
		return fmt.Errorf("failed to track repository: %w", err)
	}

	return nil
}

// Unsubscribe removes the subscription identified by token.
func (s *Service) Unsubscribe(token string) error {
	sub, err := s.subs.GetSubscriptionByToken(token)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}
	if sub == nil {
		return ErrTokenNotFound
	}

	return s.subs.DeleteSubscription(token)
}

// GetSubscriptions returns all subscriptions for the email (each with its
// status), so the caller can show pending / confirmed / failed.
func (s *Service) GetSubscriptions(email string) ([]Subscription, error) {
	if !ValidateEmail(email) {
		return nil, ErrInvalidEmail
	}
	return s.subs.GetSubscriptionsByEmail(email)
}

// ActiveRepos returns every repo that has at least one confirmed subscription.
// Part of the subscription facade — the release scanner reads it cross-domain
// instead of querying the subscriptions table itself.
func (s *Service) ActiveRepos() ([]string, error) {
	return s.subs.GetActiveRepos()
}

// SubscribersForRepo returns confirmed subscribers of a repo as slim Subscriber
// DTOs (the ACL): the full Subscription entity never leaves this domain.
func (s *Service) SubscribersForRepo(repo string) ([]Subscriber, error) {
	subs, err := s.subs.GetSubscribersByRepo(repo)
	if err != nil {
		return nil, err
	}
	out := make([]Subscriber, 0, len(subs))
	for _, sub := range subs {
		out = append(out, Subscriber{ID: sub.ID, Email: sub.Email, Token: sub.Token})
	}
	return out, nil
}
