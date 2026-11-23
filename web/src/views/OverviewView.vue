<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import ActivityChart from '../components/ActivityChart.vue'
import EmptyState from '../components/EmptyState.vue'
import GoogleSetup from '../components/GoogleSetup.vue'
import SessionRail from '../components/SessionRail.vue'
import StateChip from '../components/StateChip.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type AccountOverview, type Settings } from '../services/client'
import { useLiveStore } from '../stores/live'
import { formatDateTime, formatDuration, formatRelative } from '../utils/format'
import { accountStates, failureCategories, sessionStates, syncTypeLabels } from '../utils/status'

const router = useRouter()
const live = useLiveStore()
const settingsOverride = ref<Settings>()
const overview = useLoader(() => api.overview())
const settings = useLoader(() => api.settings())
const busy = ref('')

const configured = computed(() => (settingsOverride.value ?? settings.data.value)?.configured ?? true)
const accounts = computed(() => overview.data.value?.accounts ?? [])
const pending = computed(() => overview.data.value?.pending_failures ?? 0)

watch(() => live.finishedTick, () => overview.reload())

function activeSession(a: AccountOverview) {
  return live.activeFor(a.id) ?? a.active_session
}

async function syncNow(a: AccountOverview) {
  busy.value = a.id
  try {
    const r = await api.startSync(a.id, { type: 'incremental' })
    notify(r.already_active ? 'A sync is already running for this account' : 'Sync started', r.already_active ? 'info' : 'success')
    router.push({ name: 'sync', query: { account: a.id } })
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}

async function retryAll() {
  busy.value = 'retry'
  try {
    const r = await api.retryFailures({ all: true })
    notify(`Retrying ${r.reset} failed ${r.reset === 1 ? 'message' : 'messages'}`)
    await overview.reload()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}

function canSync(a: AccountOverview) {
  return a.status === 'ok' && !!a.destination_id && !activeSession(a)
}
</script>

<template>
  <div class="gas-page">
    <div class="d-flex align-center flex-wrap ga-2 mb-4">
      <h1 class="text-h5">Overview</h1>
      <v-spacer />
      <v-btn v-if="pending > 0" prepend-icon="mdi-reload" :loading="busy === 'retry'" @click="retryAll">
        Retry {{ pending }} failed {{ pending === 1 ? 'message' : 'messages' }}
      </v-btn>
      <v-btn prepend-icon="mdi-history" to="/history">View session history</v-btn>
    </div>

    <v-alert v-if="overview.error.value" type="error" variant="tonal" class="mb-4">
      {{ overview.error.value }}
      <template #append><v-btn @click="overview.reload">Try again</v-btn></template>
    </v-alert>

    <v-card v-if="!configured && (settingsOverride ?? settings.data.value)" class="pa-5 mb-6" data-testid="setup-wizard">
      <h2 class="text-h6 mb-1">Connect your Google project</h2>
      <p class="gas-muted lead mb-4">
        Before you can add a Gmail account, set up a Google OAuth client. It takes a few minutes and only needs to be done once.
      </p>
      <GoogleSetup :settings="(settingsOverride ?? settings.data.value)!" @saved="settingsOverride = $event" />
    </v-card>

    <EmptyState
      v-if="overview.data.value && accounts.length === 0 && configured"
      icon="mdi-email-sync-outline"
      title="No accounts yet"
      body="Connect a Gmail account to start archiving it to your IMAP server."
    >
      <v-btn color="primary" variant="flat" prepend-icon="mdi-plus" :to="{ path: '/accounts', query: { new: '1' } }">Add an account</v-btn>
    </EmptyState>

    <section v-if="accounts.length" aria-labelledby="acc-h">
      <h2 id="acc-h" class="text-h6 mb-3">Accounts</h2>
      <div class="grid">
        <v-card v-for="a in accounts" :key="a.id" class="pa-4" :data-account="a.name">
          <div class="d-flex align-start ga-2">
            <div class="min-0">
              <router-link :to="`/accounts/${a.id}`" class="title">{{ a.name }}</router-link>
              <div class="gas-muted">{{ a.gmail_address || 'Not authorised yet' }}</div>
            </div>
            <v-spacer />
            <StateChip :state="accountStates[a.status] ?? accountStates.error!" />
          </div>

          <p v-if="a.status_error" class="text-error mt-2 mb-0">{{ a.status_error }}</p>

          <dl class="facts mt-3">
            <div><dt>Destination</dt><dd>{{ a.destination_name || 'None chosen' }}</dd></div>
            <div><dt>Last successful sync</dt><dd :title="formatDateTime(a.last_successful_sync)">{{ formatRelative(a.last_successful_sync) }}</dd></div>
            <div>
              <dt>Next sync</dt>
              <dd :title="formatDateTime(a.next_scheduled_sync)">
                {{ a.next_scheduled_sync ? formatRelative(a.next_scheduled_sync) : a.sync_enabled ? 'When the account is ready' : 'Scheduling is off' }}
              </dd>
            </div>
            <div>
              <dt>Failed messages</dt>
              <dd>
                <router-link v-if="a.failures.pending + a.failures.retrying > 0" :to="{ path: '/failures', query: { account: a.id } }">
                  {{ a.failures.pending + a.failures.retrying }} waiting for retry
                </router-link>
                <template v-else>None</template>
              </dd>
            </div>
          </dl>

          <div v-if="activeSession(a)" class="mt-4">
            <SessionRail :session="activeSession(a)!" compact />
          </div>
          <p v-else-if="a.last_session" class="gas-muted mt-3 mb-0 small">
            Last run: {{ syncTypeLabels[a.last_session.sync_type] }},
            {{ sessionStates[a.last_session.status]?.label.toLowerCase() }}
            <template v-if="a.last_session.duration_ms"> in {{ formatDuration(a.last_session.duration_ms) }}</template>
          </p>

          <div class="d-flex flex-wrap ga-2 mt-4">
            <v-btn v-if="a.status === 'needs_reauth' || a.status === 'unauthorised'" color="primary" variant="flat" :to="`/accounts/${a.id}`">
              {{ a.status === 'needs_reauth' ? 'Reauthorise Google' : 'Authorise Google' }}
            </v-btn>
            <v-btn v-else color="primary" variant="flat" prepend-icon="mdi-sync" :disabled="!canSync(a)" :loading="busy === a.id" @click="syncNow(a)">
              Sync now
            </v-btn>
            <v-btn v-if="activeSession(a)" :to="{ name: 'sync', query: { account: a.id } }">Watch progress</v-btn>
          </div>
        </v-card>
      </div>
    </section>

    <section v-if="overview.data.value" class="mt-8" aria-labelledby="act-h">
      <h2 id="act-h" class="text-h6 mb-3">Activity</h2>
      <v-card class="pa-4"><ActivityChart :days="overview.data.value.activity ?? []" /></v-card>
    </section>

    <div v-if="overview.data.value" class="two mt-8">
      <section aria-labelledby="rs-h">
        <h2 id="rs-h" class="text-h6 mb-3">Recent syncs</h2>
        <v-card>
          <p v-if="!overview.data.value.recent_sessions?.length" class="pa-4 gas-muted mb-0">Sessions appear here after the first sync.</p>
          <v-list v-else lines="two">
            <v-list-item v-for="s in overview.data.value.recent_sessions" :key="s.id" :to="`/history/${s.id}`">
              <template #title>
                {{ syncTypeLabels[s.sync_type] }} · {{ formatRelative(s.created_at) }}
              </template>
              <template #subtitle>
                <span class="tnum">{{ s.copied }} copied · {{ s.skipped }} skipped · {{ s.failed }} failed</span>
              </template>
              <template #append><StateChip :state="sessionStates[s.status]!" /></template>
            </v-list-item>
          </v-list>
        </v-card>
      </section>
      <section aria-labelledby="rf-h">
        <h2 id="rf-h" class="text-h6 mb-3">Recent errors</h2>
        <v-card>
          <p v-if="!overview.data.value.recent_failures?.length" class="pa-4 gas-muted mb-0">No failed messages. Everything copied so far went through.</p>
          <v-list v-else lines="two">
            <v-list-item v-for="f in overview.data.value.recent_failures" :key="f.id" :to="{ path: '/failures', query: { id: f.id } }">
              <template #title>{{ f.subject || '(no subject)' }}</template>
              <template #subtitle>{{ failureCategories[f.category] ?? f.category }} · {{ formatRelative(f.last_failed_at) }}</template>
            </v-list-item>
          </v-list>
        </v-card>
      </section>
    </div>
  </div>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 16px; }
.title { font-weight: 600; font-size: 1.0625rem; color: inherit; text-decoration: none; }
.title:hover { text-decoration: underline; }
.min-0 { min-width: 0; }
.facts { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 16px; margin: 0; }
.facts dt { font-size: 0.8125rem; color: rgb(var(--v-theme-on-surface-variant)); }
.facts dd { margin: 0; }
.small { font-size: 0.8125rem; }
.two { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 16px; }
</style>
