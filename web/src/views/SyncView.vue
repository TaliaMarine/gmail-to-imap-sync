<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import SessionRail from '../components/SessionRail.vue'
import StateChip from '../components/StateChip.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type LabelView, type SessionEvent, type SessionSummary } from '../services/client'
import { useLiveStore } from '../stores/live'
import { formatBytes, formatDuration, formatNumber, formatRelative } from '../utils/format'
import { sessionStates, syncTypeLabels } from '../utils/status'

const route = useRoute()
const router = useRouter()
const live = useLiveStore()
const accounts = useLoader(() => api.accounts())

const accountId = computed(() => {
  const q = route.query.account ? String(route.query.account) : ''
  return q || accounts.data.value?.[0]?.id || ''
})
const account = computed(() => accounts.data.value?.find((a) => a.id === accountId.value))
function pick(id: string) {
  router.replace({ query: { account: id } })
}

// What the server says; the live stream overrides it while a session is active.
const current = ref<{ active: boolean; session?: SessionSummary }>()
const events = ref<SessionEvent[]>([])
const busy = ref('')

async function loadCurrent() {
  if (!accountId.value) return
  try {
    current.value = await api.currentSync(accountId.value)
    const s = current.value.session
    events.value = s ? (await api.sessionEvents(s.id, { limit: 200 })).slice(-8).reverse() : []
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
watch(accountId, loadCurrent, { immediate: true })
watch(() => live.finishedTick, loadCurrent)

const liveSession = computed(() => (accountId.value ? live.activeFor(accountId.value) : undefined))
const session = computed(() => liveSession.value ?? current.value?.session)
const active = computed(() => !!liveSession.value || !!current.value?.active)

// Refresh the activity list while something runs.
let timer: ReturnType<typeof setInterval> | undefined
watch(active, (on) => {
  clearInterval(timer)
  if (on) timer = setInterval(loadCurrent, 8000)
}, { immediate: true })
onBeforeUnmount(() => clearInterval(timer))

async function start() {
  busy.value = 'start'
  try {
    const r = await api.startSync(accountId.value, { type: 'incremental' })
    notify(r.already_active ? 'A sync is already running' : 'Sync started', r.already_active ? 'info' : 'success')
    await loadCurrent()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}

const cancelDialog = ref(false)
async function cancel() {
  busy.value = 'cancel'
  try {
    await api.cancelSync(accountId.value)
    notify('Stopping after the current messages')
    cancelDialog.value = false
    await loadCurrent()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}

// Rescan.
const rescan = ref(false)
const rescanDate = ref('')
const rescanLabel = ref('')
const labels = ref<LabelView[]>([])
async function openRescan() {
  rescan.value = true
  try {
    labels.value = (await api.labels(accountId.value)).filter((l) => !l.excluded)
  } catch {
    labels.value = []
  }
}
async function startRescan() {
  busy.value = 'rescan'
  try {
    await api.startSync(accountId.value, { type: 'reconcile', after: rescanDate.value || undefined, label_id: rescanLabel.value || undefined })
    notify('Rescan started')
    rescan.value = false
    await loadCurrent()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}

const canStart = computed(() => account.value?.status === 'ok' && !!account.value.destination_id && !active.value)
const startHint = computed(() => {
  const a = account.value
  if (!a) return ''
  if (a.status !== 'ok') return 'Authorise this account with Google before syncing.'
  if (!a.destination_id) return 'Choose a destination for this account first.'
  return ''
})
const sevIcon: Record<string, string> = { debug: 'mdi-bug-outline', info: 'mdi-information-outline', warning: 'mdi-alert-outline', error: 'mdi-alert-circle-outline' }
const sevColor: Record<string, string> = { debug: '', info: '', warning: 'warning', error: 'error' }
</script>

<template>
  <div class="gas-page">
    <div class="d-flex align-center flex-wrap ga-3 mb-4">
      <h1 class="text-h5">Sync</h1>
      <v-spacer />
      <v-select
        v-if="accounts.data.value?.length"
        :model-value="accountId"
        :items="accounts.data.value.map((a) => ({ title: a.name, value: a.id }))"
        label="Account"
        density="compact"
        hide-details
        class="picker"
        @update:model-value="pick"
      />
    </div>

    <v-alert v-if="accounts.error.value" type="error" variant="tonal">{{ accounts.error.value }}</v-alert>
    <EmptyState
      v-if="accounts.data.value && accounts.data.value.length === 0"
      icon="mdi-sync"
      title="Nothing to sync yet"
      body="Add and authorise a Gmail account, then start a sync to watch mail move to your IMAP server."
    >
      <v-btn color="primary" variant="flat" to="/accounts">Go to accounts</v-btn>
    </EmptyState>

    <template v-if="account">
      <v-card class="pa-5">
        <div class="d-flex align-center flex-wrap ga-3 mb-4">
          <h2 class="text-h6">{{ account.name }}</h2>
          <StateChip v-if="session" :state="sessionStates[session.status]!" />
          <v-spacer />
          <v-btn v-if="active" color="error" prepend-icon="mdi-stop" :loading="busy === 'cancel'" :disabled="session?.cancel_requested" @click="cancelDialog = true">
            {{ session?.cancel_requested ? 'Stopping…' : 'Cancel sync' }}
          </v-btn>
          <template v-else>
            <v-btn prepend-icon="mdi-magnify-scan" :disabled="!canStart" @click="openRescan">Rescan</v-btn>
            <v-btn color="primary" variant="flat" prepend-icon="mdi-sync" :loading="busy === 'start'" :disabled="!canStart" @click="start">Start sync</v-btn>
          </template>
        </div>
        <p v-if="startHint && !active" class="gas-muted">{{ startHint }}</p>

        <SessionRail v-if="session && (active || session.status === 'queued')" :session="session" />
        <EmptyState
          v-else
          icon="mdi-sync"
          title="Nothing is syncing"
          :body="session ? `The last run finished ${formatRelative(session.finished_at)}.` : 'Start a sync to watch mail move from Gmail to your IMAP server.'"
        />

        <dl v-if="session" class="stats tnum">
          <div><dt>Gmail requests</dt><dd>{{ formatNumber(session.api_requests) }}</dd></div>
          <div><dt>Data copied</dt><dd>{{ formatBytes(session.bytes_copied) }}</dd></div>
          <div><dt>Folders created</dt><dd>{{ formatNumber(session.folders_created) }}</dd></div>
          <div v-if="session.recovered"><dt>Recovered failures</dt><dd>{{ formatNumber(session.recovered) }}</dd></div>
          <div v-if="session.duration_ms"><dt>Took</dt><dd>{{ formatDuration(session.duration_ms) }}</dd></div>
        </dl>
        <p v-if="session && !active" class="mt-3 mb-0">
          <router-link :to="`/history/${session.id}`">{{ syncTypeLabels[session.sync_type] }} details</router-link>
        </p>
      </v-card>

      <section v-if="events.length" class="mt-6" aria-labelledby="ev-h">
        <h2 id="ev-h" class="text-h6 mb-2">Recent activity</h2>
        <v-card>
          <v-list density="compact">
            <v-list-item v-for="e in events" :key="e.id" :title="e.message" :subtitle="formatRelative(e.created_at)">
              <template #prepend><v-icon :icon="sevIcon[e.severity]" :color="sevColor[e.severity] || undefined" size="small" aria-hidden="true" /></template>
            </v-list-item>
          </v-list>
        </v-card>
      </section>
    </template>

    <ConfirmDialog v-model="cancelDialog" title="Cancel this sync?" confirm-label="Cancel sync" danger :loading="busy === 'cancel'" @confirm="cancel">
      <p class="mb-0">The sync finishes the messages it is handling now, saves its progress, and stops. Nothing is lost; the next sync continues from there.</p>
    </ConfirmDialog>

    <v-dialog v-model="rescan" max-width="480">
      <v-card>
        <v-card-title class="text-h6 pt-4">Rescan mail</v-card-title>
        <v-card-text>
          <p class="gas-muted">Looks at messages again and copies any that are missing. Messages already on the server are skipped, never duplicated.</p>
          <v-text-field v-model="rescanDate" type="date" label="Received on or after" hint="Leave empty to look at everything since the last sync" persistent-hint />
          <v-select v-model="rescanLabel" class="mt-3" :items="labels.map((l) => ({ title: l.name, value: l.id }))" label="Only this Gmail label" clearable />
        </v-card-text>
        <v-card-actions class="px-4 pb-4">
          <v-spacer />
          <v-btn @click="rescan = false">Cancel</v-btn>
          <v-btn color="primary" variant="flat" :loading="busy === 'rescan'" @click="startRescan">Start rescan</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<style scoped>
.picker { max-width: 260px; }
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 12px; margin: 20px 0 0; }
.stats div { border-left: 2px solid rgb(var(--v-border-color)); padding-left: 10px; }
.stats dt { font-size: 0.8125rem; color: rgb(var(--v-theme-on-surface-variant)); }
.stats dd { margin: 0; font-weight: 600; }
</style>
