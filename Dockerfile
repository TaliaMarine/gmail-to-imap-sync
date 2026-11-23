# syntax=docker/dockerfile:1

# ---- Stage 1: build the frontend (runs on the build host's architecture) ----
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /web

# Lock files first so the install layer is cached until dependencies change.
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

COPY web/ .
RUN npm run build

# ---- Stage 2: build the Go binary, cross-compiled for the target platform ----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS backend
WORKDIR /app
ENV GOFLAGS=-mod=readonly

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS TARGETARCH
# Pure Go (pgx), so the binary runs on the distroless static image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /gmail-archive-sync ./cmd/gmail-archive-sync

# ---- Stage 3: minimal runtime ----
# No shell or package manager. The nonroot tag runs as uid/gid 65532.
FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.source="https://github.com/TaliaMarine/gmail-to-imap-sync" \
      org.opencontainers.image.description="Selectively replicates Gmail messages into a self-hosted IMAP server, with a web UI"

COPY --from=backend /gmail-archive-sync /gmail-archive-sync
# The binary serves the frontend from disk (web/dist relative to /), not via go:embed.
COPY --from=frontend /web/dist /web/dist

ENV HTTP_PORT=1080
EXPOSE 1080
WORKDIR /
USER 65532:65532
ENTRYPOINT ["/gmail-archive-sync"]
# Default mode runs the web UI and the sync worker in one process.
CMD ["--mode", "all"]
