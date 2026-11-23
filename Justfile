# Default recipe
default:
    @just --list

# Run the app (web + worker) in development: no auth, plain logs. Needs DATABASE_URL.
dev:
    AUTH_DISABLED=true HTTP_PORT=8080 LOG_FORMAT=plain LOG_LEVEL=debug go run ./cmd/gmail-archive-sync --mode all

# Run only the web/API process
dev-web-only:
    AUTH_DISABLED=true HTTP_PORT=8080 LOG_FORMAT=plain LOG_LEVEL=debug go run ./cmd/gmail-archive-sync --mode web

# Run only the sync worker
dev-worker:
    AUTH_DISABLED=true LOG_FORMAT=plain LOG_LEVEL=debug go run ./cmd/gmail-archive-sync --mode worker

# Run frontend dev server (with proxy to backend)
dev-web:
    cd web && npm run dev

# --- Trying the app against your real Gmail (see docs/dev-real.md) -------------------

real_db := "postgres://gas:gas@localhost:15432/gas?sslmode=disable"

# Start local PostgreSQL and a throwaway Dovecot.
real-up:
    docker compose -f docker-compose.dev.yml up -d --wait

# Stop them and DELETE their data (database and the archived mail).
real-down:
    docker compose -f docker-compose.dev.yml down -v

# Build the UI if needed, then run the app (web + worker) on http://localhost:1080 against the local services.
real:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p .dev
    # A stable encryption key, so stored credentials survive restarts.
    [ -f .dev/encryption-keys ] || printf 'dev:%s\n' "$(head -c 32 /dev/urandom | base64)" > .dev/encryption-keys
    [ -f web/dist/index.html ] || (cd web && npm ci && npm run build)
    DATABASE_URL='{{real_db}}' AUTH_DISABLED=true BASE_URL=http://localhost:1080 HTTP_PORT=1080 \
      ENCRYPTION_KEYS="$(cat .dev/encryption-keys)" LOG_FORMAT=plain LOG_LEVEL=info \
      go run ./cmd/gmail-archive-sync --mode all

# Show what landed in the local Dovecot: folders, counts and duplicates (there must be none).
real-summary:
    uv run dev/imap_summary.py

# Open a psql shell on the local database.
real-psql:
    docker compose -f docker-compose.dev.yml exec postgres psql -U gas gas

# Build everything
build: build-web build-go

# Build Go binary
build-go:
    go build -o bin/gmail-archive-sync ./cmd/gmail-archive-sync

# Build frontend
build-web:
    cd web && npm i && npm run build

# Run unit tests
test:
    go test ./... -race

# Run integration tests (needs Docker: PostgreSQL and Dovecot via testcontainers)
test-integration:
    go test -tags integration -race -timeout 20m ./...

# Run a specific Go test
test-one TEST:
    go test -run {{TEST}} ./...

# Run Go linter
lint:
    golangci-lint run ./...

# Format Go code
fmt:
    gofmt -w .

# Run go vet
vet:
    go vet ./...

# Type-check frontend
check-web:
    cd web && npx vue-tsc --noEmit

# Build Docker image
docker-build:
    docker build -t gmail-archive-sync .

# Clean build artifacts
clean:
    rm -rf bin/ web/dist/

# Check the Helm chart renders in its main configurations
chart-check:
    helm lint chart --set authDisabled=true --set gateway.enabled=false --set database.existingSecret.name=db
    helm template gas chart --set authDisabled=true --set gateway.enabled=false --set database.existingSecret.name=db >/dev/null
    helm template gas chart --set authDisabled=true --set gateway.enabled=false --set database.existingSecret.name=db --set worker.enabled=true >/dev/null

# Check every GitHub Action is pinned to a commit SHA (pin with: pinact run)
pinact:
    pinact run --check .github/workflows/*.yml
