# Trying the application against your real Gmail

A local run that reads your **real Gmail (read-only)** and delivers into a **throwaway Dovecot in Docker**. It changes nothing in Gmail, and nothing leaves your machine except the calls to Google. Use it to see real labels, real throughput, resume after a stop, and the UI with real data.

Needs: Docker, `just`, `uv`, Go and Node (see `DEVELOPER.md`).

## 1. One more OAuth client, for the app

Follow `docs/google-setup.md` first if you have no Google project yet. In the project you use (`Google Auth Platform → Clients → Create client`):

- Application type **Web application**, name `gmail-archive-sync-local`.
- **Authorised redirect URI**: `http://localhost:1080/oauth/callback` (exactly).
- Copy the client ID and secret.

(The consent screen is shared with the validation tool: it is already published, scope `gmail.readonly` is already added.)

## 2. Start everything

```bash
just real-up    # PostgreSQL on :15432 and Dovecot on :1143 (user archive / password secret)
just real       # builds the UI the first time, then runs the app on http://localhost:1080
```

`just real` runs without sign-in (development only, listening on localhost) and keeps a stable encryption key in `.dev/` so credentials survive restarts. Leave it running in its terminal; stop it with Ctrl-C.

## 3. Configure in the UI (http://localhost:1080)

1. **Google client**: Overview shows the setup wizard (or *System → Google*). Paste the client ID and secret from step 1.
2. **Destination**: *Destinations → New*, every field is required:

   | Field | Value |
   |---|---|
   | Name | anything, e.g. `local` |
   | Host | `localhost` |
   | Port | `1143` |
   | TLS mode | `none` |
   | Username | `archive` |
   | Password | `secret` |

   The username and password are not something you create: they belong to the throwaway Dovecot that `just real-up` starts, which has this one user built in (`dev/dovecot/users`). The *Add* button stays grey until all fields are filled. **Test connection** should list `INBOX` and the delimiter `/`.
3. **Account**: *Accounts → New*, pick the destination, then **Authorise with Google** (you will see the "unverified app" warning: *Advanced → continue*).
4. **Labels and folders**: check the preview. Every Gmail label should show the folder it becomes; look for collisions, and exclude what you do not want (SPAM and TRASH are excluded by default).
5. **Schedule**: set **Initial import date** to about 30 days ago for the first try, leave *Enable synchronisation* off (you start syncs by hand for now).

## 4. What to try, and what to look at

| Try | Look for |
|---|---|
| **Sync now** (initial import of the last 30 days) | The live rail moves; counters and phase update; no failures. Gmail is read-only, so it is safe to let it run. |
| `just real-summary` during and after | Folders and counts match what you expect; the **duplicates column is 0**. |
| Stop the app with Ctrl-C **mid-import**, start it again (`just real`) | The session shows as interrupted and a recovery session resumes; the final summary has no duplicates. |
| **Sync now** again, right after it completes | It finishes quickly with 0 copied (everything already present). |
| Send yourself a new mail, or add a label to an old one, then **Sync now** | The new message, or the extra folder copy, arrives. This exercises the real Gmail history. |
| Make something fail: `docker compose -f docker-compose.dev.yml stop dovecot`, sync, start it again, **Retry** in *Failures* | Failures are listed with a plain-language reason; retry delivers only what is missing. |
| Open a few messages from the archive in a mail client (IMAP `localhost:1143`, no TLS) | Dates, flags (read/starred) and attachments look right. |

If a folder name is refused by the server (Dovecot's default Maildir++ layout does not allow `.` in names), the app creates it under a sanitised name (`Project 1.0` becomes `Project 1_0`) and says so in the session's events.

Things worth reporting back (they are the open questions in `NOTES.md`): how many requests and how long the import takes, any rate-limit messages in the session events, labels that produce surprising folder names, and whether incremental runs re-check many already-delivered messages (the History API start semantics).

## 5. Look at the data

```bash
just real-summary   # folders, counts, duplicates in the Dovecot
just real-psql      # SQL shell: select status, copied_count, failed_count from sync_sessions;
```

## 6. Clean up

```bash
just real-down      # removes PostgreSQL and Dovecot and ALL their data
rm -rf .dev         # the local encryption key
```

Revoke the app's access when you are done: Google Account → Security → Third-party access.
