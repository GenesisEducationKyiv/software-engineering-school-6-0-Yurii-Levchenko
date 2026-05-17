//go:build e2e

// Package e2e contains end-to-end tests for the HTML subscription page.
// They drive a real Chromium browser via playwright-go against the full
// application running under docker-compose.
//
// Prerequisites (one-time):
//
//	go install github.com/playwright-community/playwright-go/cmd/playwright@latest
//	playwright install --with-deps chromium
//
// Before every run:
//
//	docker-compose up --build -d
//
// Run with:
//
//	go test -tags=e2e -v ./e2e/...
//
// Configuration via env (defaults shown):
//
//	E2E_APP_URL=http://localhost:8080
//	E2E_DATABASE_URL=postgres://postgres:postgres@localhost:5433/notifier?sslmode=disable
package e2e

import (
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/playwright-community/playwright-go"
)

var (
	pw         *playwright.Playwright
	browser    playwright.Browser
	db         *sqlx.DB
	appBaseURL string
)

// TestMain initialises Playwright + a Chromium browser + a DB connection
// once for the entire e2e test run, then tears them down on exit.
func TestMain(m *testing.M) {
	appBaseURL = envOr("E2E_APP_URL", "http://localhost:8080")
	dbURL := envOr("E2E_DATABASE_URL",
		"postgres://postgres:postgres@localhost:5433/notifier?sslmode=disable")

	var err error
	pw, err = playwright.Run()
	if err != nil {
		log.Fatalf("playwright run: %v", err)
	}

	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		log.Fatalf("chromium launch: %v", err)
	}

	db, err = sqlx.Connect("postgres", dbURL)
	if err != nil {
		log.Fatalf("db connect (%s): %v", dbURL, err)
	}

	code := m.Run()

	_ = browser.Close()
	_ = pw.Stop()
	_ = db.Close()
	os.Exit(code)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newPage opens a fresh browser context (cookie/storage-isolated from
// other tests) and returns a new Page. The context is closed via
// t.Cleanup so leftover state doesn't bleed between tests.
func newPage(t *testing.T) playwright.Page {
	t.Helper()
	ctx, err := browser.NewContext()
	if err != nil {
		t.Fatalf("new context: %v", err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	return page
}

// uniqueEmail returns an email unlikely to collide with other tests.
// E2E doesn't TRUNCATE between tests (we run against a real app), so
// isolation is by unique email per test.
func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-e2e-%d@example.com", prefix, time.Now().UnixNano())
}

// step wraps a Playwright operation and aborts the test on error. It
// keeps test bodies focused on the user flow instead of plumbing.
func step(t *testing.T, name string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// navigate goes to a URL and fails the test on error.
func navigate(t *testing.T, page playwright.Page, url string) {
	t.Helper()
	if _, err := page.Goto(url); err != nil {
		t.Fatalf("goto %s: %v", url, err)
	}
}

// waitForSubscribeBanner waits for any post-submit banner (success or
// error) and fails the test with the error text if it's the error one.
// This makes server failures (SMTP misconfig, GitHub error, etc.) loud
// instead of a generic 30s timeout.
func waitForSubscribeBanner(t *testing.T, page playwright.Page) {
	t.Helper()
	step(t, "wait for any banner",
		page.Locator(".message.success, .message.error").WaitFor())

	errCount, err := page.Locator(".message.error").Count()
	if err != nil {
		t.Fatalf("count error banners: %v", err)
	}
	if errCount > 0 {
		text, _ := page.Locator(".message.error").TextContent()
		t.Fatalf("expected success banner, got error banner: %q", text)
	}
}

// --- 1. Smoke: page renders ---

func TestE2E_HomePage_Renders(t *testing.T) {
	page := newPage(t)
	navigate(t, page, appBaseURL+"/")

	// Setup phase asserts: every expected component is visible.
	step(t, "h1 visible", page.Locator("h1").WaitFor())
	step(t, "subscribe form visible", page.Locator("#subscribeForm").WaitFor())
	step(t, "email input visible", page.Locator("#email").WaitFor())
	step(t, "repo input visible", page.Locator("#repo").WaitFor())
	step(t, "submit button visible", page.Locator("#submitBtn").WaitFor())
	step(t, "lookup input visible", page.Locator("#lookupEmail").WaitFor())

	h2Text, err := page.Locator("h2").TextContent()
	if err != nil {
		t.Fatalf("read h2 text: %v", err)
	}
	if !strings.Contains(h2Text, "My Subscriptions") {
		t.Errorf("h2 text = %q, want to contain 'My Subscriptions'", h2Text)
	}
}

// --- 2. Subscribe happy path ---

func TestE2E_Subscribe_HappyPath(t *testing.T) {
	page := newPage(t)
	email := uniqueEmail("alice-happy")

	// Setup
	navigate(t, page, appBaseURL+"/")

	// Action
	step(t, "fill email", page.Locator("#email").Fill(email))
	step(t, "fill repo", page.Locator("#repo").Fill("golang/go"))
	step(t, "click submit", page.Locator("#submitBtn").Click())

	// Assertion — green success banner appears with the expected message
	waitForSubscribeBanner(t, page)

	text, err := page.Locator(".message.success").TextContent()
	if err != nil {
		t.Fatalf("read success text: %v", err)
	}
	if !strings.Contains(strings.ToLower(text), "check your email") {
		t.Errorf("success text = %q, want to contain 'check your email'", text)
	}

	// Verify the row was persisted as unconfirmed.
	var confirmed bool
	if err := db.Get(&confirmed,
		`SELECT confirmed FROM subscriptions WHERE email=$1 AND repo=$2`,
		email, "golang/go"); err != nil {
		t.Fatalf("db query: %v", err)
	}
	if confirmed {
		t.Errorf("new sub should be unconfirmed, got confirmed=true")
	}
}

// --- 3. Subscribe duplicate ---

func TestE2E_Subscribe_Duplicate_ShowsConflict(t *testing.T) {
	page := newPage(t)
	email := uniqueEmail("alice-dup")

	navigate(t, page, appBaseURL+"/")

	// Setup: a successful first subscription.
	step(t, "fill email (1st)", page.Locator("#email").Fill(email))
	step(t, "fill repo (1st)", page.Locator("#repo").Fill("golang/go"))
	step(t, "click submit (1st)", page.Locator("#submitBtn").Click())
	waitForSubscribeBanner(t, page)

	// Action: click Subscribe again with the same data. The form fields
	// stay filled, the JS clears the message before issuing the new fetch.
	step(t, "click submit (2nd)", page.Locator("#submitBtn").Click())

	// Assertion: red error banner with the conflict text.
	step(t, "wait for error banner", page.Locator(".message.error").WaitFor())

	text, err := page.Locator(".message.error").TextContent()
	if err != nil {
		t.Fatalf("read error text: %v", err)
	}
	if !strings.Contains(strings.ToLower(text), "already subscribed") {
		t.Errorf("error text = %q, want to contain 'already subscribed'", text)
	}
}

// --- 4. Load subscriptions: empty result for unknown email ---

func TestE2E_LoadSubscriptions_EmptyForUnknownEmail(t *testing.T) {
	page := newPage(t)
	email := uniqueEmail("nobody")

	navigate(t, page, appBaseURL+"/")

	step(t, "fill lookup email", page.Locator("#lookupEmail").Fill(email))
	step(t, "click load", page.Locator(".lookup button").Click())

	// IMPORTANT: #subList ALREADY contains <p class="empty"> with the
	// placeholder "Enter your email and click Load." text before Load is
	// clicked. A selector-only WaitFor would match that immediately and
	// race with the AJAX replacement. We instead wait for the specific
	// new text, which only appears AFTER the fetch completes.
	step(t, "wait for empty-state message",
		page.GetByText("No active subscriptions found.").WaitFor())
}

// --- 5. Load subscriptions: after confirmation, the repo appears ---

func TestE2E_LoadSubscriptions_AfterConfirmation_ShowsRepo(t *testing.T) {
	page := newPage(t)
	email := uniqueEmail("alice-confirmed")

	// Setup step 1: subscribe via the form.
	navigate(t, page, appBaseURL+"/")
	step(t, "fill email", page.Locator("#email").Fill(email))
	step(t, "fill repo", page.Locator("#repo").Fill("golang/go"))
	step(t, "click submit", page.Locator("#submitBtn").Click())
	waitForSubscribeBanner(t, page)

	// Setup step 2: confirm the subscription. In production the user
	// would click a link from email; we don't have access to Mailtrap,
	// so we read the token from the DB and navigate the browser to the
	// confirm URL directly. This is the only place E2E touches the DB,
	// strictly for setting up state the UI alone cannot reach.
	var token string
	if err := db.Get(&token,
		`SELECT token FROM subscriptions WHERE email=$1 AND repo=$2`,
		email, "golang/go"); err != nil {
		t.Fatalf("fetch token: %v", err)
	}
	navigate(t, page, appBaseURL+"/api/confirm/"+token)

	// Action: go back to home, look up the email.
	navigate(t, page, appBaseURL+"/")
	step(t, "fill lookup email", page.Locator("#lookupEmail").Fill(email))
	step(t, "click load", page.Locator(".lookup button").Click())

	// Assertion: a single .sub-item appears showing the confirmed repo.
	step(t, "wait for sub-item", page.Locator(".sub-item").WaitFor())

	text, err := page.Locator(".sub-item").TextContent()
	if err != nil {
		t.Fatalf("read sub-item text: %v", err)
	}
	if !strings.Contains(text, "golang/go") {
		t.Errorf("sub-item text = %q, want to contain 'golang/go'", text)
	}
}
