<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import StateChip from '../components/StateChip.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type AccountSummary } from '../services/client'
import { formatRelative } from '../utils/format'
import { accountStates } from '../utils/status'

const route = useRoute()
const router = useRouter()
const accounts = useLoader(() => api.accounts())
const destinations = useLoader(() => api.destinations())

const dialog = ref(false)
const form = reactive({ name: '', destination_id: '' as string })
const saving = ref(false)
const formError = ref('')
const toDelete = ref<AccountSummary>()
const deleting = ref(false)

onMounted(() => {
  if (route.query.new) dialog.value = true
})

async function create() {
  saving.value = true
  formError.value = ''
  try {
    const a = await api.createAccount({ name: form.name.trim(), destination_id: form.destination_id || undefined })
    notify(`Added ${a.name}`)
    dialog.value = false
    router.push({ path: `/accounts/${a.id}`, query: { tab: 'connection' } })
  } catch (e) {
    formError.value = errorText(e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  if (!toDelete.value) return
  deleting.value = true
  try {
    await api.deleteAccount(toDelete.value.id)
    notify(`Removed ${toDelete.value.name}`)
    toDelete.value = undefined
    await accounts.reload()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="gas-page">
    <div class="d-flex align-center mb-4">
      <h1 class="text-h5">Accounts</h1>
      <v-spacer />
      <v-btn color="primary" variant="flat" prepend-icon="mdi-plus" @click="dialog = true">Add account</v-btn>
    </div>

    <v-alert v-if="accounts.error.value" type="error" variant="tonal" class="mb-4">{{ accounts.error.value }}</v-alert>

    <EmptyState
      v-if="accounts.data.value && accounts.data.value.length === 0"
      icon="mdi-account-plus-outline"
      title="No accounts yet"
      body="Add a Gmail account, authorise read-only access, and choose which IMAP server receives the mail."
    >
      <v-btn color="primary" variant="flat" prepend-icon="mdi-plus" @click="dialog = true">Add account</v-btn>
    </EmptyState>

    <v-card v-else-if="accounts.data.value">
      <v-table>
        <thead>
          <tr><th>Account</th><th>Status</th><th>Destination</th><th>Last successful sync</th><th class="text-right">Actions</th></tr>
        </thead>
        <tbody>
          <tr v-for="a in accounts.data.value" :key="a.id">
            <td>
              <router-link :to="`/accounts/${a.id}`" class="fw-600">{{ a.name }}</router-link>
              <div class="gas-muted small">{{ a.gmail_address || 'Not authorised yet' }}</div>
            </td>
            <td><StateChip :state="accountStates[a.status] ?? accountStates.error!" /></td>
            <td>{{ a.destination_name || 'None chosen' }}</td>
            <td>{{ formatRelative(a.last_successful_sync) }}</td>
            <td class="text-right">
              <v-btn size="small" :to="`/accounts/${a.id}`">Open</v-btn>
              <v-btn size="small" color="error" :aria-label="`Remove ${a.name}`" @click="toDelete = a">Remove</v-btn>
            </td>
          </tr>
        </tbody>
      </v-table>
    </v-card>

    <v-dialog v-model="dialog" max-width="480">
      <v-card>
        <v-card-title class="text-h6 pt-4">Add an account</v-card-title>
        <v-form @submit.prevent="create">
          <v-card-text>
            <v-alert v-if="formError" type="error" variant="tonal" class="mb-3">{{ formError }}</v-alert>
            <v-text-field v-model="form.name" label="Name" hint="A label for you, such as “Personal Gmail”" persistent-hint autofocus />
            <v-select
              v-model="form.destination_id"
              class="mt-3"
              label="Destination IMAP server"
              :items="(destinations.data.value ?? []).map((d) => ({ title: d.name, value: d.id }))"
              clearable
              :hint="destinations.data.value?.length ? 'You can change this later' : 'No destinations yet. Add one under Destinations first, or choose later.'"
              persistent-hint
            />
          </v-card-text>
          <v-card-actions class="px-4 pb-4">
            <v-spacer />
            <v-btn @click="dialog = false">Cancel</v-btn>
            <v-btn type="submit" color="primary" variant="flat" :loading="saving" :disabled="!form.name.trim()">Add account</v-btn>
          </v-card-actions>
        </v-form>
      </v-card>
    </v-dialog>

    <ConfirmDialog :model-value="!!toDelete" title="Remove this account?" confirm-label="Remove account" danger :loading="deleting" @update:model-value="!$event && (toDelete = undefined)" @confirm="remove">
      <p>
        This removes <strong>{{ toDelete?.name }}</strong> and its sync history from this app. Your Gmail messages and everything already copied to the IMAP server stay exactly as they are.
      </p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.small { font-size: 0.8125rem; }
.fw-600 { font-weight: 600; }
</style>
