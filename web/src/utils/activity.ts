import type { ActivityDay } from '../services/client'

export interface Bar {
  key: string
  label: string
  copied: number
  failed: number
}

/** One entry per calendar day, oldest first, with zeros where nothing ran. */
export function fillDays(days: ActivityDay[], count: number, today = new Date()): Bar[] {
  const byDay = new Map(days.map((d) => [d.day.slice(0, 10), d]))
  const out: Bar[] = []
  for (let i = count - 1; i >= 0; i--) {
    const d = new Date(today.getFullYear(), today.getMonth(), today.getDate() - i)
    const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    const a = byDay.get(key)
    out.push({
      key,
      label: d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }),
      copied: a?.copied ?? 0,
      failed: a?.failed ?? 0,
    })
  }
  return out
}
