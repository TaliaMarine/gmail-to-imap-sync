# Gmail Archive Sync — Full Specification

**Status:** Reviewed, ready for implementation
**Supersedes:** `FIRST_SPECS.md` (kept for history; where they differ, this document wins)
**Purpose:** Selectively and additively replicate Gmail messages into a self-hosted Dovecot IMAP server, with a management web UI.

---

## 0. Decisions log (review outcome)

Confirmed with the owner:

| # | Topic | Decision |
|---|---|---|
| D1 | Database | **PostgreSQL**, multi-replica safe. Existing cluster, not bundled. |
| D2 | UI auth | **OIDC Authorization Code + PKCE** (public client, no secret), signed session cookie, as in `mailcatcher`. `AUTH_DISABLED` for dev only. |
| D3 | Packaging | **Helm chart only** (OCI on ghcr). No Kustomize. ArgoCD `Application` example points at the chart. Gateway API `HTTPRoute`, as in `mailcatcher`. |
| D4 | Excluded labels | An excluded label removes **only its own folder copy**. If *all* of a message's labels are excluded, the message is **not copied** (never falls through to Archive). |
| D5 | Dedup | `X-Gmail-Archive-ID` header + **per-folder in-memory index** built per session (§6.2). No PG success index. |
| D6 | Flags | `UNREAD` absent → `\Seen`; `STARRED` → `\Flagged` (no folder); `IMPORTANT` and `CATEGORY_*` ignored (no folder) by default; all overridable. |
| D7 | Google OAuth | Personal `@gmail.com`; the user's own GCP project, consent screen published **"In production" (unverified)** so refresh tokens do not expire after 7 days. Setup wizard + doc required (§4.1). |
| D8 | Process model | One image, `--mode all\|web\|worker`, **default `all`**. Chart deploys one Deployment by default; optional split via values. |
| D9 | Job queue | **In-house** `SKIP LOCKED` queue; no River. |
| D10 | Notifications | Webhook notifications (reauth needed, repeated session failure) **ship in v1**. |
| D11 | Font | **Schibsted Grotesk** (UI); mono only for identifiers. |
| D12 | Lifetime counter | **None.** Only per-session and 14-day activity are shown. |

Corrections to `FIRST_SPECS.md` found during review:

- Vuetify version was inconsistent (§2 said 4, §10 said 3). **Vuetify 4** everywhere.
- Spec had both a global `/settings` and per-account settings, and a single global `/sync`. Everything is now **per account** (`/accounts/{id}/…`) plus a small global settings object (§11).
- Destination IMAP settings appeared in both `destinations` and the UI "Settings → IMAP". Destinations are first-class objects, referenced by accounts; UI manages them under **Destinations**.
- "Checkpoint saved after the batch" was ambiguous for initial sync. See §4.3 (history ID captured **before** enumeration).
- The `X-Gmail-Archive-ID` dedup check via per-message `SEARCH` does not scale; replaced by a bulk header index (§6.2).
- Gmail `404` on history needs a bounded, cheap reconcile, not a full rescan (§4.4).
- Progress from a separate worker pod to a web pod was unspecified (§8.2).
- Cancellation, multi-replica locking and job durability are specified concretely (§12).

---

## 1. Principles

1. One-way, additive replication: Gmail → Dovecot. Never modify or delete source (read-only scope) and never delete destination messages automatically.
2. Preserve raw RFC 5322 content and Gmail `internalDate`. The only permitted modification is a single prepended `X-Gmail-Archive-ID` header.
3. Gmail labels → IMAP folders; multi-label messages are copied to each folder.
4. **No permanent per-message success record in PostgreSQL.** Only failures are stored per message.
5. Incremental sync via Gmail History API; recoverable after crashes at any point.
6. Dovecot (via the header index) is the source of truth for "already delivered"; PostgreSQL holds config, checkpoints, sessions, failures. Losing PG degrades to a rescan that dedups safely — it never causes loss or duplicates.
7. Management UI is a core deliverable; no YAML/SQL needed for normal operations.
8. No Kafka/Redis/Elasticsearch. Postgres is the only stateful dependency.

---

## 2. Technology stack

Aligned with `/Users/taliamarine/repos/mailcatcher` to reuse CI, chart and conventions.

| Concern | Choice | Notes |
|---|---|---|
| Language | Go 1.27 | `CGO_ENABLED=0`, distroless static nonroot |
| HTTP | `gin` + **`huma/v2`** | typed handlers, validation, **OpenAPI generated from code** at `/openapi.json`, docs at `/docs` |
| DB | PostgreSQL 15+, `pgx/v5` (pool) | typed queries via **`sqlc`** (compile-time checked SQL) |
| Migrations | `golang-migrate`, embedded, run at startup under advisory lock (+ optional Helm hook Job) | |
| Job queue | In-house on PG: `FOR UPDATE SKIP LOCKED` + lease/heartbeat (§12) (D9) | |
| Gmail | `google.golang.org/api/gmail/v1` + `golang.org/x/oauth2` | |
| IMAP | `emersion/go-imap/v2` (client) | v2 beta is already used in mailcatcher |
| MIME | `emersion/go-message` (only for header prepend/inspection) | raw bytes are never re-serialised |
| Rate limiting | `golang.org/x/time/rate` | Gmail quota-unit budget (§4.5) |
| Logging | `log/slog` JSON | |
| Metrics | `prometheus/client_golang` | `/metrics` |
| Tracing (optional) | OpenTelemetry OTLP, off by default | |
| Crypto | AES-256-GCM, key id in ciphertext for rotation (§14) | stdlib only |
| Frontend | Vue 3.5, Vite 8, TypeScript, Pinia, vue-router, **Vuetify 4**, `orval` generated API client from OpenAPI | same toolchain as mailcatcher |
| Live updates | **Server-Sent Events** (+ `Last-Event-ID` resume) | simpler than WebSocket, proxy friendly |
| Tests | `go test -race`, `httptest` fake Gmail, **testcontainers-go** for Postgres + Dovecot | no docker-compose required |
| Packaging | multi-stage Dockerfile as in mailcatcher, `linux/amd64,arm64` | |
| Deploy | Helm chart, ArgoCD | |

Project layout:

```
cmd/gmail-archive-sync/       main, flags (--mode)
internal/
  api/          huma routes, SSE, middleware
  auth/         OIDC, sessions, CSRF/origin checks
  config/       env config (infra only)
  crypto/       envelope encrypt/decrypt, key ring
  db/           sqlc output, migrations/, store interface
  gmail/        client, rate limiter, history, labels, message fetch
  imap/         client pool, folder mgmt, dedup index, append
  mapping/      label → folder rules (pure, heavily unit-tested)
  sync/         engine: planner, workers, checkpointing, reconciliation
  jobs/         queue, scheduler, leases, advisory-lock helpers
  failures/     failure store, classification, backoff
  retention/    pruning
  metrics/
web/            Vue app
chart/          Helm chart
.github/workflows/
```

---

## 3. Architecture

```
 Browser ──HTTPS(OIDC)──► Gateway ──► [web]  REST + SSE ─┐
                                                          │ PostgreSQL
 [worker]  scheduler + job runner ◄───── jobs/leases ─────┤  (config, checkpoints,
     │   (same binary, --mode all|web|worker)             │   sessions, failures,
     ├─► Gmail API (HTTPS, OAuth2, readonly)              │   LISTEN/NOTIFY progress)
     └─► Dovecot (IMAP over TLS, APPEND)                  ┘
```

- `--mode web`: HTTP/SSE only. `--mode worker`: scheduler + job runner only. `--mode all` (default): both in one process.
- web and worker communicate **only through PostgreSQL** (jobs table, session rows, `NOTIFY`). This makes `all` and the split identical in behaviour.

---

## 4. Gmail integration

### 4.1 OAuth

- Scope: `https://www.googleapis.com/auth/gmail.readonly` only. Access type `offline`, `prompt=consent` on (re)authorisation to guarantee a refresh token.
- Each user supplies **their own OAuth client** (ID + secret) from their own GCP project. Stored once in global settings (secret encrypted), not per account. Redirect URI: `<BASE_URL>/oauth/callback`.
- **Setup wizard** in the UI (first-run, and reachable from Settings) walks through: create GCP project → enable Gmail API → consent screen → **publish to "In production"** (otherwise refresh tokens die after 7 days; the unverified-app warning is expected, 100-user cap is irrelevant) → create *Web application* credentials → paste ID/secret. The wizard shows the exact redirect URI to register.
- State parameter: random, bound to the session cookie, single use, 10-minute TTL (CSRF). PKCE also used for the Google flow.
- Token handling: refresh token encrypted at rest; access token memory-only, refreshed automatically. `invalid_grant` → account status `needs_reauth`, sync paused for that account, UI banner + `Reauthorise` action, metric + (optional) webhook notification (§16).
- Tokens are never returned by the API or logged.

### 4.2 Operations used

`users.getProfile`, `users.labels.list`/`get`, `users.messages.list` (IDs only, `labelIds`/`q` filters, pagination), `users.messages.get?format=raw` (+ `labelIds`, `internalDate` via `format=minimal` fetch **or** `raw` response fields — `raw` response already returns `labelIds`, `internalDate`, `threadId`), `users.history.list` (`historyTypes=messageAdded,labelAdded`).

Gmail batch endpoint is **not** used (per-call quota still applies; simpler error handling with a bounded worker pool).

### 4.3 Initial synchronisation (resumable)

1. `getProfile` → record **`start_history_id` before enumerating anything**. This is the checkpoint that incremental sync will continue from, so changes during a long import are not lost (they are re-seen via history; dedup makes re-processing harmless).
2. Load labels; resolve plan per §5.
3. Enumerate with `messages.list` (page size 500) using `q=after:<initial_sync_after>` when configured, and `labelIds`/`-label` filters where the plan allows narrowing. Persist `initial_sync_cursor` (`pageToken` + list query hash) after each fully processed page.
4. For each page: bounded workers fetch raw, compute destination folders, dedup, APPEND.
5. Failures → `failed_messages` (§7). A page counts as handled when every message is either delivered, skipped-as-existing, or recorded as failure.
6. On completion set `initial_sync_completed`, `last_successful_history_id = start_history_id`.
7. Resume after crash/cancel from `initial_sync_cursor`; if the query changed (settings edited), cursor is discarded and the list restarts (safe thanks to dedup).

### 4.4 Incremental synchronisation

1. Read `last_successful_history_id`; page through `history.list`.
2. Collect `messagesAdded` and `labelsAdded` (label **removals and deletions are ignored**, additive policy). De-duplicate message IDs per run.
3. For each distinct message: fetch raw (needed for new messages and for label-added copies into a folder it is not in), plan folders, deliver.
4. Advance the checkpoint to the **response `historyId` of the last fully handled page**, in the same transaction that updates session counters. Never advance past a page that contains an unhandled message (unhandled = neither delivered nor persisted as failure). If PG is unavailable when recording a failure, the run aborts without advancing.
5. **History 404 (expired)**: run a *bounded reconcile* instead of a full rescan: list with `q=after:<last_successful_sync_at − 2 days margin>` (Gmail search uses day granularity), apply the normal pipeline (dedup absorbs overlap), then set the checkpoint to a fresh `getProfile.historyId` taken **before** the reconcile list. If `last_successful_sync_at` is unknown, fall back to a full initial-style scan. Session is typed `reconcile` and an event is recorded.
6. The scheduler interval default (2 h) is far below Gmail's history retention (≈ a week or more), but the 404 path must be tested.

### 4.5 Quota, concurrency, retries

- Gmail per-user limit ≈ 250 quota units/s; `messages.get`=5, `list`=5, `history.list`=2. A token-bucket limiter targets a configurable fraction (default 60 % ≈ 30 msg/s ceiling) shared per account.
- Worker pool size configurable (default 8 per account); a message's raw bytes are held in memory only while processed. Max message size 50 MB (Gmail cap ≈ 25 MB attachments → ~35 MB encoded); memory budget = `workers × 50 MB`, documented in chart resources.
- `429`/`403 rateLimitExceeded/userRateLimitExceeded` and `5xx`: exponential backoff with jitter, honour `Retry-After`; global slowdown of the limiter on 429 (AIMD). Auth errors are permanent for the run.
- `404` on a single message (deleted since listing) → category `gmail_message_unavailable`, recorded as `ignored` automatically (not retried).
- Optional later: Gmail push (`users.watch` + Pub/Sub) to replace polling. **Out of scope for v1** (needs a public endpoint and a GCP topic); the engine's "sync now" job is the seam.

---

## 5. Labels and folders

### 5.1 Label model

Labels are loaded per account and cached in PG (`account_labels`: id, name, type system/user, last_seen) for the UI and for rename tracking. Rules are keyed by **label ID** (stable); names are display only. A renamed Gmail label keeps its rule and **maps to its new name's folder from then on** (old folder retained, additive).

### 5.2 Rule evaluation (pure function, `internal/mapping`)

Input: message label IDs + account settings. Output: ordered set of `(folder, flags)`.

1. Each label → candidate folder via, in order: explicit `label_mapping` entry, fixed system mapping (`INBOX`→inbox folder, `SENT`→sent folder), else the label name (Gmail `/` hierarchy preserved, translated to the server's IMAP hierarchy delimiter discovered via `LIST`).
2. System labels with no folder (`STARRED`, `UNREAD`, `IMPORTANT`, `CATEGORY_*`, `CHAT`) produce no folder; `STARRED`/`UNREAD` drive flags (D6).
3. Default-excluded: `SPAM`, `TRASH`, `DRAFT` (removing a default exclusion is allowed). Exclusion matchers: label ID, exact name, case-insensitive name option, prefix on hierarchical names (`Newsletters/`).
4. **Excluded label drops only its own folder (D4).** If at least one label was present but **all** were excluded/non-folder-producing *and the exclusion caused it*, the message is **not copied**. If the message has no excluded label and no folder-producing label (pure archived, unlabelled) → `archive_folder`.
   - Precise rule: `copy_set = folders from non-excluded labels`; if `copy_set` empty and message has any excluded label → skip; if `copy_set` empty otherwise → `{archive_folder}`.
   - `SENT`-only messages go to the sent folder, never to Archive.
   - A message in `INBOX` goes to INBOX in addition to its user label folders.
5. Folder-name sanitisation: reserved characters, delimiter conflicts, leading/trailing spaces, modified UTF-7 encoding for non-ASCII (handled by the IMAP library), max depth/length guard.
6. **Collision handling**: two labels mapping to the same folder (case-insensitive servers, `A/B` vs `A.B`) are merged into one target; a **preview** in the UI shows resulting folder list and flags collisions before saving.
7. Mapping changes affect only future deliveries; no backfill unless the user triggers **"Re-scan"** (a bounded `reconcile` of a chosen date range or label).

### 5.3 Folder creation

`LIST` once per session to cache existing folders + delimiter; `CREATE` missing (and parents), then `SUBSCRIBE` (configurable, default on). Folder creation counted in session stats.

---

## 6. Synchronisation and deduplication

### 6.1 No success index

Unchanged from the original spec: successful deliveries have **no** per-message PG row. Only counters, checkpoints and failures.

### 6.2 Destination dedup (D5)

- Header: `X-Gmail-Archive-ID: <account_uuid>.<gmail_message_id>` prepended **once**, using CRLF, before the original headers, with the rest of the bytes untouched. (Account UUID prefix keeps two Gmail accounts delivered into one mailbox from colliding.)
- **Per-folder index**: the first time a folder is touched in a session, `UID FETCH 1:* (BODY.PEEK[HEADER.FIELDS (X-Gmail-Archive-ID)])` (chunked) builds an in-memory `set` for that folder. It is kept for the session, updated on every APPEND, and discarded on `UIDVALIDITY` change. No PG storage.
  - Cost: ~60 bytes × messages; 1 M messages ≈ 100 MB worst case across folders — acceptable, bounded by a per-session memory cap; beyond the cap fall back to `UID SEARCH HEADER X-Gmail-Archive-ID <id>` per message for that folder.
  - Only index folders that will actually receive messages in this session.
- Order of operations per message/folder: *index check → APPEND with original `INTERNALDATE` and flags → add to index*. Index membership means "already delivered".
- Crash recovery (APPEND done, checkpoint not saved): next run's index contains the ID → skipped and counted as skipped, not failed.
- Concurrent appends to one folder from multiple workers are serialised per folder connection (a small pool of IMAP connections, one in-flight APPEND per connection).
- Dovecot tuning documented: enable `mail_cache_fields = hdr.x-gmail-archive-id` for fast header fetch/search.
- A user-moved/deleted message in Dovecot is re-delivered by a rescan (documented trade-off of "Dovecot is truth").

### 6.3 Additive policy

Never propagate Gmail deletions or label removals. `propagate_deletions` setting is **removed** (not just defaulted false) to make the guarantee structural: there is no delete code path against IMAP or Gmail.

---

## 7. Failures

Only per-message persistent store. One row per `(account_id, gmail_message_id)`, upserted.

- Fields as in `FIRST_SPECS.md` §7.1/§9.7 (no body or attachments; subject/sender/recipients metadata kept). `error_history` JSONB (last 10 entries: time, category, message, session) added for the details view.
- Statuses: `pending`, `retrying`, `ignored`; resolved rows are **deleted** and counted in `recovered_count`.
- Categories (enum): `gmail_rate_limit`, `gmail_auth`, `gmail_fetch`, `gmail_message_unavailable`, `imap_auth`, `imap_connection`, `imap_folder`, `imap_append`, `parse`, `config`, `unknown`; each has a UI description and `retryable` flag.
- **Partial delivery**: `completed_folders` / `failed_folders` stored; retries only attempt `failed_folders` (and still dedup-check).
- Auto-retry: at the start of each sync session, failures with `next_retry_at <= now()` and retryable category are processed first. Backoff: `min(5 min × 2^attempts, 24 h)` with jitter, cap 10 attempts, then status `pending` with `retry_exhausted=true` (manual retry still allowed). Permanent categories (`gmail_auth`, `config`, `imap_auth`) are not auto-retried; they are cleared by fixing config and "retry all".
- Manual: retry one / selected / all pending / filtered set; ignore; un-ignore; clear ignored.
- Retry of a failure runs as a `retry` session (same engine, targeted message list).

---

## 8. Sessions and progress

### 8.1 Session model

As in the original (§8.1, §9.5) with additions: `sync_type` ∈ `initial | incremental | reconcile | retry`; `cancel_requested_at`; `worker_id`; `heartbeat_at`; `bytes_copied`.
`trigger_type` ∈ `scheduled | manual | initial | retry | recovery`. Status ∈ `queued | running | completed | completed_with_errors | failed | cancelled | abandoned`.

### 8.2 Live progress across pods

- The runner updates in-memory counters every message, and flushes to the `sync_sessions` row (and phase/current folder) **at most once per second** plus on phase change, then issues `pg_notify('sync_progress', session_id)`.
- Each web pod `LISTEN`s and fans out to SSE clients; `GET /api/v1/accounts/{id}/sync/events` (and an all-accounts stream for the dashboard) sends `progress`, `phase`, `session_finished` events with `id:` for `Last-Event-ID` resume; on resume or missed notification the server sends a fresh snapshot. A 15 s keep-alive comment prevents idle proxy timeouts.
- Phases: connecting, refreshing credentials, loading labels, reading history, enumerating, fetching, delivering, processing failures, saving checkpoint, finalising. Percentage is shown only when the total is known (initial sync with a counted list, or history batch); otherwise `indeterminate`.
- ETA: shown only after ≥ 30 s and ≥ 50 messages, from an exponentially weighted throughput.

### 8.3 Events and retention

Aggregated events only, batched (≤ 1 write/5 s, e.g. "Copied 250 messages to Projects"). Defaults: summaries 365 d, events 90 d, resolved failures deleted, ignored failures kept until cleared. Retention job runs daily in the worker, batched `DELETE`s.

---

## 9. PostgreSQL data model

UUID PKs (`uuidv7()` generated app-side for index locality), `timestamptz` everywhere, `created_at/updated_at`.

| Table | Purpose / key columns |
|---|---|
| `app_settings` (single row) | oauth_client_id, `enc` oauth_client_secret, retention, default limits, notification webhook (encrypted) |
| `destinations` | name, host, port, `tls_mode` (`implicit`/`starttls`/`none`), `tls_skip_verify` (explicit, warned), username, `enc` password, folder delimiter cache, enabled |
| `accounts` | name, gmail_address, `enc` refresh_token, `key_id`, status (`ok`/`needs_reauth`/`disabled`/`error`), last_connection_check, destination_id FK |
| `account_sync_settings` | account_id PK, schedule_interval, enabled, initial_sync_after, archive/sent/inbox folder, excluded_labels JSONB (matchers), label_mapping JSONB, flag_mapping JSONB, subscribe_folders, worker_count, rate_fraction, batch_size, retry policy |
| `account_labels` | account_id, label_id, name, type, last_seen_at |
| `sync_state` | account_id PK, last_successful_history_id, last_successful_sync_at, start_history_id, initial_sync_cursor JSONB, initial_sync_completed |
| `sync_sessions` | as §8.1; indexes `(account_id, started_at desc)`, `(status)`, partial unique index `(account_id) WHERE status IN ('queued','running')` = **one active session per account enforced by the DB** |
| `sync_session_events` | as original |
| `failed_messages` | as §7, `UNIQUE(account_id, gmail_message_id)`, indexes on `(status, next_retry_at)`, `(account_id, last_failed_at)` |
| `jobs` | id, account_id, kind, payload, run_at, lease_until, leased_by, attempts, status |
| `web_sessions` | optional; default is stateless signed cookie so none |

Notes:
- `enc` columns store `key_id || nonce || ciphertext` (AES-256-GCM, AAD = table+column+row id).
- Large-write friendliness: sessions/events are the only high-churn tables; failed_messages is small.
- No `success` table. A CI test greps the migrations for forbidden per-message success tables is **not** needed; an integration test asserts row counts after a 1 000-message import.

---

## 10. Web interface

### 10.1 Information architecture

Left rail (collapsible to icons; bottom sheet on mobile):

1. **Overview** (dashboard)
2. **Accounts** (list → account detail with tabs: *Connection*, *Labels & folders*, *Schedule*, *Advanced*)
3. **Destinations**
4. **Sync** (live view for the selected account; start / cancel)
5. **History** (sessions → session detail)
6. **Failures**
7. **System** (health, version, uptime, probes, queue, global settings incl. Google OAuth client wizard, retention)

First-run: if no OAuth client is configured, Overview shows the Google setup wizard as the single call to action.

### 10.2 Pages (behaviour)

- **Overview**: per-account status cards (Gmail auth, Dovecot reachability, last success, next run, active session mini-progress), failures needing attention, 14-day activity (copied/skipped/failed per day, from `sync_sessions`; **no lifetime message total** shown unless a separate counter is introduced), recent sessions, recent errors. Actions: *Sync now*, *Retry failed*.
- **Accounts**: add account (name → authorise with Google → pick destination). Statuses with clear remedies (e.g. "Google access expired — Reauthorise"). Test connection tests Gmail and IMAP separately and reports each.
- **Labels & folders**: two-pane list: Gmail labels (system/user badge, message counts from `labels.get`) ↔ resulting IMAP folder, inline edit of mapping and exclusion, **live preview of the folder tree and collisions**, "Refresh labels from Gmail". Applying a change that alters where mail lands shows a confirmation summarising effect (additive only; nothing is deleted).
- **Sync**: the live transfer view (§10.4), Start/Cancel, phase list, counters, current folder, elapsed, ETA when reliable, indeterminate state indicated explicitly. Cancel = graceful (finish in-flight messages, save checkpoint).
- **History**: server-side paginated, filterable (date range, account, status, trigger, type); detail page with stats, aggregated-event timeline, error summary, start/end history IDs, linked failures.
- **Failures**: table (subject, sender, date, folders done/failed, category with human description, attempts, last attempt, status), bulk select, filters, retry/ignore/un-ignore, detail drawer with error history and per-folder delivery state. **No body content** anywhere.
- **System**: build version, DB/Gmail/IMAP checks, scheduler and queue state, active leases, uptime.

### 10.3 Frontend engineering

- API client generated from OpenAPI with `orval` (CI fails if the generated client is stale).
- Pinia stores per domain; one SSE connection manager with auto-reconnect (exponential backoff, `Last-Event-ID`), exposed via a composable.
- Server-side pagination/sorting/filtering via cursor (`?cursor=`) for sessions and failures; `vue-router` routes are deep-linkable with filters in the query string.
- Accessibility: WCAG 2.2 AA, visible focus, keyboard-operable tables and drawer, `prefers-reduced-motion` honoured, live regions for progress announcements throttled.
- Light + dark themes from one token set; responsive to 360 px.
- Bundle budget: < 350 KB gz initial JS (Vuetify tree-shaken via `vite-plugin-vuetify`; lazy-load routes and charts).
- Embedded into the Go binary (`embed`) with SPA fallback and long-cache hashed assets.

### 10.4 Visual design direction (from the frontend-design review)

**Concept: "the mailroom."** The product moves mail between two places; the UI should feel like a calm, precise sorting desk, not a generic SaaS dashboard. One memorable element, everything else quiet.

- **Signature element — the transfer rail** (Sync page, and a compact version on Overview cards): a horizontal track from a *Gmail* end to a *Dovecot* end with one lane per active destination folder. Delivered messages settle as ticks at the folder lane end; the lane for the current folder is highlighted. When progress is indeterminate (enumerating) the rail shows a travelling marker instead of a percentage, making "unknown total" visible honestly. Errors drop out of the lane into a small "set aside" tray linking to Failures. Pure CSS/SVG, throttled to ~4 updates/s; static (no animation) under reduced-motion.
- **Palette** (tokens; verify AA contrast in both themes):
  - `paper` `#EDEFF1` (cool grey page), `surface` `#FAFBFB`, `ink` `#18222E` (text, avoid pure black)
  - `rail` `#2D6B6A` deep teal = primary/"Dovecot side"
  - `origin` `#B8522E`-adjacent is **avoided** (reads as a generic clay accent); use `gmail-ink` `#4A5FA3` slate-indigo as the secondary "source" colour
  - `caution` `#B7860B` amber (needs attention), `fault` `#B3372F` red (failed), `settled` `#3C7A4B` green (success), used only for state, never decoration
  - Dark theme: `paper` `#12171D`, `surface` `#1A222B`, text `#E6EAEE`, same hues lightened.
- **Type**: one family for the UI, a second only for numbers/IDs. UI: *Schibsted Grotesk* (or *Instrument Sans*) 400/500/600; data and IDs: *JetBrains Mono* **not** used as decorative micro-labels — only for genuine identifiers (message IDs, history IDs, folder paths). Tabular numerals everywhere in counters. Scale 12/14/16/20/28; body 14–16, line length < 75ch. No all-caps labels, no tracked eyebrows; sentence-case headings.
- **Layout**: left-aligned content, 8 px grid, a fixed rail + a content column max 1200 px. Density: compact tables (44 px rows). Cards only where content is genuinely discrete (account status); lists and tables elsewhere; **one** corner radius scale (4 px controls, 8 px containers), borders over shadows, almost no shadow.
- **Status language**: icon + word + colour (never colour alone). Plain verbs on buttons: *Sync now*, *Cancel sync*, *Retry*, *Ignore*, *Reauthorise Google*. Same noun everywhere (a "session" is always a "sync session"; failed items are always "failures"). Error copy states what happened and the next step, e.g. "Google access was revoked. Reauthorise to resume syncing this account."
- **Motion**: one orchestrated moment only — the rail. Otherwise functional transitions (drawer open, row retry result) ≤ 150 ms; no entrance animations on every section; no hover lifts.
- **Empty states** are invitations ("No accounts yet. Connect a Gmail account to start archiving.").

---

## 11. REST API (`/api/v1`, OpenAPI-generated by huma)

| Method | Path | Purpose |
|---|---|---|
| GET/PUT | `/settings` | global settings (OAuth client, retention); secrets write-only |
| GET/POST | `/destinations` · GET/PUT/DELETE `/destinations/{id}` · POST `/destinations/{id}/test` | |
| GET/POST | `/accounts` · GET/PUT/DELETE `/accounts/{id}` | |
| POST | `/accounts/{id}/oauth/start` | returns auth URL (state bound to session) |
| GET | `/oauth/callback` | (unversioned path) completes authorisation |
| POST | `/accounts/{id}/test` | Gmail and IMAP checks, separate results |
| GET/PUT | `/accounts/{id}/sync-settings` | |
| GET | `/accounts/{id}/labels` · POST `/accounts/{id}/labels/refresh` · POST `/accounts/{id}/folder-preview` | |
| POST | `/accounts/{id}/sync` (`{type: incremental\|reconcile, after?, label?}`) | enqueue |
| POST | `/accounts/{id}/sync/cancel` | graceful cancel |
| GET | `/accounts/{id}/sync/current` | snapshot |
| GET | `/events` (SSE, optional `?account=`) | progress stream |
| GET | `/sessions` (cursor, filters) · `/sessions/{id}` · `/sessions/{id}/events` | |
| GET | `/failures` · `/failures/{id}` | |
| POST | `/failures/{id}/retry` · `/failures/{id}/ignore` · `/failures/retry` (ids \| filter \| all) · `/failures/clear-ignored` | |
| GET | `/system/status` | |
| GET | `/health/live` · `/health/ready` · `/metrics` | unauthenticated; metrics optionally behind bearer |

Conventions: RFC 9457 problem+json errors; idempotent `POST /sync` (returns existing queued/running session); all mutating requests require same-origin (Origin/Sec-Fetch-Site check) + session cookie `SameSite=Lax`, `HttpOnly`, `Secure`.

---

## 12. Scheduling, concurrency, jobs

- **Scheduler** (worker side): each tick (30 s) enqueues `incremental` sessions for enabled accounts whose `last_started + interval <= now`; jitter ±10 %. Leader election not required: enqueue is idempotent through the partial unique index on active sessions.
- **Runner**: claims jobs with `FOR UPDATE SKIP LOCKED`, sets `lease_until = now()+60 s`, heartbeats every 15 s. Expired lease ⇒ session marked `abandoned` and the work is re-queued (type `recovery`) — resumes from checkpoint/cursor, dedup makes it safe.
- **At most one active session per account** guaranteed by the DB partial unique index (not only by a lock); additionally a session-level advisory lock on a dedicated connection protects the Gmail/IMAP side effects. Different accounts run in parallel up to `MAX_CONCURRENT_ACCOUNTS`.
- Cancellation: `cancel_requested_at` is polled between messages; in-flight APPENDs complete; checkpoint saved; status `cancelled`.
- Graceful shutdown on SIGTERM: stop claiming, let in-flight message finish (bounded by `terminationGracePeriodSeconds`), release lease, flush counters.
- Migrations: executed by one replica under a PG advisory lock at startup; readiness stays false until migrated.

---

## 13. Reliability guarantees

1. No code path deletes or modifies Gmail data (scope `readonly`) or Dovecot messages.
2. No per-message success rows in PG.
3. One failure row per account/message.
4. Duplicate-free delivery via the header index, scoped per folder.
5. Partial multi-folder delivery is resumable.
6. Checkpoints survive restart and never pass unhandled messages.
7. Expired history ⇒ bounded reconcile.
8. One failing message never blocks the batch.
9. PG loss ⇒ re-run from scratch is safe (dedup), only the failure list/history are lost; documented in backup guidance (backup PG; Dovecot mail is backed up separately by the operator).
10. Clock/ordering: `INTERNALDATE` is set from Gmail `internalDate`, so folder ordering matches Gmail regardless of delivery order.

---

## 14. Security

- Secrets (OAuth client secret, refresh tokens, IMAP passwords, webhook URLs) AES-256-GCM encrypted with a key ring from a Kubernetes Secret (`ENCRYPTION_KEYS=id1:base64,id2:base64`, first = active). Rotation: decrypt with any key, re-encrypt lazily and via an admin "rotate" job. If no key is provided, the chart/app generates one in a Secret on first start (same pattern as mailcatcher's `SESSION_KEY_SECRET_NAME`); documented that losing it means re-entering credentials.
- OIDC sign-in (PKCE S256 + nonce), 24 h signed cookie; optional `OIDC_ALLOWED_GROUPS`/email allow-list claim check. State-changing requests and SSE reject cross-origin.
- CSP strict (`default-src 'self'`), no inline scripts; no email bodies ever rendered.
- IMAP TLS verification on by default; `tls_skip_verify` per destination requires explicit opt-in and shows a persistent warning.
- SSRF guard on the destination host field is **not** applied (internal Dovecot is the intended target) but the endpoint is admin-only and rate-limited.
- Log redaction: structured fields allow-list; never log tokens, passwords, bodies, or full subjects (subject only in the failures table).
- Container: non-root (65532), read-only root FS, no capabilities, `seccompProfile: RuntimeDefault`, NetworkPolicy (egress: DNS, PG, Dovecot, `*.googleapis.com`/`oauth2.googleapis.com`/`accounts.google.com` via configurable CIDR/FQDN policy, OIDC issuer).
- Supply chain: pinned base images by digest, `govulncheck` + `npm audit` + Trivy in CI, SBOM and build provenance attached to the image, images signed with cosign (keyless OIDC).

---

## 15. Deployment (Helm)

`chart/` modelled on `mailcatcher/chart` (same helpers, `values.yaml` style, `httproute.yaml`, ServiceAccount, no secret material rendered by Helm):

- Deployment (`mode: all` default) with optional `worker.enabled=true` which switches the main Deployment to `--mode web` (replicas configurable) and adds a worker Deployment (replicas 1–N safe).
- Service (ClusterIP), Gateway API `HTTPRoute` (values like mailcatcher: `gateway.host`, `parentRefs`).
- `values`: `database.existingSecret` (DSN) or `database.host/...`; `oidc.*`; `baseUrl`; `encryption.existingSecret`; `resources`; `securityContext`; `networkPolicy.enabled`; `serviceMonitor.enabled`; `podDisruptionBudget`; `extraEnv`.
- Probes: `/health/live`, `/health/ready` (DB + migrations), startup probe for migrations.
- Migrations run in-process under advisory lock; optional pre-upgrade hook Job.
- Provide `examples/argocd-application.yaml` (OCI chart source, `syncPolicy.automated`, `ServerSideApply`).
- `helm lint`/`template` matrix in CI (default, worker split, networkPolicy, serviceMonitor, missing-required failure cases — as mailcatcher's CI does for IMAP hostname).
- Runtime settings live in the UI/PG; chart carries only infrastructure config and secret references.

---

## 16. Observability

- JSON logs (`LOG_LEVEL`, `LOG_FORMAT`).
- Prometheus metrics (prefix `gmailsync_`), labels limited to `account` (UUID or name) and low-cardinality enums:
  `sessions_total{type,status}`, `messages_discovered_total`, `messages_copied_total`, `messages_skipped_total`, `messages_failed_total{category}`, `gmail_api_requests_total{method,code}`, `gmail_quota_throttle_seconds_total`, `imap_operations_total{op,result}`, `session_duration_seconds` (histogram), `sync_active`, `pending_failures`, `oauth_needs_reauth`, `last_success_timestamp_seconds{account}`, `queue_depth`.
- Suggested alerts (shipped in chart as optional `PrometheusRule`): no success in 3× interval, `oauth_needs_reauth > 0`, failures growing.
- Notification webhook (generic JSON POST; ntfy/Discord/Slack-compatible) for reauth-needed and repeated session failure — in v1, configured in System settings, off until a URL is set.
- Optional OTel tracing.

---

## 17. Testing

- **Unit**: mapping/exclusion/collision (table-driven, the most important suite), header prepend (byte-exact fidelity, idempotence), crypto round-trip + rotation, backoff, classifier.
- **Fake Gmail** (`httptest`): pagination, history pagination, 404 expiry, 429/`Retry-After`, deleted message, large message.
- **Integration (testcontainers-go)**: real Postgres + real Dovecot image: multi-label delivery, Archive/Sent rules, folder creation + delimiter handling, INTERNALDATE preservation, MIME byte fidelity (compare bytes after removing the one header), dedup after simulated crash (kill between APPEND and checkpoint), partial delivery + retry, UIDVALIDITY change, cancellation, two concurrent workers on one account (second blocked by index), lease expiry recovery, row-count assertion that a 1 000-message import adds **zero** per-message rows.
- **Frontend**: Vitest component tests (rail, tables), Playwright smoke (login-disabled mode) against a stack with fake Gmail + Dovecot; axe accessibility checks on every route.
- **Chart**: `helm lint`, `helm template` matrix, `kubeconform`.
- CI also runs `go vet`, `gofmt`, `staticcheck`/`golangci-lint`, `govulncheck`, orval/sqlc "generated files up to date" checks.

---

## 18. CI/CD (reuse from `/Users/taliamarine/repos/mailcatcher/.github/workflows`)

Copy and adapt the four workflows; rename image/chart (`mailcatcher` → `gmail-archive-sync`):

| Workflow | Reuse | Adaptation |
|---|---|---|
| `ci.yml` | backend (build/vet/gofmt/`test -race`), frontend (`npm ci && npm run build`), helm (lint + template matrix), docker build with GHA cache | add Postgres/Docker-capable job for integration tests (testcontainers works on `ubuntu-latest`), `govulncheck`, `golangci-lint`, `sqlc diff`, orval diff, vitest, Playwright, kubeconform; split fast/slow jobs |
| `docker-publish.yml` | tag-triggered + `workflow_dispatch`, `prepare` computing semver tags, multi-arch buildx push to `ghcr.io/<owner>/gmail-archive-sync`, Helm OCI push after verifying `Chart.yaml` version == tag | add `provenance: mode=max`, `sbom: true`, cosign sign of image and chart, Trivy scan (fail on HIGH/CRITICAL fixable); fix chart tarball name `gmail-archive-sync-<v>.tgz` |
| `release-please.yml` | Conventional Commits release PR, dispatch publish via `gh workflow run` | config bumps `chart/Chart.yaml` (version, appVersion) and `web/package.json` |
| `pinact-check.yml` | every action pinned to SHA | unchanged |

Also add **Dependabot** (gomod, npm, github-actions, docker) with grouped updates; keep pinact SHA pins compatible (Dependabot can update SHA-pinned actions).
`release-please-config.json`, `.release-please-manifest.json`, `Justfile`, `DEVELOPER.md`, `CHANGELOG.md` follow the mailcatcher pattern.

---

## 19. Phases

1. **Foundations**: repo scaffold from mailcatcher conventions, config, PG + migrations + sqlc, crypto, OIDC auth, huma/OpenAPI, CI + pinact + Helm skeleton (chart renders, image builds from day one).
2. **Engine**: Gmail client + rate limiter, IMAP client + header index, mapping package, initial sync, history sync, failures, sessions, jobs/scheduler/leases, reconcile. Integration tests with Dovecot.
3. **UI**: shell + Overview, Accounts + Google wizard + OAuth, Labels & folders + preview, Sync (rail) + SSE, History, Failures, System.
4. **Hardening**: metrics/alerts, NetworkPolicy, cosign/SBOM, docs (Google setup, Dovecot tuning, backup/restore, key rotation), ArgoCD example, load test with ≥ 200 k synthetic messages.

---

## 20. Acceptance criteria

Original list (§19 of `FIRST_SPECS.md`) plus:

- A Gmail account can be authorised from the UI using the user's own OAuth client; refresh tokens survive > 7 days with the app published "In production".
- Label/folder preview shows collisions and excluded labels drop only their own folder; all-excluded messages are not copied; unlabelled non-sent messages go to Archive.
- Killing the process between APPEND and checkpoint produces no duplicate and no loss on restart.
- A forced history `404` completes through bounded reconcile with no duplicates.
- Two replicas in `all` mode never run two sessions for one account (verified in test).
- After importing N messages, PG contains no per-message rows other than failures.
- Live progress seen from a web-only pod while a worker-only pod runs the session.
- `helm install` from the published OCI chart + ArgoCD Application example works on a clean cluster; images and chart are signed and carry SBOM/provenance.

---

## 21. Implementation notes

- Implemented: config, crypto ring, migrations + sqlc, OIDC auth, huma API (all routes of §11, SSE, Google OAuth flow), `internal/mapping`, `internal/gmail` (rate-limited client), `internal/imapdest` (delivery + dedup index), `internal/engine` (initial, incremental, reconcile, retry), `internal/jobs` (runner, scheduler, recovery, retention, webhook), Vue UI, CI, Helm chart.
- Package names differ from the §2 layout: `engine` (not `sync`), `imapdest` (not `imap`), plus `failures`, `clients`, `progress`, `metrics`, `testenv`. See NOTES.md for the full list of implementation decisions and open questions.
- There is no `jobs` table: the queue is `sync_sessions` itself (D9).
- Go history IDs are stored as `bigint`.
- Integration tests carry the `integration` build tag (`go test -tags integration ./...`); with Rancher Desktop set `DOCKER_HOST=unix://$HOME/.rd/docker.sock TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock TESTCONTAINERS_RYUK_DISABLED=true`. `internal/e2e` runs the whole pipeline against a fake Gmail and a real Dovecot and PostgreSQL.

## 22. Open items

None. All review questions are resolved (see §0, D1–D12).
