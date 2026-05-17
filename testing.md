# Testing

Three test layers, each runnable with one command. Each layer has its own
GitHub Actions workflow in `.github/workflows/` so they can fail and be
re-run independently.

| Layer | Where | Build tag | Needs Docker | Typical time |
|---|---|---|---|---|
| **Unit** | `internal/*/  *_test.go` | — | ❌ | < 1s |
| **Integration** | `internal/integration/` | `integration` | ✅ Postgres via testcontainers | ~6s |
| **E2E** | `e2e/` | `e2e` | ✅ full app via docker-compose | ~3s after warm-up |

## Prerequisites

| | Required for |
|---|---|
| Go (`go.mod` version) | all |
| Docker Desktop running | integration, e2e |
| `playwright` CLI + chromium | e2e (one-time setup, see below) |

## Quick start

### Unit tests — fastest, no Docker

```powershell
go test ./...
```

`go test ./...` skips packages with build tags (`integration`, `e2e`), so
it only runs the plain unit tests. Used for fast PR feedback.

### Integration tests — real Postgres in Docker

```powershell
docker info                                            # ensure Docker is running
go test -tags=integration -v ./internal/integration/...
```

`testcontainers-go` spins up a fresh `postgres:16-alpine` container per
test run, applies migrations, then tears it down at the end. No
`docker-compose up` needed.

### E2E tests — real Chromium against the full app

One-time setup:

```powershell
go install github.com/playwright-community/playwright-go/cmd/playwright@latest
playwright install --with-deps chromium
```

Before every run:

```powershell
docker-compose up --build -d   # app + db + redis + mailpit
```

Then:

```powershell
go test -tags=e2e -v ./e2e/...
```

Captured emails are visible at <http://localhost:8025> (Mailpit web UI).

### Everything in one go

```powershell
docker-compose up --build -d
go test -tags=integration -v ./...   # `-tags=integration` enables those; e2e stays excluded because it has its own tag
go test -tags=e2e -v ./e2e/...
```

Or one line per layer if you want clean separation in the output.

## CI

Each test layer has its own workflow:

| Workflow file | What runs |
|---|---|
| `.github/workflows/lint.yml` | `golangci-lint` |
| `.github/workflows/unit.yml` | `go test ./...` |
| `.github/workflows/integration.yml` | `go test -tags=integration ./internal/integration/...` |
| `.github/workflows/e2e.yml` | docker-compose + playwright + `go test -tags=e2e ./e2e/...` |

Each one triggers on every `push` to `main` and every `pull_request`,
runs independently, and reports separately on the PR. If unit tests
fail you immediately know it isn't an infrastructure issue.

## Notes on isolation

- **Unit tests** use in-memory mocks for all dependencies — no shared state.
- **Integration tests** truncate Postgres tables between tests
  (`TRUNCATE ... RESTART IDENTITY`), so each test starts from a clean schema.
- **E2E tests** do NOT truncate, because they run against the real
  long-running app. Each test uses a unique email
  (`<prefix>-e2e-<unix-nano>@example.com`) so test data never collides.
