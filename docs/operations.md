# Operations

## Deploying

Install the chart from the OCI registry (see `chart/values.yaml` for all options):

```bash
helm install gmail-archive-sync oci://ghcr.io/<owner>/charts/gmail-archive-sync \
  --version <x.y.z> \
  --set database.existingSecret.name=gmailsync-db \
  --set oidc.issuerUrl=https://auth.example.com/application/o/gmailsync/ \
  --set oidc.clientId=<public client id> \
  --set gateway.host=sync.example.com
```

- The database secret holds a URL in key `dsn`: `postgres://user:pass@host:5432/gmailsync?sslmode=verify-full`. Use the URL form (key/value DSNs are not supported by the migration driver).
- The application creates two Secrets in its namespace on first start (session signing key and the encryption key ring) unless you supply your own (`encryption.existingSecret`, `ENCRYPTION_KEYS`).
- `worker.enabled=true` splits the deployment into web pods and worker pods; the default is a single `--mode all` pod. Several web pods and several workers can share one database safely.
- An example Argo CD `Application` is in `examples/argocd-application.yaml`.

## Dovecot

Any IMAP server with `APPEND` works; Dovecot is the tested target (2.4.x, Maildir, `/` separator; the separator is detected).

- Create a dedicated mailbox user for the archive and enter it under *Destinations*. Use **Test connection** first: it lists the folders and reports the hierarchy delimiter.
- Use TLS (`implicit` on 993, or `starttls`). `none` is for tests only. *Skip certificate verification* is an explicit opt-in and shows a warning.
- Duplicate checks read one header (`X-Gmail-Archive-ID`) per message of each folder a session writes to. Let Dovecot cache it so this stays fast on large mailboxes:

  ```
  mail_cache_fields { hdr.x-gmail-archive-id = yes }   # Dovecot 2.4 syntax
  mail_cache_fields = hdr.x-gmail-archive-id          # Dovecot 2.3 syntax
  ```
  (Check the exact syntax against your Dovecot version; this setting is an optimisation and was not benchmarked.)
- Messages are only ever appended. Moving or deleting an archived message in Dovecot means a rescan (*Sync → Rescan*) delivers it again, because Dovecot is the record of what exists.

## Secrets and key rotation

All credentials (Google client secret, refresh tokens, IMAP passwords, webhook URL) are stored encrypted with AES-256-GCM. The key ring is `ENCRYPTION_KEYS="id:base64,id:base64"`; the first key encrypts, every key decrypts.

To rotate:

1. Generate a key: `head -c 32 /dev/urandom | base64`.
2. Put it **first** in `ENCRYPTION_KEYS` (keep the old one after it) and restart.
3. Run `gmail-archive-sync --rotate-keys` with the same environment (for example a one-off `kubectl exec` or Job). It re-encrypts everything and prints how many values changed; it is safe to repeat.
4. Remove the old key from `ENCRYPTION_KEYS`.

If the key is lost, stored credentials cannot be decrypted: enter the Google client secret and IMAP passwords again and reauthorise the accounts.

## Backup and recovery

- **PostgreSQL** holds configuration, checkpoints, session history and failed messages. Back it up as usual.
- **Dovecot** mail is backed up by whatever protects your mail server today.
- If the database is lost, nothing is lost from the mailbox: recreate the configuration (or restore a backup) and run an initial import again. Messages already in Dovecot are recognised by their archive header and skipped. Only the failure list and session history are gone.

## Monitoring

`/metrics` (Prometheus) exposes `gmailsync_*` metrics; the chart can create a `ServiceMonitor` and a `PrometheusRule` (no success in 3x the interval, accounts needing reauthorisation, growing failures). `/health/live` and `/health/ready` are the probes. Set a notification webhook under *Settings* (ntfy, Slack and Discord incoming webhooks work) to be told when an account needs reauthorisation or sessions keep failing.

## Behaviour worth knowing

- Sync is **additive**: label removals and Gmail deletions never touch the archive.
- A message with a label you excluded is not copied to that label's folder; if all of its labels are excluded it is not copied at all.
- Each session reads its work from Gmail history. If Gmail has discarded the history since the last run (offline for more than about a week), the next session automatically reconciles recent messages instead.
- Stopping the pod mid-sync is safe: the session is saved as interrupted and resumed in a new session.
