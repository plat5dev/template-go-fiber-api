# Go + Fiber API Starter

A lean, idiomatic **Go 1.27 + Fiber v3** reference API for [Plat5](https://github.com/plat5dev/plat5), backed by SQLite (`modernc.org/sqlite`), Prometheus, and OpenTelemetry.

Plat5's gateway authenticates callers, enforces permissions, and resolves slugs to node IDs before forwarding requests here. This service owns domain logic only ([Write a service](https://github.com/plat5dev/plat5/blob/master/docs/guides/write-a-service.md)).

## Quick start

Requires Docker and the `plat5` CLI:

```bash
plat5 start
```

This starts Plat5, applies `plat5.yml`, builds and runs this API from `docker-compose.yml`, and seeds a dev user with an organization (`/orgs/dev`) and personal key (`plat5 status` prints credentials).

```bash
KEY=plat5-k-2-…   # from `plat5 status`
curl -X POST localhost:5400/orgs/dev/projects -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"display_name": "Demo"}'
curl -X POST localhost:5400/orgs/dev/projects/demo/tasks -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"title": "Ship it"}'
curl localhost:5400/orgs/dev/projects/demo/tasks -H "Authorization: Bearer $KEY"
curl -X PUT localhost:5400/user/profile -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"bio": "Hello"}'
```

To run the API locally on your host instead of in Docker:

```bash
plat5 start -x app
TASK_ASSIGNEE_PERMISSION=project:read go run .
```

(`-x app` points the gateway at your host port `3000`. Because identity's internal port is only on the Docker network, the lifecycle consumer is disabled and assignee checks return `503` when `IDENTITY_INTERNAL_URL` is unset.)

## Routes

### Public API (`PORT`, default `3000`)

| Gateway path | Service route | Permission | Behavior |
|---|---|---|---|
| `GET /orgs/{org}/projects/{project}/tasks` | `GET /projects/:project_id/tasks` | `project:read` | Cursor-paginated list (`limit` default 50 / max 100, `starting_after` lowercase ULID) |
| `POST /orgs/{org}/projects/{project}/tasks` | `POST /projects/:project_id/tasks` | `project:write` | Creates a task (`X-Plat5-Member-Id` required); checks `assignee_id` via identity |
| `GET /orgs/{org}/projects/{project}/tasks/{task_id}` | `GET /projects/:project_id/tasks/:task_id` | `project:read` | Fetches a task scoped to `project_id` (`404` across tenants) |
| `PATCH /orgs/{org}/projects/{project}/tasks/{task_id}` | `PATCH /projects/:project_id/tasks/:task_id` | `project:write` | Partial update (`title`, `status`, `assignee_id`; `null` unassigns; no-op skips DB write) |
| `DELETE /orgs/{org}/projects/{project}/tasks/{task_id}` | `DELETE /projects/:project_id/tasks/:task_id` | `project:write` | Deletes a task (`204` or `404`) |
| `GET /user/profile` | `GET /users/:user_id/profile` | user route | Lazily creates and returns `{user_id, bio, created_at, updated_at}` |
| `PUT /user/profile` | `PUT /users/:user_id/profile` | user route | Upserts `bio` (≤ 2000 runes) and emits audit diff |

### Internal server (`INTERNAL_PORT`, default `3001`, stdlib `net/http`)

- `GET /health/live` — liveness probe (`200 {"status":"healthy"}`)
- `GET /health/ready` — SQLite readiness check (`200` or `503 {"status":"unhealthy"}`)
- `GET /metrics` — Prometheus metrics (`http_requests_total`, `http_request_duration_seconds`, plus Go and process collectors)

## Plat5 integrations

- **Error envelope & pagination:** Standard Plat5 error envelope ([Errors](https://github.com/plat5dev/plat5/blob/master/docs/reference/errors.md#envelope)) and cursor pagination ([Lists](https://github.com/plat5dev/plat5/blob/master/docs/reference/lists.md)).
- **Audit headers:** Writes set `X-Plat5-Audit-Details` (visible-ASCII JSON diff, ≤ 4096 bytes) and `POST` sets `X-Plat5-Audit-Created` ([Audit](https://github.com/plat5dev/plat5/blob/master/docs/concepts/audit.md#what-a-service-adds)).
- **Access evaluation:** Setting a task's `assignee_id` checks `POST {IDENTITY_INTERNAL_URL}/access/v1/evaluation` for `TASK_ASSIGNEE_PERMISSION` on the project, failing closed with `503` if identity cannot answer ([Access evaluation](https://github.com/plat5dev/plat5/blob/master/docs/reference/api/internal.md#access-evaluation)).
- **Lifecycle cleanup:** Polls `GET {IDENTITY_INTERNAL_URL}/internal/events?types=node.purged,user.deleted` in a single SQLite transaction with `feed_cursors`, removing tasks when a project is purged and profiles when a user is deleted ([Following the log](https://github.com/plat5dev/plat5/blob/master/docs/concepts/lifecycle.md#following-the-log)).
- **Telemetry:** Structured JSON logs (`log/slog`) with `request_id`, `trace_id`, `span_id`, and `X-Plat5-*` caller IDs; W3C `traceparent` propagation; `/metrics` scrape always on; optional OTLP/HTTP trace and metric push when `OTEL_EXPORTER_OTLP_ENDPOINT` is set ([Telemetry](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md)).

## Environment

See `.env.example`.

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `3000` | Public Fiber HTTP port |
| `INTERNAL_PORT` | `3001` | Internal `net/http` port (`/health/*`, `/metrics`) |
| `DATABASE_PATH` | `./data/app.db` | SQLite database file (opened in WAL mode) |
| `TASK_ASSIGNEE_PERMISSION` | **required** | Permission an assignee needs on the project (e.g. `project:read`) |
| `IDENTITY_INTERNAL_URL` | unset | Identity internal base URL (`http://identity:3001` in Docker Compose) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | Enables OTLP/HTTP trace & metric push (standard `OTEL_*` vars supported by the SDK) |
| `OTEL_METRICS_EXPORTER` | `otlp` when endpoint set | Set `prometheus` to push traces only and scrape `/metrics` |
| `OTEL_SDK_DISABLED` | `false` | Set `true` to disable OTLP push while keeping stdout logs and `/metrics` |

## Layout & commands

```
main.go          # Config, public Fiber app, internal net/http server, graceful shutdown
db.go            # SQLite open (WAL mode) + schema migration
plat5.go         # Error envelope, JSON decoding, ULIDs, pagination, audit headers
tasks.go         # Project-scoped task store & handlers
profiles.go      # User profile store & handlers
access.go        # Identity access evaluation client (fail-closed)
lifecycle.go     # Identity lifecycle event consumer + cursor management
telemetry.go     # slog JSON logger, Prometheus metrics, OTel spans & OTLP export
*_test.go        # Tests for routes, validation, access eval, lifecycle, health/metrics
plat5.yml        # Deployment config (kinds, roles, rate limits, services/)
plat5.dev.yml    # Local dev stack config for `plat5 start`
```

```bash
go test -race ./...                             # run tests
go vet ./...                                    # static checks
TASK_ASSIGNEE_PERMISSION=project:read go run .  # run locally
docker build --target prod --build-arg VERSION=v1.0.0 -t api .
```
