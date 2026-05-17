//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// getRaw returns the raw body bytes — needed for endpoints that return
// arrays (which don't parse into map[string]any).
func getRaw(t *testing.T, url string) (int, []byte) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestListSubscriptions_HappyPath(t *testing.T) {
	ta := newTestApp(t)
	// Setup: alice subscribes to two repos, confirms one, leaves the other unconfirmed.
	tokenGo := subscribeAndGetToken(t, ta, "alice@example.com", "golang/go")
	_ = subscribeAndGetToken(t, ta, "alice@example.com", "gin-gonic/gin")

	if status, _ := get(t, ta.server.URL+"/api/confirm/"+tokenGo); status != http.StatusOK {
		t.Fatalf("confirm setup: got %d, want 200", status)
	}

	// Action
	status, body := getRaw(t, ta.server.URL+"/api/subscriptions?email=alice@example.com")
	if status != http.StatusOK {
		t.Fatalf("list: got %d, want 200; body=%s", status, body)
	}

	// Assertion — only the confirmed one should come back.
	var subs []map[string]any
	if err := json.Unmarshal(body, &subs); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, body)
	}
	if len(subs) != 1 {
		t.Fatalf("active subs: got %d, want 1 (only confirmed); body=%s",
			len(subs), body)
	}
	if subs[0]["repo"] != "golang/go" {
		t.Errorf("returned repo: got %v, want golang/go", subs[0]["repo"])
	}
}

func TestListSubscriptions_EmptyResult(t *testing.T) {
	ta := newTestApp(t)

	status, body := getRaw(t, ta.server.URL+"/api/subscriptions?email=nobody@example.com")
	if status != http.StatusOK {
		t.Fatalf("list with no data: got %d, want 200", status)
	}

	// Must serialize to "[]" not "null" (handler explicitly normalises this).
	if string(body) != "[]" {
		t.Errorf("empty list body: got %q, want %q", string(body), "[]")
	}
}

func TestListSubscriptions_MissingEmail_400(t *testing.T) {
	ta := newTestApp(t)

	status, _ := get(t, ta.server.URL+"/api/subscriptions")
	if status != http.StatusBadRequest {
		t.Fatalf("list without ?email: got %d, want 400", status)
	}
}

func TestListSubscriptions_InvalidEmail_400(t *testing.T) {
	ta := newTestApp(t)

	status, _ := get(t, ta.server.URL+"/api/subscriptions?email=not-an-email")
	// service.GetSubscriptions runs ValidateEmail → ErrInvalidEmail → 400.
	if status != http.StatusBadRequest {
		t.Fatalf("list with invalid email: got %d, want 400", status)
	}
}
