# Implementation notes: questions, issues, deferred items

Running log while implementing FULL_SPECS.md autonomously. Newest at the bottom of each section.

## Open questions for the owner
1. **Google refresh-token lifetime in "In production, unverified" mode.** Researched in Google's documentation: the only documented time-based expiry is 7 days for status *Testing*; nothing documents an expiry for unverified production apps, but Google does not state the opposite explicitly either. Other documented causes: password change (Gmail scopes), revocation, 6 months unused, 100-token cap per client, admin policy. Mitigation built in: the account stores `authorised_at`, and when Google rejects the token the status message says how long it lived (about 7 days points at Testing). Remaining action for you: authorise once and look at the account after ~8 days; if it needs reauthorisation, the message will say why.
2. **Gmail `history.list` start semantics (inclusive or exclusive of `startHistoryId`).** The engine is correct either way (re-reading a record is harmless thanks to dedup), but it affects how many messages are re-checked per run. Worth observing on the real API.
3. **Should accounts in `needs_reauth` accept manual "retry failures"?** Currently blocked with a reason (API fork). Alternative: allow and let it fail fast.
4. **Excluded-label listing cost.** The initial import lists every message and skips excluded ones after fetching them (SPAM/TRASH are never listed). A large excluded label (for example Newsletters) therefore costs quota. Narrowing the list query with `-label:` would avoid it; deferred because label names with spaces/slashes need careful query escaping. Do you have big excluded labels?
5. **Dovecot layout.** Everything is tested against Dovecot 2.4.5 with Maildir and `/`. If your server uses another layout/separator (for example `.` with Maildir++), run "Test connection" first; the delimiter is detected, not assumed.

## Issues / risks found
- (phase 1) `golang-migrate` pgx5 driver needs a URL-form DATABASE_URL (key=value DSNs are not supported).

## Engine / backend decisions (implementation)
- Package names differ from the spec's layout: `internal/engine` (sync engine), `internal/imapdest` (IMAP delivery), `internal/jobs` (runner, scheduler, notifier), `internal/failures`, `internal/clients`, `internal/progress`, `internal/metrics`, `internal/testenv` (test containers).
- The job queue is the `sync_sessions` table itself (`status='queued'`, `run_at`, lease columns); no separate `jobs` table (D9).
- History checkpointing: intermediate pages save the last *record* ID of the page (the response's `historyId` is the mailbox's current ID, the same on every page, and is only safe after the last page).
- An empty history still saves the checkpoint, so it never ages out.
- Initial import resumes from `sync_state.initial_sync_cursor` ({query, page_token}); the cursor is discarded when the query (date limit) changes. The history ID is captured before enumerating.
- A message that no longer exists in Gmail (or exceeds 64 MiB) becomes an `ignored` failure row and counts as skipped.
- `failed_messages.error_history` entries: `{at, category, message, session_id}`, newest first, max 10.
- Failure retries: automatic ones happen at the start of each session (`DueFailures`, backoff 5 min doubling to 24 h, 10 attempts by default). Permanent categories (gmail_auth, imap_auth, config, parse, gmail_message_unavailable) are never retried automatically.
- Shutdown (SIGTERM) finishes sessions as `abandoned` and enqueues a `recovery` session; after more than 3 abandoned sessions in an hour for one account the requeue stops (protects against crash loops, e.g. out-of-memory).
- Lost lease (another worker took over) leaves the session row untouched.
- Notification webhook fires on `needs_reauth` and when 3 consecutive sessions failed (once, at the streak); body `{event, account, text, message}`, URL stored encrypted.
- Memory: a message is held in memory while processed; peak is roughly `worker_count × message size` (max 64 MiB each). The chart default limit (768 Mi) suits the default 8 workers with typical mail; lower `worker_count` for accounts with huge attachments.
- Per-folder dedup index is rebuilt every session (reads one header per message of folders touched). For very large folders (> 2,000,000 IDs) the engine falls back to a server-side search per message.
- A single account never runs two sessions at once (partial unique index), so the Gmail rate budget is per session.

## Independent review (backend) and what changed
An independent review found the items below; all confirmed ones are fixed and covered by tests.
- Reconcile cutoff after an expired history could skip mail (it used the last time anything succeeded). Now `sync_state.history_checkpoint_at` records when the stored history ID was read and only moves with it; an interrupted initial import keeps `start_history_at`.
- OIDC had no allow-list: `OIDC_ALLOWED_EMAILS` (addresses, `@domain`) and `OIDC_ALLOWED_GROUPS` now restrict sign-in (verified email required). With both empty everyone the identity provider authenticates is let in and a warning is logged at startup. **Please set one in production.**
- The stored IMAP password could be sent to an arbitrary host through "test destination"/"update destination"; reuse now requires the same host, port, username and no weaker TLS.
- Open redirect through control characters in the post-login return URL (fixed).
- A worker that cannot heartbeat now stops by itself after 2/3 of the lease, so a swept session cannot keep appending. The per-account PostgreSQL advisory lock of spec §12 was NOT implemented: the partial unique index plus the lease deadline plus per-folder duplicate checks cover it; revisit if two workers ever overlap in practice.
- A label added after a message was first fetched (on a later history page) is no longer dropped by the dedupe.
- Pending failures are resolved whenever their message is delivered, by any route; a manual "retry" now also retries permanent categories once (`retry_requested`).
- Reauthorising with a different Google account resets the sync state and clears that account's failures.
- Folder indexes have a session-wide budget (4,000,000 IDs) besides the per-folder cap; panics no longer log request headers (session cookie); heartbeat/finish have timeouts.

Not changed (accepted for now):
- Shutdown cancels in-flight work immediately instead of letting the current APPEND finish (safe: dedup recovers; the destination session runs a verify-before-retry).
- The SSE endpoint has no subscriber cap; every subscriber costs one session read per event. Fine for one or a few admins.
- With the destination down, every message of a session is recorded as a failure (and the checkpoint advances) until retries are exhausted (10 attempts); then a manual retry is needed. Only Gmail auth aborts a session.
- When an in-process worker's hub receives both the local publish and the LISTEN notification, SSE clients may get an event twice (harmless).

## Found in the first real-Gmail run
- 125 of 164 messages failed with `imap_folder: CANNOT Character not allowed in mailbox name: '.'`: Gmail labels containing "." (for example "A Processer ...", "Project 1.0") cannot be created on a Dovecot with the default Maildir++ layout, even when the hierarchy separator is "/". Fixed: when CREATE refuses a name, the session tries "." replaced by "_", then all punctuation replaced; the working spelling is used for that folder, found again by later sessions (no repeated CREATE), and reported as a `folders_renamed` session event. Tested with fakes and a real Dovecot (Maildir++). Retry the failed messages from the Failures page.
- Open: should the mapping page warn about such labels up front? Today the preview shows the Gmail-derived name, not the sanitised one the server will get.

- Gmail answered `403 Quota exceeded for quota metric 'Total Query Cost' and limit 'Units per minute per user'` at about 30 messages/s with 8 workers (2 messages ended as `gmail_rate_limit` after 6 attempts). Gentler now: defaults 4 workers and 30 % of the quota (about 15 messages/s); a rate-limit response pauses ALL requests of the account for at least 20 s (or Retry-After) and halves the rate, which recovers by 1 % per second (was 5 %); rate-limit errors get 10 attempts instead of 6. Pacing now **learns**: the rate at which Gmail pushed back becomes a ceiling at 80 % of it (restart at 75 % of that, creeping back up by 1 %/s), and the ceiling is probed upwards by 10 % after every quiet five minutes up to the configured maximum, so a long first import settles near what the account tolerates instead of oscillating. The learned ceiling is stored per account (`sync_state.learned_rate_fraction`) when a session was rate limited, and the next session starts at 110 % of it (never above the configured share). Each rate-limited session gets a `rate_limited` event with the number of hits, the time paused and the pace reached. Existing accounts keep their stored values: change them under Account → Advanced (or `update account_sync_settings set worker_count = 4, rate_fraction = 0.3;`).

## Setup feedback recorded in the docs
From the first real Google setup, now part of `docs/google-setup.md` (and `~/google-validate-grants/README.md`): the Gmail API page's "Create credentials" button is a trap (consent screen first, reached through the direct link or *APIs & Services → OAuth consent screen*, not found under the name "Google Auth Platform" by everyone); pressing **Create** at the end of the first wizard is fine; publishing needs the Branding page complete (app name without trailing dot, support email, home page URL, privacy policy URL, no logo); the repository ships `PRIVACY.md` for that; the scope is added under Data Access; credentials come from *Clients*; the destination form needs name/host/port/TLS/username/password and the username/password of the local Dovecot are fixed (`archive`/`secret`). Unconfirmed: whether Google accepts `github.com` under Authorized domains for an app you do not verify.

## Deferred / later
- Gmail push (`users.watch`) is out of scope for v1 (spec §4.5).

## API fork
Implemented in `internal/api` (+ `auth.Sealer`). Integration tests: `go test -tags integration ./internal/api`.

Deviations / decisions:
- OAuth start is `POST /api/v1/accounts/{id}/oauth/start` (spec §11 said GET `/accounts/{id}/oauth`); callback is the gin route `GET /oauth/callback` (not in OpenAPI). On errors it redirects to `/accounts/{id}?oauth_error=<code>` with codes: missing_state, expired_state, state_mismatch, denied, google_error, not_configured, exchange_failed, no_refresh_token, profile_failed, unknown_account, internal. The frontend must show these.
- SSE `GET /api/v1/events` is a gin route, so it is NOT in `/openapi.json`; the frontend uses `useEventStream` directly. Events: `snapshot` ({sessions: SessionSummary[]}) then `session` (SessionSummary). Reconnect (Last-Event-ID / ?lastEventId) just yields a fresh snapshot.
- Failure retry (`POST /failures/{id}/retry`, `/failures/retry`) resets the failures (`ResetFailureForRetry`) and queues a `sync_type=retry` session with params `{"failure_ids":[...]}`. If a session is already active it is NOT queued behind; the response says `queued:false` with a reason and the engine's automatic retry must pick the reset failures up. If the account is unauthorised / needs reauth / has no (enabled) destination, failures are still reset but nothing is queued (reason given).
- `POST /accounts/{id}/sync`: 409 when the account is unauthorised, needs_reauth, or has no enabled destination. First-ever sync is forced to `sync_type=initial`, trigger `initial`; a second request while a session is active returns that session with `already_active:true` (HTTP 200).
- Failure `auto_retry` is derived from the category (not retried automatically: gmail_auth, imap_auth, config, parse, gmail_message_unavailable); matches the exclusions in the `DueFailures` query.
- Account test: a non-auth Gmail error keeps the account status and only records `last_connection_error`, so a transient network error cannot block scheduled syncs. `gmail.ErrAuth` sets `needs_reauth`. A successful test sets status `ok` (unless `disabled`).
- Deleting an account is refused (409) while a session is running.
- The Google state cookie is signed with a key derived from the OIDC session key; with AUTH_DISABLED a random per-process key is used (OAuth only works against the same process then).
- `GET /overview` "next_scheduled_sync" comes from `ListScheduledAccounts` (enabled, status ok, enabled destination); it is absent otherwise. `recent_failures` lists pending ones only.

Needs from the parent:
- Wire `api.Deps{Ring, Hub, Clients}` in `cmd/gmail-archive-sync/main.go` (Clients falls back to a Factory built from Ring/BaseURL if nil; Hub is required for SSE and should run `hub.Run(ctx)`). The worker must call `progress.Notify` on progress/finish.
- The engine must read `params` of sync_sessions: `{after, label_id}` for reconcile, `{failure_ids}` for retry, and set `phase`/`current_folder` via heartbeats.

Questions:
- Should "needs_reauth" accounts also be reachable by `retry`? Currently blocked with a reason.
- The error history JSON shape in `failed_messages.error_history` is passed through untyped (`json.RawMessage`); the engine fork should document it (suggested entry: `{at, category, message, session_id}`) so the frontend can type it.
- No query exists for "sessions per status count"; the status endpoint uses a raw SQL query with five scalar subselects (could become a sqlc query).

## Frontend fork
Built in `web/` (see `web/README.md`). Verified against the real backend (fresh PostgreSQL, seeded sessions/failures, live SSE via `pg_notify`), `npm run build`, `npm test` (45 tests) and the Playwright smoke suite (6 tests) pass.

Decisions:
- Generated client: orval `fetch` client from the checked-in `web/openapi.json`, with `src/services/http.ts` as mutator (throws `ApiError` for every non-2xx, so `call()` returns only success bodies).
- SSE (`/api/v1/events`) is handled by the `live` Pinia store; a session that leaves `queued/running` bumps `finishedTick` and views reload.
- Initial imports and rescans always show the transfer rail as *indeterminate* (counters, no percentage): the engine lists Gmail page by page, so `discovered` is not a total. ETA only for incremental/retry runs, after >= 30 s and >= 50 messages.
- Label switches follow the unsaved draft at once (pending overrides), then the server preview (`folder-preview`) corrects folders and collisions after 300 ms.
- Saving sync settings always goes through a confirmation that lists the changes and says they are additive.
- With the Vite dev server the backend needs `BASE_URL=http://localhost:3000`, otherwise the CSRF origin check returns 403 (Origin is :3000, backend Host is :1080).

Questions / suggestions for the backend:
- `GET /overview` has no per-destination reachability, so the account cards show the destination *name* and the account's last check, not a live "Dovecot status". Add `destination_ok`/`last_checked` (or a cached IMAP check) if the dashboard should show it (spec §10.2 lists "Dovecot connection status").
- `GET /failures` cannot filter by session, so the session detail page fetches the account's latest 100 failures and filters `last_session_id` in the browser. A `session` query parameter would make it exact.
- `error_history` entries are read as `{at, category, message, session}` (what the engine writes). Worth typing in the OpenAPI schema.
- No endpoint for "next scheduled run" of an account whose last session is still running; the overview shows the server's `next_scheduled_sync` only.
- The sync page does not show the IMAP connection live state (same gap as above).
- `POST /failures/{id}/ignore` is called once per id for "Ignore selected"; a bulk ignore endpoint would avoid N requests.

Untested:
- The real Google OAuth round trip (start -> Google -> callback -> `?oauth_error=` codes): only the error banner is covered, with a hand-written query.
- TLS dialogs against a real IMAP server (only the form and the API call are exercised); test connection results shown from fake data only.
- Cancel and rescan flows against a running engine; Firefox/Safari; mobile layouts beyond a quick look at the CSS breakpoints; screen reader behaviour (labels and roles are set, not audited with a screen reader or axe).
- Playwright suite is not wired into CI.
