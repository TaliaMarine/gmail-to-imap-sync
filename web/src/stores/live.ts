import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useEventStream } from '../composables/useEventStream'
import type { SessionSummary } from '../services/client'
import { isActive } from '../utils/status'

/** Applies one SSE event to a session map; returns what changed. Pure, so it is unit tested. */
export function applyLiveEvent(
  sessions: Record<string, SessionSummary>,
  name: string,
  data: unknown,
): { sessions: Record<string, SessionSummary>; finished?: SessionSummary } {
  if (name === 'snapshot') {
    const list = (data as { sessions?: SessionSummary[] } | undefined)?.sessions ?? []
    return { sessions: Object.fromEntries(list.filter((s) => isActive(s.status)).map((s) => [s.id, s])) }
  }
  if (name === 'session') {
    const s = data as SessionSummary | undefined
    if (!s?.id) return { sessions }
    if (isActive(s.status)) return { sessions: { ...sessions, [s.id]: s } }
    const { [s.id]: _removed, ...rest } = sessions
    return { sessions: rest, finished: s }
  }
  return { sessions }
}

/**
 * Live session state from the server-sent event stream. Views read
 * `activeFor(accountId)` and watch `finishedTick` to reload their data when a
 * session ends.
 */
export const useLiveStore = defineStore('live', () => {
  const sessions = ref<Record<string, SessionSummary>>({})
  const finishedTick = ref(0)
  const lastFinished = ref<SessionSummary | null>(null)

  const stream = useEventStream('/api/v1/events', {
    events: ['snapshot', 'session'],
    onEvent(name, data) {
      const r = applyLiveEvent(sessions.value, name, data)
      sessions.value = r.sessions
      if (r.finished) {
        lastFinished.value = r.finished
        finishedTick.value++
      }
    },
  })

  const list = computed(() => Object.values(sessions.value))
  const activeFor = (accountId: string) => list.value.find((s) => s.account_id === accountId)

  return { sessions, list, activeFor, finishedTick, lastFinished, status: stream.status, connect: stream.open, disconnect: stream.close }
})
