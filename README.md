# Gmail Archive Sync

Selectively and additively replicates Gmail messages into a self-hosted IMAP server (Dovecot), with a web UI for configuration, live progress, history and failure management. Read-only against Gmail; never deletes anything.

See [FULL_SPECS.md](FULL_SPECS.md) for the full design and decisions.

## What it does

- Copies Gmail messages into a self-hosted IMAP server (Dovecot): labels become folders, a message with several labels is copied to each folder, unlabelled mail goes to `Archive`, sent mail to `Sent`.
- Read-only against Gmail (`gmail.readonly`); never deletes or modifies anything, in Gmail or in the archive.
- Resumable initial import, then incremental sync through the Gmail History API; interrupted runs resume where they stopped, and an expired history triggers an automatic reconcile.
- No per-message success records: duplicates are avoided by an `X-Gmail-Archive-ID` header checked on the destination. Only failed messages are stored, with retry, backoff and manual retry/ignore.
- Web UI (Vue/Vuetify): accounts, Google authorisation, label and folder rules with preview, live progress, session history, failures, system status. Sign-in through OIDC.
- Prometheus metrics, structured logs, Helm chart, optional notification webhook.

Design and decisions: [FULL_SPECS.md](FULL_SPECS.md). Open questions and implementation notes: [NOTES.md](NOTES.md). Guides: [docs/google-setup.md](docs/google-setup.md), [docs/dev-real.md](docs/dev-real.md) (try it against your real Gmail), [docs/operations.md](docs/operations.md), [DEVELOPER.md](DEVELOPER.md).

## Run locally

```bash
docker run -d --name gas-pg -e POSTGRES_PASSWORD=gas -e POSTGRES_USER=gas -e POSTGRES_DB=gas -p 5432:5432 postgres:17-alpine
export DATABASE_URL='postgres://gas:gas@localhost:5432/gas?sslmode=disable' AUTH_DISABLED=true LOG_FORMAT=plain
go run ./cmd/gmail-archive-sync            # --mode all|web|worker, default all
cd web && npm ci && npm run dev            # UI on :3000, proxies /api to :1080
```

## Configuration (environment)

| Variable | Description | Default |
|---|---|---|
| `DATABASE_URL` | PostgreSQL URL (required) | — |
| `HTTP_PORT` | HTTP port (also serves health/metrics in worker mode) | `1080` |
| `BASE_URL` | Public URL; forms the OIDC and Google redirect URIs | — |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | OIDC sign-in (PKCE; secret optional) | — |
| `AUTH_DISABLED` | Run without authentication (development only) | `false` |
| `SESSION_KEY` or `SESSION_KEY_SECRET_NAME`/`_KEY` | Cookie signing key, or a k8s Secret created on first start | — |
| `ENCRYPTION_KEYS` or `ENCRYPTION_KEY_SECRET_NAME`/`_KEY` | Key ring `id:base64,...` for credentials at rest, or a k8s Secret created on first start | — |
| `LOG_LEVEL` / `LOG_FORMAT` | `debug`/`info`/`warn`/`error`, `json`/`plain` | `info`, `json` |

| `MAX_CONCURRENT_SYNCS` | Sessions (accounts) one process runs at once | `2` |

Everything else (Google OAuth client, accounts, destinations, labels, schedules, retention) is configured in the UI and stored in PostgreSQL.

Flags: `--mode all|web|worker` (default `all`); `--rotate-keys` re-encrypts stored secrets with the active key and exits.

## Development

`just --list` shows the tasks. Integration tests: `go test -tags integration ./...` (needs Docker).
