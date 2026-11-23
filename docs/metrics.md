# Metrics

taiga serves Prometheus metrics at `GET /metrics` on a port of its own, `app.metrics_port` (env `TAIGA_APP_METRICS_PORT`), and not at all unless it's set.
It's apart from the API port so the frontend's `/api` proxy never reaches it, and it needs no sign-in: keep the port inside the cluster.

```yaml
app:
  port: "8080"
  metrics_port: "9464"
```

No metric has a per-user label. Label values are kinds, routes, states and outcomes, a fixed handful each; who did what stays in the log.

## What's there

Go's runtime (`go_*`) and the process (`process_*`) come with the client library. The rest:

| Metric | Type | Labels | What |
| --- | --- | --- | --- |
| `taiga_http_request_duration_seconds` | histogram | `method`, `route`, `status` | Every API request. `route` is chi's pattern (`/workouts/{id}`), never the path. A request turned away before its route is matched (a 401 from the auth middleware, a 404) is under its mount's wildcard (`/workouts/*`, `/*`) |
| `taiga_job_duration_seconds` | histogram | `kind`, `outcome` | Each job run; `outcome` is `done`, `retried` or `dead` |
| `taiga_jobs` | gauge | `kind`, `state` | The `jobs` table at the scrape, every kind and state given even at 0 |
| `taiga_jobs_oldest_due_seconds` | gauge | `kind` | How long the longest-waiting job of that kind that could run now has been due; 0 when none is. One waiting out a retry's backoff isn't counted |
| `taiga_source_sync_duration_seconds` | histogram | `kind`, `outcome` | Each sync of a connection; `outcome` is `done`, `failed` or `needs_reconnect` (COROS ended the grant) |
| `taiga_source_activities_total` | counter | `kind`, `result` | What syncs did with the activities: `listed`, `imported`, `already_had`, `unreadable`, `waiting`, `detailed`, `matched` (see `models.SyncResult`) |
| `taiga_source_files_requested_total` | counter | `kind` | Files COROS issued links for, each counting against the day's limit |
| `taiga_source_daily_files_used` | gauge | `kind` | The busiest user's files in the last 24 hours, read at the scrape |
| `taiga_source_daily_files_limit` | gauge | `kind` | `models.SourceDailyFiles` (45) |
| `taiga_source_connections` | gauge | `kind`, `state` | Connections that are `ok`, `failing` (the last sync left an error) or `needs_reconnect` |
| `taiga_source_token_refreshes_total` | counter | `kind`, `outcome` | Token refreshes: `refreshed` (COROS issued new tokens) or `failed` |
| `taiga_outbound_request_duration_seconds` | histogram | `destination`, `outcome` | Calls taiga makes elsewhere, timed to the response headers: `destination` is `coros`, `open-meteo` or `oidc:<provider name>`; `outcome` is `2xx`…`5xx`, or `error` when there was no response |
| `taiga_filerepo_operation_duration_seconds` | histogram | `backend`, `op`, `outcome` | File repo `store`, `exists` and `fetch` (timed to opening the file), `ok` or `error`, on `local` or `s3` |
| `taiga_db_connections` | gauge | `state` | The pool's `in_use` and `idle` connections |
| `taiga_db_connections_max` | gauge | | The pool's limit |
| `taiga_db_waits_total`, `taiga_db_wait_seconds_total` | counter | | Acquires that had to wait for a connection, and how long they waited |
| `taiga_state_up` | gauge | | 1 when the scrape could read the queue and connections from the database, 0 when it couldn't (the rest of the scrape is still served) |

The gauges read at the scrape (`taiga_jobs`, `taiga_jobs_oldest_due_seconds`, `taiga_source_*` gauges) count across all users in a handful of indexed queries (`models.MetricsSnapshot`), bounded at 5 seconds.

## Alerts worth having

```promql
# Dead jobs: something failed five times, or couldn't succeed. GET /jobs?state=dead says which.
sum by (kind) (taiga_jobs{state="dead"}) > 0

# The queue isn't draining: a job has been due for 15 minutes. The worker polls every 2 seconds.
max by (kind) (taiga_jobs_oldest_due_seconds) > 900

# A connection needs the user to connect again.
sum(taiga_source_connections{state="needs_reconnect"}) > 0

# The busiest user is close to COROS's daily limit.
max(taiga_source_daily_files_used / taiga_source_daily_files_limit) > 0.9

# The database couldn't be read at the scrape.
taiga_state_up == 0
```
