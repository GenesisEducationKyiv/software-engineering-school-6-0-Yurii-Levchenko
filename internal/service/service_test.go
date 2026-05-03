package service

import (
	"context"
	"errors"
	"fmt"
	"github-release-notifier/internal/model"
	"testing"
)

// --- Mock implementations for testing ---
// These implement the interfaces defined in service.go

type mockRepo struct {
	subscriptions map[string]*model.Subscription // keyed by "email|repo"
	tokenMap      map[string]*model.Subscription // keyed by token
	repoTracking  map[string]string              // repo -> lastSeenTag
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		subscriptions: make(map[string]*model.Subscription),
		tokenMap:      make(map[string]*model.Subscription),
		repoTracking:  make(map[string]string),
	}
}

func (m *mockRepo) CreateSubscription(sub *model.Subscription) error {
	key := sub.Email + "|" + sub.Repo
	m.subscriptions[key] = sub
	m.tokenMap[sub.Token] = sub
	return nil
}

func (m *mockRepo) GetSubscriptionByToken(token string) (*model.Subscription, error) {
	sub, ok := m.tokenMap[token]
	if !ok {
		return nil, nil
	}
	return sub, nil
}

func (m *mockRepo) GetSubscriptionByEmailAndRepo(email, repo string) (*model.Subscription, error) {
	key := email + "|" + repo
	sub, ok := m.subscriptions[key]
	if !ok {
		return nil, nil
	}
	return sub, nil
}

func (m *mockRepo) ConfirmSubscription(token string) error {
	if sub, ok := m.tokenMap[token]; ok {
		sub.Confirmed = true
	}
	return nil
}

func (m *mockRepo) DeleteSubscription(token string) error {
	if sub, ok := m.tokenMap[token]; ok {
		key := sub.Email + "|" + sub.Repo
		delete(m.subscriptions, key)
		delete(m.tokenMap, token)
	}
	return nil
}

func (m *mockRepo) GetActiveSubscriptionsByEmail(email string) ([]model.Subscription, error) {
	var result []model.Subscription
	for _, sub := range m.subscriptions {
		if sub.Email == email && sub.Confirmed {
			result = append(result, *sub)
		}
	}
	return result, nil
}

func (m *mockRepo) UpsertRepoTracking(repo, lastSeenTag string) error {
	m.repoTracking[repo] = lastSeenTag
	return nil
}

type mockGitHub struct {
	existingRepos map[string]bool
}

func (m *mockGitHub) CheckRepoExists(_ context.Context, owner, repo string) (bool, error) {
	key := owner + "/" + repo
	return m.existingRepos[key], nil
}

type mockNotifier struct {
	emailsSent []string
}

func (m *mockNotifier) SendConfirmationEmail(to, confirmURL string) error {
	m.emailsSent = append(m.emailsSent, to)
	return nil
}

// --- Helper to create a Service with mocks ---

func setupTestService() (*Service, *mockRepo, *mockGitHub, *mockNotifier) {
	repo := newMockRepo()
	gh := &mockGitHub{existingRepos: map[string]bool{
		"golang/go":      true,
		"facebook/react": true,
	}}
	notif := &mockNotifier{}
	svc := New(repo, gh, notif, "http://localhost:8080")
	return svc, repo, gh, notif
}

// --- Tests ---

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"user@example.com", true},
		{"test.user+tag@domain.org", true},
		{"a@b.co", true},
		{"", false},
		{"notanemail", false},
		{"@domain.com", false},
		{"user@", false},
		{"user@.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			result := ValidateEmail(tt.email)
			if result != tt.valid {
				t.Errorf("ValidateEmail(%q) = %v, want %v", tt.email, result, tt.valid)
			}
		})
	}
}

func TestParseRepo(t *testing.T) {
	tests := []struct {
		input     string
		owner     string
		repo      string
		expectErr bool
	}{
		{"golang/go", "golang", "go", false},
		{"facebook/react", "facebook", "react", false},
		{"owner/repo-name", "owner", "repo-name", false},
		{"invalid", "", "", true},
		{"/repo", "", "", true},
		{"owner/", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, repo, err := ParseRepo(tt.input)
			if tt.expectErr && err == nil {
				t.Errorf("ParseRepo(%q) expected error, got nil", tt.input)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("ParseRepo(%q) unexpected error: %v", tt.input, err)
			}
			if owner != tt.owner || repo != tt.repo {
				t.Errorf("ParseRepo(%q) = (%q, %q), want (%q, %q)", tt.input, owner, repo, tt.owner, tt.repo)
			}
		})
	}
}

func TestSubscribe_Success(t *testing.T) {
	svc, repo, _, notif := setupTestService()

	err := svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Check subscription was created
	sub, _ := repo.GetSubscriptionByEmailAndRepo("user@example.com", "golang/go")
	if sub == nil {
		t.Fatal("Subscription not found in repository")
	}
	if sub.Confirmed {
		t.Error("New subscription should not be confirmed")
	}
	if sub.Token == "" {
		t.Error("Subscription should have a token")
	}

	// check whether confirmation email was sent
	if len(notif.emailsSent) != 1 || notif.emailsSent[0] != "user@example.com" {
		t.Errorf("Expected confirmation email to user@example.com, got %v", notif.emailsSent)
	}
}

func TestSubscribe_InvalidEmail(t *testing.T) {
	svc, _, _, _ := setupTestService()

	err := svc.Subscribe(context.Background(), "notanemail", "golang/go")
	if !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("Expected ErrInvalidEmail, got %v", err)
	}
}

func TestSubscribe_InvalidRepoFormat(t *testing.T) {
	svc, _, _, _ := setupTestService()

	err := svc.Subscribe(context.Background(), "user@example.com", "invalid-format")
	if !errors.Is(err, ErrInvalidRepoFormat) {
		t.Errorf("Expected ErrInvalidRepoFormat, got %v", err)
	}
}

func TestSubscribe_RepoNotFound(t *testing.T) {
	svc, _, _, _ := setupTestService()

	err := svc.Subscribe(context.Background(), "user@example.com", "nonexistent/repo")
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("Expected ErrRepoNotFound, got %v", err)
	}
}

func TestSubscribe_AlreadySubscribed(t *testing.T) {
	svc, _, _, _ := setupTestService()

	// first subscription
	err := svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	if err != nil {
		t.Fatalf("First subscribe failed: %v", err)
	}

	// duplicate subscription
	err = svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	if !errors.Is(err, ErrAlreadySubscribed) {
		t.Errorf("Expected ErrAlreadySubscribed, got %v", err)
	}
}

func TestConfirm_Success(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	// create a subscription first
	_ = svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	sub, _ := repo.GetSubscriptionByEmailAndRepo("user@example.com", "golang/go")

	// confirm it
	err := svc.Confirm(sub.Token)
	if err != nil {
		t.Fatalf("Confirm failed: %v", err)
	}

	// verify it's confirmed
	updated, _ := repo.GetSubscriptionByToken(sub.Token)
	if !updated.Confirmed {
		t.Error("Subscription should be confirmed")
	}
}

func TestConfirm_TokenNotFound(t *testing.T) {
	svc, _, _, _ := setupTestService()

	err := svc.Confirm("nonexistent-token")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("Expected ErrTokenNotFound, got %v", err)
	}
}

func TestConfirm_Idempotent(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	_ = svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	sub, _ := repo.GetSubscriptionByEmailAndRepo("user@example.com", "golang/go")

	// confirm twice. should not error
	_ = svc.Confirm(sub.Token)
	err := svc.Confirm(sub.Token)
	if err != nil {
		t.Errorf("Second confirm should succeed (idempotent), got %v", err)
	}
}

func TestUnsubscribe_Success(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	_ = svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	sub, _ := repo.GetSubscriptionByEmailAndRepo("user@example.com", "golang/go")

	err := svc.Unsubscribe(sub.Token)
	if err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	// verify it's deleted
	deleted, _ := repo.GetSubscriptionByToken(sub.Token)
	if deleted != nil {
		t.Error("Subscription should be deleted")
	}
}

func TestUnsubscribe_TokenNotFound(t *testing.T) {
	svc, _, _, _ := setupTestService()

	err := svc.Unsubscribe("nonexistent-token")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("Expected ErrTokenNotFound, got %v", err)
	}
}

func TestGetSubscriptions_ReturnsOnlyConfirmed(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	// create two subscriptions
	_ = svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	_ = svc.Subscribe(context.Background(), "user@example.com", "facebook/react")

	// confirm only one
	sub1, _ := repo.GetSubscriptionByEmailAndRepo("user@example.com", "golang/go")
	_ = svc.Confirm(sub1.Token)

	// get subscriptions. should only return the confirmed one
	subs, err := svc.GetSubscriptions("user@example.com")
	if err != nil {
		t.Fatalf("GetSubscriptions failed: %v", err)
	}

	if len(subs) != 1 {
		t.Errorf("Expected 1 subscription, got %d", len(subs))
	}
	if len(subs) > 0 && subs[0].Repo != "golang/go" {
		t.Errorf("Expected golang/go, got %s", subs[0].Repo)
	}
}

func TestGetSubscriptions_InvalidEmail(t *testing.T) {
	svc, _, _, _ := setupTestService()

	_, err := svc.GetSubscriptions("notanemail")
	if !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("Expected ErrInvalidEmail, got %v", err)
	}
}

func TestGetSubscriptions_Empty(t *testing.T) {
	svc, _, _, _ := setupTestService()

	subs, err := svc.GetSubscriptions("nobody@example.com")
	if err != nil {
		t.Fatalf("GetSubscriptions failed: %v", err)
	}
	if subs == nil {
		// nil is ok, it means empty
		fmt.Println("returned nil (no subscriptions)")
	}
	if len(subs) != 0 {
		t.Errorf("Expected 0 subscriptions, got %d", len(subs))
	}
}
