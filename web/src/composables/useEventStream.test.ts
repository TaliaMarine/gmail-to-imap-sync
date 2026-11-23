import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEventStream } from './useEventStream'

class FakeSource {
  static all: FakeSource[] = []
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((e: MessageEvent<string>) => void) | null = null
  listeners = new Map<string, (e: MessageEvent<string>) => void>()
  closed = false
  url: string
  constructor(url: string) {
    this.url = url
    FakeSource.all.push(this)
  }
  addEventListener(name: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(name, fn)
  }
  close() {
    this.closed = true
  }
  emit(name: string, data: unknown, id = '') {
    this.listeners.get(name)?.({ data: JSON.stringify(data), lastEventId: id } as MessageEvent<string>)
  }
}

describe('useEventStream', () => {
  beforeEach(() => {
    FakeSource.all = []
    vi.stubGlobal('EventSource', FakeSource)
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5) // jitter factor 1.0
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  function start(opts: Partial<Parameters<typeof useEventStream>[1]> = {}) {
    const scope = effectScope()
    const onEvent = vi.fn()
    const stream = scope.run(() => useEventStream('/api/v1/events', { events: ['session'], onEvent, baseDelayMs: 1000, maxDelayMs: 8000, ...opts }))!
    return { scope, stream, onEvent }
  }

  it('connects, reports open and delivers parsed events', () => {
    const { stream, onEvent } = start()
    stream.open()
    expect(stream.status.value).toBe('connecting')
    const src = FakeSource.all[0]!
    src.onopen?.()
    expect(stream.status.value).toBe('open')
    src.emit('session', { id: 's1' }, '4')
    expect(onEvent).toHaveBeenCalledWith('session', { id: 's1' }, '4')
  })

  it('reconnects with doubling delays capped at the maximum', () => {
    const { stream } = start()
    stream.open()
    const delays: number[] = []
    for (let i = 0; i < 5; i++) {
      FakeSource.all[i]!.onerror?.()
      expect(stream.status.value).toBe('reconnecting')
      const before = FakeSource.all.length
      // Advance in small steps to find when the next connection is made.
      let waited = 0
      while (FakeSource.all.length === before && waited < 20000) {
        vi.advanceTimersByTime(250)
        waited += 250
      }
      delays.push(waited)
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 8000])
  })

  it('resets the backoff once a connection opens', () => {
    const { stream } = start()
    stream.open()
    FakeSource.all[0]!.onerror?.()
    vi.advanceTimersByTime(1000)
    FakeSource.all[1]!.onerror?.()
    vi.advanceTimersByTime(2000)
    FakeSource.all[2]!.onopen?.()
    expect(stream.attempts.value).toBe(0)
    FakeSource.all[2]!.onerror?.()
    const n = FakeSource.all.length
    vi.advanceTimersByTime(1000)
    expect(FakeSource.all.length).toBe(n + 1)
  })

  it('passes the last event id on reconnect', () => {
    const { stream } = start()
    stream.open()
    FakeSource.all[0]!.emit('session', {}, '7')
    FakeSource.all[0]!.onerror?.()
    vi.advanceTimersByTime(1000)
    expect(FakeSource.all[1]!.url).toBe('/api/v1/events?lastEventId=7')
  })

  it('does not reconnect after close', () => {
    const { stream } = start()
    stream.open()
    stream.close()
    FakeSource.all[0]!.onerror?.()
    vi.advanceTimersByTime(60000)
    expect(FakeSource.all).toHaveLength(1)
    expect(stream.status.value).toBe('closed')
    expect(FakeSource.all[0]!.closed).toBe(true)
  })

  it('closes when its scope is disposed', () => {
    const { scope, stream } = start()
    stream.open()
    scope.stop()
    expect(stream.status.value).toBe('closed')
  })
})
