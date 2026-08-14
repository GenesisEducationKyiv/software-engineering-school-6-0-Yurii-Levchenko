//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHealth_200(t *testing.T) {
	ta := newTestApp(t)

	status, body := get(t, ta.server.URL+"/health")
	if status != http.StatusOK {
		t.Fatalf("health: got %d, want 200", status)
	}
	if body["status"] != "ok" {
		t.Errorf("health body: got %v, want status=ok", body)
	}
}

func TestMetrics_ExposesPrometheusCounters(t *testing.T) {
	ta := newTestApp(t)

	resp, err := http.Get(ta.server.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status: got %d, want 200", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Unlabeled counters are exposed at value 0 even before any traffic.
	// We pick a few that prove the registry is wired up.
	wantSubstrings := []string{
		"subscriptions_created_total",
		"subscriptions_confirmed_total",
		"unsubscribes_total",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q", want)
		}
	}
}
