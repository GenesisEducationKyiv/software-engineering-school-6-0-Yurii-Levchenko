//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// post is a small helper to send a JSON body and return status + decoded
// body. It keeps test bodies focused on what's being verified.
func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()

	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp, err := testClient.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var parsed map[string]any
	if len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, &parsed); err != nil {
			t.Fatalf("unmarshal body: %v (raw: %s)", err, string(respBytes))
		}
	}
	return resp.StatusCode, parsed
}

func TestSubscribe_HappyPath(t *testing.T) {
	ta := newTestApp(t)
	ta.github.repos["golang/go"] = true

	status, body := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com",
		"repo":  "golang/go",
	})

	if status != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%v", status, body)
	}
	if _, ok := body["message"]; !ok {
		t.Errorf("response missing 'message' field: %v", body)
	}

	// Verify persisted row
	var count int
	if err := ta.db.Get(&count,
		`SELECT COUNT(*) FROM subscriptions WHERE email=$1 AND repo=$2 AND confirmed=false`,
		"alice@example.com", "golang/go"); err != nil {
		t.Fatalf("db query: %v", err)
	}
	if count != 1 {
		t.Errorf("subscription rows: got %d, want 1", count)
	}

	// Verify the saga was started and the confirmation command enqueued to the
	// outbox (the relay publishes it — no direct notifier call anymore).
	var sagaCount int
	if err := ta.db.Get(&sagaCount,
		`SELECT COUNT(*) FROM saga WHERE email=$1 AND repo=$2 AND state='subscription_created'`,
		"alice@example.com", "golang/go"); err != nil {
		t.Fatalf("db query saga: %v", err)
	}
	if sagaCount != 1 {
		t.Errorf("saga rows: got %d, want 1", sagaCount)
	}

	var outboxCount int
	if err := ta.db.Get(&outboxCount,
		`SELECT COUNT(*) FROM outbox WHERE routing_key='confirmation'`); err != nil {
		t.Fatalf("db query outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox confirmation rows: got %d, want 1", outboxCount)
	}
}

func TestSubscribe_InvalidEmail_400(t *testing.T) {
	ta := newTestApp(t)
	ta.github.repos["golang/go"] = true

	// Gin's binding `email` validator catches this before reaching the
	// service layer, so the message is generic "invalid request body".
	status, _ := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "not-an-email",
		"repo":  "golang/go",
	})

	if status != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", status)
	}
}

func TestSubscribe_InvalidRepoFormat_400(t *testing.T) {
	ta := newTestApp(t)

	status, body := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com",
		"repo":  "no-slash-here",
	})

	if status != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%v", status, body)
	}
}

func TestSubscribe_RepoNotFound_404(t *testing.T) {
	ta := newTestApp(t)
	// fakeGitHubClient returns false for any repo not in the map.

	status, _ := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com",
		"repo":  "ghost/does-not-exist",
	})

	if status != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", status)
	}

	// Should NOT have persisted anything.
	var count int
	if err := ta.db.Get(&count, `SELECT COUNT(*) FROM subscriptions`); err != nil {
		t.Fatalf("db query: %v", err)
	}
	if count != 0 {
		t.Errorf("subscription rows after 404: got %d, want 0", count)
	}
}

func TestSubscribe_Duplicate_409(t *testing.T) {
	ta := newTestApp(t)
	ta.github.repos["golang/go"] = true

	// First subscribe succeeds.
	status, _ := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com",
		"repo":  "golang/go",
	})
	if status != http.StatusOK {
		t.Fatalf("first subscribe: got %d, want 200", status)
	}

	// Second subscribe with same (email, repo) → 409.
	status, body := post(t, ta.server.URL+"/api/subscribe", map[string]string{
		"email": "alice@example.com",
		"repo":  "golang/go",
	})
	if status != http.StatusConflict {
		t.Fatalf("duplicate subscribe: got %d, want 409; body=%v", status, body)
	}

	// Only the first call should have created a saga + outbox command.
	var sagaCount int
	if err := ta.db.Get(&sagaCount, `SELECT COUNT(*) FROM saga`); err != nil {
		t.Fatalf("db query saga: %v", err)
	}
	if sagaCount != 1 {
		t.Errorf("saga rows after duplicate: got %d, want 1", sagaCount)
	}
}

func TestSubscribe_MissingFields_400(t *testing.T) {
	ta := newTestApp(t)

	// Empty body → Gin's `binding:"required"` validator rejects.
	status, _ := post(t, ta.server.URL+"/api/subscribe", map[string]string{})

	if status != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", status)
	}
}
