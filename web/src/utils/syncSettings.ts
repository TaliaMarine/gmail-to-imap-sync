import type { Matcher, SyncSettings } from '../services/client'

export const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T

/** Interval presets in seconds; the API minimum is 5 minutes. */
export const intervalOptions = [
  { title: 'Every 15 minutes', value: 900 },
  { title: 'Every 30 minutes', value: 1800 },
  { title: 'Every hour', value: 3600 },
  { title: 'Every 2 hours', value: 7200 },
  { title: 'Every 6 hours', value: 21600 },
  { title: 'Every 12 hours', value: 43200 },
  { title: 'Every day', value: 86400 },
]

export function isDirty(server: SyncSettings | undefined, draft: SyncSettings | undefined): boolean {
  if (!server || !draft) return false
  return JSON.stringify(normalise(server)) !== JSON.stringify(normalise(draft))
}

function normalise(s: SyncSettings) {
  return {
    ...s,
    excluded_labels: s.excluded_labels ?? [],
    flag_mapping: { ...s.flag_mapping, folder_labels: s.flag_mapping.folder_labels ?? [] },
    label_mapping: Object.fromEntries(Object.entries(s.label_mapping ?? {}).sort()),
    initial_sync_after: s.initial_sync_after || undefined,
  }
}

const idRule = (m: Matcher) => m.kind === 'id'

/** The exclusion rules that are not per-label switches. */
export const nameRules = (s: SyncSettings): Matcher[] => (s.excluded_labels ?? []).filter((m) => !idRule(m))

export function setLabelExcluded(s: SyncSettings, id: string, excluded: boolean): void {
  const rest = (s.excluded_labels ?? []).filter((m) => !(idRule(m) && m.value === id))
  s.excluded_labels = excluded ? [...rest, { kind: 'id', value: id }] : rest
}

/** Plain-language list of what saving will change, shown before applying. */
export function describeChanges(server: SyncSettings, draft: SyncSettings): string[] {
  const out: string[] = []
  const ids = (s: SyncSettings) => new Set((s.excluded_labels ?? []).filter(idRule).map((m) => m.value))
  const a = ids(server)
  const b = ids(draft)
  const nowExcluded = [...b].filter((x) => !a.has(x)).length
  const nowIncluded = [...a].filter((x) => !b.has(x)).length
  if (nowExcluded) out.push(`${nowExcluded} ${nowExcluded === 1 ? 'label stops' : 'labels stop'} being copied`)
  if (nowIncluded) out.push(`${nowIncluded} ${nowIncluded === 1 ? 'label starts' : 'labels start'} being copied (new mail only; run a rescan to include older mail)`)
  const rules = (s: SyncSettings) => JSON.stringify(nameRules(s))
  if (rules(server) !== rules(draft)) out.push('Exclusion rules changed')
  const m1 = JSON.stringify(Object.entries(server.label_mapping ?? {}).sort())
  const m2 = JSON.stringify(Object.entries(draft.label_mapping ?? {}).sort())
  if (m1 !== m2) out.push('Folder names changed: new mail goes to the new folders, existing folders stay as they are')
  if (JSON.stringify(server.flag_mapping) !== JSON.stringify(draft.flag_mapping)) out.push('Flag and extra folder options changed')
  for (const [key, label] of [
    ['archive_folder', 'Archive folder'],
    ['sent_folder', 'Sent folder'],
    ['inbox_folder', 'Inbox folder'],
  ] as const) {
    if (server[key] !== draft[key]) out.push(`${label} is now ${draft[key]}`)
  }
  if (server.enabled !== draft.enabled) out.push(draft.enabled ? 'Scheduled sync is turned on' : 'Scheduled sync is turned off')
  if (server.schedule_interval_seconds !== draft.schedule_interval_seconds) out.push('Schedule interval changed')
  if ((server.initial_sync_after ?? '') !== (draft.initial_sync_after ?? '')) out.push('Initial import date changed')
  if (
    server.worker_count !== draft.worker_count ||
    server.rate_fraction !== draft.rate_fraction ||
    server.batch_size !== draft.batch_size ||
    server.retry_max_attempts !== draft.retry_max_attempts ||
    server.subscribe_folders !== draft.subscribe_folders
  )
    out.push('Performance and retry options changed')
  return out
}
