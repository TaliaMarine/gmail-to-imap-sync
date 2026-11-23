# Gmail Archive Sync: web UI

Vue 3 + Vuetify 4 + Pinia, built with Vite. The API client is generated from the backend's OpenAPI document.

## Develop

```bash
# backend (from the repo root): PostgreSQL and AUTH_DISABLED, see ../README.md
BASE_URL=http://localhost:3000 AUTH_DISABLED=true DATABASE_URL=... go run ./cmd/gmail-archive-sync

# UI (this folder)
npm ci
npm run dev        # http://localhost:3000, proxies /api, /auth and /oauth to :1080
```

Set the backend's `BASE_URL` to the **UI origin** (`http://localhost:3000`) while using the dev server: the backend rejects state-changing requests whose `Origin` is not its own host or `BASE_URL`, and the Google redirect URI is built from it.

## Scripts

| Script | What it does |
|---|---|
| `npm run build` | Type-check (`vue-tsc`) and build to `dist/` (the Go binary serves it from `web/dist`) |
| `npm test` | Vitest component and unit tests (jsdom) |
| `npm run generate:api` | Regenerate `src/api/generated.ts` from the checked-in `openapi.json` (works offline) |
| `npm run snapshot:api` | Refresh `openapi.json` from a running backend (`API_URL`, default `http://localhost:1080`) |
| `npm run e2e` | Playwright smoke tests against a running stack (see below) |

When an API route changes: `npm run snapshot:api && npm run generate:api`, then commit both files. CI can check for drift with `npm run generate:api && git diff --exit-code src/api openapi.json`.

## Smoke tests (Playwright)

Not part of CI yet. They need a **fresh** backend (empty PostgreSQL, `AUTH_DISABLED=true`, `BASE_URL=http://localhost:3000`) and the UI on port 3000:

```bash
npx playwright install chromium     # once
npm run build && npm run preview &  # or: npm run dev &
npm run e2e                         # E2E_URL overrides http://localhost:3000
```

The tests create their own uniquely named destination and account, so they can be rerun on the same database. They do not authorise a Google account.

## Layout

```
src/api/generated.ts    orval output (do not edit)
src/services/           http.ts (fetch wrapper, ApiError), client.ts (typed calls)
src/stores/live.ts      live session state from the SSE stream (/api/v1/events, not in OpenAPI)
src/composables/        useEventStream (reconnect with backoff), useLoader, useSnackbar
src/components/         TransferRail, SessionRail, ActivityChart, LabelPreviewList, FailureTable, ...
src/views/              one lazy-loaded view per route
```
