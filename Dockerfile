# ======================================================================
# Local Stage for development
FROM golang:1.27-trixie AS local

WORKDIR /app

RUN go install github.com/air-verse/air@v1.67.4
# sqlite3, to look in the database or reset the lifecycle cursor (README).
RUN apt-get update && apt-get install -y --no-install-recommends sqlite3 \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV PORT=3000
ENV INTERNAL_PORT=3001
ENV DATABASE_PATH=/data/app.db

CMD ["air", "-c", ".air.toml"]

# ======================================================================
# Builder Stage
FROM golang:1.27-trixie AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# The release this binary reports as service.version; dev without one.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X main.version=${VERSION}" \
    -o server ./

# ======================================================================
# Prod Stage
FROM debian:trixie-slim AS prod

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -r app && useradd -r -g app app \
    && mkdir -p /data && chown app:app /data

COPY --from=builder /app/server ./server

USER app

ENV PORT=3000
ENV INTERNAL_PORT=3001
ENV DATABASE_PATH=/data/app.db

EXPOSE 3000

CMD ["./server"]
