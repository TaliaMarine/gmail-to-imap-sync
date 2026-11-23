<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import GoogleSetup from '../components/GoogleSetup.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type Settings } from '../services/client'
import { formatDuration, formatRelative } from '../utils/format'

const status = useLoader(() => api.systemStatus())
const settings = useLoader(() => api.settings())

const form = reactive({ session_retention_days: 365, event_retention_days: 90, webhook: '' })
const saving = ref(false)
const error = ref('')
const googleOpen = ref(false)

watch(() => settings.data.value, (s) => {
  if (!s) return
  form.session_retention_days = s.session_retention_days
  form.event_retention_days = s.event_retention_days
}, { immediate: true })

function base(s: Settings) {
  return { oauth_client_id: s.oauth_client_id, session_retention_days: form.session_retention_days, event_retention_days: form.event_retention_days }
}

async function save(extra: { notification_webhook?: string } = {}) {
  const s = settings.data.value
  if (!s) return
  saving.value = true
  error.value = ''
  try {
    settings.data.value = await api.updateSettings({ ...base(s), ...extra })
    form.webhook = ''
    notify('Saved settings')
  } catch (e) {
    error.value = errorText(e)
  } finally {
    saving.value = false
  }
}
const saveRetention = () => save()
const saveWebhook = () => save({ notification_webhook: form.webhook.trim() })
const removeWebhook = () => save({ notification_webhook: '' })

function uptime(s: number): string {
  return formatDuration(s * 1000)
}
</script>

<template>
  <div class="gas-page">
    <div class="d-flex align-center mb-4">
      <h1 class="text-h5">System</h1>
      <v-spacer />
      <v-btn prepend-icon="mdi-refresh" :loading="status.loading.value" @click="status.reload">Refresh</v-btn>
    </div>

    <v-alert v-if="status.error.value" type="error" variant="tonal" class="mb-4">
      {{ status.error.value }} Check that the server is running, then refresh.
    </v-alert>

    <section aria-labelledby="st-h" class="mb-8">
      <h2 id="st-h" class="text-h6 mb-3">Status</h2>
      <v-card v-if="status.data.value">
        <v-list lines="two">
          <v-list-item title="Version"><template #subtitle><span class="mono">{{ status.data.value.version }}</span></template></v-list-item>
          <v-list-item title="Mode"><template #subtitle><span class="mono">{{ status.data.value.mode }}</span></template></v-list-item>
          <v-list-item title="Uptime" :subtitle="uptime(status.data.value.uptime_seconds)" />
          <v-list-item title="Database">
            <template #subtitle>
              <v-icon :icon="status.data.value.database.ok ? 'mdi-check-circle-outline' : 'mdi-close-circle-outline'" :color="status.data.value.database.ok ? 'success' : 'error'" size="small" aria-hidden="true" />
              {{ status.data.value.database.ok ? 'Connected' : `Unavailable${status.data.value.database.error ? `: ${status.data.value.database.error}` : ''}` }}
            </template>
          </v-list-item>
          <v-list-item title="Syncs" :subtitle="`${status.data.value.sessions_running} running, ${status.data.value.sessions_queued} waiting`" />
          <v-list-item title="Last successful sync" :subtitle="status.data.value.last_successful_session ? formatRelative(status.data.value.last_successful_session) : 'No sync has completed yet'" />
          <v-list-item title="Accounts needing Google reauthorisation">
            <template #subtitle>
              <template v-if="status.data.value.oauth_needs_reauth === 0">None</template>
              <router-link v-else to="/accounts" class="text-error">{{ status.data.value.oauth_needs_reauth }}: open accounts to reauthorise</router-link>
            </template>
          </v-list-item>
        </v-list>
      </v-card>
      <p v-else-if="!status.error.value" class="gas-muted">Loading system status…</p>
    </section>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-4">{{ error }}</v-alert>

    <template v-if="settings.data.value">
      <section aria-labelledby="g-h" class="mb-8">
        <h2 id="g-h" class="text-h6 mb-3">Google sign-in</h2>
        <v-card class="pa-5">
          <div class="d-flex align-center flex-wrap ga-3">
            <v-chip :color="settings.data.value.configured ? 'success' : 'warning'" variant="tonal" label>
              <v-icon :icon="settings.data.value.configured ? 'mdi-check-circle-outline' : 'mdi-alert-outline'" start size="small" aria-hidden="true" />
              {{ settings.data.value.configured ? 'Google OAuth client is set up' : 'Not set up yet' }}
            </v-chip>
            <span v-if="settings.data.value.oauth_client_id" class="mono">{{ settings.data.value.oauth_client_id }}</span>
            <v-spacer />
            <v-btn @click="googleOpen = !googleOpen">{{ googleOpen || !settings.data.value.configured ? 'Hide steps' : settings.data.value.configured ? 'Change client' : 'Set up' }}</v-btn>
          </div>
          <div v-if="googleOpen || !settings.data.value.configured" class="mt-4">
            <GoogleSetup :settings="settings.data.value" @saved="settings.data.value = $event; googleOpen = false" />
          </div>
        </v-card>
      </section>

      <section aria-labelledby="r-h" class="mb-8">
        <h2 id="r-h" class="text-h6 mb-3">History retention</h2>
        <v-card class="pa-5">
          <v-form class="ret" @submit.prevent="saveRetention">
            <v-text-field v-model.number="form.session_retention_days" type="number" min="1" max="3650" label="Keep session summaries (days)" />
            <v-text-field v-model.number="form.event_retention_days" type="number" min="1" max="3650" label="Keep session events (days)" />
            <v-btn type="submit" color="primary" variant="flat" :loading="saving">Save retention</v-btn>
          </v-form>
          <p class="gas-muted mt-3 mb-0">Failed messages stay until they are recovered or you clear them. Copied messages are never recorded here.</p>
        </v-card>
      </section>

      <section aria-labelledby="w-h" class="mb-8">
        <h2 id="w-h" class="text-h6 mb-3">Notifications</h2>
        <v-card class="pa-5">
          <p class="gas-muted">
            Sends a small JSON message to a web address when an account needs reauthorising or syncs keep failing. Works with ntfy, Slack and Discord incoming webhooks.
          </p>
          <v-form class="hook" @submit.prevent="saveWebhook">
            <v-text-field
              v-model="form.webhook"
              label="Webhook URL"
              type="password"
              autocomplete="off"
              :placeholder="settings.data.value?.notification_webhook_set ? 'Stored. Enter a new URL to replace it.' : 'https://ntfy.example.com/archive'"
              hint="The address is never shown again once saved."
              persistent-hint
            />
            <v-btn type="submit" color="primary" variant="flat" :loading="saving" :disabled="!form.webhook.trim()">Save webhook</v-btn>
            <v-btn v-if="settings.data.value?.notification_webhook_set" color="error" :loading="saving" @click="removeWebhook">Remove webhook</v-btn>
          </v-form>
        </v-card>
      </section>
    </template>
  </div>
</template>

<style scoped>
.ret { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; align-items: start; }
.hook { display: grid; gap: 12px; max-width: 560px; justify-items: start; }
.hook > :first-child { justify-self: stretch; }
</style>
