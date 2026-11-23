<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import EmptyState from '../components/EmptyState.vue'
import StateChip from '../components/StateChip.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { api, type ListSessionsParams, type SessionSummary } from '../services/client'
import { formatDateTime, formatDuration, formatNumber } from '../utils/format'
import { sessionStates, syncTypeLabels, triggerLabels } from '../utils/status'

const route = useRoute()
const router = useRouter()
const accounts = useLoader(() => api.accounts())

const FILTERS = ['account', 'status', 'trigger', 'type', 'from', 'to'] as const
const filters = computed(() => Object.fromEntries(FILTERS.map((k) => [k, route.query[k] ? String(route.query[k]) : ''])) as Record<(typeof FILTERS)[number], string>)
function setFilter(key: (typeof FILTERS)[number], v: string | null) {
  router.replace({ query: { ...route.query, [key]: v || undefined } })
}

const items = ref<SessionSummary[]>([])
const cursor = ref<string>()
const loading = ref(false)
const error = ref('')
const loaded = ref(false)

async function load(more = false) {
  loading.value = true
  error.value = ''
  try {
    const f = filters.value
    const params: ListSessionsParams = {
      account: f.account || undefined,
      status: (f.status || undefined) as ListSessionsParams['status'],
      trigger: (f.trigger || undefined) as ListSessionsParams['trigger'],
      type: (f.type || undefined) as ListSessionsParams['type'],
      // The date inputs are local days: [from 00:00, to + 1 day).
      from: f.from ? new Date(`${f.from}T00:00:00`).toISOString() : undefined,
      to: f.to ? new Date(new Date(`${f.to}T00:00:00`).getTime() + 86_400_000).toISOString() : undefined,
      cursor: more ? cursor.value : undefined,
      limit: 25,
    }
    const r = await api.sessions(params)
    items.value = more ? [...items.value, ...(r.items ?? [])] : (r.items ?? [])
    cursor.value = r.next_cursor || undefined
    loaded.value = true
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}
watch(() => JSON.stringify(filters.value), () => load(), { immediate: true })

const accountName = (id: string) => accounts.data.value?.find((a) => a.id === id)?.name ?? '–'
const states = Object.entries(sessionStates).map(([value, v]) => ({ title: v.label, value }))
const types = Object.entries(syncTypeLabels).map(([value, title]) => ({ title, value }))
const triggers = Object.entries(triggerLabels).map(([value, title]) => ({ title, value }))
const anyFilter = computed(() => FILTERS.some((k) => filters.value[k]))
</script>

<template>
  <div class="gas-page">
    <h1 class="text-h5 mb-4">History</h1>

    <div class="filters mb-4">
      <v-select :model-value="filters.account" label="Account" :items="(accounts.data.value ?? []).map((a) => ({ title: a.name, value: a.id }))" clearable density="compact" hide-details @update:model-value="setFilter('account', $event)" />
      <v-select :model-value="filters.status" label="Status" :items="states" clearable density="compact" hide-details @update:model-value="setFilter('status', $event)" />
      <v-select :model-value="filters.type" label="Type" :items="types" clearable density="compact" hide-details @update:model-value="setFilter('type', $event)" />
      <v-select :model-value="filters.trigger" label="Started by" :items="triggers" clearable density="compact" hide-details @update:model-value="setFilter('trigger', $event)" />
      <v-text-field :model-value="filters.from" type="date" label="From" density="compact" hide-details @update:model-value="setFilter('from', $event)" />
      <v-text-field :model-value="filters.to" type="date" label="To" density="compact" hide-details @update:model-value="setFilter('to', $event)" />
    </div>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-4">{{ error }}</v-alert>

    <EmptyState
      v-if="loaded && !items.length"
      icon="mdi-history"
      :title="anyFilter ? 'No sessions match' : 'No sessions yet'"
      :body="anyFilter ? 'Try clearing a filter.' : 'Every sync is recorded here, with what it copied and what went wrong.'"
    >
      <v-btn v-if="anyFilter" @click="router.replace({ query: {} })">Clear filters</v-btn>
    </EmptyState>

    <v-card v-if="items.length">
      <v-table class="tnum">
        <thead>
          <tr>
            <th>Started</th><th>Account</th><th>Type</th><th>Started by</th><th>Status</th>
            <th class="text-right">Duration</th><th class="text-right">Found</th><th class="text-right">Copied</th><th class="text-right">Skipped</th><th class="text-right">Failed</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in items" :key="s.id" class="row" data-testid="session-row" @click="router.push(`/history/${s.id}`)">
            <td><router-link :to="`/history/${s.id}`" @click.stop>{{ formatDateTime(s.started_at ?? s.created_at) }}</router-link></td>
            <td>{{ accountName(s.account_id) }}</td>
            <td>{{ syncTypeLabels[s.sync_type] }}</td>
            <td>{{ triggerLabels[s.trigger_type] }}</td>
            <td><StateChip :state="sessionStates[s.status]!" /></td>
            <td class="text-right">{{ formatDuration(s.duration_ms) }}</td>
            <td class="text-right">{{ formatNumber(s.discovered) }}</td>
            <td class="text-right">{{ formatNumber(s.copied) }}</td>
            <td class="text-right">{{ formatNumber(s.skipped) }}</td>
            <td class="text-right" :class="{ 'text-error': s.failed > 0 }">{{ formatNumber(s.failed) }}</td>
          </tr>
        </tbody>
      </v-table>
      <div v-if="cursor" class="pa-3 text-center"><v-btn :loading="loading" @click="load(true)">Show older sessions</v-btn></div>
    </v-card>
  </div>
</template>

<style scoped>
.filters { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; }
.row { cursor: pointer; }
</style>
