package service

import (
	"context"
	"fmt"
	"github-release-notifier/internal/model"
	"regexp"

	"github.com/google/uuid"
)

// RepositoryStore defines the interface for database operations
// we code against the interface, not the implementation. This makes unit testing easy (we can mock it)
type RepositoryStore interface {
	CreateSubscription(sub *model.Subscription) error
	GetSubscriptionByToken(token string) (*model.Subscription, error)
	GetSubscriptionByEmailAndRepo(email, repo string) (*model.Subscription, error)
	ConfirmSubscription(token string) error
	DeleteSubscription(token string) error
	GetActiveSubscriptionsByEmail(email string) ([]model.Subscription, error)
	UpsertRepoTracking(repo, lastSeenTag string) error
}

// GitHubClient defines the interface for GitHub API operations
type GitHubClient interface {
	CheckRepoExists(ctx context.Context, owner, repo string) (bool, error)
}

// EmailNotifier defines the interface for sending emails
type EmailNotifier interface {
	SendConfirmationEmail(to, confirmURL string) error
}

// Service contains all business logic for the subscription system
// validation, orchestration, and rules live in this layer
// Handlers call Service methods; Service calls Repository and external clients
type Service struct {
	repo     RepositoryStore
	github   GitHubClient
	notifier EmailNotifier
	baseURL  string
}

// creates a new Service
func New(repo RepositoryStore, github GitHubClient, notifier EmailNotifier, baseURL string) *Service {
	return &Service{
		repo:     repo,
		github:   github,
		notifier: notifier,
		baseURL:  baseURL,
	}
}

// Custom error types so handlers can return proper HTTP status codes
var (
	ErrRepoNotFound      = fmt.Errorf("repository not found on GitHub")
	ErrAlreadySubscribed = fmt.Errorf("email is already subscribed to this repository")
	ErrTokenNotFound     = fmt.Errorf("subscription not found")
	ErrInvalidEmail      = fmt.Errorf("invalid email address")
)

// ErrInvalidRepoFormat is re-exported from the model package so existing
// callers (handler, tests) can keep using service.ErrInvalidRepoFormat
// without importing model directly. The canonical definition lives in
// model.ErrInvalidRepoFormat (GRASP Principle - Information Expert: parsing rules belong
// to the value object)
var ErrInvalidRepoFormat = model.ErrInvalidRepoFormat

// email validation pattern
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// checks if email address is valid
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
	spec, err := model.ParseRepoSpec(repoStr)
	if err != nil {
		return err
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

	// 4. Check if already subscribed
	existing, err := s.repo.GetSubscriptionByEmailAndRepo(email, repoStr)
	// it returns nil - no duplicates so we can proceed with creating a subscription
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}
	if existing != nil {
		return ErrAlreadySubscribed
	}

	// 5. Create subscription with a unique token
	token := uuid.New().String()
	sub := &model.Subscription{
		Email:     email,
		Repo:      repoStr,
		Token:     token,
		Confirmed: false,
	}

	if err := s.repo.CreateSubscription(sub); err != nil {
		return fmt.Errorf("failed to create subscription: %w", err)
	}

	// 6. Send confirmation email
	confirmURL := fmt.Sprintf("%s/api/confirm/%s", s.baseURL, token)
	if err := s.notifier.SendConfirmationEmail(email, confirmURL); err != nil {
		return fmt.Errorf("failed to send confirmation email: %w", err)
	}

	return nil
}

// Confirm handles the email confirmation logic
func (s *Service) Confirm(token string) error {
	sub, err := s.repo.GetSubscriptionByToken(token)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}
	if sub == nil {
		return ErrTokenNotFound
	}

	// Idempotent: if already confirmed, just return success
	if sub.Confirmed {
		return nil
	}

	if err := s.repo.ConfirmSubscription(token); err != nil {
		return fmt.Errorf("failed to confirm subscription: %w", err)
	}

	// Ensure repo is being tracked
	if err := s.repo.UpsertRepoTracking(sub.Repo, ""); err != nil {
		return fmt.Errorf("failed to track repository: %w", err)
	}

	return nil
}

// unsubscription logic
func (s *Service) Unsubscribe(token string) error {
	sub, err := s.repo.GetSubscriptionByToken(token)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}
	if sub == nil {
		return ErrTokenNotFound
	}

	return s.repo.DeleteSubscription(token)
}

// returns all active subscriptions for email. basically runs SQL query
func (s *Service) GetSubscriptions(email string) ([]model.Subscription, error) {
	if !ValidateEmail(email) {
		return nil, ErrInvalidEmail
	}
	return s.repo.GetActiveSubscriptionsByEmail(email)
}
