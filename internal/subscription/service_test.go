package subscription

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// --- Mock implementations for testing ---
// These implement the interfaces defined in service.go

type mockRepo struct {
	subscriptions map[string]*Subscription // keyed by "email|repo"
	tokenMap      map[string]*Subscription // keyed by token
	repoTracking  map[string]string        // repo -> lastSeenTag
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		subscriptions: make(map[string]*Subscription),
		tokenMap:      make(map[string]*Subscription),
		repoTracking:  make(map[string]string),
	}
}

func (m *mockRepo) CreateSubscription(sub *Subscription) error {
	key := sub.Email + "|" + sub.Repo
	m.subscriptions[key] = sub
	m.tokenMap[sub.Token] = sub
	return nil
}

func (m *mockRepo) GetSubscriptionByToken(token string) (*Subscription, error) {
	sub, ok := m.tokenMap[token]
	if !ok {
		return nil, nil
	}
	return sub, nil
}

func (m *mockRepo) GetSubscriptionByEmailAndRepo(email, repo string) (*Subscription, error) {
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
		sub.Status = StatusConfirmed
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

func (m *mockRepo) GetSubscriptionsByEmail(email string) ([]Subscription, error) {
	var result []Subscription
	for _, sub := range m.subscriptions {
		if sub.Email == email {
			result = append(result, *sub)
		}
	}
	return result, nil
}

func (m *mockRepo) RegisterRepo(repo string) error {
	if _, ok := m.repoTracking[repo]; !ok {
		m.repoTracking[repo] = ""
	}
	return nil
}

func (m *mockRepo) GetActiveRepos() ([]string, error) {
	seen := map[string]bool{}
	var repos []string
	for _, sub := range m.subscriptions {
		if sub.Confirmed && !seen[sub.Repo] {
			seen[sub.Repo] = true
			repos = append(repos, sub.Repo)
		}
	}
	return repos, nil
}

func (m *mockRepo) GetSubscribersByRepo(repo string) ([]Subscription, error) {
	var subs []Subscription
	for _, sub := range m.subscriptions {
		if sub.Repo == repo && sub.Confirmed {
			subs = append(subs, *sub)
		}
	}
	return subs, nil
}

type mockGitHub struct {
	existingRepos map[string]bool
}

func (m *mockGitHub) CheckRepoExists(_ context.Context, owner, repo string) (bool, error) {
	key := owner + "/" + repo
	return m.existingRepos[key], nil
}

// sagaCall records one StartConfirmation invocation for assertions.
type sagaCall struct {
	Email      string
	Repo       string
	Token      string
	ConfirmURL string
}

// reactivateCall records one ReactivateConfirmation invocation.
type reactivateCall struct {
	SubID int
	Email string
}

// fakeSaga implements sagaStarter. It records calls (and can be made to fail) so
// unit tests can verify Subscribe hands off correctly without a real DB/broker.
type fakeSaga struct {
	starts      []sagaCall
	reactivates []reactivateCall
	err         error
}

func (f *fakeSaga) StartConfirmation(_ context.Context, email, repo, token, confirmURL string) error {
	if f.err != nil {
		return f.err
	}
	f.starts = append(f.starts, sagaCall{Email: email, Repo: repo, Token: token, ConfirmURL: confirmURL})
	return nil
}

func (f *fakeSaga) ReactivateConfirmation(_ context.Context, subID int, email, repo, token, confirmURL string) error {
	if f.err != nil {
		return f.err
	}
	f.reactivates = append(f.reactivates, reactivateCall{SubID: subID, Email: email})
	return nil
}

// --- Helper to create a Service with mocks ---

func setupTestService() (*Service, *mockRepo, *mockGitHub, *fakeSaga) {
	repo := newMockRepo()
	gh := &mockGitHub{existingRepos: map[string]bool{
		"golang/go":      true,
		"facebook/react": true,
	}}
	saga := &fakeSaga{}
	svc := New(repo, repo, gh, saga, "http://localhost:8080")
	return svc, repo, gh, saga
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

func TestSubscribe_Success(t *testing.T) {
	svc, _, _, saga := setupTestService()

	err := svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Subscribe should have started exactly one saga with the right arguments.
	if len(saga.starts) != 1 {
		t.Fatalf("saga starts: got %d, want 1", len(saga.starts))
	}
	c := saga.starts[0]
	if c.Email != "user@example.com" || c.Repo != "golang/go" {
		t.Errorf("saga call = %+v, want email/repo user@example.com / golang/go", c)
	}
	if c.Token == "" {
		t.Error("saga call should carry a generated token")
	}
	if !strings.Contains(c.ConfirmURL, "/api/confirm/") {
		t.Errorf("confirmURL missing /api/confirm/: %q", c.ConfirmURL)
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
	svc, repo, _, _ := setupTestService()

	// Seed a confirmed subscription directly (creation now lives in the saga,
	// so we set up the duplicate state in the store rather than via Subscribe).
	_ = repo.CreateSubscription(&Subscription{
		Email: "user@example.com", Repo: "golang/go", Token: "seed-token", Status: StatusConfirmed,
	})

	err := svc.Subscribe(context.Background(), "user@example.com", "golang/go")
	if !errors.Is(err, ErrAlreadySubscribed) {
		t.Errorf("Expected ErrAlreadySubscribed, got %v", err)
	}
}

func TestSubscribe_ReactivatesFailedSubscription(t *testing.T) {
	svc, repo, _, saga := setupTestService()

	// A previously failed subscription exists for this (email, repo).
	_ = repo.CreateSubscription(&Subscription{
		ID: 7, Email: "user@example.com", Repo: "golang/go", Token: "old", Status: StatusFailed,
	})

	if err := svc.Subscribe(context.Background(), "user@example.com", "golang/go"); err != nil {
		t.Fatalf("Subscribe (reactivate) failed: %v", err)
	}

	// It must reactivate the existing row, not start a brand-new saga.
	if len(saga.reactivates) != 1 || saga.reactivates[0].SubID != 7 {
		t.Fatalf("reactivations = %+v, want one for subID 7", saga.reactivates)
	}
	if len(saga.starts) != 0 {
		t.Errorf("must not StartConfirmation when a failed subscription exists, got %d", len(saga.starts))
	}
}

func TestConfirm_Success(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	_ = repo.CreateSubscription(&Subscription{
		Email: "user@example.com", Repo: "golang/go", Token: "tok-confirm",
	})

	if err := svc.Confirm("tok-confirm"); err != nil {
		t.Fatalf("Confirm failed: %v", err)
	}

	updated, _ := repo.GetSubscriptionByToken("tok-confirm")
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

	_ = repo.CreateSubscription(&Subscription{
		Email: "user@example.com", Repo: "golang/go", Token: "tok-idem",
	})

	// confirm twice. should not error
	_ = svc.Confirm("tok-idem")
	err := svc.Confirm("tok-idem")
	if err != nil {
		t.Errorf("Second confirm should succeed (idempotent), got %v", err)
	}
}

func TestUnsubscribe_Success(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	_ = repo.CreateSubscription(&Subscription{
		Email: "user@example.com", Repo: "golang/go", Token: "tok-unsub",
	})

	err := svc.Unsubscribe("tok-unsub")
	if err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	deleted, _ := repo.GetSubscriptionByToken("tok-unsub")
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

func TestGetSubscriptions_ReturnsAllWithStatus(t *testing.T) {
	svc, repo, _, _ := setupTestService()

	// Two subscriptions: confirm one, leave the other pending.
	_ = repo.CreateSubscription(&Subscription{
		Email: "user@example.com", Repo: "golang/go", Token: "t1", Status: StatusPending,
	})
	_ = repo.CreateSubscription(&Subscription{
		Email: "user@example.com", Repo: "facebook/react", Token: "t2", Status: StatusPending,
	})
	_ = svc.Confirm("t1") // golang/go -> confirmed

	subs, err := svc.GetSubscriptions("user@example.com")
	if err != nil {
		t.Fatalf("GetSubscriptions failed: %v", err)
	}

	// Now ALL subscriptions are returned, each with its status (not only confirmed).
	if len(subs) != 2 {
		t.Fatalf("Expected 2 subscriptions, got %d", len(subs))
	}
	statusByRepo := map[string]string{}
	for _, s := range subs {
		statusByRepo[s.Repo] = s.Status
	}
	if statusByRepo["golang/go"] != StatusConfirmed {
		t.Errorf("golang/go status = %q, want confirmed", statusByRepo["golang/go"])
	}
	if statusByRepo["facebook/react"] != StatusPending {
		t.Errorf("facebook/react status = %q, want pending", statusByRepo["facebook/react"])
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
