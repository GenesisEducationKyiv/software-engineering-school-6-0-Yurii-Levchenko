# GitHub Release Notification API

A service that lets users subscribe to email notifications about new releases of GitHub repositories. **Two processes** — a modular Go monolith (HTTP API + release scanner + saga orchestrator) and a separate **notifier** microservice — communicating asynchronously over **RabbitMQ**.

Built with **Go**, **Gin**, **PostgreSQL**, **Redis**, **RabbitMQ**, **Docker**.

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
                    ┌──────────────────────── Docker Compose ─────────────────────────────┐
 User ─HTTP─▶:8080  │  ┌─ Monolith ("server") ───────────────────────────────────────────┐ │
 (browser/curl)     │  │  Gin router → handlers → subscription.Service                     │ │
                    │  │  Scanner goroutine (polls GitHub via Redis-cached client)         │ │
                    │  │  Orchestrator: saga T1 · outbox relay · reply-consumer · sweeper  │ │
                    │  └───────────────┬─────────────────────────────────┬──────────────────┘ │
                    │  Postgres ───────┘  (subscriptions·repositories·    │ publish / consume   │
                    │  Redis (cache + dedup)   saga·outbox)               ▼                      │
                    │                                          [ RabbitMQ ] exchange            │
                    │                                          cmd: confirmation/release        │
                    │                                          reply: saga.reply                │
                    │  ┌─ Notifier microservice ("notifier") ───────────┴─────────────────────┐ │
                    │  │  consume → dedup (Redis) → SMTP → Mailpit → reply sent/failed         │ │
                    │  └────────────────────────────────────────────────────────────────────────┘ │
                    │  GitHub API (HTTPS, from monolith)                                          │
                    └──────────────────────────────────────────────────────────────────────────────┘
```
> Detailed component design, diagrams, and trade-offs: [`system-design/README.md`](./system-design/README.md) and [ADRs](./system-design/ADR).

### Data Flow: Subscribe Request
```
User → POST /api/subscribe {"email":"...", "repo":"owner/repo"}
         │
         ▼
      Handler: parse JSON body
         │
         ▼
      Service: validate email → validate repo format
         │
         ▼
      CachedGitHub: Redis has "repo_exists:owner/repo"?
         ├── Cache HIT → return cached result (skip GitHub API)
         └── Cache MISS → call GitHub API → store in Redis (TTL 10 min)
         │
         ▼
      Store: check DB for duplicate (email + repo) — reactivate if previously failed
         │
         ▼
      Orchestrator (saga T1, one tx): INSERT subscription(pending) + saga + outbox command
         │
         ▼
      Return 200 {"message": "subscription created"}
         ┊
         ┄┄ async ┄┄▶ relay → RabbitMQ → notifier → SMTP (confirmation email)
                       notifier reply → orchestrator → saga = completed
```

### Data Flow: Scanner Cycle (every 5 minutes)
```
Scanner goroutine wakes up
         │
         ▼
      Repository: SELECT DISTINCT repo FROM subscriptions WHERE confirmed=true
         │
         ▼
      For each repo:
         │
         ▼
      CachedGitHub: Redis has "latest_release:owner/repo"?
         ├── Cache HIT → use cached tag
         └── Cache MISS → call GitHub API → store in Redis (TTL 10 min)
         │
         ▼
      Repository: compare tag with last_seen_tag
         ├── Same tag → skip
         └── New tag → UPDATE last_seen_tag
                          │
                          ▼
                       Store: get all confirmed subscribers for this repo
                          │
                          ▼
                       Publish a release command per subscriber → RabbitMQ → notifier sends the email
```

### How It Works

**1. Subscribe** — `POST /api/subscribe`
- Validates email format and repo format (`owner/repo`)
- Calls GitHub API to verify the repository exists (404 if not, 400 if bad format)
- Checks for duplicate subscription (409 if already subscribed)
- Starts the subscribe **saga** (step T1, one transaction): creates the subscription (`pending`, UUID token) + the saga record + an outbox command — atomically
- The confirmation email is sent **asynchronously** by the notifier (relay → RabbitMQ → notifier → SMTP); a permanent send failure compensates the saga and marks the subscription `failed`
- Returns 200 ("check your email")

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
- Returns all subscriptions for the given email, each with its `status` (`pending` / `confirmed` / `failed`)

## gRPC vs REST — transport benchmark (HW10 ⭐)

HW10 adds a synchronous **gRPC** transport for the confirmation step alongside the
default async broker path (see [ADR-011](system-design/ADR/011-grpc-for-confirmation-transport.md)).
To compare it fairly against **REST** (HTTP/1.1 + JSON), `cmd/confirmbench` drives
both transports for the *same* `SendConfirmation` operation from **one** Go harness:
same machine, connection reuse, and a **no-op backend** (no SMTP) so only the
transport differs. It measures throughput, latency, and **bytes on the wire**.

```bash
go run ./cmd/confirmbench
```

Indicative local run (30k requests per cell; numbers are noisy, especially the
low-end latency percentiles — the Windows timer rounds sub-millisecond values):

| Transport | Conc | req/s | p95 | p99 | req bytes/wire | resp bytes/wire |
|---|---|---|---|---|---|---|
| gRPC | 1 | ~4.7k | 0.55 ms | 1.5 ms | **196 B** | **91 B** |
| REST | 1 | ~7.4k | 1.0 ms | 1.5 ms | 320 B | 120 B |
| gRPC | 8 | ~13k | 0.74 ms | — | **151 B** | **47 B** |
| REST | 8 | ~15k | 1.2 ms | 1.7 ms | 320 B | 120 B |
| gRPC | 64 | ~48k | 2.3 ms | 2.8 ms | **145 B** | **41 B** |
| REST | 64 | ~51k | 4.0 ms | 5.3 ms | 320 B | 120 B |

Payload size (serialization only): Protobuf **101 B** vs JSON **134 B** (small, 1.33×);
**195 B** vs **233 B** (large, 1.19×).

**What we got — and why.** On localhost with a tiny message, raw **throughput is
roughly a wash** (REST even edges ahead: Go's `net/http` with keep-alive is very
fast, and HTTP/2 framing overhead offsets multiplexing for such small payloads).
gRPC's real, repeatable wins are:

- **~2× fewer bytes on the wire** (145 B vs 320 B per request), and the gap **grows
  with concurrency** — HTTP/2 **HPACK** compresses/indexes headers across the one
  multiplexed connection, while HTTP/1.1 re-sends full plain-text headers every
  request. Protobuf also drops JSON's repeated key names.
- **Tighter latency tail** at high concurrency (one multiplexed connection vs an
  HTTP/1.1 connection pool).
- A **typed, schema-first contract** (`.proto`) instead of an implicit JSON shape.

So we default to the broker for the production email path (async decoupling from
HW8/HW9) and keep gRPC as the leaner synchronous option — chosen for its contract,
wire efficiency, and tail latency, not for peak localhost throughput.

## Tested and Verified

The full end-to-end flow has been tested with real GitHub repos and Mailtrap:

- Subscribed to `gin-gonic/gin`, `docker/compose`, `NousResearch/hermes-agent`, and others
- Scanner detected real releases: `gin-gonic/gin v1.12.0`, `docker/compose v5.1.2`, `NousResearch/hermes-agent v2026.4.8`
- Confirmation emails and release notification emails delivered successfully via Mailtrap
- Unsubscribe links in release emails work correctly
- Redis cache verified: `Cache HIT` on repeated GitHub API lookups, `Cache MISS` on first call
- All error cases tested: 400 (bad input), 404 (repo not found / bad token), 409 (duplicate subscription)
- 13 unit tests passing with 82.7% coverage on business logic

## API Documentation (Swagger)

View the API spec in Swagger Editor: [Open in Swagger Editor](https://editor.swagger.io/?url=https://raw.githubusercontent.com/Yurii-Levchenko/github-release-notifier/master/swagger.yaml)

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
# Optional — increases GitHub API rate limit from 60 to 5000 req/hr
# Get from https://github.com/settings/tokens (no scopes needed)
GITHUB_TOKEN=your_github_token

# Email in dev goes to Mailpit (no credentials needed) — UI at http://localhost:8025.
# For a real provider (SES/SendGrid/Mailgun) set SMTP_HOST/PORT/USER/PASS for the notifier.
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
| GET | `/metrics` | Prometheus metrics | 200 | — |

## Extras Implemented

- **HTML subscription page** — served at `/`, dark-themed UI for subscribing, viewing subscriptions, and unsubscribing from the browser
- **GitHub Actions CI** — runs `go build`, `go test`, and `go vet` on every push to `main`/`master` and on pull requests
- **Graceful shutdown** — the server listens for SIGINT/SIGTERM signals, stops the scanner goroutine via `context.Context`, and gives in-flight HTTP requests 5 seconds to complete before exiting
- **Redis caching** — GitHub API responses are cached with a configurable TTL (default 10 minutes). The `CachedClient` wrapper checks Redis before making API calls, reducing rate limit usage. Logs `Cache HIT` / `Cache MISS` for observability. App works without Redis (graceful fallback with a warning log)
- **Prometheus metrics** — `/metrics` endpoint exposes: HTTP request counts and duration by method/path/status, subscriptions created/confirmed/unsubscribed, scanner run cycles, releases detected, notifications sent, GitHub API cache hit/miss rates
- **API key authentication** — set `API_KEY` env var to protect all `/api/*` endpoints with `X-API-Key` header. Returns 401 (missing) or 403 (invalid). Disabled by default (empty `API_KEY`) for easy development. Public endpoints (`/`, `/health`, `/metrics`) are never protected

## Project Structure

```
├── main.go                          # Monolith entry: wiring; server + scanner + outbox relay + saga reply-consumer + sweeper
├── cmd/notifier/                    # Notifier microservice (separate binary): consumer, dedup, SMTP
├── go.mod / go.sum                  # Dependencies
├── Dockerfile / Dockerfile.notifier # Multi-stage builds for the two binaries
├── docker-compose.yml               # app + notifier + postgres + redis + rabbitmq + mailpit
├── swagger.yaml                     # OpenAPI 2.0 spec for the 4 endpoints
├── static/index.html                # HTML subscription page served at /
├── migrations/                      # 000001 init … 000005 subscription status (golang-migrate, runs on startup)
├── internal/
│   ├── app/                         # BuildRouter — wires routes (shared by main + integration tests)
│   ├── config/                      # env → Config
│   ├── subscription/                # domain: Service + Store (subscriptions) + entity + errors + Subscriber DTO
│   ├── releasetracking/             # domain: Scanner + Store (repositories) + Repository entity
│   ├── orchestrator/                # Saga: orchestrator (T1) + saga Store + reply-consumer + sweeper
│   ├── outbox/                      # transactional outbox: Store + relay
│   ├── notification/                # BrokerPublisher + command/reply DTOs (wire contract)
│   ├── githubgateway/               # GitHub client + Redis-cached wrapper
│   ├── repospec/                    # shared kernel: RepoSpec value object
│   ├── handler/  middleware/        # Gin handlers; API-key auth + request-logger
│   ├── cache/  metrics/  logging/   # Redis cache; Prometheus; slog setup
│   └── integration/                 # integration tests (build tag `integration`, testcontainers)
├── e2e/                             # Playwright-go e2e tests (build tag `e2e`)
└── system-design/                   # system-design doc + ADRs (001–010)
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
| `SMTP_HOST` | No | `mailpit` | SMTP host (**notifier**) — Mailpit in dev |
| `SMTP_PORT` | No | `1025` | SMTP port (notifier) |
| `SMTP_USER` | No | — | SMTP username (empty for Mailpit; set for a real provider) |
| `SMTP_PASS` | No | — | SMTP password (empty for Mailpit) |
| `SMTP_FROM` | No | `noreply@github-notifier.local` | Sender email address (notifier) |
| `GITHUB_TOKEN` | No | — | GitHub token (60 → 5000 req/hr) |
| `SCAN_INTERVAL_SECONDS` | No | `600` | Scanner polling interval in seconds |
| `REDIS_URL` | No | `redis://redis:6379/0` | Redis URL (GitHub cache + notifier dedup) |
| `CACHE_TTL_SECONDS` | No | `600` | Cache TTL for GitHub API responses (10 min) |
| `RABBITMQ_URL` | No | `amqp://guest:guest@rabbitmq:5672/` | RabbitMQ connection URL |
| `OUTBOX_POLL_INTERVAL_MS` | No | `1000` | How often the outbox relay polls for unpublished commands |
| `SAGA_SWEEP_INTERVAL_SECONDS` | No | `60` | How often the resume-sweeper scans for stuck sagas |
| `SAGA_STALE_AFTER_SECONDS` | No | `120` | How long a saga may sit non-terminal before it's re-driven |
| `API_KEY` | No | — | API key for endpoint protection (empty = auth disabled) |
