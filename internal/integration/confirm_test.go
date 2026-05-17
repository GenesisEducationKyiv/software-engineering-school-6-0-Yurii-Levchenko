//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// get is a small helper that GETs a URL and returns status + parsed body.
func get(t *testing.T, url string) (int, map[string]any) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	var parsed map[string]any
	if len(respBytes) > 0 {
		// `/metrics` returns text/plain; tests that care about that
		// should not call this helper.
		_ = json.Unmarshal(respBytes, &parsed)
	}
	return resp.StatusCode, parsed
}

// subscribeAndGetToken is a small test driver that performs the prerequisite
// subscribe step and returns the generated token so the test body can stay
// focused on the action under test (Lecture 6: "Test Drivers" pattern).
func subscribeAndGetToken(t *testing.T, ta *testApp, email, repo string) string {
	t.Helper()
	ta.github.repos[repo] = true

	status, _ := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": email,
		"repo":  repo,
	})
	if status != http.StatusOK {
		t.Fatalf("setup subscribe: got %d, want 200", status)
	}

	var token string
	if err := ta.db.Get(&token,
		`SELECT token FROM subscriptions WHERE email=$1 AND repo=$2`,
		email, repo); err != nil {
		t.Fatalf("fetch token from DB: %v", err)
	}
	return token
}

func TestConfirm_HappyPath(t *testing.T) {
	ta := newTestApp(t)
	token := subscribeAndGetToken(t, ta, "alice@example.com", "golang/go")

	status, body := get(t, ta.server.URL+"/api/confirm/"+token)

	if status != http.StatusOK {
		t.Fatalf("confirm: got %d, want 200; body=%v", status, body)
	}

	// subscription.confirmed should now be true
	var confirmed bool
	if err := ta.db.Get(&confirmed,
		`SELECT confirmed FROM subscriptions WHERE token=$1`, token); err != nil {
		t.Fatalf("db query: %v", err)
	}
	if !confirmed {
		t.Errorf("subscription.confirmed: got false, want true")
	}

	// Service.Confirm also upserts the repo into `repositories` for scanner tracking.
	var repoCount int
	if err := ta.db.Get(&repoCount,
		`SELECT COUNT(*) FROM repositories WHERE repo=$1`, "golang/go"); err != nil {
		t.Fatalf("db query: %v", err)
	}
	if repoCount != 1 {
		t.Errorf("repositories rows for golang/go: got %d, want 1", repoCount)
	}
}

func TestConfirm_InvalidToken_404(t *testing.T) {
	ta := newTestApp(t)

	status, _ := get(t, ta.server.URL+"/api/confirm/this-token-does-not-exist")

	if status != http.StatusNotFound {
		t.Fatalf("confirm with bad token: got %d, want 404", status)
	}
}

func TestConfirm_AlreadyConfirmed_Idempotent(t *testing.T) {
	ta := newTestApp(t)
	token := subscribeAndGetToken(t, ta, "alice@example.com", "golang/go")

	// Confirm once
	status, _ := get(t, ta.server.URL+"/api/confirm/"+token)
	if status != http.StatusOK {
		t.Fatalf("first confirm: got %d, want 200", status)
	}

	// Confirm again — service.Confirm is idempotent, should still return 200
	status, _ = get(t, ta.server.URL+"/api/confirm/"+token)
	if status != http.StatusOK {
		t.Fatalf("second confirm (idempotent): got %d, want 200", status)
	}
}
