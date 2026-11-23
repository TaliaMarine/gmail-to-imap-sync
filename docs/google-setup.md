# Setting up Google access

Gmail Archive Sync reads your mail through the Gmail API with the read-only scope (`gmail.readonly`). Every installation uses **its own** Google OAuth client, created in **your own** Google Cloud project. Nothing is shared with anyone else, and the app can never change or delete mail in Gmail. Everything below is free; no billing account or credit card is needed.

The steps follow what a first-time setup actually looks like in the console (Google renamed the "OAuth consent screen" area to **Google Auth Platform**). If a menu differs, the search box at the top of the console finds any page by name.

## 1. Create the project

1. Sign in at <https://console.cloud.google.com/> with any Google account (it does not have to be the mailbox you archive).
2. Open <https://console.cloud.google.com/projectcreate>, name it `gmail-archive-sync`, leave *Location* on "No organization", **Create**.
3. Wait for the notification, then check that the project picker at the top shows this project. Every following step happens in it.

## 2. Enable the Gmail API

Open <https://console.cloud.google.com/apis/library/gmail.googleapis.com> and click **Enable**.

> The page then offers **Create credentials**. **Do not use it yet**: a consent screen must exist first, and it is set up in the next step.

## 3. Configure the consent screen (Google Auth Platform)

Open <https://console.cloud.google.com/auth/overview>, or use the ☰ menu → *APIs & Services* → *OAuth consent screen*, or search for "Google Auth Platform". It says it is not configured yet: click **Get started**.

1. *App information*: app name `gmail-archive-sync` (no trailing dot or punctuation), your email as support email. **Next**.
2. *Audience*: **External** (a personal Gmail account cannot use Internal; Workspace accounts may). **Next**.
3. *Contact information*: your email. **Next**.
4. Accept the user data policy and click **Continue**, then **Create**. Pressing **Create** here is fine: the remaining settings are filled in from the menu afterwards.

## 4. Fill in the branding (required before you can publish)

*Google Auth Platform → Branding*. Publishing to production is refused ("Valid app name, support email, homepage url, and privacy policy url are required") until these are set:

- **App name**: as above. **User support email**: yours.
- **App logo**: leave empty. Uploading a logo forces an app verification.
- **Application home page**: any public page. This repository works: `https://github.com/<owner>/gmail-to-imap-sync`.
- **Application privacy policy link**: a public page. This repository ships a ready one, [`PRIVACY.md`](../PRIVACY.md); use `https://github.com/<owner>/gmail-to-imap-sync/blob/main/PRIVACY.md` (the repository must be public, or host the text on any public page you control).
- **Application terms of service**: leave empty.
- **Authorized domains**: add the domain of the pages you used. Google may refuse a domain you do not own (for example `github.com`); if so, leave it empty for now, or host the two pages on a domain you own and use those URLs. The domain you will deploy the app on has to be added here later anyway (step 7).
- **Developer contact information**: your email. **Save**.

> **Hosting the two pages yourself.** Only the home page and privacy policy URLs must be publicly reachable; the application itself can stay internal. The OAuth redirect (`https://<host>/oauth/callback`) is followed by your own browser, so it only has to resolve where you sign in (LAN or VPN) and have a valid HTTPS certificate on a real domain; Google never calls it. A ready-made static site is in `docs/site/` (`index.html`, `privacy.html`): upload it anywhere under a domain you own, add that domain under *Authorized domains*, and use its URLs for the two Branding fields.

## 5. Publish the app to production

*Google Auth Platform → Audience → Publishing status → Publish app*, confirm. The status must read **In production** (the button then offers *Back to testing*; do not use it).

**Why this matters:** a consent screen left in *Testing* gives refresh tokens that **expire after 7 days**, so syncing would stop every week. An unverified app in production shows an "unverified app" warning when you authorise, which is expected for a personal installation, and is limited to 100 new users over the project's lifetime, which does not matter here. No test users have to be listed once it is in production. Workspace accounts can choose the *Internal* audience instead, which needs neither publishing nor shows the warning.

## 6. Add the Gmail scope

*Google Auth Platform → Data Access → Add or remove scopes*: find or paste `https://www.googleapis.com/auth/gmail.readonly` ("View your email messages and settings"), tick it, **Update**, **Save**. It is a restricted scope, which is why the unverified-app screen appears at authorisation.

## 7. Create the OAuth client(s)

*Google Auth Platform → Clients → Create client* (<https://console.cloud.google.com/auth/clients>). Copy the **client ID** and **client secret** from the dialog; the secret is shown there and can be downloaded as JSON.

| For | Application type | Authorised redirect URI |
|---|---|---|
| Gmail Archive Sync, running locally (`docs/dev-real.md`) | **Web application** | `http://localhost:1080/oauth/callback` |
| Gmail Archive Sync, deployed | **Web application** | `https://<your host>/oauth/callback` (shown on the app's Settings page; must match `BASE_URL` exactly) |
| The grant-lifetime check tool | **Desktop app** | none needed |

**Authorised domains matter for the deployed client.** Google's Branding page states that any domain used in an OAuth client's configuration must be pre-registered under *Authorized domains*. So before creating the deployed Web client, add the **registrable domain** of your host there: for `sync.example.com` that is `example.com` (add the domain you own, not the full host name; `localhost` needs no entry). You own the domain, so this is accepted.

All clients of the project share the consent screen you published. Enter the Web client's ID and secret in the app under *Settings → Google* (the secret is stored encrypted and never shown again).

## 8. Authorise an account in the app

Create the account in the app, then **Authorise with Google**. Choose the Gmail account, and on the "Google hasn't verified this app" screen click **Advanced → Go to gmail-archive-sync (unsafe)**: that is your own app. The refresh token is stored encrypted. If Google later rejects it the account shows **Reauthorise needed**, syncing for it pauses, and the optional webhook fires.

## How long does access last?

From Google's documentation (OAuth 2.0 for web server applications, "Refresh token expiration", and the consent-screen publishing-status help page, checked November 2026):

- A consent screen with status **Testing** gives refresh tokens that **expire after 7 days**. This is the only time-based expiry Google documents.
- **In production** has no such expiry in the documentation; Google does not say explicitly that unverified production apps keep their tokens indefinitely, so the application records when each token was granted and tells you what it saw. (The standalone check tool described in the project notes validates this on a real account.)
- A token also stops working when: you **change your Google password** (tokens with Gmail scopes are revoked), you remove the app under *Google Account → Security → Third-party access*, the token is **unused for 6 months** (a 2-hour schedule keeps it used), more than 100 tokens exist for one account and client (the oldest is dropped), or a Workspace administrator restricts the app.

If access ends, the message says how long the token lived: about 7 days points at a consent screen still in *Testing*; a long time points at a password change or a revoked app. Reauthorising keeps all synchronisation state.

## Troubleshooting

| Symptom | Cause |
|---|---|
| "Create credentials" on the Gmail API page asks about a consent screen | Set up the consent screen first (step 3), then create clients under *Google Auth Platform → Clients*. |
| **Publish app** is greyed out or refuses | The Branding page is incomplete (step 4): app name, support email, home page and privacy policy URL. |
| `redirect_uri_mismatch` | The redirect URI in Google differs from the one the app uses (scheme, host, port and path must match exactly; `BASE_URL` must be the public URL). |
| "Google hasn't verified this app" | Expected for an unverified app: *Advanced → Go to … (unsafe)*. |
| No refresh token returned | The account already granted access before. The app asks with `prompt=consent`, so this should not happen; remove the app under your Google account's security settings and authorise again. |
| Account needs reauthorisation about weekly | The consent screen is still in *Testing*. Publish it (step 5). |
| Rate-limit errors during a large import | Expected on the first import; the app pauses and retries. Lower the account's quota share or workers under *Advanced* if it happens a lot. |
