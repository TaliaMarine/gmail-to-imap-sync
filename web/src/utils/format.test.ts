import { describe, expect, it } from 'vitest'
import { estimateRemaining, formatBytes, formatDuration, formatInterval, formatRelative } from './format'
import { fillDays } from './activity'
import { describeChanges, isDirty, setLabelExcluded } from './syncSettings'
import type { SyncSettings } from '../services/client'

describe('format', () => {
  it('formats durations and bytes', () => {
    expect(formatDuration(500)).toBe('<1s')
    expect(formatDuration(42_000)).toBe('42s')
    expect(formatDuration(125_000)).toBe('2m 5s')
    expect(formatDuration(3_725_000)).toBe('1h 2m')
    expect(formatDuration(undefined)).toBe('–')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(52_428_800)).toBe('50 MB')
  })

  it('formats intervals', () => {
    expect(formatInterval(7200)).toBe('2 hours')
    expect(formatInterval(3600)).toBe('1 hour')
    expect(formatInterval(900)).toBe('15 minutes')
    expect(formatInterval(86400)).toBe('1 day')
  })

  it('formats relative times against a fixed clock', () => {
    const now = Date.parse('2026-01-01T12:00:00Z')
    expect(formatRelative('2026-01-01T09:00:00Z', now)).toMatch(/3 hours ago/)
    expect(formatRelative('2026-01-01T14:00:00Z', now)).toMatch(/in 2 hours/)
    expect(formatRelative(undefined, now)).toBe('never')
  })
})

describe('estimateRemaining', () => {
  const now = Date.parse('2026-01-01T12:10:00Z')
  const started = '2026-01-01T12:00:00Z' // 10 minutes earlier
  it('estimates from throughput once the run is long and large enough', () => {
    // 100 of 400 in 10 min -> 300 left at 10/min = 30 min
    expect(estimateRemaining({ discovered: 400, processed: 100, started_at: started }, now)).toBe(30 * 60_000)
  })
  it('refuses to guess before 50 messages', () => {
    expect(estimateRemaining({ discovered: 400, processed: 49, started_at: started }, now)).toBeUndefined()
  })
  it('refuses to guess before 30 seconds', () => {
    expect(estimateRemaining({ discovered: 400, processed: 100, started_at: '2026-01-01T12:09:45Z' }, now)).toBeUndefined()
  })
  it('refuses when the total is unknown or already reached', () => {
    expect(estimateRemaining({ discovered: 0, processed: 100, started_at: started }, now)).toBeUndefined()
    expect(estimateRemaining({ discovered: 100, processed: 100, started_at: started }, now)).toBeUndefined()
  })
})

describe('fillDays', () => {
  it('returns one entry per day, oldest first, with zeros for quiet days', () => {
    const today = new Date(2026, 0, 15)
    const bars = fillDays([{ day: '2026-01-14T00:00:00Z', copied: 5, failed: 1, skipped: 0, sessions: 1 }], 3, today)
    expect(bars.map((b) => b.key)).toEqual(['2026-01-13', '2026-01-14', '2026-01-15'])
    expect(bars.map((b) => b.copied)).toEqual([0, 5, 0])
    expect(bars[1]!.failed).toBe(1)
  })
})

const settings = (o: Partial<SyncSettings> = {}): SyncSettings => ({
  archive_folder: 'Archive', batch_size: 100, enabled: true, excluded_labels: [{ kind: 'id', value: 'SPAM' }],
  flag_mapping: { folder_labels: [], starred_as_flagged: true, unread_as_seen: true }, inbox_folder: 'INBOX', label_mapping: {},
  rate_fraction: 0.6, retry_max_attempts: 10, schedule_interval_seconds: 7200, sent_folder: 'Sent', subscribe_folders: true, worker_count: 8, ...o,
})

describe('sync settings helpers', () => {
  it('detects changes and ignores equivalent representations', () => {
    expect(isDirty(settings(), settings())).toBe(false)
    expect(isDirty(settings(), settings({ worker_count: 4 }))).toBe(true)
    expect(isDirty(settings({ excluded_labels: null }), settings({ excluded_labels: [] }))).toBe(false)
    expect(isDirty(settings({ initial_sync_after: '' }), settings())).toBe(false)
  })

  it('excludes and re-includes a label without duplicating rules', () => {
    const s = settings()
    setLabelExcluded(s, 'L1', true)
    setLabelExcluded(s, 'L1', true)
    expect(s.excluded_labels!.filter((m) => m.value === 'L1')).toHaveLength(1)
    setLabelExcluded(s, 'L1', false)
    expect(s.excluded_labels!.some((m) => m.value === 'L1')).toBe(false)
    expect(s.excluded_labels!.some((m) => m.value === 'SPAM')).toBe(true)
  })

  it('summarises what saving will change and states it is additive', () => {
    const a = settings()
    const b = settings({ label_mapping: { L1: 'Clients' }, archive_folder: 'Old', worker_count: 2 })
    setLabelExcluded(b, 'L3', true)
    const text = describeChanges(a, b).join(' | ')
    expect(text).toContain('1 label stops being copied')
    expect(text).toContain('Folder names changed')
    expect(text).toContain('Archive folder is now Old')
    expect(text).toContain('Performance and retry options changed')
    expect(describeChanges(a, settings())).toEqual([])
  })
})
