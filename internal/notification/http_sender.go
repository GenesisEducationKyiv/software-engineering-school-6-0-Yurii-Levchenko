package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ConfirmationRequest and ReleaseRequest are the JSON bodies the monolith sends
// to the notifier service. Shared by both sides so the wire shape stays in sync.
type ConfirmationRequest struct {
	To         string `json:"to"`
	ConfirmURL string `json:"confirm_url"`
}

type ReleaseRequest struct {
	To             string `json:"to"`
	Repo           string `json:"repo"`
	Tag            string `json:"tag"`
	UnsubscribeURL string `json:"unsubscribe_url"`
}

// HTTPSender delivers notifications by calling the notifier service over
// HTTP/JSON. It satisfies the same EmailNotifier/ReleaseNotifier interfaces as
// SMTPSender, so wiring it in is a one-line swap; it is the ACL that translates
// a domain call into the service's wire format.
type HTTPSender struct {
	baseURL string
	client  *http.Client
}

func NewHTTPSender(baseURL string) *HTTPSender {
	// Timeout bounds a hung service so one send can't block a scanner cycle.
	return &HTTPSender{baseURL: baseURL, client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *HTTPSender) SendConfirmationEmail(to, confirmURL string) error {
	return s.post("/send/confirmation", ConfirmationRequest{To: to, ConfirmURL: confirmURL})
}

func (s *HTTPSender) SendReleaseNotification(to, repo, tag, unsubscribeURL string) error {
	return s.post("/send/release", ReleaseRequest{To: to, Repo: repo, Tag: tag, UnsubscribeURL: unsubscribeURL})
}

func (s *HTTPSender) post(path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notification request: %w", err)
	}

	// Interfaces carry no ctx yet; Background is bounded by the client Timeout.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build notifier request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("call notifier service: %w", err)
	}
	defer resp.Body.Close()

	// Any non-2xx is a failed send — surface it just like an SMTP error was.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("notifier service returned %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
