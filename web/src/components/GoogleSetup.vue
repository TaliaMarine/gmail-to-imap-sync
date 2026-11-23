<script setup lang="ts">
import { ref, watch } from 'vue'
import { errorText } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type Settings } from '../services/client'
import CopyField from './CopyField.vue'

const props = defineProps<{ settings: Settings }>()
const emit = defineEmits<{ saved: [Settings] }>()

const clientId = ref(props.settings.oauth_client_id)
const secret = ref('')
const saving = ref(false)
const error = ref('')

watch(() => props.settings.oauth_client_id, (v) => (clientId.value = v))

async function save() {
  saving.value = true
  error.value = ''
  try {
    const s = await api.updateSettings({
      oauth_client_id: clientId.value.trim(),
      // Omitted = keep the stored secret.
      ...(secret.value ? { oauth_client_secret: secret.value } : {}),
      session_retention_days: props.settings.session_retention_days,
      event_retention_days: props.settings.event_retention_days,
    })
    secret.value = ''
    notify('Saved Google settings')
    emit('saved', s)
  } catch (e) {
    error.value = errorText(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <ol class="steps">
      <li>
        <strong>Create a Google Cloud project</strong> and turn on the <em>Gmail API</em> for it. Each person uses their own project, so the app only ever reads your mail. Do not use the “Create credentials” button on that page yet.
      </li>
      <li>
        <strong>Set up the consent screen</strong> under <em>Google Auth Platform</em> (menu: APIs &amp; Services → OAuth consent screen): choose <em>Get started</em>, audience <em>External</em>, then <em>Create</em>.
      </li>
      <li>
        <strong>Fill in the Branding page</strong>: app name, support email, a home page URL and a privacy policy URL (this project's <span class="mono">PRIVACY.md</span> on GitHub works). Leave the logo empty. Google will not let you publish without these.
      </li>
      <li>
        <strong>Publish the app: choose “In production”</strong> on the Audience page. While it is in “Testing”, Google ends the sign-in after 7 days and syncing stops silently. The “unverified app” warning is expected for a personal project.
      </li>
      <li>
        <strong>Add the scope</strong> <span class="mono">gmail.readonly</span> under <em>Data Access</em>.
      </li>
      <li>
        <strong>Create the client</strong> under <em>Clients</em>: type <em>Web application</em>, with this redirect URI:
        <CopyField :value="settings.redirect_uri" label="Redirect URI" class="my-2" />
      </li>
      <li><strong>Paste the client ID and secret</strong> below. Full walkthrough: <span class="mono">docs/google-setup.md</span>.</li>
    </ol>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-3">{{ error }}</v-alert>
    <v-form @submit.prevent="save">
      <v-text-field v-model="clientId" label="Client ID" autocomplete="off" spellcheck="false" />
      <v-text-field
        v-model="secret"
        label="Client secret"
        type="password"
        autocomplete="new-password"
        :placeholder="settings.oauth_client_secret_set ? 'Stored. Leave blank to keep it.' : ''"
        :hint="settings.oauth_client_secret_set ? 'The secret is never shown again.' : ''"
        persistent-hint
      />
      <v-btn type="submit" color="primary" variant="flat" class="mt-4" :loading="saving" :disabled="!clientId.trim() || (!secret && !settings.oauth_client_secret_set)">
        Save Google settings
      </v-btn>
    </v-form>
  </div>
</template>

<style scoped>
.steps { padding-left: 1.25rem; margin: 0 0 16px; display: grid; gap: 10px; max-width: 70ch; }
</style>
