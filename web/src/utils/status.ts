/** State language: icon + word + colour, never colour alone (FULL_SPECS.md §10.4). */
export interface StateView {
  label: string
  color: 'success' | 'warning' | 'error' | 'primary' | 'secondary' | 'default'
  icon: string
}

export const accountStates: Record<string, StateView> = {
  ok: { label: 'Connected', color: 'success', icon: 'mdi-check-circle-outline' },
  unauthorised: { label: 'Needs authorising', color: 'warning', icon: 'mdi-key-outline' },
  needs_reauth: { label: 'Reauthorise Google', color: 'error', icon: 'mdi-key-alert-outline' },
  disabled: { label: 'Disabled', color: 'default', icon: 'mdi-pause-circle-outline' },
  error: { label: 'Connection error', color: 'error', icon: 'mdi-alert-circle-outline' },
}

export const sessionStates: Record<string, StateView> = {
  queued: { label: 'Waiting to start', color: 'secondary', icon: 'mdi-timer-sand' },
  running: { label: 'Running', color: 'primary', icon: 'mdi-sync' },
  completed: { label: 'Completed', color: 'success', icon: 'mdi-check-circle-outline' },
  completed_with_errors: { label: 'Completed with failures', color: 'warning', icon: 'mdi-alert-outline' },
  failed: { label: 'Failed', color: 'error', icon: 'mdi-close-circle-outline' },
  cancelled: { label: 'Cancelled', color: 'default', icon: 'mdi-cancel' },
  abandoned: { label: 'Interrupted', color: 'warning', icon: 'mdi-lan-disconnect' },
}

export const failureStates: Record<string, StateView> = {
  pending: { label: 'Waiting for retry', color: 'warning', icon: 'mdi-clock-outline' },
  retrying: { label: 'Retrying', color: 'primary', icon: 'mdi-sync' },
  ignored: { label: 'Ignored', color: 'default', icon: 'mdi-eye-off-outline' },
}

export const syncTypeLabels: Record<string, string> = {
  initial: 'Initial import',
  incremental: 'Regular sync',
  reconcile: 'Rescan',
  retry: 'Retry failures',
}

export const triggerLabels: Record<string, string> = {
  scheduled: 'Scheduled',
  manual: 'Manual',
  initial: 'First sync',
  retry: 'Retry',
  recovery: 'Recovery',
}

export const phaseLabels: Record<string, string> = {
  loading_labels: 'Loading Gmail labels',
  reading_history: 'Reading Gmail history',
  enumerating: 'Listing messages',
  copying: 'Copying to IMAP',
  processing_failures: 'Retrying failed messages',
  saving_checkpoint: 'Saving checkpoint',
}

export const failureCategories: Record<string, string> = {
  gmail_rate_limit: 'Gmail rate limit',
  gmail_auth: 'Gmail authorisation',
  gmail_fetch: 'Gmail download',
  gmail_message_unavailable: 'Message gone from Gmail',
  imap_auth: 'IMAP sign-in',
  imap_connection: 'IMAP connection',
  imap_folder: 'IMAP folder',
  imap_append: 'IMAP delivery',
  parse: 'Unreadable message',
  config: 'Configuration',
  unknown: 'Unknown',
}

export const isActive = (status: string) => status === 'queued' || status === 'running'

export function phaseLabel(phase: string): string {
  return phaseLabels[phase] ?? (phase || 'Starting')
}

/** Plain-language text for the ?oauth_error=<code> the callback redirects with. */
export const oauthErrors: Record<string, string> = {
  missing_state: 'The sign-in link was incomplete. Start authorising again.',
  expired_state: 'The sign-in took too long (over 10 minutes). Start authorising again.',
  state_mismatch: 'The sign-in did not match this browser session. Start authorising again from this page.',
  denied: 'Google access was not granted. Authorise again and allow read-only access to Gmail.',
  google_error: 'Google reported a problem. Authorise again in a moment.',
  not_configured: 'The Google OAuth client is not set up yet. Add it under System, then authorise.',
  exchange_failed: 'Google would not complete the sign-in. Check the client ID, client secret and redirect URI in your Google project.',
  no_refresh_token: 'Google did not hand over a long-lived token. Remove this app under your Google account’s third-party access, then authorise again.',
  profile_failed: 'Signed in, but reading the Gmail address failed. Authorise again.',
  unknown_account: 'That account no longer exists.',
  internal: 'Something went wrong on the server. Try again.',
}

export function oauthErrorMessage(code: string): string {
  return oauthErrors[code] ?? 'Authorisation did not complete. Try again.'
}
