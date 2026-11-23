# Developer guide

## Prerequisites

Go 1.27+, Node.js 26+, Docker (integration tests and a local PostgreSQL), [just](https://github.com/casey/just), [sqlc](https://sqlc.dev) (only when changing SQL).

## Run it

```bash
docker run -d --name gas-pg -e POSTGRES_PASSWORD=gas -e POSTGRES_USER=gas -e POSTGRES_DB=gas -p 5432:5432 postgres:17-alpine
export DATABASE_URL='postgres://gas:gas@localhost:5432/gas?sslmode=disable' AUTH_DISABLED=true LOG_FORMAT=plain
go run ./cmd/gmail-archive-sync
cd web && npm ci && npm run dev     # UI on :3000, proxies /api to :1080
```

`just --list` shows the common tasks.

## Layout

```
cmd/gmail-archive-sync/   entrypoint: flags, wiring, graceful shutdown
internal/
  api/        huma routes (OpenAPI generated from code), OAuth callback, SSE, SPA serving
  auth/       OIDC (PKCE), session cookies, CSRF/origin checks, cookie sealer
  clients/    builds Gmail/IMAP clients from stored settings; owns encryption contexts; key rotation
  config/     environment configuration
  crypto/     AES-GCM key ring
  db/         pgx pool, embedded migrations, sqlc queries (internal/db/queries -> internal/db/store)
  engine/     one sync session: initial, incremental, reconcile, retry; checkpoints; failures
  failures/   error categories, backoff, failure metadata
  gmail/      rate-limited Gmail client (quota-unit budget, retries)
  imapdest/   IMAP delivery, folder creation, per-folder dedup index
  jobs/       runner (claim/lease), scheduler, recovery, retention, webhook notifier
  mapping/    pure label -> folder/flag rules
  metrics/ progress/ testenv/ version/ kube/
web/          Vue 3 + Vuetify 4 UI (orval-generated API client from web/openapi.json)
chart/        Helm chart; examples/ Argo CD application
```

## Tests

```bash
go test -race ./...                         # unit tests, no Docker
go test -tags integration -race ./...       # + PostgreSQL, Dovecot and end-to-end (needs Docker)
```

With Rancher Desktop point testcontainers at its socket:

```bash
export DOCKER_HOST=unix://$HOME/.rd/docker.sock TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock TESTCONTAINERS_RYUK_DISABLED=true
```

`internal/e2e` runs the whole pipeline: a fake Gmail HTTP API (with the real OAuth refresh path), the real engine and runner, PostgreSQL and Dovecot. Fuzz targets: `go test -fuzz FuzzFolderName ./internal/mapping`, `-fuzz FuzzAddHeader ./internal/imapdest`.

## Changing the schema or queries

Edit `internal/db/migrations/*.sql` (new numbered file once a release exists) and `internal/db/queries/*.sql`, then `sqlc generate`. CI fails when generated files are stale.

## Invariants to keep

1. No code path deletes or modifies messages (Gmail scope is read-only; the destination only gets `APPEND`).
2. No per-message success table. Only `failed_messages` is per message.
3. A checkpoint moves only past work that was delivered, found present, or persisted as a failure.
4. Anything that may be re-run must be safe to re-run (the destination deduplicates).
