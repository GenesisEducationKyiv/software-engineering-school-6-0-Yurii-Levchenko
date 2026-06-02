# Observability

How this service is observed: two independent pipelines — **logs** and **metrics**.
Architecture rationale lives in [ADR-007](system-design/ADR/007-structured-logging-and-log-pipeline.md)
(logging) and [ADR-008](system-design/ADR/008-red-metrics-prometheus-grafana.md) (metrics).

| | Logs | Metrics |
|---|---|---|
| Question | "what exactly happened to request X?" | "how many / how fast / error trend?" |
| Store | Elasticsearch | Prometheus |
| View | Kibana (`:5601`) | Grafana (`:3000`) |
| Pipeline | app → Filebeat → Elasticsearch → Kibana | app `/metrics` ← Prometheus (pull) ← Grafana |

Both run in a **separate** compose file so normal dev/test stays light:

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml up --build -d
```

| Service | URL | Notes |
|---|---|---|
| App | http://localhost:8080 | `/metrics` exposes Prometheus metrics |
| Kibana | http://localhost:5601 | data view `notifier-logs-*` |
| Elasticsearch | http://localhost:9200 | `notifier-logs-*` index |
| Prometheus | http://localhost:9090 | `/targets` shows scrape health |
| Grafana | http://localhost:3000 | dashboard "Notifier — Overview" (anonymous, dev only) |

## Logs

- App logs JSON via stdlib `log/slog` to **stdout**. Level from `LOG_LEVEL` env (debug/info/warn/error).
- HTTP requests are logged by `RequestLogger` middleware: `method, path, status, duration_ms, client_ip, request_id`.
- **Filebeat** (separate container) tails container stdout, parses the JSON, and ships only the app's logs (filtered by the `logging: notifier` label) to Elasticsearch.
- Search/aggregate in **Kibana → Discover** (data view `notifier-logs-*`). Examples:
  - `level : "ERROR"` — all errors
  - `status >= 400` — failed HTTP requests
  - `msg : "New release detected" and repo : "golang/go"`
  - `subscription_id : 42` — everything about one subscription

> PII: we log `subscription_id`, **not** email (email is personal data). Map it back via the DB: `SELECT email FROM subscriptions WHERE id = ?`.

## Metrics (RED)

The app is instrumented per the **RED** method (Rate / Errors / Duration):

| Signal | HTTP | Scanner |
|---|---|---|
| Rate | `http_requests_total` | `scanner_runs_total` |
| Errors | `http_requests_total{status=~"5.."}` | `scanner_errors_total{stage}` |
| Duration | `http_request_duration_seconds` | `scanner_cycle_duration_seconds` |

Plus `github_api_calls_total{cache="hit|miss"}` (cache efficiency) and business counters.

Prometheus scrapes `app:8080/metrics` every 15s. Grafana visualises it — open the
**Notifier — Overview** dashboard (RED + cache hit ratio + scanner health). Generate
traffic to populate the graphs:

```powershell
1..50 | ForEach-Object { curl.exe -s http://localhost:8080/health | Out-Null }
```

> "No data" / 0 on a panel usually means that event simply hasn't happened in the
> window (e.g. no 5xx, no scanner errors), not that something is broken.

## Investigation runbook — "user doesn't receive release emails"

1. **DB first** (logs hold `subscription_id`, you start from an email):
   ```sql
   SELECT id, repo, confirmed FROM subscriptions WHERE email = 'user@example.com';
   ```
   - no row → never subscribed; `confirmed = false` → **most common cause** (never confirmed); else note the `id`.
2. **Did the scanner see a release?** Kibana: `msg : "New release detected" and repo : "<repo>"`.
   If absent, check the scanner is alive (`msg : "Scanner cycle started"`) and not erroring (`msg : "Scanner failed to get latest release"`).
3. **Did it try to notify this subscription?** Kibana: `subscription_id : <id>`:
   - `Scanner notified subscriber` → we handed the mail to SMTP OK; problem is downstream (delivery/spam).
   - `Scanner failed to notify subscriber` + `err` → SMTP failure, read `err`.
   - nothing → release wasn't detected or scanner failed earlier.

> Logs say "we sent it to SMTP", not "it was delivered" — inbox/spam delivery is the mail provider's domain, not ours.

## Notes / limitations
- Security disabled on ES/Kibana/Grafana — **dev only**, not production.
- No ILM on Elasticsearch yet → log indices grow unbounded (see `OBSERVABILITY_TODO`).
- No alerting yet — metrics are visualised, not alerted on.
