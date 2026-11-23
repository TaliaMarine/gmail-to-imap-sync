<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import AccountConnection from '../components/AccountConnection.vue'
import AccountLabels from '../components/AccountLabels.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import StateChip from '../components/StateChip.vue'
import SyncSettingsForm from '../components/SyncSettingsForm.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type SyncSettings } from '../services/client'
import { accountStates, oauthErrorMessage } from '../utils/status'
import { clone, describeChanges, isDirty } from '../utils/syncSettings'

const route = useRoute()
const router = useRouter()
const id = computed(() => String(route.params.id))

const account = useLoader(() => api.account(id.value))
const settings = useLoader(() => api.settings())
const destinations = useLoader(() => api.destinations())
const server = useLoader(() => api.syncSettings(id.value))

const draft = ref<SyncSettings>()
watch(() => server.data.value, (v) => v && (draft.value = clone(v)), { immediate: true })

const tab = computed({
  get: () => String(route.query.tab ?? 'connection'),
  set: (v: string) => router.replace({ query: { ...route.query, tab: v, oauth_error: undefined } }),
})
const oauthError = computed(() => (route.query.oauth_error ? oauthErrorMessage(String(route.query.oauth_error)) : ''))
const dirty = computed(() => isDirty(server.data.value, draft.value))
const changes = computed(() => (server.data.value && draft.value ? describeChanges(server.data.value, draft.value) : []))

const confirm = ref(false)
const saving = ref(false)
const saveError = ref('')

async function save() {
  if (!draft.value) return
  saving.value = true
  saveError.value = ''
  try {
    const saved = await api.updateSyncSettings(id.value, draft.value)
    server.data.value = saved
    draft.value = clone(saved)
    confirm.value = false
    notify('Saved settings')
  } catch (e) {
    saveError.value = errorText(e)
    confirm.value = false
  } finally {
    saving.value = false
  }
}
function discard() {
  if (server.data.value) draft.value = clone(server.data.value)
}

// Unsaved changes: guard in-app navigation and tab close.
onBeforeRouteLeave(() => (dirty.value ? window.confirm('You have unsaved changes. Leave without saving?') : true))
const beforeUnload = (e: BeforeUnloadEvent) => {
  if (dirty.value) e.preventDefault()
}
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

const reloadAccount = () => account.reload()
const showSave = computed(() => tab.value !== 'connection')
</script>

<template>
  <div class="gas-page">
    <v-btn variant="text" prepend-icon="mdi-arrow-left" to="/accounts" class="mb-2 ml-n3">All accounts</v-btn>

    <v-alert v-if="account.error.value" type="error" variant="tonal">{{ account.error.value }}</v-alert>
    <template v-if="account.data.value">
      <div class="d-flex align-center flex-wrap ga-3 mb-4">
        <h1 class="text-h5">{{ account.data.value.name }}</h1>
        <StateChip :state="accountStates[account.data.value.status] ?? accountStates.error!" />
        <v-spacer />
        <v-btn prepend-icon="mdi-sync" :to="{ name: 'sync', query: { account: id } }">Open sync page</v-btn>
      </div>

      <v-alert v-if="oauthError" type="error" variant="tonal" closable class="mb-4" data-testid="oauth-error" @click:close="tab = tab">
        {{ oauthError }}
      </v-alert>
      <v-alert v-if="saveError" type="error" variant="tonal" closable class="mb-4" @click:close="saveError = ''">{{ saveError }}</v-alert>

      <v-tabs v-model="tab" color="primary" class="mb-4">
        <v-tab value="connection">Connection</v-tab>
        <v-tab value="labels">Labels and folders</v-tab>
        <v-tab value="schedule">Schedule</v-tab>
        <v-tab value="advanced">Advanced</v-tab>
      </v-tabs>

      <AccountConnection v-if="tab === 'connection'" :account="account.data.value" :settings="settings.data.value" :destinations="destinations.data.value ?? []" @changed="reloadAccount" />
      <template v-else-if="draft">
        <AccountLabels v-if="tab === 'labels'" :account-id="id" :draft="draft" :authorised="account.data.value.status !== 'unauthorised'" @changed="() => {}" />
        <SyncSettingsForm v-else :draft="draft" :section="tab === 'advanced' ? 'advanced' : 'schedule'" />
      </template>
      <p v-else-if="server.loading.value" class="gas-muted">Loading settings…</p>
      <v-alert v-else-if="server.error.value" type="error" variant="tonal">{{ server.error.value }}</v-alert>

      <v-slide-y-reverse-transition>
        <div v-if="showSave && dirty" class="savebar" role="region" aria-label="Unsaved changes">
          <span>You have unsaved changes.</span>
          <v-spacer />
          <v-btn @click="discard">Discard</v-btn>
          <v-btn color="primary" variant="flat" @click="confirm = true">Review and save</v-btn>
        </div>
      </v-slide-y-reverse-transition>
    </template>

    <ConfirmDialog v-model="confirm" title="Save these changes?" confirm-label="Save changes" :loading="saving" @confirm="save">
      <ul class="mb-3">
        <li v-for="c in changes" :key="c">{{ c }}</li>
      </ul>
      <p class="mb-0 gas-muted">Changes only apply to future syncs, and are additive: nothing already copied is moved or deleted.</p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.savebar {
  position: fixed;
  inset-inline: 0;
  bottom: 0;
  z-index: 5;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 24px;
  background: rgb(var(--v-theme-surface));
  border-top: 1px solid rgb(var(--v-border-color));
}
@media (min-width: 1280px) { .savebar { inset-inline-start: 232px; } }
</style>
