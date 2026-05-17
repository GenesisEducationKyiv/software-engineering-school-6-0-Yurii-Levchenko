//go:build integration

package integration

import (
	"net/http"
	"testing"
)

func TestUnsubscribe_HappyPath(t *testing.T) {
	ta := newTestApp(t)
	token := subscribeAndGetToken(t, ta, "alice@example.com", "golang/go")

	status, _ := get(t, ta.server.URL+"/api/unsubscribe/"+token)
	if status != http.StatusOK {
		t.Fatalf("unsubscribe: got %d, want 200", status)
	}

	// Row should be gone.
	var count int
	if err := ta.db.Get(&count,
		`SELECT COUNT(*) FROM subscriptions WHERE token=$1`, token); err != nil {
		t.Fatalf("db query: %v", err)
	}
	if count != 0 {
		t.Errorf("subscription rows after unsubscribe: got %d, want 0", count)
	}
}

func TestUnsubscribe_InvalidToken_404(t *testing.T) {
	ta := newTestApp(t)

	status, _ := get(t, ta.server.URL+"/api/unsubscribe/no-such-token")

	if status != http.StatusNotFound {
		t.Fatalf("unsubscribe with bad token: got %d, want 404", status)
	}
}
