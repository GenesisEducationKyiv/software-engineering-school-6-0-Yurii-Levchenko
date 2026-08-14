package notification

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// HTTPSender is exercised against an httptest stub standing in for the notifier
// service — no real network, no SMTP. We assert it (a) hits the right path,
// (b) sends the right JSON, and (c) maps a non-2xx response to an error.

func TestHTTPSender_SendConfirmationEmail_PostsExpectedJSON(t *testing.T) {
	var gotPath string
	var req ConfirmationRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := NewHTTPSender(srv.URL)
	if err := s.SendConfirmationEmail("a@b.com", "http://x/confirm/tok"); err != nil {
		t.Fatalf("SendConfirmationEmail returned error: %v", err)
	}

	if gotPath != "/send/confirmation" {
		t.Errorf("path = %q, want /send/confirmation", gotPath)
	}
	if req.To != "a@b.com" || req.ConfirmURL != "http://x/confirm/tok" {
		t.Errorf("decoded body = %+v, want To=a@b.com ConfirmURL=http://x/confirm/tok", req)
	}
}

func TestHTTPSender_SendReleaseNotification_PostsExpectedJSON(t *testing.T) {
	var gotPath string
	var req ReleaseRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := NewHTTPSender(srv.URL)
	if err := s.SendReleaseNotification("a@b.com", "golang/go", "v1.22.0", "http://x/unsub/tok"); err != nil {
		t.Fatalf("SendReleaseNotification returned error: %v", err)
	}

	if gotPath != "/send/release" {
		t.Errorf("path = %q, want /send/release", gotPath)
	}
	if req.Repo != "golang/go" || req.Tag != "v1.22.0" || req.UnsubscribeURL != "http://x/unsub/tok" {
		t.Errorf("decoded body = %+v", req)
	}
}

func TestHTTPSender_Non2xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	s := NewHTTPSender(srv.URL)
	if err := s.SendConfirmationEmail("a@b.com", "url"); err == nil {
		t.Fatal("expected error on 502, got nil")
	}
}
