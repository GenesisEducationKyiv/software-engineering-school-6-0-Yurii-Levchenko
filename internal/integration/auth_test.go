//go:build integration

package integration

import (
	"net/http"
	"testing"
)

const testAPIKey = "secret-test-key"

// doAuthRequest issues a GET to the auth-protected /api/subscriptions route,
// optionally setting the X-API-Key header (apiKey="" sends no header).
func doAuthRequest(t *testing.T, ta *testApp, apiKey string) int {
	t.Helper()

	req, err := http.NewRequest(
		http.MethodGet,
		ta.server.URL+"/api/subscriptions?email=nobody@example.com",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestAPIKey_MissingHeader_401(t *testing.T) {
	ta := newTestAppWithKey(t, testAPIKey)

	if status := doAuthRequest(t, ta, ""); status != http.StatusUnauthorized {
		t.Fatalf("no key: got %d, want 401", status)
	}
}

func TestAPIKey_WrongKey_403(t *testing.T) {
	ta := newTestAppWithKey(t, testAPIKey)

	if status := doAuthRequest(t, ta, "wrong-key"); status != http.StatusForbidden {
		t.Fatalf("wrong key: got %d, want 403", status)
	}
}

func TestAPIKey_CorrectKey_PassesThrough(t *testing.T) {
	ta := newTestAppWithKey(t, testAPIKey)

	// Correct key → middleware passes the request through → the handler runs
	// and returns 200 with an empty list (valid email, no data).
	if status := doAuthRequest(t, ta, testAPIKey); status != http.StatusOK {
		t.Fatalf("correct key: got %d, want 200", status)
	}
}
