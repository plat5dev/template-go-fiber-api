# Go + Fiber API starter

A Plat5 API starter: **Go** + **Fiber v3**, SQLite, Prometheus and OpenTelemetry. It serves tasks inside a project, and the person's own profile. The repo is a Plat5 project you can run as is: `plat5 start` in this directory runs Plat5, applies `plat5.yml`, and runs the API beside it.

Plat5 decides who may call what, at its gateway. This service owns business logic only: it reads the node ids and the caller's member id the front door gives it, and checks nothing about the caller ([plat5 `docs/guides/write-a-service.md`](https://github.com/plat5dev/plat5/blob/master/docs/guides/write-a-service.md)).

## Stack

| Piece | Choice |
|-------|--------|
| Go | 1.27 (`go.mod`'s toolchain is go1.27.3; the Dockerfile and CI follow it) |
| HTTP | Fiber v3 (`github.com/gofiber/fiber/v3`): its router, error handler, `recover` middleware and `app.Test` in the tests |
| Bodies | `encoding/json/v2`: keys matched exactly, into `plat5.Field`s, which tell an absent value from `null` from the wrong type (`plat5/body.go`) |
| DB | SQLite via `modernc.org/sqlite` (pure Go, no cgo) on `database/sql`; the schema is created at boot (`db/`) |
| Errors | `plat5.Error`, answered in [the envelope](https://github.com/plat5dev/plat5/blob/master/docs/reference/errors.md) by `plat5.ErrorHandler`, Fiber's error handler |
| Logs | `log/slog`'s JSON handler, with Plat5's field names (`telemetry.NewLogger`) |
| Traces | OpenTelemetry's SDK and OTLP/HTTP exporter; HTTP server spans from Fiber's otel middleware (`github.com/gofiber/contrib/v3/otel`) |
| Metrics | `prometheus/client_golang` on its default registry, process series included, served at `/metrics`; the same series pushed by OpenTelemetry's Prometheus bridge |
| IDs | ULID, lowercase (`github.com/oklog/ulid/v2`) |
| Tests | `go test`, the API through `app.Test`, identity faked by `httptest` servers |
| Dev | `air` (hot reload) in the compose file's `local` target |

## What it serves

Organizations and their projects are Plat5 nodes, kinds in `plat5.yml` (organization > project): created, listed, renamed, deleted and restored through identity's routes (`/orgs`, `/orgs/{organization}/projects`…), with their name as the node's `display_name`. The API keeps no organizations or projects of its own. It serves what's inside a project:

| Public (gateway) | The API's own path | Permission |
|--|--|--|
| `GET …/tasks` | `GET /projects/{project_id}/tasks` | `project:read` |
| `POST …/tasks` | `POST /projects/{project_id}/tasks` | `project:write` |
| `GET …/tasks/{task_id}` | `GET /projects/{project_id}/tasks/{task_id}` | `project:read` |
| `PATCH`, `DELETE …/tasks/{task_id}` | the same, on `/projects/{project_id}/tasks/{task_id}` | `project:write` |
| `GET`, `PUT /user/profile` | `/users/{user_id}/profile` | user route |

`…` is `/orgs/{organization}/projects/{project}`. `{organization}` and `{project}` take a slug or an id; the gateway resolves them and sends the project's id. They answer only for what the caller may reach: a project they can't use, or one that doesn't exist, is the gateway's **403**.

| Data | Keyed by | Fields |
|------|----------|--------|
| Task | The project's node id | `id`, `project_id`, `title`, `status` (`todo`, `in_progress`, `done`), `assignee_id` (a member id, or `null`), `created_by`, `created_at`, `updated_at` |
| Profile | The user id | `user_id`, `bio`, `created_at`, `updated_at` |

A task is authored: `created_by` is the member id of whoever created it, and stays theirs if they leave the tree ([where a field goes](https://github.com/plat5dev/plat5/blob/master/docs/concepts/model.md#where-a-field-goes)). Its `assignee_id` is a member of the same tree who can see the project, checked with identity when it's set ([Checking an assignee](#checking-an-assignee)). A profile is the person's, the same in every tree, so it sits on a user route. The person's name is identity's (`GET /user`), not the profile's.

Ids are lowercase ULIDs with no prefix, like every Plat5 id.

A write's body is JSON: not sent as `application/json` (with any parameters), empty, or not JSON is a **400** `INVALID_REQUEST`, as is a create without `X-Plat5-Member-Id`, before its body is read ([plat5 `docs/reference/errors.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/errors.md#which-status)). JSON that isn't an object (an array, a string, `null`) is a **422** at `body`. Keys match exactly: `{"Title": …}` isn't `title`, and a body with an unknown key is refused for it before its values are checked, `{"path": "Title", "message": "Title isn't a field."}`; the decoder stops at the first unknown key, so only that one is named. Then each wrong value is in the **422**'s `details.fields`, at its path, with its sentence, and the first one's sentence is the `message`:

| Field | Create | Update | Wrong |
|-------|--------|--------|-------|
| `title` | Required: absent, or empty once trimmed, is `Title is required.` | Absent or `null` leaves it | Trimmed of white space; over 255 characters is `Title is too long.`; `null` on a create, or not a string, is `That doesn't look right.` |
| `status` | Absent is `todo` | Absent or `null` leaves it | Anything but `todo`, `in_progress` or `done`, `null` on a create included, is `Status must be todo, in progress, or done.` |
| `assignee_id` | Absent or `null` is none | Absent leaves it, `null` unassigns | Not a lowercase ULID or `null`: `That doesn't look right.` ([Checking an assignee](#checking-an-assignee)) |
| `bio` | `PUT`: required, `Bio is required.` | | Kept as sent (not trimmed, `""` is a bio); over 2000 characters is `Bio is too long.`; `null` or not a string, `That doesn't look right.` |

Characters are code points. A value is never coerced: `5` isn't a string. Times are RFC 3339 in UTC, to the millisecond (`2026-10-28T00:00:00.100Z`). An unknown route, or a known path in a method it doesn't serve, is the **404** `NOT_FOUND` envelope; `HEAD` on a `GET` route answers as the `GET` does, without a body.

## The config

| File | What |
|------|------|
| `plat5.yml` | The deployment's config: settings, the `organization` and `project` kinds (projects don't bind their creator), the example roles (`admin`, `member`, `developer`, `viewer`), `creator_role`, the `standard` rate limit that `defaults.rate_limit` names, and the service files ([plat5 `docs/concepts/config.md`](https://github.com/plat5dev/plat5/blob/master/docs/concepts/config.md#the-config)) |
| `services/api.yml` | This API's service file: its url (`http://api:3000`) and its routes, in the `project` and `user` scopes ([services](https://github.com/plat5dev/plat5/blob/master/docs/reference/config/services.md)) |
| `services/identity.yml`, `services/audit.yml` | Identity's and audit's service files: their routes at every kind, service accounts and invites at both organizations and projects, written by `plat5 routes generate`. Run it again after changing the kinds ([generated routes](https://github.com/plat5dev/plat5/blob/master/docs/reference/config/generated-routes.md)) |
| `plat5.dev.yml` | How `plat5 start` runs the project on your machine: image versions, modules, and `app.compose` |
| `docker-compose.yml` | The API's container, `api`, on port 3000 |
| `routes.operator.yml` | What the operator plane may call on this API: the read routes, by project id (operator `docs/reference/routes.md`) |
| `plat5.template.yml` | The starter's manifest for `plat5 init`: its config, operator routes, compose file and next steps |

In a project made with `plat5 init --template go-fiber-api`, this API sits in `api/`, its service file is the project's `services/api.yml`, its operator routes are in the project's `operator.routes.yml`, and the project has its own `plat5.yml` and `plat5.dev.yml` at its root.

## What it trusts

Only front doors may reach the API: Plat5's gateway, an operator plane, anything else on the private network. Anyone who can call it directly can name any project in the path. Locally, `plat5 start` puts the container on the Plat5 network, where the gateway calls it as `http://api:3000`, and the host port is bound to `127.0.0.1` for your own curls. In production, don't publish the port at all.

| It reads | From | On |
|----------|------|----|
| The project's id | The path, `/projects/{project_id}/…`, written by `upstream_prefix: /projects/{project.id}` | Task routes; the assignee check |
| The caller's member id | `X-Plat5-Member-Id` | Task creates (required: **400** `INVALID_REQUEST` without it); request logs |
| The caller's user id | The path, `/users/{user_id}/profile`, written by `upstream: /users/{user.id}/profile` | Profile routes |
| `X-Request-ID` | The front door | Every request: logged, and in error bodies. Never set on a response |
| `traceparent` | The front door | Every request: the server span's parent |

The gateway also sends `X-Plat5-Project-Id`, `X-Plat5-Organization-Id` and `X-Plat5-User-Id`; the API reads the project's and the user's ids from the path, logs the project's and the user's from their headers, and has no use for the organization's. A body's size is the gateway's to limit (`max_body_bytes`): the API sets none of its own. It never sees `Authorization`, a slug, a principal id, or the caller's permissions or role ([plat5 `docs/reference/headers.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/headers.md)).

Every lookup is scoped by the project id in the path, and nothing checks that the project exists or that the caller may act: the front door decided. A task id under another project is **404**.

## Checking an assignee

The caller may assign: the gateway let them write. The assignee isn't the caller, so the gateway decided nothing about them, and the API asks identity before writing a body that sets one ([plat5 `docs/guides/write-a-service.md`](https://github.com/plat5dev/plat5/blob/master/docs/guides/write-a-service.md#checking-access-outside-a-request)), from `access/`:

```
POST {IDENTITY_INTERNAL_URL}/access/v1/evaluation
{ "subject":  { "type": "member",  "id": "<assignee_id>" },
  "resource": { "type": "project", "id": "<project_id>" },
  "action":   { "name": "<TASK_ASSIGNEE_PERMISSION>" } }
```

It sends the request's `traceparent` and `X-Request-ID`. After the body (**422**) and, on `PATCH`, the task (**404**), it asks, on the project in the path, and then writes or doesn't:

| Identity answers | The API |
|------------------|---------|
| `200 {"decision": true}` | Writes |
| `200 {"decision": false}` | **422** `VALIDATION_ERROR`, `That person can't see this project.`, at `assignee_id`. Nothing written |
| **503** (its Postgres is unavailable), any other status, a body without a boolean `decision`, no answer, or `IDENTITY_INTERNAL_URL` unset | **503** `SERVICE_UNAVAILABLE`, logged with the request id. Nothing written: no answer is no, and the caller retries later |

`null` unassigns and an absent `assignee_id` leaves the task's as it is; neither asks. Anything but a lowercase ULID or `null` is **422** at `assignee_id`.

The assignee is checked in `tasks/handler.go`, with `access/`. `TASK_ASSIGNEE_PERMISSION` names the permission, and the API won't start without it. The name is the deployment's, not the code's: the starter's is `project:read`, the permission `services/api.yml` requires to read a project's tasks, so an assignee is someone who can see them. A deployment that renames that permission sets this too; otherwise identity answers `false` for everyone and every assignee is refused. The resource is the `project` kind, its id from the path, both following `plat5.yml`'s kinds.

## Following the lifecycle

The API follows identity's lifecycle log, `GET /internal/events` on `IDENTITY_INTERNAL_URL`, in `lifecycle/`. What the log guarantees and how a service follows it: [plat5 `docs/concepts/lifecycle.md`](https://github.com/plat5dev/plat5/blob/master/docs/concepts/lifecycle.md#following-the-log); its params and answers: [`docs/reference/api/internal.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/api/internal.md#lifecycle-events).

It asks only for the two events it acts on, `?types=node.purged,user.deleted`:

| Event | The API |
|-------|---------|
| `node.purged` with `kind: project` | Deletes that project's tasks. A purged organization's projects are purged with it, each with its own event |
| `user.deleted` | Deletes that person's profile |

Every other event needs nothing. Tasks are authored, so `member.removed` keeps them; there's no background work to stop on `node.deleted`; and a profile is made on the person's first call, not on `user.created`.

One cursor, the last event `id` acted on, is kept in SQLite beside the data it cleans up (`feed_cursors`, one row). With no cursor it reads from the log's first event; otherwise it asks with `&after=<cursor>`. Either way identity waits for an event, so the API needs no pacing of its own. A page's deletes and the new cursor commit in one transaction, then it asks again at once; after a **204** (nothing came) it asks again at once. A **409** `CURSOR_AHEAD` (the cursor is past identity's newest event: its log was reset) deletes the cursor, logs a `warn`, and reads again from the first event. After an error (a **503** while identity's Postgres is unavailable, any other status, or no answer) it logs it and asks again in 5 seconds, from the same cursor. Deleting a purged project's tasks or a deleted person's profile again changes nothing, so an event read twice is harmless. With `IDENTITY_INTERNAL_URL` unset the log isn't read, and the API says so once at boot.

To replay the log, from its first event (after restoring the database, or fixing a handler), stop the API, delete the cursor, and start it:

```bash
docker compose stop api
docker compose run --rm --no-deps api sqlite3 /data/app.db 'DELETE FROM feed_cursors'
docker compose start api
```

It logs `following the lifecycle log from its first event`, then each page it reads. Against another database the SQL is the same: `DELETE FROM feed_cursors` in the file at `DATABASE_PATH`.

## Lists

The task list pages like every Plat5 list ([plat5 `docs/reference/lists.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/lists.md)). `plat5/lists.go` reads the params; when both are wrong, both are named.

| | |
|---|---|
| `limit` | Default 50, max 100. Over the max is clamped. Under 1 or not a whole number → **422** |
| `starting_after` | The `id` of the last item on the previous page. Not a lowercase ULID → **422** |
| Answer | `{"tasks": [...], "has_more": false}`, in `id` order (oldest first) |

## Audit details

The gateway records each write before it reaches this service. Each write says what it changed on `X-Plat5-Audit-Details`, and a create names its new id on `X-Plat5-Audit-Created` ([plat5 `docs/concepts/audit.md`](https://github.com/plat5dev/plat5/blob/master/docs/concepts/audit.md#what-a-service-adds)). The gateway stores them with the event and strips them before the client sees the response. `plat5/audit.go` builds and encodes them: one JSON object, keys sorted, everything but visible ASCII escaped.

| Write | Details |
|-------|---------|
| `POST` tasks | `task_id`, `title`, `status`, `assignee_id`. Created: the task id |
| `PATCH` a task | `title`, `status`, `assignee_id` as changes |
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
curl -X POST localhost:5400/orgs/dev/projects/demo/tasks -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"title": "Ship it"}'
curl localhost:5400/orgs/dev/projects/demo/tasks -H "Authorization: Bearer $KEY"
curl -X PUT localhost:5400/user/profile -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"bio": "Hello"}'
```

To run the API on your own machine instead (your debugger, your editor's runner):

```bash
plat5 start -x app
TASK_ASSIGNEE_PERMISSION=project:read go run .
```

`-x app` runs everything but the API, and points the gateway's `api` at your machine, so the API must listen on port 3000. Identity's internal port isn't published there, so in this mode the lifecycle log isn't read and a task with an assignee is a **503**.

`plat5 stop` stops it all; data is kept.

## Build your first feature

A `GET …/stats` for a project, end to end.

**1. Add the handler.** A package `stats/` with a handler on `/projects/:project_id/stats`, registered in `newApp` in `main.go` beside the task routes (copy `tasks/`). Read `c.Params("project_id")`. Don't check who called: the front door did, and the path is the answer. Return the errors in `plat5/errors.go` (`plat5.NotFound`, `plat5.Invalid`, `plat5.DB`…); a body is `plat5.ReadBody` into a struct of `plat5.Field`s, a list's params `plat5.ReadPage`.

**2. Route it.** Add it to `services/api.yml`, in the `project` scope. The path is relative to the project; `upstream_prefix` makes its upstream `/projects/{project.id}/stats`:

```yaml
        - path: /stats
          methods:
            GET: { required_permissions: [stats:read] }
```

**3. Grant the permission.** Add `stats:read` to a role's `permissions` in `plat5.yml` (`developer`, say). `admin`'s `"*"` already holds it.

**4. Apply.** `plat5 apply`, or `plat5 start` again. Nothing restarts.

**5. Call it.** `curl localhost:5400/orgs/dev/projects/demo/stats -H "Authorization: Bearer $KEY"`. With a credential whose roles there lack `stats:read`: **403** `FORBIDDEN`, with `required_permissions` in `details`.

**6. Writes: say what changed.** For a `POST`, `PUT`, `PATCH` or `DELETE`, set the audit details where the old and new values are in hand ([Audit details](#audit-details)), and read the author from `X-Plat5-Member-Id` if the record has one.

**7. Let the operator plane see it.** Add the API's own path, `/projects/{project_id}/stats` with `node: project_id`, to the operator plane's route file beside the routes from `routes.operator.yml`.

## Commands

```bash
TASK_ASSIGNEE_PERMISSION=project:read go run .  # start
go test ./...                                   # the tests
go vet ./...
air -c .air.toml                                # hot reload (optional)
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
| `IDENTITY_INTERNAL_URL` | unset | Identity's internal base URL, for its lifecycle log and the assignee check. The compose file sets `http://identity:3001`. Unset → the log isn't read, and a task with an assignee is a **503** |
| `TASK_ASSIGNEE_PERMISSION` | required | The permission an assignee needs on the task's project ([Checking an assignee](#checking-an-assignee)). The compose file sets `project:read`. Unset or empty → the API exits at boot |
| `OTEL_SERVICE_NAME` | `api` | Resource `service.name` |
| `OTEL_SERVICE_NAMESPACE` | `api` | Resource `service.namespace` |
| `OTEL_SERVICE_VERSION` | the build's `VERSION`, else `dev` | Resource `service.version` ([resource identity](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md#resource-identity), [Docker](#docker)) |
| `OTEL_SERVICE_INSTANCE_ID` | `$HOSTNAME`, else `api-local` | Resource `service.instance.id` |
| `DEPLOYMENT_ENV` / `OTEL_DEPLOYMENT_ENV` | `development` | Resource `deployment.environment` (`OTEL_DEPLOYMENT_ENV` wins) |
| `OTEL_RESOURCE_ATTRIBUTES` | unset | Standard bag: `service.namespace=…,service.version=…,…` (vars above win) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP base URL. Unset → no OTLP |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | unset | Optional full traces URL |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | unset | Optional full metrics URL |
| `OTEL_TRACES_EXPORTER` | `otlp` when endpoint set | Include `otlp` to push traces |
| `OTEL_METRICS_EXPORTER` | `otlp` when endpoint set | Set `prometheus` to push-off; `/metrics` always on |
| `OTEL_EXPORTER_OTLP_PROTOCOL` (and `_TRACES_`, `_METRICS_`) | `http/protobuf` | The only one supported |
| `OTEL_EXPORTER_OTLP_HEADERS` | unset | Headers on every export (e.g. a collector's auth) |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60000` | ms (OTLP metrics only) |
| `OTEL_TRACES_SAMPLER_RATIO` | `1` | Trace sampling ratio, `0` to `1`; parent-based |
| `OTEL_SDK_DISABLED` | `false` | `true` → no OTLP; stdout + `/metrics` remain |

A telemetry variable that is set but invalid stops the API at boot, naming it ([invalid values](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md#invalid-values)).

## Telemetry

Contract: Plat5 [`docs/reference/telemetry.md`](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md).

| Signal | Path |
|--------|------|
| Logs | JSON stdout (`log/slog`): one `request completed` line per request, `info`, `warn` on a 4xx, `error` on a 5xx with `error_type` (`internal`, `db`, `network`) and `error_message`; with `route` (the template, or `unknown`), `request_id`, `trace_id`, and `node_id`, `member_id` and `user_id` from `X-Plat5-Project-Id`, `X-Plat5-Member-Id` and `X-Plat5-User-Id` ([logs](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md#logs)) |
| Metrics scrape | Prometheus `/metrics` on `INTERNAL_PORT` |
| Traces | OTLP HTTP when endpoint set (default); W3C Trace Context, `traceparent` in, and on to identity; HTTP server spans from Fiber's otel middleware, as [the contract](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md#http-server-spans) has them: `{method} {route}`, `{method}` alone when no route matched, `url.query` redacted, `Error` on a 5xx with the log's `error_type` as `error.type`, never on a 4xx; `request_id` and the same ids as `plat5.node_id`, `plat5.member_id`, `plat5.user_id` |
| Metrics OTLP | On when endpoint set (default); set `OTEL_METRICS_EXPORTER=prometheus` to opt out |

```bash
# traces and metrics pushed: don't also scrape /metrics into the same backend
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318

# traces pushed, metrics scraped (no double count)
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 OTEL_METRICS_EXPORTER=prometheus
```

Under `plat5 start`, the compose file gets `OTEL_EXPORTER_OTLP_ENDPOINT` from the CLI when the observability module is on. Running on your own machine, set it yourself. Without it there's no OTLP: stdout logs and `/metrics` only, and the request's `traceparent` still goes on to identity.

An export that fails, the collector unreachable say, is an `error` line, `telemetry error` with the cause in `error_message`, each time it fails; the API keeps serving ([invalid values](https://github.com/plat5dev/plat5/blob/master/docs/reference/telemetry.md#invalid-values)).

Health and `/metrics` are on the internal app, which isn't traced. `telemetry/` is all of it: `telemetry.go` reads the variables and sets up the SDK, `metrics.go` the series, `http.go` the request middleware.

## Layout

```
main.go              # config, the public app's routes (newApp), the internal app, the lifecycle reader
*_test.go            # the HTTP contract, against newApp
plat5/               # errors and their envelope, request bodies, list params, ids and times, audit details
db/                  # SQLite open + schema
tasks/ profiles/     # the API: handler + store each
access/              # identity's access evaluation: assignees
lifecycle/           # identity's lifecycle log: purged projects, deleted accounts
telemetry/           # logs, OTel setup (the OTEL_* variables), Prometheus series, the request middleware
plat5.yml services/  # the deployment's config
plat5.dev.yml        # plat5 start
routes.operator.yml  # the operator plane's routes on this API
plat5.template.yml   # the starter's manifest
```

## Docker

```bash
docker compose up --build
docker build --target prod --build-arg VERSION=v1.2.3 -t api .   # service.version v1.2.3; dev without VERSION
```

- `local` target: hot reload with `air`, source mounted
- `prod` target: a static binary (`CGO_ENABLED=0`) on `debian:trixie-slim`, non-root
