<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import StateChip from '../components/StateChip.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { api, type Failure, type SessionEvent } from '../services/client'
import { elapsedMs, formatBytes, formatDateTime, formatDuration, formatNumber } from '../utils/format'
import { failureCategories, sessionStates, syncTypeLabels, triggerLabels } from '../utils/status'

const route = useRoute()
const id = computed(() => String(route.params.id))
const session = useLoader(() => api.session(id.value))
const accounts = useLoader(() => api.accounts())

const events = ref<SessionEvent[]>([])
const moreEvents = ref(false)
const eventsError = ref('')
const failures = ref<Failure[]>([])

async function loadEvents(after = 0) {
  try {
    const batch = await api.sessionEvents(id.value, { after, limit: 100 })
    events.value = after ? [...events.value, ...batch] : batch
    moreEvents.value = batch.length === 100
  } catch (e) {
    eventsError.value = errorText(e)
  }
}
loadEvents()

// Failures recorded by this session. The API filters by account only, so the
// session is matched here (see NOTES.md).
async function loadFailures(accountId: string) {
  try {
    const r = await api.failures({ account: accountId, limit: 100 })
    failures.value = (r.items ?? []).filter((f) => f.last_session_id === id.value)
  } catch {
    failures.value = []
  }
}
const stop = computed(() => session.data.value?.account_id)
watch(stop, (a) => a && loadFailures(a), { immediate: true })

const account = computed(() => accounts.data.value?.find((a) => a.id === session.data.value?.account_id))
const errorSummary = computed(() => {
  const m = new Map<string, number>()
  for (const f of failures.value) m.set(f.category, (m.get(f.category) ?? 0) + 1)
  return [...m.entries()].sort((a, b) => b[1] - a[1])
})
const sevIcon: Record<string, string> = { debug: 'mdi-bug-outline', info: 'mdi-information-outline', warning: 'mdi-alert-outline', error: 'mdi-alert-circle-outline' }
const sevColor: Record<string, string | undefined> = { debug: undefined, info: undefined, warning: 'warning', error: 'error' }
</script>

<template>
  <div class="gas-page">
    <v-btn variant="text" prepend-icon="mdi-arrow-left" to="/history" class="mb-2 ml-n3">All sessions</v-btn>
    <v-alert v-if="session.error.value" type="error" variant="tonal">{{ session.error.value }}</v-alert>

    <template v-if="session.data.value">
      <div class="d-flex align-center flex-wrap ga-3 mb-4">
        <h1 class="text-h5">{{ syncTypeLabels[session.data.value.sync_type] }}</h1>
        <StateChip :state="sessionStates[session.data.value.status]!" />
        <span class="gas-muted">{{ account?.name }} · {{ triggerLabels[session.data.value.trigger_type] }} · {{ formatDateTime(session.data.value.started_at ?? session.data.value.created_at) }}</span>
      </div>

      <v-alert v-if="session.data.value.last_error" type="error" variant="tonal" class="mb-4">{{ session.data.value.last_error }}</v-alert>

      <v-card class="pa-4 mb-6">
        <dl class="stats tnum">
          <div><dt>Found</dt><dd>{{ formatNumber(session.data.value.discovered) }}</dd></div>
          <div><dt>Handled</dt><dd>{{ formatNumber(session.data.value.processed) }}</dd></div>
          <div><dt>Copied</dt><dd>{{ formatNumber(session.data.value.copied) }}</dd></div>
          <div><dt>Already there or skipped</dt><dd>{{ formatNumber(session.data.value.skipped) }}</dd></div>
          <div><dt>Failed</dt><dd :class="{ 'text-error': session.data.value.failed > 0 }">{{ formatNumber(session.data.value.failed) }}</dd></div>
          <div><dt>Recovered</dt><dd>{{ formatNumber(session.data.value.recovered) }}</dd></div>
          <div><dt>Folders created</dt><dd>{{ formatNumber(session.data.value.folders_created) }}</dd></div>
          <div><dt>Gmail requests</dt><dd>{{ formatNumber(session.data.value.api_requests) }}</dd></div>
          <div><dt>Data copied</dt><dd>{{ formatBytes(session.data.value.bytes_copied) }}</dd></div>
          <div><dt>Duration</dt><dd>{{ formatDuration(session.data.value.duration_ms ?? elapsedMs(session.data.value.started_at)) }}</dd></div>
          <div>
            <dt>Gmail history</dt>
            <dd class="mono">
              {{ session.data.value.starting_history_id ?? '–' }} → {{ session.data.value.ending_history_id ?? '–' }}
            </dd>
          </div>
        </dl>
      </v-card>

      <div class="two">
        <section aria-labelledby="tl-h">
          <h2 id="tl-h" class="text-h6 mb-3">What happened</h2>
          <v-alert v-if="eventsError" type="error" variant="tonal">{{ eventsError }}</v-alert>
          <v-card>
            <p v-if="!events.length" class="pa-4 gas-muted mb-0">No events recorded.</p>
            <v-list v-else density="compact">
              <v-list-item v-for="e in events" :key="e.id" :title="e.message" :subtitle="formatDateTime(e.created_at)">
                <template #prepend><v-icon :icon="sevIcon[e.severity]" :color="sevColor[e.severity]" size="small" aria-hidden="true" /></template>
              </v-list-item>
            </v-list>
            <div v-if="moreEvents" class="pa-3 text-center"><v-btn @click="loadEvents(events[events.length - 1]?.id)">Show more events</v-btn></div>
          </v-card>
        </section>

        <section aria-labelledby="fl-h">
          <h2 id="fl-h" class="text-h6 mb-3">Failed messages</h2>
          <v-card>
            <p v-if="!failures.length" class="pa-4 gas-muted mb-0">This session left no failed messages waiting for retry.</p>
            <template v-else>
              <div class="d-flex flex-wrap ga-2 pa-3">
                <v-chip v-for="[cat, n] in errorSummary" :key="cat" label color="error" variant="tonal">{{ failureCategories[cat] ?? cat }}: {{ n }}</v-chip>
              </div>
              <v-list lines="two">
                <v-list-item v-for="f in failures" :key="f.id" :to="{ path: '/failures', query: { id: f.id } }" :title="f.subject || '(no subject)'" :subtitle="f.description" />
              </v-list>
            </template>
          </v-card>
        </section>
      </div>
    </template>
  </div>
</template>

<style scoped>
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 14px; margin: 0; }
.stats div { border-left: 2px solid rgb(var(--v-border-color)); padding-left: 10px; }
.stats dt { font-size: 0.8125rem; color: rgb(var(--v-theme-on-surface-variant)); }
.stats dd { margin: 0; font-weight: 600; }
.two { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 16px; align-items: start; }
</style>
