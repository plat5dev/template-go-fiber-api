# Go + Fiber API template

Reference Plat5 business service: **Go** + **Fiber v3**, SQLite (`modernc.org/sqlite`), Prometheus + OTel.

Gateway admits the caller and fills `{subject.*}` into `upstream`. This service trusts that path and owns business logic only. It does not parse `Authorization` or `X-API-Key`, and it does not read identity from headers.

That trust is the security model: **nothing but the gateway may reach this service.** Anyone who can call it directly can name any org in the path. Locally, `plat5 start` puts the container on the Plat5 network, the gateway calls it as `http://api:3000`, and the host port is bound to `127.0.0.1` for your own curls. In production, don't publish the port at all.

## Stack

| Piece | Choice |
|-------|--------|
| Runtime | Go 1.25+ |
| HTTP | Fiber v3 |
| DB | SQLite (pure Go, no CGO) |
| IDs | ULID |
| Logs | zerolog JSON stdout |
| Metrics | Prometheus scrape + optional OTLP bridge |
| Traces | OTLP HTTP (opt-in via endpoint) |

## Demo domain

This process listens on the rewritten path.

| Resource | Scope | Credential | Edge | Listen path |
|----------|-------|------------|------|-------------|
| Profiles | `user` | user JWT or user API key | `/user/profile` | `/users/{user_id}/profile` |
| Projects | `organization` | member key or member session | `/org/projects` | `/organizations/{organization_id}/projects` |
| Tasks | `organization` | member key or member session | `/org/projects/{project_id}/tasks` | `.../projects/{project_id}/tasks` |

`GET` and `PUT` on the profile. Projects and tasks: `GET` and `POST` on the collection; `GET`, `PATCH`, and `DELETE` on `{project_id}` / `{task_id}`.

Profiles are the person's: this app's data about them, keyed by the directory id. The person's name is identity's (`GET /user`), not this app's. Projects and tasks are the org's: they store the owner, not who made them (the org's audit log records who did what). Organization scope is a member key or member session, not a user JWT. Handlers read `user_id` and `organization_id` from those path params.

## Build your first feature

A `GET /org/stats` for the caller's org, end to end. About ten minutes, no restart.

**1. Add the handler.** A package `stats/` with a handler on `/organizations/:organization_id/stats`, registered in `main.go` beside the projects group (copy `projects/`). Read `c.Params("organization_id")`. Don't check who called: the gateway did, and the path is the answer.

**2. Route it.** Add the edge path to `routes.yml`, under the `api` service's `organization` routes. `{subject.organization_id}` is filled from the caller's credential, never from the client:

```yaml
        - path: /org/stats
          upstream: /organizations/{subject.organization_id}/stats
          methods: [GET]
          required_labels: [stats:read]   # step 3; leave it out for any member
```

**3. Require a permission.** `required_labels` names the label; grant it to roles in `roles.yml` (`admin: [..., stats:read]`). A role with `"*"` holds every label. `plat5 start` and `plat5 roles apply` list labels no role grants by name, which is how a typo (`stats:raed`) shows up.

**4. Apply.** `plat5 routes apply && plat5 roles apply`, or `plat5 start`. Neither restarts anything.

**5. Call it.** With the member key from `plat5 status`: `curl localhost:5400/org/stats -H "X-API-Key: …"`. With a key whose role lacks the label: **403** `FORBIDDEN`, naming `required_labels`. With a user key: **401**, saying the route takes a member credential.

**6. Writes: say what changed.** For a `POST`, `PATCH`, or `DELETE`, set the audit details where the old and new values are in hand ([Audit details](#audit-details)). The event shows up in the console's org audit log, and on the demo app's Audit log page.

**7. Optional: let staff see it.** Add the listen path to an operator route list and point `operator.routes` at it in `plat5.yml` (cli README, "Operator routes").

## Lists

Project and task lists page like every Plat5 list (plat5 [`docs/lists.md`](https://github.com/plat5dev/plat5/blob/master/docs/lists.md)). `pagination/` reads the params.

| | |
|---|---|
| `limit` | Default 50, max 100. Over the max is clamped. Under 1 or not a number → **422** |
| `starting_after` | The `id` of the last item on the previous page. Not a ULID → **422** |
| Answer | `{"projects": [...], "has_more": false}`, in `id` order (oldest first) |

## Audit details

The gateway records each write before it reaches this service. Each write says what it changed on `X-Plat5-Audit-Details`, and a create names its new id on `X-Plat5-Audit-Created` (plat5 [`docs/audit.md`](https://github.com/plat5dev/plat5/blob/master/docs/audit.md#what-a-service-adds)). The gateway stores them with the event and strips them before the client sees the response. `audit/` builds and encodes them.

| Write | Details |
|-------|---------|
| `POST` projects | `project_id`, `name`. Created: the project id |
| `PATCH` a project | `name`, `description` as changes |
| `POST` tasks | `task_id`, `title`, `status`. Created: the task id |
| `PATCH` a task | `title`, `status` as changes |
| `PUT` the profile | `bio` as a change |
| `DELETE` | None: the route and its path say what happened |

A change is `{"from": …, "to": …}`, only for fields that changed; a write that changed nothing sends no header. Never put a secret in either header.

## Quick start

```bash
plat5 init my-app --template go-fiber-api
cd my-app
plat5 start
```

You need Docker, nothing else. `plat5 start` runs Plat5, its modules, this API on `:3000`, and the starter web app on `:3001`, then prints every link, login, and a key to call the API with. Sign in to the web app as `dev@example.com` / `password`.

`plat5.template.yml` drives init: upstreams, routes, and `app:` (this compose file, and the web app's origin). The CLI fetches this repo, copies the tree, and writes `plat5.yml`.

To run the API on your own machine instead (your debugger, your editor's runner):

```bash
plat5 start -x app
go mod tidy
go run .           # API :3000, health :3001
```

Community / fork:
```bash
plat5 init my-app --template plat5dev/template-go-fiber-api
```

## Commands

```bash
go run .          # start
go test ./...     # unit tests
go vet ./...
air -c .air.toml  # hot reload (optional)
```

## Ports

| Port | Env | Purpose |
|------|-----|---------|
| 3000 | `PORT` | Public API |
| 3001 | `INTERNAL_PORT` | `/health/live`, `/health/ready`, `/metrics` |

## Environment

See `.env.example`. Export vars or use a process manager; Go does not load `.env` by default.

| Variable | Default | Notes |
|----------|---------|-------|
| `PORT` | `3000` | Public |
| `INTERNAL_PORT` | `3001` | Health + `/metrics` |
| `DATABASE_PATH` | `./data/app.db` | SQLite file |
| `OTEL_SERVICE_NAME` | `api` | Resource `service.name` |
| `OTEL_SERVICE_NAMESPACE` | `api` | Resource `service.namespace` |
| `OTEL_SERVICE_VERSION` | `CI_COMMIT_TAG`, else `0.0.0` | Resource `service.version` |
| `OTEL_SERVICE_INSTANCE_ID` | `$HOSTNAME`, else `api-local` | Resource `service.instance.id` |
| `DEPLOYMENT_ENV` / `OTEL_DEPLOYMENT_ENV` | `development` | Resource `deployment.environment` (`OTEL_DEPLOYMENT_ENV` wins) |
| `OTEL_RESOURCE_ATTRIBUTES` | unset | Standard bag: `service.namespace=…,service.version=…,…` (vars above win) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP base URL. Unset → no OTLP |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | unset | Optional full traces URL |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | unset | Optional full metrics URL |
| `OTEL_TRACES_EXPORTER` | `otlp` when endpoint set | Include `otlp` to push traces |
| `OTEL_METRICS_EXPORTER` | `otlp` when endpoint set | Set `prometheus` to push-off; `/metrics` always on |
| `OTEL_METRIC_EXPORT_INTERVAL` | SDK default | ms (OTLP metrics only) |
| `OTEL_TRACES_SAMPLER_RATIO` | `1` | Trace sampling ratio |
| `OTEL_SDK_DISABLED` | unset | `true` → no OTLP; stdout + `/metrics` remain |

## Telemetry

Contract: Plat5 [`docs/telemetry.md`](https://github.com/plat5dev/plat5/blob/master/docs/telemetry.md).

| Signal | Path |
|--------|------|
| Logs | JSON stdout (zerolog); access line per request |
| Metrics scrape | Prometheus `/metrics` on `INTERNAL_PORT` |
| Traces | OTLP HTTP when endpoint set (default) |
| Metrics OTLP | On when endpoint set (default); set `OTEL_METRICS_EXPORTER=prometheus` to opt out |

```bash
# traces push + scrape metrics (no double count)
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318

# full OTLP push — do not also scrape /metrics into the same backend
# OTEL_METRICS_EXPORTER=prometheus  # opt out of metrics push
```

Health and `/metrics` are on the internal app (not OTel-instrumented).

## Layout

```
main.go              # dual listen: public + internal
db/                  # SQLite open + migrate
errors/              # Plat5 error envelope
audit/               # X-Plat5-Audit-Details / -Created
pagination/          # limit, starting_after, has_more
metrics/             # Prometheus SoT
middleware/          # request logger
telemetry/           # OTel init (exporter matrix)
profiles|projects|tasks/
routes.identity.yml  # identity public surface (edit or omit)
routes.audit.yml     # GET /org/audit-events, /user/audit-events (omit with AUDIT_ENABLED=false)
routes.yml           # app routes (edge path + upstream)
roles.yml            # roles → labels (member gets projects:write)
plat5.template.yml   # CLI init metadata
```

## Docker

```bash
docker compose up --build
```

## Gold standard

Clone this template (or copy packages) when starting a new Go Plat5 service. Keep telemetry gating in-tree — do not publish a shared telemetry library across products.
