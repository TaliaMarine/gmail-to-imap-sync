const nf = new Intl.NumberFormat()

export const formatNumber = (n: number) => nf.format(n)

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

/** 3725000 ms -> "1h 2m", 42000 -> "42s". */
export function formatDuration(ms: number | undefined): string {
  if (ms === undefined || ms < 0) return '–'
  if (ms < 1000) return '<1s'
  const s = Math.round(ms / 1000)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d) return `${d}d ${h}h`
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${sec}s`
  return `${sec}s`
}

const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const df = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' })
const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

export function formatDateTime(iso: string | undefined | null): string {
  if (!iso) return '–'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '–' : dtf.format(d)
}

export function formatDate(iso: string | undefined | null): string {
  if (!iso) return '–'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '–' : df.format(d)
}

/** "3 minutes ago", "in 2 hours". `now` is injectable for tests. */
export function formatRelative(iso: string | undefined | null, now = Date.now()): string {
  if (!iso) return 'never'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '–'
  const diff = (t - now) / 1000
  const abs = Math.abs(diff)
  if (abs < 45) return rtf.format(0, 'second')
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  return rtf.format(Math.round(diff / 86400), 'day')
}

/** Seconds to a short interval label: 7200 -> "2 hours". */
export function formatInterval(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400} day${seconds === 86400 ? '' : 's'}`
  if (seconds % 3600 === 0) return `${seconds / 3600} hour${seconds === 3600 ? '' : 's'}`
  return `${Math.round(seconds / 60)} minutes`
}

/**
 * Time left, only when the estimate is trustworthy: at least 30 s of running
 * time and 50 processed messages, and a known total.
 */
export function estimateRemaining(
  s: { discovered: number; processed: number; started_at?: string },
  now = Date.now(),
): number | undefined {
  if (!s.started_at || s.discovered <= 0 || s.processed < 50 || s.processed >= s.discovered) return undefined
  const elapsed = now - new Date(s.started_at).getTime()
  if (elapsed < 30_000) return undefined
  const rate = s.processed / elapsed
  return (s.discovered - s.processed) / rate
}

export function elapsedMs(startedAt: string | undefined, finishedAt?: string, now = Date.now()): number | undefined {
  if (!startedAt) return undefined
  const end = finishedAt ? new Date(finishedAt).getTime() : now
  return Math.max(0, end - new Date(startedAt).getTime())
}
