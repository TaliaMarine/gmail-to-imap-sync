import { onScopeDispose, ref, type Ref } from 'vue'

export type StreamStatus = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed'

export interface EventStreamOptions {
  /** Event names to subscribe to (in addition to the default `message`). */
  events?: string[]
  onEvent: (name: string, data: unknown, lastEventId: string) => void
  /** First retry delay; doubles up to maxDelayMs, with jitter. */
  baseDelayMs?: number
  maxDelayMs?: number
}

export interface EventStream {
  status: Ref<StreamStatus>
  attempts: Ref<number>
  open: () => void
  close: () => void
}

/**
 * Server-Sent Events with our own backoff. EventSource sends Last-Event-ID by
 * itself on reconnect, but only for its built-in retries; we close and recreate
 * the connection on error (so the delay is ours), so the id is tracked here and
 * passed as a query parameter the server also accepts (`?lastEventId=`).
 */
export function useEventStream(url: string, opts: EventStreamOptions): EventStream {
  const status = ref<StreamStatus>('idle')
  const attempts = ref(0)
  const base = opts.baseDelayMs ?? 1000
  const max = opts.maxDelayMs ?? 30000

  let source: EventSource | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let lastEventId = ''
  let wanted = false

  function connect() {
    const target = lastEventId ? `${url}${url.includes('?') ? '&' : '?'}lastEventId=${encodeURIComponent(lastEventId)}` : url
    status.value = attempts.value === 0 ? 'connecting' : 'reconnecting'
    source = new EventSource(target, { withCredentials: true })

    source.onopen = () => {
      attempts.value = 0
      status.value = 'open'
    }
    const handle = (name: string) => (e: MessageEvent<string>) => {
      if (e.lastEventId) lastEventId = e.lastEventId
      let data: unknown = e.data
      try {
        data = JSON.parse(e.data)
      } catch {
        // keep raw text
      }
      opts.onEvent(name, data, lastEventId)
    }
    source.onmessage = handle('message')
    for (const name of opts.events ?? []) source.addEventListener(name, handle(name) as EventListener)

    source.onerror = () => {
      source?.close()
      source = null
      if (!wanted) return
      attempts.value += 1
      status.value = 'reconnecting'
      const delay = Math.min(max, base * 2 ** (attempts.value - 1)) * (0.75 + Math.random() * 0.5)
      timer = setTimeout(connect, delay)
    }
  }

  function open() {
    if (wanted) return
    wanted = true
    attempts.value = 0
    connect()
  }

  function close() {
    wanted = false
    clearTimeout(timer)
    source?.close()
    source = null
    status.value = 'closed'
  }

  onScopeDispose(close)
  return { status, attempts, open, close }
}
