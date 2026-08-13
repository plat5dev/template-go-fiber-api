# Go + Fiber API template

Reference Plat5 business service: **Go** + **Fiber v3**, SQLite (`modernc.org/sqlite`), Prometheus + OTel.

Gateway authenticates. This service trusts identity headers and owns business logic only.

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

| Resource | Scope | Identity headers |
|----------|-------|------------------|
| Profiles | `user` | `X-User-Id` |
| Projects | `organization` | `X-Organization-Id`, `X-Member-Id` |
| Tasks | `organization` (nested under project) | same |

Missing expected identity headers → **500 `INTERNAL_ERROR`** (platform bug), never 401.

## Quick start (host app + Plat5 CLI)

```bash
mkdir my-app && cd my-app
plat5 init --template go-fiber-api --auth -y

# edit go.mod module path if desired, then:
go mod tidy
plat5 start          # gateway :5001, registry :5002, applies routes.identity.yml + routes.yml
go run .             # API :3000, health :3001
```

`plat5.template.yml` drives init (upstreams, routes, next steps). The CLI fetches this repo, copies the tree, and writes `plat5.yml`.

Community / fork:
```bash
plat5 init --template plat5dev/template-go-fiber-api --auth -y
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
| `OTEL_SERVICE_VERSION` | `0.0.0` / `CI_COMMIT_TAG` | Resource `service.version` |
| `OTEL_SERVICE_INSTANCE_ID` | hostname / local | Resource `service.instance.id` |
| `DEPLOYMENT_ENV` / `OTEL_DEPLOYMENT_ENV` | `development` | Resource `deployment.environment` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP base URL. Unset → no OTLP |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | unset | Optional full traces URL |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | unset | Optional full metrics URL |
| `OTEL_TRACES_EXPORTER` | `otlp` when endpoint set | Include `otlp` to push traces |
| `OTEL_METRICS_EXPORTER` | `otlp` when endpoint set | Set `prometheus` to push-off; `/metrics` always on |
| `OTEL_METRIC_EXPORT_INTERVAL` | SDK default | ms (OTLP metrics only) |
| `OTEL_TRACES_SAMPLER_RATIO` | `1` | Trace sampling ratio |
| `OTEL_SDK_DISABLED` | unset | `true` → no OTLP; stdout + `/metrics` remain |

## Telemetry

Contract: Plat5 [`docs/telemetry.md`](../../docs/telemetry.md).

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
metrics/             # Prometheus SoT
middleware/          # identity + request logger
telemetry/           # OTel init (exporter matrix)
profiles|projects|tasks/
routes.identity.yml  # identity public surface (edit or omit)
routes.yml           # app gateway routes
plat5.template.yml   # CLI init metadata
```

## Docker

```bash
docker compose up --build
```

## Gold standard

Clone this template (or copy packages) when starting a new Go Plat5 service. Keep telemetry gating in-tree — do not publish a shared telemetry library across products.
