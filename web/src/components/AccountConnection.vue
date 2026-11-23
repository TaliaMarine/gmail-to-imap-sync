<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { errorText } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type AccountSummary, type AccountTest, type Destination, type Settings } from '../services/client'
import { formatDateTime } from '../utils/format'
import { accountStates } from '../utils/status'
import StateChip from './StateChip.vue'

const props = defineProps<{ account: AccountSummary; settings?: Settings; destinations: Destination[] }>()
const emit = defineEmits<{ changed: [] }>()

const state = computed(() => accountStates[props.account.status] ?? accountStates.error!)
const starting = ref(false)
const testing = ref(false)
const result = ref<AccountTest>()
const destination = ref(props.account.destination_id ?? '')
const saving = ref(false)

watch(() => props.account.destination_id, (v) => (destination.value = v ?? ''))

const needsAuth = computed(() => props.account.status === 'unauthorised' || props.account.status === 'needs_reauth')

async function authorise() {
  starting.value = true
  try {
    const r = await api.startOAuth(props.account.id)
    window.location.assign(r.url)
  } catch (e) {
    notify(errorText(e), 'error')
    starting.value = false
  }
}

async function test() {
  testing.value = true
  result.value = undefined
  try {
    result.value = await api.testAccount(props.account.id)
    emit('changed') // the test records the status
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    testing.value = false
  }
}

async function saveDestination() {
  saving.value = true
  try {
    await api.updateAccount(props.account.id, { name: props.account.name, destination_id: destination.value || undefined })
    notify('Saved destination')
    emit('changed')
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="stack">
    <v-card class="pa-4">
      <h2 class="text-h6 mb-2">Google access</h2>
      <div class="d-flex align-center flex-wrap ga-3">
        <StateChip :state="state" />
        <span v-if="account.gmail_address" class="mono">{{ account.gmail_address }}</span>
        <span class="gas-muted">Last checked {{ formatDateTime(account.last_connection_check) }}</span>
        <span v-if="account.authorised_at" class="gas-muted">Google access granted {{ formatDateTime(account.authorised_at) }}</span>
      </div>
      <p v-if="account.status_error" class="text-error mt-3 mb-0">{{ account.status_error }}</p>
      <p class="gas-muted mt-3 lead">
        The app asks Google for read-only access to Gmail. It can never change or delete your mail.
      </p>
      <v-alert v-if="settings && !settings.configured" type="warning" variant="tonal" class="mb-3">
        The Google OAuth client is not set up yet. <router-link to="/system">Set it up under System</router-link>, then authorise this account.
      </v-alert>
      <v-btn
        :color="needsAuth ? 'primary' : undefined"
        :variant="needsAuth ? 'flat' : 'text'"
        prepend-icon="mdi-google"
        :loading="starting"
        :disabled="settings && !settings.configured"
        @click="authorise"
      >
        {{ account.status === 'unauthorised' ? 'Authorise with Google' : 'Reauthorise with Google' }}
      </v-btn>
    </v-card>

    <v-card class="pa-4">
      <h2 class="text-h6 mb-3">Destination</h2>
      <div class="d-flex align-start ga-3 flex-wrap">
        <v-select
          v-model="destination"
          label="IMAP server"
          class="dest-select"
          :items="destinations.map((d) => ({ title: d.name, value: d.id }))"
          hide-details
        />
        <v-btn :loading="saving" :disabled="destination === (account.destination_id ?? '')" @click="saveDestination">Save destination</v-btn>
      </div>
      <p v-if="!destinations.length" class="gas-muted mt-2 mb-0">No destinations yet. <router-link to="/destinations">Add one</router-link>.</p>
    </v-card>

    <v-card class="pa-4">
      <div class="d-flex align-center mb-3">
        <h2 class="text-h6">Test the connection</h2>
        <v-spacer />
        <v-btn prepend-icon="mdi-lan-connect" :loading="testing" @click="test">Test connection</v-btn>
      </div>
      <p v-if="!result" class="gas-muted mb-0">Checks Gmail and the IMAP server separately, so you can see which one needs attention.</p>
      <div v-else class="results" data-testid="account-test">
        <v-alert :type="result.gmail.ok ? 'success' : 'error'" variant="tonal">
          <strong>Gmail:</strong>
          <template v-if="result.gmail.ok"> signed in as <span class="mono">{{ result.gmail.email }}</span>.</template>
          <template v-else> {{ result.gmail.error }}</template>
        </v-alert>
        <v-alert :type="result.imap.ok ? 'success' : 'error'" variant="tonal">
          <strong>IMAP:</strong>
          <template v-if="result.imap.ok"> connected. {{ result.imap.folders?.length ?? 0 }} folders found.</template>
          <template v-else> {{ result.imap.error }}</template>
        </v-alert>
      </div>
    </v-card>
  </div>
</template>

<style scoped>
.stack { display: grid; gap: 16px; }
.dest-select { max-width: 360px; }
.results { display: grid; gap: 12px; }
</style>
