# Gmail Archive Sync
## Functional and Technical Specification

**Project:** Gmail Archive Sync  
**Status:** Proposed – Ready for implementation  
**Version:** 1.0  
**Implementation:** Go backend, Vue 3 + Vuetify frontend, PostgreSQL  
**Deployment:** Docker / Kubernetes / ArgoCD  
**Primary purpose:** Selectively replicate Gmail messages into a self-hosted Dovecot IMAP server.

---

# 1. Project overview

Gmail Archive Sync is a self-hosted application that periodically copies emails from one or more Gmail accounts to a locally managed IMAP server.

The application uses the official Google Gmail API for reading emails and a standard IMAP connection for delivering messages to the destination.

The application provides a web interface for:

- Configuring Gmail accounts and OAuth authentication.
- Configuring destination IMAP accounts.
- Managing label inclusion and exclusion rules.
- Configuring synchronisation behaviour.
- Starting and monitoring synchronisation sessions.
- Viewing historical synchronisation sessions.
- Tracking progress and synchronisation statistics.
- Inspecting and managing failed messages.
- Reviewing application health and connection status.

## 1.1 Core design principles

1. One-way synchronisation: Gmail → Dovecot.
2. Never modify or delete source messages.
3. Preserve original email content, attachments and metadata as accurately as possible.
4. Translate Gmail labels into IMAP folders.
5. Support multiple labels per email.
6. Avoid storing successful message-level synchronisation records.
7. Store individual failed messages for diagnosis and retry.
8. Maintain incremental synchronisation using the Gmail History API.
9. Make interrupted synchronisation sessions recoverable.
10. Keep the application stateless apart from PostgreSQL and external credentials.
11. Provide a complete web-based management interface.
12. Support containerised deployment and GitOps.

# 2. Technology stack

| Component         | Technology                |
| ----------------- | ------------------------- |
| Backend           | Go                        |
| Frontend          | Vue 3                     |
| UI framework      | Vuetify 4                 |
| Frontend build    | Vite                      |
| API               | REST / JSON               |
| Database          | PostgreSQL                |
| Database access   | pgx + SQL migrations      |
| Gmail integration | Google Gmail API (latest) |
| Authentication    | Google OAuth2             |
| Destination       | IMAP / Dovecot            |
| IMAP library      | emersion/go-imap          |
| Logging           | Structured JSON / slog    |
| Metrics           | Prometheus-compatible     |
| Packaging         | Docker                    |
| Deployment        | Kubernetes                |
| GitOps            | ArgoCD                    |

Prefer a single repository and a single deployable application image containing the Go backend and compiled frontend assets.

The application should support a separate worker process mode so that web serving and synchronisation can be scaled or deployed independently in the future.

# 3. High-level architecture

    +-------------------------------+
    |       Vue 3 / Vuetify UI       |
    |                               |
    | Dashboard / Settings / History|
    | Failures / Account management |
    +---------------+---------------+
                    |
                 REST API
                    |
    +---------------v---------------+
    |          Go Backend           |
    |                               |
    | Account management            |
    | OAuth management              |
    | Gmail API integration         |
    | IMAP delivery                 |
    | Synchronisation engine        |
    | Scheduling / Job management   |
    | Failure management            |
    +-------+---------------+-------+
            |               |
    +-------v------+  +-----v-------+
    |  PostgreSQL  |  |   Gmail API |
    |              |  |             |
    | Settings     |  | Messages    |
    | Checkpoints  |  | Labels      |
    | Sessions     |  | History     |
    | Failures     |  | MIME data   |
    +--------------+  +-------------+
                            |
                       IMAP APPEND
                            |
                     +------v------+
                     |   Dovecot   |
                     |             |
                     | IMAP folders|
                     | Mail storage|
                     +-------------+

PostgreSQL is the authoritative source for application configuration, synchronisation state, session history and failures.

# 4. Gmail integration

## 4.1 Authentication

Use Google's official OAuth2 implementation.

Required scope:

    https://www.googleapis.com/auth/gmail.readonly

The application must support:

- Initial OAuth authorisation.
- Offline access and refresh tokens.
- Automatic access-token renewal.
- Detection of revoked or expired authorisation.
- Display of connection status in the UI.
- Reauthorisation workflow.

OAuth client ID and client secret should be configurable.

Refresh tokens must be encrypted at rest in PostgreSQL using an application encryption key supplied through Kubernetes Secrets.

Never expose OAuth tokens through the REST API or frontend.

## 4.2 Gmail API operations

Use the following Gmail API operations:

- `users.labels.list`: retrieve Gmail labels.
- `users.messages.list`: enumerate messages during initial synchronisation.
- `users.messages.get`: retrieve complete message content.
- `users.history.list`: retrieve incremental changes.
- `users.getProfile`: retrieve account information and current history ID.

Retrieve messages using the `format=raw` option where possible, preserving the original RFC 2822/MIME representation.

The application must support Gmail API pagination and quota-aware retries.

## 4.3 Initial synchronisation

On first execution:

1. Retrieve the Gmail profile.
2. Retrieve all labels.
3. Resolve label IDs and names.
4. Apply inclusion and exclusion rules.
5. Enumerate eligible messages.
6. Retrieve their raw MIME content.
7. Determine their destination folders.
8. Deliver messages through IMAP.
9. Record individual failures.
10. Save the Gmail history checkpoint after successfully processing the relevant batch.

Support an optional initial date restriction, such as importing only messages received after a configured date.

## 4.4 Incremental synchronisation

After initial synchronisation, use Gmail History API.

Store the latest successfully processed history ID in PostgreSQL.

On each execution:

1. Read the last stored history ID.
2. Request subsequent Gmail history entries.
3. Identify newly added messages and label changes.
4. Determine destination folders.
5. Process messages.
6. Persist the new checkpoint only after successful processing of the corresponding batch.

Handle HTTP 404 responses from Gmail History API, indicating an expired history checkpoint.

In that situation, initiate a reconciliation/full scan without blindly duplicating previously imported messages.

# 5. Gmail labels and destination folders

## 5.1 Label handling

All Gmail user labels should be included by default.

The administrator can define excluded labels.

Exclusion rules should support:

- Exact label matching.
- Label ID matching.
- Optional case-insensitive matching.
- Gmail system labels.
- Prefix matching for hierarchical user labels.

The application must distinguish between user labels and system labels.

Examples of Gmail system labels:

- INBOX
- SENT
- DRAFT
- STARRED
- IMPORTANT
- SPAM
- TRASH
- CATEGORY_PROMOTIONS
- CATEGORY_SOCIAL
- CATEGORY_UPDATES
- CATEGORY_FORUMS
- CATEGORY_PERSONAL

Default exclusions:

- SPAM
- TRASH
- DRAFT
- Configured newsletter labels.
- Other explicitly configured exclusions.

The administrator should be able to override defaults.

## 5.2 Folder mapping

Use Gmail label names as destination folder names wherever possible.

Examples:

| Gmail label | Destination folder |
|---|---|
| Projects | Projects |
| Finance | Finance |
| Work/Customers | Work/Customers |
| Travel | Travel |
| Personal | Personal |

Support configurable mapping rules for labels that need different destination names.

Automatically create missing IMAP folders.

Handle IMAP folder separators and namespace conventions dynamically where possible.

Avoid accidental collisions between different Gmail labels that map to the same destination folder.

## 5.3 Multiple labels

An email with multiple Gmail labels should be copied to each corresponding destination folder.

Example:

Gmail message:

- Projects
- Finance
- INBOX

Destination:

- Projects/
- Finance/
- INBOX

Each destination copy must contain the same original message content.

The application must avoid creating multiple copies in the same destination folder.

## 5.4 Unlabelled messages

Messages without any applicable user label should be placed in a configurable fallback folder.

Default:

    Archive

This also applies to archived messages that are not in INBOX and have no user-defined labels.

## 5.5 Sent messages

Messages carrying the Gmail SENT label should be mapped to the configurable destination Sent folder.

Sent messages should retain their original MIME content and timestamps.

Avoid placing sent messages in Archive solely because they lack user-defined labels.

# 6. Synchronisation and deduplication

## 6.1 No permanent success-message index

The application must NOT maintain a permanent database record for every successfully copied email.

This is an explicit architectural requirement.

The number of successful messages can grow to hundreds of thousands or millions. Storing a permanent row for each one would introduce unnecessary database growth and maintenance overhead.

Instead, use:

- Gmail History API checkpoints.
- Synchronisation session checkpoints.
- Gmail message IDs during processing.
- A destination-side deduplication mechanism.
- A database record only when an email fails.

## 6.2 Destination-side deduplication

Add a private header to imported messages:

    X-Gmail-Archive-ID: <gmail-message-id>

Before APPEND, check whether this identifier already exists in the destination folder.

This makes retries and interrupted jobs recoverable without a permanent success index in PostgreSQL.

The application must ensure the header is added only once and must not modify the original message's other content.

Use the Gmail message ID rather than the Gmail thread ID.

Deduplication must be scoped to the destination folder, since one Gmail message can legitimately exist in multiple folders.

## 6.3 Crash recovery

Consider the following sequence:

1. Message successfully appended to Dovecot.
2. Application crashes before recording successful processing.
3. Next session attempts the same operation.

The application must detect the existing destination message using the private header and skip the duplicate APPEND.

The operation should then be considered successfully processed.

## 6.4 Deletions and label removals

Never propagate Gmail message deletion to Dovecot.

Never delete local messages when Gmail labels are removed.

The default synchronisation behaviour is additive.

Adding a label to a Gmail message can create an additional local folder copy.

Removing a label must not remove an existing local copy.

# 7. Failure management

This is the only area where permanent message-level records should be stored.

## 7.1 Failure records

Store failed messages in a dedicated PostgreSQL table.

Each Gmail message should have at most one active failure record per account.

Use a unique constraint on:

    (account_id, gmail_message_id)

A failure record should contain:

- Gmail message ID.
- Gmail thread ID.
- Account ID.
- Subject.
- Sender.
- Recipient information where available.
- Original message date.
- Current intended destination folders.
- Successfully completed destination folders.
- Failed destination folders.
- Error category.
- Error message.
- First failure timestamp.
- Last failure timestamp.
- Number of attempts.
- Last attempted timestamp.
- Retry status.
- Last associated synchronisation session.

Do not store the complete email body or attachments in the failure record.

## 7.2 Failure lifecycle

Suggested statuses:

- pending
- retrying
- resolved
- ignored

Resolved records should normally be removed after successful recovery.

Ignored records may be retained until explicitly cleared.

A failed delivery to one folder must not cause already completed deliveries to be repeated unnecessarily.

## 7.3 Error categories

Classify errors into:

- Gmail API rate limit.
- Gmail API authentication.
- Gmail message retrieval.
- Gmail message unavailable.
- IMAP authentication.
- IMAP connection.
- IMAP folder creation.
- IMAP APPEND.
- Message parsing.
- Configuration.
- Unknown.

Provide human-readable descriptions in the UI.

## 7.4 Retry handling

Support:

- Automatic retry during subsequent sessions.
- Manual retry of one failed message.
- Manual retry of all pending failures.
- Retry of selected failures.

Use exponential backoff for temporary errors.

Do not endlessly retry permanent errors such as invalid configuration or revoked OAuth credentials.

A failed message should be removed from the active failure table after successful recovery, while the recovery is reflected in the associated session statistics.

# 8. Synchronisation sessions and history

## 8.1 Session model

Every synchronisation execution creates one session record.

A session represents one execution of the synchronisation engine.

Store:

- Session ID.
- Account ID.
- Start timestamp.
- End timestamp.
- Status.
- Trigger type.
- Initial/full or incremental sync.
- Starting Gmail history ID.
- Ending Gmail history ID.
- Number of messages discovered.
- Number of messages processed.
- Number of messages successfully copied.
- Number of existing messages skipped.
- Number of failed messages.
- Number of folders created.
- Number of recovered failures.
- Number of API requests.
- Total execution duration.
- Last error, if applicable.

Trigger types:

- scheduled
- manual
- initial
- retry

Statuses:

- queued
- running
- completed
- completed_with_errors
- failed
- cancelled

## 8.2 Session progress

During an active session, expose live progress through REST polling or Server-Sent Events.

Progress fields:

- Current operation.
- Current phase.
- Messages discovered.
- Messages processed.
- Messages copied.
- Messages skipped.
- Messages failed.
- Current label/folder.
- Percentage complete, when determinable.
- Elapsed time.
- Estimated remaining time, when reliable.

Possible phases:

1. Connecting to Gmail.
2. Refreshing OAuth credentials.
3. Loading labels.
4. Reading history.
5. Enumerating messages.
6. Retrieving message content.
7. Copying to IMAP.
8. Processing failures.
9. Saving checkpoint.
10. Finalising session.

The UI must indicate when progress is indeterminate, especially during large Gmail API enumeration operations.

## 8.3 Session event history

Store aggregated session events, not a permanent log entry for every successfully copied email.

Examples:

- Started initial synchronisation.
- Retrieved 500 messages.
- Created folder Projects.
- Copied 250 messages.
- 3 messages failed.
- Gmail API rate limit encountered.
- Checkpoint successfully updated.
- Session completed.

Use batching or aggregation to avoid excessive database writes.

Session event retention should be configurable, with a sensible default such as 90 days.

## 8.4 Session retention

Suggested defaults:

- Session summaries: 365 days.
- Session events: 90 days.
- Active failures: retained until resolved or ignored.
- Resolved failures: removed after recovery.
- Successful message records: none.

The administrator should be able to adjust retention settings.

# 9. PostgreSQL data model

Use UUID primary keys where appropriate.

All tables should include timestamps where relevant.

## 9.1 accounts

Stores configured Gmail accounts.

Fields:

- id UUID
- name
- gmail_address
- oauth_client_id
- encrypted_refresh_token
- enabled
- last_connection_status
- last_connection_check
- created_at
- updated_at

The OAuth client secret may be stored globally in environment configuration rather than repeated per account.

## 9.2 destinations

Stores IMAP destination configurations.

Fields:

- id UUID
- name
- hostname
- port
- tls_enabled
- username
- encrypted_password
- enabled
- created_at
- updated_at

## 9.3 account_sync_settings

Stores synchronisation configuration per Gmail account.

Fields:

- account_id
- destination_id
- schedule_interval
- enabled
- initial_sync_completed
- initial_sync_after
- archive_folder
- sent_folder
- inbox_folder
- include_system_labels JSONB
- excluded_labels JSONB
- label_mapping JSONB
- preserve_flags
- preserve_internal_date
- propagate_deletions (always false by default)
- created_at
- updated_at

## 9.4 sync_state

Stores durable synchronisation checkpoints.

Fields:

- account_id
- last_successful_history_id
- last_successful_sync_at
- initial_sync_cursor
- initial_sync_completed
- updated_at

Initial synchronisation must be resumable.

Do not advance a checkpoint before all operations represented by that checkpoint have been handled successfully or safely recorded for retry.

## 9.5 sync_sessions

Stores session summaries.

Fields:

- id UUID
- account_id
- status
- trigger_type
- sync_type
- started_at
- finished_at
- starting_history_id
- ending_history_id
- discovered_count
- processed_count
- copied_count
- skipped_count
- failed_count
- recovered_count
- folders_created_count
- api_request_count
- duration_ms
- last_error
- created_at

Indexes:

- account_id + started_at
- status
- started_at

## 9.6 sync_session_events

Stores aggregated operational events.

Fields:

- id BIGSERIAL
- session_id
- event_type
- severity
- message
- metadata JSONB
- created_at

Avoid storing one event per successfully copied email.

## 9.7 failed_messages

Stores individual failures.

Fields:

- id UUID
- account_id
- gmail_message_id
- gmail_thread_id
- subject
- sender
- recipients JSONB
- message_date
- intended_folders JSONB
- completed_folders JSONB
- failed_folders JSONB
- error_category
- error_message
- status
- attempt_count
- first_failed_at
- last_failed_at
- last_attempted_at
- last_session_id
- created_at
- updated_at

Constraint:

    UNIQUE(account_id, gmail_message_id)

An updated failure record must replace the previous failure information rather than create a new record.

# 10. Web interface

Use Vue 3 with Vuetify 3.

Design the UI as a responsive self-hosted administration application.

Use a conventional navigation drawer with a clean, compact dashboard layout.

## 10.1 Navigation

Main navigation:

- Dashboard
- Accounts
- Synchronisation
- Session history
- Failed messages
- Settings
- System status

## 10.2 Dashboard

Display:

- Gmail connection status.
- Dovecot connection status.
- Last successful synchronisation.
- Next scheduled synchronisation.
- Current synchronisation status.
- Total messages copied in the current session.
- Total failures awaiting recovery.
- Recent synchronisation sessions.
- Recent errors.

Provide buttons:

- Synchronise now.
- Retry failed messages.
- View session history.

Use charts for daily synchronisation activity and failures over time.

Do not present a cumulative successful-message count as an exact lifetime total unless a separate, explicitly defined counter is maintained.

## 10.3 Accounts

Manage Gmail accounts.

Display:

- Account name.
- Gmail address.
- OAuth status.
- Destination IMAP account.
- Enabled/disabled.
- Last successful sync.

Actions:

- Add account.
- Authorise Google account.
- Test connection.
- Edit settings.
- Disable account.
- Remove account.

## 10.4 Synchronisation settings

Provide editable settings:

**General**

- Enable synchronisation.
- Schedule interval.
- Initial import date.
- Archive folder.
- Sent folder.
- Inbox folder.

**Labels**

- Display labels retrieved from Gmail.
- Show system/user label distinction.
- Exclude individual labels.
- Add prefix exclusions.
- Configure label-to-folder mappings.
- Refresh labels from Gmail.

**IMAP**

- Destination host.
- Port.
- TLS.
- Username.
- Password.
- Test connection.

**Advanced**

- Batch size.
- Gmail API retry limits.
- IMAP connection timeout.
- Maximum concurrent message processing.
- Failure retry policy.
- Session retention.
- Event retention.

Require explicit confirmation before applying potentially destructive or disruptive configuration changes.

## 10.5 Synchronisation screen

Display the current session.

Show:

- Current phase.
- Progress indicator.
- Messages discovered.
- Messages processed.
- Messages copied.
- Messages skipped.
- Messages failed.
- Current folder.
- Elapsed time.

Provide:

- Start synchronisation.
- Cancel synchronisation.

Cancellation must be graceful: finish the current message operation, preserve already completed work and safely save the latest checkpoint.

## 10.6 Session history

Provide a paginated table.

Columns:

- Started.
- Duration.
- Trigger.
- Type.
- Status.
- Discovered.
- Copied.
- Skipped.
- Failed.

Filters:

- Date range.
- Account.
- Status.
- Trigger type.
- Full/incremental.

Selecting a session opens a detail page with:

- Complete session statistics.
- Timeline of aggregated events.
- Error summary.
- Associated failed messages.
- Starting and ending history IDs.

## 10.7 Failed messages

Provide a dedicated failure-management interface.

Table columns:

- Subject.
- Sender.
- Date.
- Destination folders.
- Error category.
- Error description.
- Attempt count.
- Last attempted.
- Status.

Actions:

- Retry.
- Retry selected.
- Retry all pending.
- Ignore.
- View details.

Message details should display the available metadata, error history and folder delivery state.

Do not expose email body content in the failure-management interface.

Support pagination, sorting and filtering.

## 10.8 System status

Display:

- Application version.
- Backend health.
- Database connectivity.
- Gmail API connectivity.
- IMAP connectivity.
- Last successful session.
- Scheduled job status.
- Queue status.
- Current application uptime.

# 11. REST API

Use `/api/v1` as the API prefix.

Suggested endpoints:

| Method | Endpoint | Purpose |
|---|---|---|
| GET | /accounts | List accounts |
| POST | /accounts | Create account |
| GET | /accounts/{id} | Account details |
| PUT | /accounts/{id} | Update account |
| DELETE | /accounts/{id} | Remove account |
| GET | /accounts/{id}/labels | Retrieve Gmail labels |
| GET | /accounts/{id}/oauth | OAuth authorisation URL |
| GET | /oauth/callback | OAuth callback |
| POST | /accounts/{id}/test | Test connections |
| GET | /settings | Read settings |
| PUT | /settings | Update settings |
| POST | /sync | Start synchronisation |
| GET | /sync/current | Current progress |
| POST | /sync/cancel | Cancel active session |
| GET | /sessions | List sessions |
| GET | /sessions/{id} | Session details |
| GET | /sessions/{id}/events | Session events |
| GET | /failures | List failures |
| GET | /failures/{id} | Failure details |
| POST | /failures/{id}/retry | Retry one failure |
| POST | /failures/retry | Retry selected/all failures |
| POST | /failures/{id}/ignore | Ignore failure |
| GET | /health/live | Liveness |
| GET | /health/ready | Readiness |
| GET | /metrics | Prometheus metrics |

Use Server-Sent Events for live progress:

    GET /api/v1/sync/events

The frontend should automatically reconnect if the connection is interrupted.

# 12. Scheduling and concurrency

The application must support periodic synchronisation configured through the web interface.

Default schedule: every 2 hours.

Prefer an internal scheduler for the first implementation, backed by PostgreSQL.

Requirements:

- Only one active synchronisation per Gmail account.
- Multiple accounts may run independently.
- Prevent concurrent sessions across multiple application replicas.
- Persist queued/manual jobs.
- Recover abandoned sessions after application restarts.
- Use PostgreSQL advisory locks or equivalent distributed locking.
- Do not rely exclusively on in-memory goroutines for durable job state.

A Kubernetes deployment should support multiple replicas of the web/API component without creating duplicate scheduled jobs.

A separate worker deployment is recommended for production.

# 13. Reliability and data integrity

The application must guarantee:

1. No source messages are deleted.
2. No destination messages are deleted automatically.
3. Successful messages are not permanently indexed in PostgreSQL.
4. Failures are recorded once per Gmail message/account.
5. Destination duplicates are avoided.
6. Partial delivery across multiple folders is recoverable.
7. Session checkpoints survive application restarts.
8. Expired Gmail history checkpoints trigger reconciliation.
9. Failed messages can be retried independently.
10. A failure in one message does not unnecessarily block other messages.

Use bounded concurrency and configurable batch sizes.

Never advance a Gmail history checkpoint beyond changes that have been fully handled or safely persisted for retry.

The IMAP destination-side deduplication mechanism must be treated as an essential part of crash recovery, not merely an optimisation.

# 14. Security

- Encrypt OAuth refresh tokens at rest.
- Encrypt IMAP credentials at rest.
- Store encryption keys outside PostgreSQL.
- Never log credentials, access tokens or message bodies.
- Use TLS for external Gmail and IMAP connections.
- Support application-level authentication for the web UI.
- Protect OAuth callbacks against CSRF.
- Restrict CORS.
- Validate all API inputs.
- Use parameterised SQL queries.
- Run containers as non-root.
- Support read-only root filesystem.
- Do not expose the IMAP service publicly.
- Support Kubernetes Secrets or External Secrets.

The application should not require Google write permissions.

# 15. Kubernetes deployment

Provide Kubernetes manifests suitable for ArgoCD.

Recommended resources:

- Deployment: web/API service.
- Deployment: synchronisation worker.
- Service: ClusterIP.
- ConfigMap: non-sensitive application configuration.
- Secret: encryption key, OAuth credentials and initial credentials.
- PostgreSQL: use an existing cluster/service, not a bundled database by default.
- NetworkPolicy: restrict egress to Gmail API and internal Dovecot/PostgreSQL.
- ServiceMonitor: optional Prometheus integration.

Use one container image for both web and worker processes, with different startup commands.

Provide:

- Dockerfile.
- Kubernetes manifests.
- Kustomize configuration.
- ArgoCD Application example.
- Database migration job or startup migration mechanism.
- Health probes.
- Resource requests and limits.
- Configurable replicas.
- Graceful shutdown.

All runtime settings should be managed through the web interface and persisted in PostgreSQL. Kubernetes configuration should primarily contain infrastructure-level settings and secrets.

# 16. Observability

Provide structured JSON logs.

Suggested Prometheus metrics:

- gmail_sync_sessions_total
- gmail_messages_discovered_total
- gmail_messages_copied_total
- gmail_messages_skipped_total
- gmail_messages_failed_total
- gmail_api_requests_total
- gmail_api_errors_total
- imap_append_errors_total
- sync_duration_seconds
- sync_active
- sync_pending_failures

Expose metrics without sensitive message information.

# 17. Testing requirements

Implement automated tests for:

- OAuth token refresh.
- Gmail API pagination.
- Gmail History API pagination.
- Expired history recovery.
- Label inclusion and exclusion.
- System label handling.
- Multiple-label delivery.
- Unlabelled Archive delivery.
- Sent-folder mapping.
- IMAP folder creation.
- MIME fidelity.
- Internal date preservation.
- Duplicate detection.
- Partial delivery.
- Interrupted synchronisation.
- Failure persistence.
- Failure retry.
- Concurrent worker protection.
- Session statistics.
- Configuration persistence.

Use mock Gmail and IMAP servers where appropriate.

Include integration tests against a real Dovecot test instance using Docker Compose.

# 18. Implementation phases

## Phase 1 – Core backend

- Go project structure.
- PostgreSQL migrations.
- OAuth2 authentication.
- Gmail API client.
- IMAP client.
- Initial full import.
- Label mapping.
- Basic deduplication.
- Incremental history synchronisation.

## Phase 2 – Reliability

- Failure database.
- Retry handling.
- Crash recovery.
- Partial delivery recovery.
- Session tracking.
- Checkpoint management.
- Distributed locking.

## Phase 3 – Web interface

- Vue 3 + Vuetify setup.
- Dashboard.
- Account management.
- OAuth authorisation.
- Settings interface.
- Synchronisation progress.
- Session history.
- Failure management.

## Phase 4 – Production readiness

- Kubernetes deployment.
- ArgoCD configuration.
- Security hardening.
- Metrics.
- Automated tests.
- Documentation.
- Backup and recovery instructions.

# 19. Acceptance criteria

The application is considered ready for production when:

- A Gmail account can be authorised through the web interface.
- Gmail labels can be retrieved and configured.
- Messages can be imported into Dovecot.
- Multiple Gmail labels result in multiple corresponding folders.
- Unlabelled messages are copied to Archive.
- Excluded labels are not copied.
- Sent messages are handled correctly.
- Incremental synchronisation works through Gmail History API.
- Repeated synchronisation does not generate duplicates.
- Successful messages do not create permanent PostgreSQL records.
- Failed messages create exactly one record per account/message.
- Failures can be inspected and retried.
- Session statistics and history are available through the UI.
- Progress is visible during active sessions.
- Application restarts do not lose synchronisation state.
- Gmail source messages remain untouched.
- The application deploys successfully through ArgoCD.

---

# 20. Important architectural constraints for the implementation agent

These requirements take precedence over implementation convenience.

**A. Do not introduce a permanent successful-message index.**

The only per-message persistent records should be active failures. Session statistics and Gmail history checkpoints provide the operational tracking.

**B. Do not use Gmail IMAP as the source integration.**

Use the official Gmail API and History API.

**C. Do not make the application destructive.**

Never delete or modify source or destination messages automatically.

**D. Make synchronisation recoverable.**

A crash between Gmail retrieval, IMAP APPEND and PostgreSQL checkpoint updates must not cause permanent message loss or uncontrolled duplication.

**E. Keep the application modular.**

Separate Gmail, IMAP, database, synchronisation engine, scheduling and HTTP API into distinct Go packages.

**F. Build a genuinely usable management UI.**

The web interface is a core part of the product, not an afterthought. All normal configuration and operational management should be possible without editing YAML or manually manipulating PostgreSQL.

**G. Avoid unnecessary complexity.**

Do not introduce Kafka, Redis, Elasticsearch, or other infrastructure dependencies unless a demonstrated requirement justifies them.

The initial target is one or a small number of Gmail accounts, one or a small number of IMAP destinations, and a reliable self-hosted deployment.