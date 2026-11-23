<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import FailureTable from '../components/FailureTable.vue'
import StateChip from '../components/StateChip.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type Failure, type FailureDetail } from '../services/client'
import { formatDateTime, formatRelative } from '../utils/format'
import { failureCategories, failureStates } from '../utils/status'

const route = useRoute()
const router = useRouter()
const accounts = useLoader(() => api.accounts())

const FILTERS = ['account', 'status', 'category', 'search'] as const
const filters = computed(() => Object.fromEntries(FILTERS.map((k) => [k, route.query[k] ? String(route.query[k]) : ''])) as Record<(typeof FILTERS)[number], string>)
function setFilter(key: (typeof FILTERS)[number], v: string | null) {
  router.replace({ query: { ...route.query, [key]: v || undefined } })
}
const searchText = ref(filters.value.search)
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(searchText, (v) => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => setFilter('search', v), 300)
})

const items = ref<Failure[]>([])
const cursor = ref<string>()
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const selected = ref<string[]>([])
const busy = ref('')

async function load(more = false) {
  loading.value = true
  error.value = ''
  try {
    const f = filters.value
    const r = await api.failures({
      account: f.account || undefined,
      status: (f.status || undefined) as 'pending' | 'retrying' | 'ignored' | undefined,
      category: f.category || undefined,
      search: f.search || undefined,
      cursor: more ? cursor.value : undefined,
      limit: 50,
    })
    items.value = more ? [...items.value, ...(r.items ?? [])] : (r.items ?? [])
    cursor.value = r.next_cursor || undefined
    if (!more) selected.value = []
    loaded.value = true
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}
watch(() => JSON.stringify(filters.value), () => load(), { immediate: true })

// Drawer: details of one failure (?id=...). No message body is ever shown.
const detail = ref<FailureDetail>()
const detailError = ref('')
const drawer = computed({
  get: () => !!route.query.id,
  set: (v: boolean) => !v && router.replace({ query: { ...route.query, id: undefined } }),
})
watch(
  () => route.query.id,
  async (id) => {
    detail.value = undefined
    detailError.value = ''
    if (!id) return
    try {
      detail.value = await api.failure(String(id))
    } catch (e) {
      detailError.value = errorText(e)
    }
  },
  { immediate: true },
)
const history = computed(() => {
  const h = detail.value?.error_history
  return Array.isArray(h) ? (h as { at?: string; category?: string; message?: string }[]) : []
})
const open = (f: Failure) => router.replace({ query: { ...route.query, id: f.id } })

async function act(label: string, fn: () => Promise<string>) {
  busy.value = label
  try {
    notify(await fn())
    await load()
    if (detail.value && route.query.id) detail.value = await api.failure(String(route.query.id)).catch(() => undefined)
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}
const retryResult = (r: { reset: number; sessions: { queued: boolean }[] | null }) =>
  `Retrying ${r.reset} ${r.reset === 1 ? 'message' : 'messages'}${r.sessions?.some((s) => !s.queued) ? ' (they run with the sync already in progress)' : ''}`
const retrySelected = () => act('retry', async () => retryResult(await api.retryFailures({ ids: selected.value })))
const retryAll = () => act('retryall', async () => {
  confirmAll.value = false
  return retryResult(await api.retryFailures({ all: true, account_id: filters.value.account || undefined }))
})
const ignoreSelected = () => act('ignore', async () => {
  await Promise.all(selected.value.map((id) => api.ignoreFailure(id)))
  return `Ignored ${selected.value.length} ${selected.value.length === 1 ? 'message' : 'messages'}`
})
const retryOne = (id: string) => act('retry1', async () => retryResult(await api.retryFailure(id)))
const ignoreOne = (id: string) => act('ignore1', async () => (await api.ignoreFailure(id), 'Ignored this message'))
const unignoreOne = (id: string) => act('unignore1', async () => (await api.unignoreFailure(id), 'Moved back to the retry list'))
const confirmAll = ref(false)
const confirmClear = ref(false)
const clearIgnored = () => act('clear', async () => {
  confirmClear.value = false
  const r = await api.clearIgnored(filters.value.account || undefined)
  return `Cleared ${r.cleared} ignored ${r.cleared === 1 ? 'failure' : 'failures'}`
})

const categories = Object.entries(failureCategories).map(([value, title]) => ({ title, value }))
const statuses = Object.entries(failureStates).map(([value, v]) => ({ title: v.label, value }))
const anyFilter = computed(() => FILTERS.some((k) => filters.value[k]))
</script>

<template>
  <div class="gas-page">
    <div class="d-flex align-center flex-wrap ga-2 mb-4">
      <h1 class="text-h5">Failures</h1>
      <v-spacer />
      <v-btn :disabled="!selected.length" :loading="busy === 'retry'" prepend-icon="mdi-reload" @click="retrySelected">Retry selected</v-btn>
      <v-btn :disabled="!selected.length" :loading="busy === 'ignore'" prepend-icon="mdi-eye-off-outline" @click="ignoreSelected">Ignore selected</v-btn>
      <v-btn :disabled="!items.length" prepend-icon="mdi-reload-alert" @click="confirmAll = true">Retry all waiting</v-btn>
      <v-btn color="error" prepend-icon="mdi-broom" @click="confirmClear = true">Clear ignored</v-btn>
    </div>
    <p class="gas-muted lead mb-4">
      A message lands here when it could not be copied. The rest of its sync carried on. Only the subject, sender and date are kept, never the message itself.
    </p>

    <div class="filters mb-4">
      <v-select :model-value="filters.account" label="Account" :items="(accounts.data.value ?? []).map((a) => ({ title: a.name, value: a.id }))" clearable density="compact" hide-details @update:model-value="setFilter('account', $event)" />
      <v-select :model-value="filters.status" label="Status" :items="statuses" clearable density="compact" hide-details @update:model-value="setFilter('status', $event)" />
      <v-select :model-value="filters.category" label="Problem" :items="categories" clearable density="compact" hide-details @update:model-value="setFilter('category', $event)" />
      <v-text-field v-model="searchText" label="Search subject or sender" prepend-inner-icon="mdi-magnify" clearable density="compact" hide-details />
    </div>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-4">{{ error }}</v-alert>

    <EmptyState
      v-if="loaded && !items.length"
      icon="mdi-check-circle-outline"
      :title="anyFilter ? 'No failures match' : 'No failed messages'"
      :body="anyFilter ? 'Try clearing a filter.' : 'Everything that was copied went through. Anything that fails will show up here with the reason.'"
    >
      <v-btn v-if="anyFilter" @click="router.replace({ query: {} })">Clear filters</v-btn>
    </EmptyState>

    <v-card v-if="items.length">
      <FailureTable v-model:selected="selected" :items="items" @open="open" />
      <div v-if="cursor" class="pa-3 text-center"><v-btn :loading="loading" @click="load(true)">Show more</v-btn></div>
    </v-card>

    <v-navigation-drawer v-model="drawer" location="right" temporary width="440" aria-label="Failure details">
      <div v-if="detailError" class="pa-4"><v-alert type="error" variant="tonal">{{ detailError }}</v-alert></div>
      <div v-else-if="detail" class="pa-5 drawer">
        <div class="d-flex align-start ga-2">
          <h2 class="text-h6">{{ detail.subject || '(no subject)' }}</h2>
          <v-spacer />
          <v-btn icon="mdi-close" size="small" aria-label="Close details" @click="drawer = false" />
        </div>
        <StateChip :state="failureStates[detail.status] ?? failureStates.pending!" class="my-2" />
        <dl class="kv">
          <dt>From</dt><dd>{{ detail.sender || '–' }}</dd>
          <dt>To</dt><dd>{{ detail.recipients?.join(', ') || '–' }}</dd>
          <dt>Date</dt><dd>{{ formatDateTime(detail.message_date) }}</dd>
          <dt>Gmail message</dt><dd class="mono">{{ detail.gmail_message_id }}</dd>
          <dt>Attempts</dt><dd>{{ detail.attempt_count }}</dd>
          <dt>Next retry</dt>
          <dd>
            <template v-if="detail.status === 'ignored'">Never (ignored)</template>
            <template v-else-if="detail.retry_exhausted">Automatic retries used up. Retry by hand.</template>
            <template v-else-if="!detail.auto_retry">Not automatic: fix the cause, then retry by hand</template>
            <template v-else>{{ detail.next_retry_at ? formatRelative(detail.next_retry_at) : 'At the next sync' }}</template>
          </dd>
        </dl>

        <h3 class="text-subtitle-1 mt-4 mb-1">What went wrong</h3>
        <p class="mb-1"><strong>{{ failureCategories[detail.category] ?? detail.category }}.</strong> {{ detail.description }}</p>
        <p class="gas-muted small mono">{{ detail.error_message }}</p>

        <h3 class="text-subtitle-1 mt-4 mb-1">Folders</h3>
        <ul class="plain">
          <li v-for="f in detail.completed_folders ?? []" :key="'c' + f"><v-icon icon="mdi-check" color="success" size="small" aria-label="Delivered" /> <span class="mono">{{ f }}</span> delivered</li>
          <li v-for="f in detail.failed_folders ?? []" :key="'f' + f"><v-icon icon="mdi-close" color="error" size="small" aria-label="Not delivered" /> <span class="mono">{{ f }}</span> not delivered</li>
          <li v-if="!(detail.completed_folders?.length || detail.failed_folders?.length)" class="gas-muted">The message could not be read, so no folders were tried.</li>
        </ul>

        <h3 class="text-subtitle-1 mt-4 mb-1">Attempts</h3>
        <ol v-if="history.length" class="plain hist">
          <li v-for="(h, i) in history" :key="i">
            <span class="gas-muted small">{{ formatDateTime(h.at) }}</span>
            <div>{{ failureCategories[h.category ?? ''] ?? h.category }}: {{ h.message }}</div>
          </li>
        </ol>
        <p v-else class="gas-muted">No earlier attempts recorded.</p>

        <div class="d-flex flex-wrap ga-2 mt-5">
          <v-btn color="primary" variant="flat" prepend-icon="mdi-reload" :loading="busy === 'retry1'" @click="retryOne(detail.id)">Retry</v-btn>
          <v-btn v-if="detail.status !== 'ignored'" prepend-icon="mdi-eye-off-outline" :loading="busy === 'ignore1'" @click="ignoreOne(detail.id)">Ignore</v-btn>
          <v-btn v-else prepend-icon="mdi-eye-outline" :loading="busy === 'unignore1'" @click="unignoreOne(detail.id)">Stop ignoring</v-btn>
        </div>
      </div>
    </v-navigation-drawer>

    <ConfirmDialog v-model="confirmAll" title="Retry every waiting failure?" confirm-label="Retry all" :loading="busy === 'retryall'" @confirm="retryAll">
      <p class="mb-0">Every failed message{{ filters.account ? ' of this account' : '' }} that is not ignored is tried again. Messages already delivered are never copied twice.</p>
    </ConfirmDialog>
    <ConfirmDialog v-model="confirmClear" title="Clear ignored failures?" confirm-label="Clear ignored" danger :loading="busy === 'clear'" @confirm="clearIgnored">
      <p class="mb-0">The ignored records are removed from this list. The messages themselves are not touched anywhere.</p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.filters { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
.drawer { overflow-wrap: anywhere; }
.kv { display: grid; grid-template-columns: 110px 1fr; gap: 6px 12px; margin: 8px 0 0; }
.kv dt { color: rgb(var(--v-theme-on-surface-variant)); font-size: 0.8125rem; }
.kv dd { margin: 0; }
.plain { list-style: none; padding: 0; margin: 0; display: grid; gap: 4px; }
.hist li { border-left: 2px solid rgb(var(--v-border-color)); padding-left: 10px; margin-bottom: 8px; }
.small { font-size: 0.8125rem; }
</style>
