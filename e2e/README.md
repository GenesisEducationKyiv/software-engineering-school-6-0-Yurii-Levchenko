# E2E tests

End-to-end tests that drive a real Chromium browser via
[playwright-go](https://github.com/playwright-community/playwright-go)
against the full application running under docker-compose.

## One-time setup

```powershell
go install github.com/playwright-community/playwright-go/cmd/playwright@latest
playwright install --with-deps chromium
```

This installs the Playwright Go CLI and downloads the Chromium binary
that the tests drive. The Chromium binary lives in your user cache
(no system-wide install).

## Before every run

The app, Postgres and Redis must all be running:

```powershell
docker-compose up --build -d
docker ps   # all three containers healthy
```

## Run

```powershell
go test -tags=e2e -v ./e2e/...

# Just one scenario
go test -tags=e2e -v -run TestE2E_Subscribe_HappyPath ./e2e/...
```

## What each test does

| Test | Scenario |
|---|---|
| `TestE2E_HomePage_Renders` | smoke: page loads, form + lookup section visible |
| `TestE2E_Subscribe_HappyPath` | fill valid form → success banner; row persisted (unconfirmed) |
| `TestE2E_Subscribe_Duplicate_ShowsConflict` | second subscribe to same email+repo → red error |
| `TestE2E_LoadSubscriptions_EmptyForUnknownEmail` | lookup for unknown email → "No active subscriptions" |
| `TestE2E_LoadSubscriptions_AfterConfirmation_ShowsRepo` | subscribe → confirm (via DB-extracted token) → lookup → repo appears in list |

## Configuration

| Env | Default | Purpose |
|---|---|---|
| `E2E_APP_URL` | `http://localhost:8080` | base URL of the running app |
| `E2E_DATABASE_URL` | `postgres://postgres:postgres@localhost:5433/notifier?sslmode=disable` | direct DB access for token lookup in scenario 5 |

## Notes

- E2E tests do **not** truncate the DB. Each test uses a unique email
  (`<prefix>-e2e-<unixnano>@example.com`) for isolation.
- DB access is only used in scenario 5 to fetch the confirmation token —
  in production the user would click a link in their email; we can't
  reach a real mail UI from a test, so the DB is the cleanest substitute.
- Browser is headless by default. To debug visually, change
  `Headless: playwright.Bool(true)` to `false` in `TestMain`.
- SMTP is captured by the local **Mailpit** container (defined in
  `docker-compose.yml`). The actual emails are visible at
  <http://localhost:8025> if you want to inspect them — but tests don't
  read them; they only assert on what the UI shows after submit.
