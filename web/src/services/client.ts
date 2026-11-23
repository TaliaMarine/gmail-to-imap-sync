/**
 * Typed access to the backend. The functions come from the orval generated
 * client (src/api/generated.ts); `call` unwraps their `{data, status}`
 * envelope. The fetch wrapper throws ApiError for every non-2xx response, so
 * what `call` returns is always the success body.
 */
import * as gen from '../api/generated'

export * from '../api/generated'
export { ApiError, problemMessage } from './http'
export type { Problem } from './http'

type Success<R> = R extends { status: infer S; data: infer D } ? ([S] extends [200 | 201 | 202 | 204] ? D : never) : never

export async function call<R extends { data: unknown }>(p: Promise<R>): Promise<Success<R>> {
  return (await p).data as Success<R>
}

/** Convenience wrappers for calls with the same shape everywhere. */
export const api = {
  me: () => call(gen.getMe()),
  signOut: async () => {
    await fetch('/auth/logout', { method: 'POST', credentials: 'same-origin' })
  },
  systemStatus: () => call(gen.getSystemStatus()),
  overview: () => call(gen.getOverview()),
  settings: () => call(gen.getSettings()),
  updateSettings: (s: gen.SettingsUpdate) => call(gen.updateSettings(s)),
  accounts: async () => (await call(gen.listAccounts())).items ?? [],
  account: (id: string) => call(gen.getAccount(id)),
  createAccount: (a: gen.AccountInput) => call(gen.createAccount(a)),
  updateAccount: (id: string, a: gen.AccountInput) => call(gen.updateAccount(id, a)),
  deleteAccount: (id: string) => call(gen.deleteAccount(id)),
  startOAuth: (id: string) => call(gen.startAccountOauth(id)),
  testAccount: (id: string) => call(gen.testAccount(id)),
  syncSettings: (id: string) => call(gen.getSyncSettings(id)),
  updateSyncSettings: (id: string, s: gen.SyncSettings) => call(gen.updateSyncSettings(id, s)),
  labels: async (id: string) => (await call(gen.listLabels(id))).items ?? [],
  refreshLabels: async (id: string) => (await call(gen.refreshLabels(id))).items ?? [],
  previewFolders: async (id: string, s: gen.SyncSettings) => (await call(gen.previewFolders(id, s))).items ?? [],
  startSync: (id: string, r: gen.SyncRequest) => call(gen.startSync(id, r)),
  cancelSync: (id: string) => call(gen.cancelSync(id)),
  currentSync: (id: string) => call(gen.getCurrentSync(id)),
  destinations: async () => (await call(gen.listDestinations())).items ?? [],
  createDestination: (d: gen.DestinationConfig) => call(gen.createDestination(d)),
  updateDestination: (id: string, d: gen.DestinationConfig) => call(gen.updateDestination(id, d)),
  deleteDestination: (id: string) => call(gen.deleteDestination(id)),
  testDestination: (id: string) => call(gen.testDestination(id)),
  testDestinationConfig: (d: gen.DestinationTestInputBody) => call(gen.testDestinationConfig(d)),
  sessions: (p?: gen.ListSessionsParams) => call(gen.listSessions(p)),
  session: (id: string) => call(gen.getSession(id)),
  sessionEvents: async (id: string, p?: gen.ListSessionEventsParams) => (await call(gen.listSessionEvents(id, p))).items ?? [],
  failures: (p?: gen.ListFailuresParams) => call(gen.listFailures(p)),
  failure: (id: string) => call(gen.getFailure(id)),
  retryFailure: (id: string) => call(gen.retryFailure(id)),
  retryFailures: (b: gen.RetryManyInputBody) => call(gen.retryFailures(b)),
  ignoreFailure: (id: string) => call(gen.ignoreFailure(id)),
  unignoreFailure: (id: string) => call(gen.unignoreFailure(id)),
  clearIgnored: (account?: string) => call(gen.clearIgnoredFailures({ account_id: account })),
}
