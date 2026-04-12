# GitHub Release Notification API

A monolith API service that allows users to subscribe to email notifications about new releases of GitHub repositories.

Built with **Go**, **Gin**, **PostgreSQL**, **Docker**.

## Design Decisions

1. **Go + Gin** — Thin framework with minimal abstraction. No framework lock-in. Aligned with the "lightweight frameworks only" requirement.

2. **sqlx (raw SQL) over ORM** — Full control over queries, easy to debug, no hidden N+1 problems. Struct tags (`db:"column"`) map SQL rows to Go structs automatically.

3. **Interface-based architecture** — Repository, GitHub client, and notifier are defined as interfaces. The service layer depends on interfaces, not implementations. This enables unit testing with mocks without needing a real database or network.

4. **Goroutine-based scanner** — Go's built-in concurrency replaces the need for external job schedulers (like Celery in my Python/Django projects). The scanner is a lightweight background thread within the same process, started with the `go` keyword.

5. **Multi-stage Docker build** — Build stage uses the full Go SDK image; runtime stage uses Alpine Linux with only the compiled binary (~15MB vs ~1GB). Fast startup, small attack surface.

6. **Token-based confirmation** — Each subscription gets a UUID token used for both confirmation and unsubscription. Stateless, no session management required.

7. **Error handling via custom types** — Business errors (`ErrRepoNotFound`, `ErrAlreadySubscribed`, etc.) are defined in the service layer. Handlers map them to HTTP status codes. This keeps the handler layer thin and the business logic testable.

8. **Graceful shutdown** — The server listens for OS signals (SIGINT/SIGTERM) and shuts down cleanly: cancels the scanner goroutine via `context.Context`, then gives in-flight HTTP requests 5 seconds to complete. This prevents data corruption and lost requests during Docker stop or deployment.

## Architecture

```
  User (curl / Postman / browser)
              │
              ▼
  ┌───────────────────────┐
  │   Gin HTTP Router     │  Receives HTTP requests, routes to handlers
  │   (port 8080)         │
  └───────────┬───────────┘
              ▼
  ┌───────────────────────┐
  │   Handlers            │  Parses request, calls service, returns JSON response
  └───────────┬───────────┘
              ▼
  ┌─────────────────────────────────┐
  │   Service                       │  Business logic: validates input, orchestrates
  │                                 │  calls to repository, GitHub API, and email notifier
  └──┬────────────┬──────────────┬──┘
     │            │              │
     ▼            ▼              ▼
  ┌───────────┐ ┌────────┐ ┌──────────┐
  │Repository │ │GitHub  │ │Notifier  │
  │   (SQL)   │ │Client  │ │(SMTP)    │
  │           │ │(API)   │ │          │
  └──┬────────┘ └────────┘ └──────────┘
     ▼
  ┌──────────┐
  │PostgreSQL│
  └──────────┘

  Background goroutine:
  ┌─────────────────────────────────────────────────────────┐
  │ Scanner: polling loop that runs every 5 minutes.        │
  │ Checks GitHub API for new releases of tracked repos.    │
  │ If new release detected → updates DB → sends emails     │
  │ to all confirmed subscribers.                           │
  └─────────────────────────────────────────────────────────┘
```

### How It Works

**1. Subscribe** — `POST /api/subscribe`
- Validates email format and repo format (`owner/repo`)
- Calls GitHub API to verify the repository exists (404 if not, 400 if bad format)
- Checks for duplicate subscription (409 if already subscribed)
- Creates subscription record with `confirmed=false` and a UUID token
- Sends confirmation email via SMTP with a clickable confirm link
- Returns 200

**2. Confirm** — `GET /api/confirm/{token}`
- Looks up subscription by token
- Sets `confirmed=true` and registers the repo for release tracking
- Returns 200 (idempotent — confirming twice is safe)

**3. Scanner detects new releases** (background goroutine)
- Runs every 5 minutes (configurable via `SCAN_INTERVAL_SECONDS`)
- Queries DB for all repos with at least one confirmed subscriber
- For each repo: calls GitHub API `/repos/{owner}/{repo}/releases/latest`
- Compares returned tag with `last_seen_tag` stored in DB
- If different → new release detected → sends email to all subscribers → updates `last_seen_tag`
- Handles GitHub API rate limits with exponential backoff retry on 429

**4. Unsubscribe** — `GET /api/unsubscribe/{token}`
- Deletes the subscription from the database
- Returns 200

**5. List subscriptions** — `GET /api/subscriptions?email={email}`
- Returns all confirmed subscriptions for the given email

## Prerequisites

- **Docker Desktop** (includes docker-compose)
- **Git**

You don't need Go installed locally — Docker handles the build.

## Quick Start

### 1. Clone and configure

```bash
git clone https://github.com/Yurii-Levchenko/github-release-notifier.git
cd github-release-notifier
cp .env.example .env
```

### 2. Fill in `.env`

```env
# Required — get from https://mailtrap.io -> Email Testing -> Inboxes -> SMTP Settings
SMTP_USER=your_mailtrap_username
SMTP_PASS=your_mailtrap_password

# Optional — increases GitHub API rate limit from 60 to 5000 req/hr
# Get from https://github.com/settings/tokens (no scopes needed)
GITHUB_TOKEN=your_github_token
```

### 3. Start everything

```bash
docker-compose up --build
```

This single command:
- Starts a PostgreSQL 16 container and creates the `notifier` database
- Builds the Go application in a multi-stage Docker build
- Runs database migrations automatically on startup
- Starts the HTTP server on port 8080
- Starts the background release scanner goroutine
- Serves the HTML subscription page at http://localhost:8080

### 4. Open the UI

Navigate to **http://localhost:8080** in your browser. You can subscribe, view your active subscriptions, and unsubscribe — all from the web page.

### 5. Or test via curl

```bash
# Health check
curl http://localhost:8080/health

# Subscribe
curl -X POST http://localhost:8080/api/subscribe \
  -H "Content-Type: application/json" \
  -d '{"email":"your@email.com","repo":"gin-gonic/gin"}'

# Check Mailtrap inbox → copy the token UUID from the confirmation link

# Confirm
curl http://localhost:8080/api/confirm/YOUR-TOKEN-HERE

# List active subscriptions
curl "http://localhost:8080/api/subscriptions?email=your@email.com"

# Unsubscribe
curl http://localhost:8080/api/unsubscribe/YOUR-TOKEN-HERE
```

## API Endpoints

| Method | Endpoint | Description | Success | Errors |
|--------|----------|-------------|---------|--------|
| GET | `/health` | Health check | 200 | — |
| POST | `/api/subscribe` | Subscribe to repo releases | 200 | 400 (bad input), 404 (repo not found), 409 (duplicate) |
| GET | `/api/confirm/{token}` | Confirm email subscription | 200 | 404 (bad token) |
| GET | `/api/unsubscribe/{token}` | Unsubscribe | 200 | 404 (bad token) |
| GET | `/api/subscriptions?email={email}` | List active subscriptions | 200 | 400 (bad email) |
| GET | `/` | HTML subscription page | 200 | — |

## Extras Implemented

- **HTML subscription page** — served at `/`, dark-themed UI for subscribing, viewing subscriptions, and unsubscribing from the browser
- **GitHub Actions CI** — runs `go build`, `go test` with coverage, and `golangci-lint` on every push to `main` and on pull requests
- **Graceful shutdown** — the server listens for SIGINT/SIGTERM signals, stops the scanner goroutine via `context.Context`, and gives in-flight HTTP requests 5 seconds to complete before exiting

## Project Structure

```
├── main.go                          # Entry point: wires dependencies, starts server + scanner
├── go.mod / go.sum                  # Dependencies
├── Dockerfile                       # Multi-stage build (golang → alpine, ~15MB final image)
├── docker-compose.yml               # Orchestrates app + PostgreSQL containers
├── .env.example                     # Template for environment variables
├── .github/workflows/ci.yml         # GitHub Actions CI pipeline (test + lint)
├── static/
│   └── index.html                   # HTML subscription page served at /
├── migrations/
│   ├── 000001_init.up.sql           # Creates subscriptions + repositories tables
│   └── 000001_init.down.sql         # Drops tables (rollback)
├── internal/
│   ├── config/config.go             # Loads environment variables into Config struct
│   ├── model/subscription.go        # Data structures with JSON and DB tags
│   ├── handler/handler.go           # HTTP handlers — parse requests, return responses
│   ├── service/service.go           # Business logic — validation, orchestration, error types
│   ├── service/service_test.go      # Unit tests (13 tests, 82% coverage)
│   ├── repository/repository.go     # Database layer — SQL queries with sqlx
│   ├── github/client.go             # GitHub API client with 429 retry
│   ├── scanner/scanner.go           # Background release checker goroutine
│   └── notifier/notifier.go         # SMTP email sender
└── postman_collection.json          # Importable Postman collection for all endpoints
```

## Running Tests

```bash
# Run all unit tests
go test ./... -v

# Run with coverage
go test ./internal/service/ -v -cover
# My output: 13 tests, 82.7% coverage
```

Tests use Go interfaces with mock implementations — no database or network required.

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | No | `postgres://postgres:postgres@db:5432/notifier?sslmode=disable` | PostgreSQL connection string |
| `APP_PORT` | No | `8080` | HTTP server port |
| `BASE_URL` | No | `http://localhost:8080` | Base URL for email links |
| `SMTP_HOST` | Yes | `sandbox.smtp.mailtrap.io` | SMTP server host |
| `SMTP_PORT` | No | `587` | SMTP server port |
| `SMTP_USER` | Yes | — | SMTP username (Mailtrap) |
| `SMTP_PASS` | Yes | — | SMTP password (Mailtrap) |
| `SMTP_FROM` | No | `noreply@github-notifier.local` | Sender email address |
| `GITHUB_TOKEN` | No | — | GitHub token (60 → 5000 req/hr) |
| `SCAN_INTERVAL_SECONDS` | No | `300` | Scanner polling interval in seconds |
