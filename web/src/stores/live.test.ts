import { describe, expect, it } from 'vitest'
import type { SessionSummary } from '../services/client'
import { applyLiveEvent } from './live'

const s = (id: string, status: SessionSummary['status'], account = 'a1'): SessionSummary =>
  ({ id, account_id: account, status, copied: 0 }) as SessionSummary

describe('applyLiveEvent', () => {
  it('replaces everything on a snapshot and keeps only active sessions', () => {
    const r = applyLiveEvent({ old: s('old', 'running') }, 'snapshot', { sessions: [s('n1', 'running'), s('n2', 'queued'), s('n3', 'completed')] })
    expect(Object.keys(r.sessions).sort()).toEqual(['n1', 'n2'])
  })

  it('tolerates an empty or malformed snapshot', () => {
    expect(applyLiveEvent({ a: s('a', 'running') }, 'snapshot', {}).sessions).toEqual({})
    expect(applyLiveEvent({ a: s('a', 'running') }, 'snapshot', undefined).sessions).toEqual({})
  })

  it('updates an active session in place', () => {
    const r = applyLiveEvent({ a: s('a', 'queued') }, 'session', s('a', 'running'))
    expect(r.sessions.a?.status).toBe('running')
    expect(r.finished).toBeUndefined()
  })

  it('removes a finished session and reports it', () => {
    const r = applyLiveEvent({ a: s('a', 'running'), b: s('b', 'running') }, 'session', s('a', 'completed'))
    expect(Object.keys(r.sessions)).toEqual(['b'])
    expect(r.finished?.id).toBe('a')
  })

  it('ignores unknown events and events without an id', () => {
    const cur = { a: s('a', 'running') }
    expect(applyLiveEvent(cur, 'ping', {}).sessions).toBe(cur)
    expect(applyLiveEvent(cur, 'session', {}).sessions).toBe(cur)
  })
})
