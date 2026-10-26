# Go + Fiber API starter

A Plat5 API starter: **Go** + **Fiber v3**, SQLite (`modernc.org/sqlite`), Prometheus + OTel. It serves tasks inside an environment, and the person's own profile. The repo is a Plat5 project you can run as is: `plat5 start` in this directory runs Plat5, applies `plat5.yml`, and runs the API beside it.

Plat5 decides who may call what, at its gateway. This service owns business logic only: it reads the node ids and the caller's member id the front door gives it, and checks nothing about the caller ([plat5 `docs/guides/write-a-service.md`](https://github.com/plat5dev/plat5/blob/master/docs/guides/write-a-service.md)).

## What it serves

Projects and their environments are Plat5 nodes, kinds in `plat5.yml` (organization > project > environment): created, listed, renamed, deleted and restored through identity's routes (`/orgs/{organization}/projects`, `/orgs/{organization}/projects/{project}/envs`…), with their name as the node's `display_name`. The API keeps no projects or environments of its own. It serves what's inside an environment:

| Public (gateway) | The API's own path | Permission |
|--|--|--|
| `GET …/envs/{environment}/tasks` | `GET /environments/{environment_id}/tasks` | `environment:read` |
| `POST …/envs/{environment}/tasks` | `POST /environments/{environment_id}/tasks` | `environment:write` |
| `GET …/envs/{environment}/tasks/{task_id}` | `GET /environments/{environment_id}/tasks/{task_id}` | `environment:read` |
| `PATCH`, `DELETE …/envs/{environment}/tasks/{task_id}` | the same, on `/environments/{environment_id}/tasks/{task_id}` | `environment:write` |
| `GET`, `PUT /user/profile` | `/users/{user_id}/profile` | user route |

`…` is `/orgs/{organization}/projects/{project}`. `{organization}`, `{project}` and `{environment}` take a slug or an id; the gateway resolves them and sends the environment's id. They answer only for what the caller may reach: an environment they can't use, or one that doesn't exist, is the gateway's **403**.

| Data | Keyed by | Fields |
|------|----------|--------|
| Task | The environment's node id | `id`, `environment_id`, `title`, `status` (`todo`, `in_progress`, `done`), `created_by`, `created_at`, `updated_at` |
| Profile | The user id | `user_id`, `bio`, `created_at`, `updated_at` |

A task is authored: `created_by` is the member id of whoever created it, and stays theirs if they leave the tree ([where a field goes](https://github.com/plat5dev/plat5/blob/master/docs/concepts/model.md#where-a-field-goes)). A profile is the person's, the same in every tree, so it sits on a user route. The person's name is identity's (`GET /user`), not the profile's.

Ids are lowercase ULIDs with no prefix, like every Plat5 id.

## The config

| File | What |
|------|------|
| `plat5.yml` | The deployment's config: settings, the `organization`, `project` and `environment` kinds (projects and environments don't bind their creator), the example roles (`admin`, `member`, `developer`), `creator_role`, the `default` rate limit, and the service files ([plat5 `docs/concepts/config.md`](https://github.com/plat5dev/plat5/blob/master/docs/concepts/config.md#the-config)) |
| `services/api.yml` | This API's service file: its url (`http://api:3000`) and its routes, in the `environment` and `user` scopes ([services](https://github.com/plat5dev/plat5/blob/master/docs/reference/config/services.md)) |
| `services/identity.yml`, `services/audit.yml` | Identity's and audit's service files: their urls, and identity's overrides: no service accounts at projects, so they belong to an organization or an environment, and no invites at environments ([resources](https://github.com/plat5dev/plat5/blob/master/docs/reference/config/resources.md)) |
| `plat5.dev.yml` | How `plat5 start` runs the project on your machine: image versions, modules, and `app.compose` |
| `docker-compose.yml` | The API's container, `api`, on port 3000 |
| `routes.operator.yml` | What the operator plane may call on this API: the read routes, by environment id (operator `docs/reference/routes.md`) |
| `plat5.template.yml` | The starter's manifest for `plat5 init`: its service file, operator routes, compose file and next steps |

In a project made with `plat5 init --template go-fiber-api`, this API sits in `api/`, its service file is the project's `services/api.yml`, its operator routes are in the project's `operator.routes.yml`, and the project has its own `plat5.yml` and `plat5.dev.yml` at its root.

## What it trusts

Only front doors may reach the API: Plat5's gateway, an operator plane, anything else on the private network. Anyone who can call it directly can name any environment in the path. Locally, `plat5 start` puts the container on the Plat5 network, where the gateway calls it as `http://api:3000`, and the host port is bound to `127.0.0.1` for your own curls. In production, don't publish the port at all.

| It reads | From | On |
|----------|------|----|
| The environment's id | The path, `/environments/{environment_id}/…`, written by `upstream_prefix: /environments/{environment.id}` | Task routes |
| The caller's member id | `X-Plat5-Member-Id` | Task creates (required: **400** `INVALID_REQUEST` without it); request logs |
| The caller's user id | The path, `/users/{user_id}/profile`, written by `upstream: /users/{user.id}/profile` | Profile routes |
| `X-Request-ID` | The front door | Every request: logged, and in error bodies. Never set on a response |
| `traceparent` | The front door | Every request: the server span's parent |

The gateway also sends the same ids as `X-Plat5-Environment-Id`, `X-Plat5-Project-Id`, `X-Plat5-Organization-Id` and `X-Plat5-User-Id`; the API reads the path instead. It never sees `Authorization`, a slug, a principal id, or the caller's permissions or role ([plat5 `docs/reference/headers.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/headers.md)).

Every lookup is scoped by the environment id in the path, and nothing checks that the environment exists or that the caller may act: the front door decided. A task id under another environment is **404**.

## Following the lifecycle

The API reads identity's lifecycle feed, `GET {IDENTITY_INTERNAL_URL}/internal/lifecycle-events?starting_after=<id>&limit=100` ([plat5 `docs/concepts/lifecycle.md`](https://github.com/plat5dev/plat5/blob/master/docs/concepts/lifecycle.md#lifecycle-events)), in `lifecycle/`.

| Event | The API |
|-------|---------|
| `node.purged` with `kind: environment` | Deletes that environment's tasks. A purged project's environments are purged with it, each with its own event |
| Everything else | Nothing. Tasks are authored, so `member.removed` keeps them, and there's no background work to stop on `node.deleted` |

It polls every 5 seconds, and reads again at once while `has_more` is `true`. The cursor, the last event `id` acted on, is kept in SQLite (`lifecycle_cursor`) and advanced after acting; deleting a purged environment's tasks again changes nothing, so an event read twice is harmless. A failed poll is logged and tried on the next tick. With `IDENTITY_INTERNAL_URL` unset the feed isn't read, and the API says so once at boot.

## Lists

The task list pages like every Plat5 list ([plat5 `docs/reference/lists.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/lists.md)). `pagination/` reads the params.

| | |
|---|---|
| `limit` | Default 50, max 100. Over the max is clamped. Under 1 or not a number → **422** |
| `starting_after` | The `id` of the last item on the previous page. Not a lowercase ULID → **422** |
| Answer | `{"tasks": [...], "has_more": false}`, in `id` order (oldest first) |

## Audit details

The gateway records each write before it reaches this service. Each write says what it changed on `X-Plat5-Audit-Details`, and a create names its new id on `X-Plat5-Audit-Created` ([plat5 `docs/concepts/audit.md`](https://github.com/plat5dev/plat5/blob/master/docs/concepts/audit.md#what-a-service-adds)). The gateway stores them with the event and strips them before the client sees the response. `audit/` builds and encodes them.

| Write | Details |
|-------|---------|
| `POST` tasks | `task_id`, `title`, `status`. Created: the task id |
| `PATCH` a task | `title`, `status` as changes |
| `PUT` the profile | `bio` as a change |
| `DELETE` a task | None: the route and its path say what happened |

A change is `{"from": …, "to": …}`, only for fields that changed; a write that changed nothing sends no header. Never put a secret in either header.

## Run it

You need Docker and the `plat5` CLI.

```bash
plat5 start
```

`plat5 start` runs Plat5 and its modules, applies `plat5.yml`, builds and runs the API from `docker-compose.yml`, and seeds a dev person with an org, `Dev` (`/orgs/dev`), and a personal key. It prints the links, logins and key; `plat5 status` prints them again. Every credential is sent as `Authorization: Bearer`:

```bash
KEY=plat5-k-2-…   # from plat5 status
curl -X POST localhost:5400/orgs/dev/projects -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"display_name": "Demo"}'
curl -X POST localhost:5400/orgs/dev/projects/demo/envs -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"display_name": "Development"}'
curl -X POST localhost:5400/orgs/dev/projects/demo/envs/development/tasks -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"title": "Ship it"}'
curl localhost:5400/orgs/dev/projects/demo/envs/development/tasks -H "Authorization: Bearer $KEY"
curl -X PUT localhost:5400/user/profile -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"bio": "Hello"}'
```

To run the API on your own machine instead (your debugger, your editor's runner):

```bash
plat5 start -x app
go run .
```

`-x app` runs everything but the API, and points the gateway's `api` at your machine, so the API must listen on port 3000. Identity's internal port isn't published there, so the lifecycle feed isn't read in this mode.

`plat5 stop` stops it all; data is kept.

## Build your first feature

A `GET …/envs/{environment}/stats` for an environment, end to end.

**1. Add the handler.** A package `stats/` with a handler on `/environments/:environment_id/stats`, registered in `main.go` beside the task routes (copy `tasks/`). Read `c.Params("environment_id")`. Don't check who called: the front door did, and the path is the answer.

**2. Route it.** Add it to `services/api.yml`, in the `environment` scope. The path is relative to the environment; `upstream_prefix` makes its upstream `/environments/{environment.id}/stats`:

```yaml
        - path: /stats
          methods:
            GET: { required_permissions: [stats:read] }
```

**3. Grant the permission.** Add `stats:read` to a role's `permissions` in `plat5.yml` (`developer`, say). `admin`'s `"*"` already holds it. A permission a role grants that no route requires is refused at apply, so a typo on either side shows up there.

**4. Apply.** `plat5 apply`, or `plat5 start` again. Nothing restarts.

**5. Call it.** `curl localhost:5400/orgs/dev/projects/demo/envs/development/stats -H "Authorization: Bearer $KEY"`. With a credential whose roles there lack `stats:read`: **403** `FORBIDDEN`, with `required_permissions` in `details`.

**6. Writes: say what changed.** For a `POST`, `PUT`, `PATCH` or `DELETE`, set the audit details where the old and new values are in hand ([Audit details](#audit-details)), and read the author from `X-Plat5-Member-Id` if the record has one.

**7. Let the operator plane see it.** Add the API's own path, `/environments/{environment_id}/stats` with `node: environment_id`, to the operator plane's route file beside the routes from `routes.operator.yml`.

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
| `IDENTITY_INTERNAL_URL` | unset | Identity's internal base URL, for the lifecycle feed. The compose file sets `http://identity:3001`. Unset → the feed isn't read |
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

Contract: Plat5 [`docs/reference/telemetry.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md).

| Signal | Path |
|--------|------|
| Logs | JSON stdout (zerolog); access line per request, with `request_id`, and `node_id` and `member_id` on task routes or `user_id` on the profile |
| Metrics scrape | Prometheus `/metrics` on `INTERNAL_PORT` |
| Traces | OTLP HTTP when endpoint set (default); the same ids as `plat5.node_id`, `plat5.member_id`, `plat5.user_id` |
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
main.go              # dual listen: public + internal; starts the lifecycle reader
db/                  # SQLite open + schema
errors/              # Plat5 error envelope
audit/               # X-Plat5-Audit-Details / -Created
pagination/          # limit, starting_after, has_more
lifecycle/           # identity's lifecycle feed: purged environments
metrics/             # Prometheus SoT
middleware/          # request logger
telemetry/           # OTel init (exporter matrix)
profiles/ tasks/     # the API
plat5.yml services/  # the deployment's config
plat5.dev.yml        # plat5 start
routes.operator.yml  # the operator plane's routes on this API
plat5.template.yml   # the starter's manifest
```

## Docker

```bash
docker compose up --build
```

## Gold standard

Clone this starter (or copy packages) when starting a new Go Plat5 service. Keep telemetry gating in-tree — do not publish a shared telemetry library across products.
