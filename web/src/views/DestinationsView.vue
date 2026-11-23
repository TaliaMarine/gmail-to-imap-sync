<script setup lang="ts">
import { reactive, ref } from 'vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import { errorText, useLoader } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type ConnectionTest, type Destination, type DestinationConfig } from '../services/client'

const list = useLoader(() => api.destinations())
const dialog = ref(false)
const editing = ref<Destination>()
const saving = ref(false)
const testing = ref(false)
const formError = ref('')
const testResult = ref<ConnectionTest>()
const toDelete = ref<Destination>()
const deleting = ref(false)

type Form = Omit<DestinationConfig, 'password'> & { password: string }
const blank = (): Form => ({ name: '', host: '', port: 993, tls_mode: 'implicit', tls_skip_verify: false, username: '', password: '', enabled: true })
const form = reactive<Form>(blank())

const tlsModes = [
  { title: 'TLS from the start (usually port 993)', value: 'implicit' },
  { title: 'STARTTLS (usually port 143)', value: 'starttls' },
  { title: 'No encryption (local testing only)', value: 'none' },
]
const defaultPorts: Record<string, number> = { implicit: 993, starttls: 143, none: 143 }

function open(d?: Destination) {
  editing.value = d
  testResult.value = undefined
  formError.value = ''
  Object.assign(form, d ? { name: d.name, host: d.host, port: d.port, tls_mode: d.tls_mode, tls_skip_verify: d.tls_skip_verify, username: d.username, password: '', enabled: d.enabled } : blank())
  dialog.value = true
}

function onTlsMode(mode: string) {
  if (!editing.value) form.port = defaultPorts[mode] ?? form.port
}

function payload(): DestinationConfig {
  const { password, ...rest } = form
  return { ...rest, ...(password ? { password } : {}) }
}

async function test() {
  testing.value = true
  testResult.value = undefined
  try {
    testResult.value = await api.testDestinationConfig({ ...payload(), destination_id: editing.value?.id })
  } catch (e) {
    testResult.value = { ok: false, error: errorText(e) }
  } finally {
    testing.value = false
  }
}

async function save() {
  saving.value = true
  formError.value = ''
  try {
    if (editing.value) await api.updateDestination(editing.value.id, payload())
    else await api.createDestination(payload())
    notify(editing.value ? 'Saved destination' : 'Added destination')
    dialog.value = false
    await list.reload()
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
    await api.deleteDestination(toDelete.value.id)
    notify(`Removed ${toDelete.value.name}`)
    toDelete.value = undefined
    await list.reload()
  } catch (e) {
    notify(errorText(e), 'error')
    toDelete.value = undefined
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="gas-page">
    <div class="d-flex align-center mb-4">
      <h1 class="text-h5">Destinations</h1>
      <v-spacer />
      <v-btn color="primary" variant="flat" prepend-icon="mdi-plus" @click="open()">Add destination</v-btn>
    </div>
    <p class="gas-muted lead mb-4">An IMAP server that receives the copied mail, such as your Dovecot. Accounts choose one.</p>

    <v-alert v-if="list.error.value" type="error" variant="tonal" class="mb-4">{{ list.error.value }}</v-alert>

    <EmptyState
      v-if="list.data.value && list.data.value.length === 0"
      icon="mdi-server-plus-outline"
      title="No destinations yet"
      body="Add the IMAP server and mailbox login that should receive your archived mail."
    >
      <v-btn color="primary" variant="flat" prepend-icon="mdi-plus" @click="open()">Add destination</v-btn>
    </EmptyState>

    <v-card v-else-if="list.data.value">
      <v-table>
        <thead><tr><th>Name</th><th>Server</th><th>Login</th><th>Used by</th><th class="text-right">Actions</th></tr></thead>
        <tbody>
          <tr v-for="d in list.data.value" :key="d.id">
            <td>
              <strong>{{ d.name }}</strong>
              <v-chip v-if="!d.enabled" size="x-small" class="ml-2" label>Disabled</v-chip>
            </td>
            <td><span class="mono">{{ d.host }}:{{ d.port }}</span> <span class="gas-muted">· {{ d.tls_mode === 'none' ? 'unencrypted' : d.tls_mode }}</span></td>
            <td><span class="mono">{{ d.username }}</span></td>
            <td>{{ d.account_count }} {{ d.account_count === 1 ? 'account' : 'accounts' }}</td>
            <td class="text-right">
              <v-btn size="small" @click="open(d)">Edit</v-btn>
              <v-btn size="small" color="error" :aria-label="`Remove ${d.name}`" @click="toDelete = d">Remove</v-btn>
            </td>
          </tr>
        </tbody>
      </v-table>
    </v-card>

    <v-dialog v-model="dialog" max-width="560" scrollable>
      <v-card>
        <v-card-title class="text-h6 pt-4">{{ editing ? 'Edit destination' : 'Add a destination' }}</v-card-title>
        <v-form @submit.prevent="save">
          <v-card-text>
            <v-alert v-if="formError" type="error" variant="tonal" class="mb-3">{{ formError }}</v-alert>
            <v-text-field v-model="form.name" label="Name" />
            <div class="row2">
              <v-text-field v-model="form.host" label="Server" placeholder="imap.example.com" autocomplete="off" />
              <v-text-field v-model.number="form.port" label="Port" type="number" />
            </div>
            <v-select v-model="form.tls_mode" label="Encryption" :items="tlsModes" @update:model-value="onTlsMode" />
            <v-alert v-if="form.tls_mode === 'none'" type="warning" variant="tonal" class="mb-3">
              Your login and mail cross the network unencrypted. Use this only for a server on the same machine.
            </v-alert>
            <v-switch v-model="form.tls_skip_verify" color="warning" hide-details label="Trust any certificate" />
            <v-alert v-if="form.tls_skip_verify" type="warning" variant="tonal" class="my-3">
              The server’s certificate will not be checked, so anyone on the network path could impersonate it. Only use this for a server you control with a self-signed certificate.
            </v-alert>
            <v-text-field v-model="form.username" label="Username" autocomplete="off" class="mt-3" />
            <v-text-field
              v-model="form.password"
              label="Password"
              type="password"
              autocomplete="new-password"
              :placeholder="editing?.password_set ? 'Stored. Leave blank to keep it.' : ''"
              :hint="editing?.password_set ? 'The password is never shown again.' : ''"
              persistent-hint
            />
            <v-switch v-model="form.enabled" color="primary" hide-details label="Enabled" class="mt-2" />

            <div class="mt-4">
              <v-btn :loading="testing" prepend-icon="mdi-lan-connect" :disabled="!form.host || !form.username" @click="test">Test connection</v-btn>
              <v-alert v-if="testResult" :type="testResult.ok ? 'success' : 'error'" variant="tonal" class="mt-3" data-testid="dest-test">
                <template v-if="testResult.ok">
                  Connected and signed in. Found {{ testResult.folders?.length ?? 0 }} folders, separated by
                  <span class="mono">{{ testResult.delimiter }}</span>.
                </template>
                <template v-else>{{ testResult.error }}</template>
              </v-alert>
            </div>
          </v-card-text>
          <v-card-actions class="px-4 pb-4">
            <v-spacer />
            <v-btn @click="dialog = false">Cancel</v-btn>
            <v-btn type="submit" color="primary" variant="flat" :loading="saving" :disabled="!form.name || !form.host || !form.username">
              {{ editing ? 'Save changes' : 'Add destination' }}
            </v-btn>
          </v-card-actions>
        </v-form>
      </v-card>
    </v-dialog>

    <ConfirmDialog :model-value="!!toDelete" title="Remove this destination?" confirm-label="Remove destination" danger :loading="deleting" @update:model-value="!$event && (toDelete = undefined)" @confirm="remove">
      <p>Removes only the saved connection settings. Mail already copied stays on the server.</p>
      <p v-if="toDelete && toDelete.account_count > 0" class="text-warning">
        {{ toDelete.account_count }} {{ toDelete.account_count === 1 ? 'account uses' : 'accounts use' }} it, so it cannot be removed until you move them to another destination.
      </p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.row2 { display: grid; grid-template-columns: 1fr 120px; gap: 12px; }
</style>
