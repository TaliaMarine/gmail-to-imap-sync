<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { errorText } from '../composables/useAsync'
import { notify } from '../composables/useSnackbar'
import { api, type LabelView, type Matcher, type SyncSettings } from '../services/client'
import { nameRules, setLabelExcluded } from '../utils/syncSettings'
import LabelPreviewList from './LabelPreviewList.vue'

const props = defineProps<{ accountId: string; draft: SyncSettings; authorised: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const labels = ref<LabelView[]>([])
// Switch positions the user just changed, shown at once while the server
// preview (which decides folders and collisions) catches up.
const pending = reactive<Record<string, boolean>>({})
const shown = computed(() => labels.value.map((l) => (l.id in pending ? { ...l, excluded: pending[l.id]! } : l)))
const clearPending = () => Object.keys(pending).forEach((k) => delete pending[k])
const loading = ref(false)
const refreshing = ref(false)
const error = ref('')
const search = ref('')

async function load() {
  loading.value = true
  try {
    labels.value = await api.labels(props.accountId)
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}
load()

async function refresh() {
  refreshing.value = true
  error.value = ''
  try {
    labels.value = await api.refreshLabels(props.accountId)
    notify('Refreshed labels from Gmail')
  } catch (e) {
    error.value = errorText(e)
  } finally {
    refreshing.value = false
  }
}

// The preview follows the unsaved draft, so collisions show before saving.
let timer: ReturnType<typeof setTimeout> | undefined
watch(
  () => JSON.stringify([props.draft.excluded_labels, props.draft.label_mapping, props.draft.flag_mapping, props.draft.inbox_folder, props.draft.sent_folder, props.draft.archive_folder]),
  () => {
    clearTimeout(timer)
    timer = setTimeout(async () => {
      try {
        labels.value = await api.previewFolders(props.accountId, props.draft)
        clearPending()
      } catch (e) {
        error.value = errorText(e)
      }
    }, 300)
  },
)
onBeforeUnmount(() => clearTimeout(timer))

const visible = computed(() => {
  const q = search.value.trim().toLowerCase()
  return q ? shown.value.filter((l) => l.name.toLowerCase().includes(q)) : shown.value
})
const system = computed(() => visible.value.filter((l) => l.system))
const user = computed(() => visible.value.filter((l) => !l.system))
const collisions = computed(() => labels.value.filter((l) => l.collides_with?.length).length)
const ruleExcluded = computed(() => labels.value.filter((l) => l.excluded && !(props.draft.excluded_labels ?? []).some((m) => m.kind === 'id' && m.value === l.id)))

function toggle(id: string, excluded: boolean) {
  pending[id] = excluded
  setLabelExcluded(props.draft, id, excluded)
  emit('changed')
}

// Folder naming dialog.
const folderDialog = ref(false)
const editLabel = ref<LabelView>()
const folderName = ref('')
function editFolder(l: LabelView) {
  editLabel.value = l
  folderName.value = props.draft.label_mapping[l.id] ?? l.folder
  folderDialog.value = true
}
function applyFolder() {
  if (!editLabel.value) return
  const v = folderName.value.trim()
  if (!v || v === editLabel.value.name) delete props.draft.label_mapping[editLabel.value.id]
  else props.draft.label_mapping[editLabel.value.id] = v
  folderDialog.value = false
  emit('changed')
}
function resetFolder() {
  if (editLabel.value) delete props.draft.label_mapping[editLabel.value.id]
  folderDialog.value = false
  emit('changed')
}

// Exclusion rules.
const rule = reactive<{ kind: 'prefix' | 'name'; value: string; case_insensitive: boolean }>({ kind: 'prefix', value: '', case_insensitive: true })
function addRule() {
  const v = rule.value.trim()
  if (!v) return
  const m: Matcher = { kind: rule.kind, value: v, case_insensitive: rule.case_insensitive }
  props.draft.excluded_labels = [...(props.draft.excluded_labels ?? []), m]
  rule.value = ''
  emit('changed')
}
function removeRule(m: Matcher) {
  props.draft.excluded_labels = (props.draft.excluded_labels ?? []).filter((x) => x !== m)
  emit('changed')
}
const rules = computed(() => nameRules(props.draft))

// Gmail system labels that can also become folders.
const optionalSystem = computed(() =>
  labels.value
    .filter((l) => l.system && !['INBOX', 'SENT', 'SPAM', 'TRASH', 'DRAFT', 'UNREAD'].includes(l.id))
    .map((l) => ({ title: l.name, value: l.id })),
)
</script>

<template>
  <div class="stack">
    <v-alert v-if="error" type="error" variant="tonal">{{ error }}</v-alert>
    <v-alert v-if="!authorised" type="info" variant="tonal">
      Authorise this account with Google first, then refresh the labels to see them here.
    </v-alert>

    <v-card>
      <div class="d-flex align-center flex-wrap ga-2 pa-4">
        <div>
          <h2 class="text-h6">Labels and folders</h2>
          <p class="gas-muted mb-0">Each Gmail label becomes an IMAP folder. Switch a label off to stop copying it.</p>
        </div>
        <v-spacer />
        <v-text-field v-model="search" density="compact" label="Find a label" prepend-inner-icon="mdi-magnify" hide-details clearable class="search" />
        <v-btn prepend-icon="mdi-refresh" :loading="refreshing" :disabled="!authorised" @click="refresh">Refresh from Gmail</v-btn>
      </div>
      <v-alert v-if="collisions" type="warning" variant="tonal" class="mx-4 mb-3" data-testid="collision-warning">
        {{ collisions }} {{ collisions === 1 ? 'label shares' : 'labels share' }} a folder with another label. Their mail will be merged into one folder. Rename a folder if that is not what you want.
      </v-alert>
      <v-alert v-if="ruleExcluded.length" type="info" variant="tonal" class="mx-4 mb-3">
        {{ ruleExcluded.length }} {{ ruleExcluded.length === 1 ? 'label is' : 'labels are' }} excluded by a rule below, not by its switch.
      </v-alert>

      <p v-if="loading" class="px-4 pb-4 gas-muted">Loading labels…</p>
      <p v-else-if="!labels.length" class="px-4 pb-4 gas-muted">No labels yet. Authorise the account, then choose “Refresh from Gmail”.</p>
      <template v-else>
        <h3 class="px-4 text-subtitle-2 gas-muted">Gmail labels</h3>
        <LabelPreviewList :labels="system" :mapped="draft.label_mapping" @toggle-exclude="toggle" @edit-folder="editFolder" />
        <h3 class="px-4 pt-4 text-subtitle-2 gas-muted">Your labels</h3>
        <LabelPreviewList :labels="user" :mapped="draft.label_mapping" @toggle-exclude="toggle" @edit-folder="editFolder" />
      </template>
    </v-card>

    <v-card class="pa-4">
      <h2 class="text-h6 mb-1">Exclusion rules</h2>
      <p class="gas-muted">Skip every label that matches. “Starts with” also covers labels nested below it, such as <span class="mono">Newsletters/Weekly</span>.</p>
      <div class="d-flex flex-wrap ga-2 mb-3">
        <v-chip v-for="(m, i) in rules" :key="i" closable label @click:close="removeRule(m)">
          {{ m.kind === 'prefix' ? 'Starts with' : 'Named' }} “{{ m.value }}”<template v-if="m.case_insensitive"> (any case)</template>
        </v-chip>
        <span v-if="!rules.length" class="gas-muted">No rules yet.</span>
      </div>
      <div class="d-flex flex-wrap ga-3 align-center">
        <v-select v-model="rule.kind" :items="[{ title: 'Starts with', value: 'prefix' }, { title: 'Exact name', value: 'name' }]" label="Match" density="compact" hide-details class="kind" />
        <v-text-field v-model="rule.value" label="Label name" density="compact" hide-details class="val" @keyup.enter="addRule" />
        <v-checkbox v-model="rule.case_insensitive" label="Ignore case" density="compact" hide-details />
        <v-btn :disabled="!rule.value.trim()" @click="addRule">Add rule</v-btn>
      </div>
    </v-card>

    <v-card class="pa-4">
      <h2 class="text-h6 mb-2">Flags</h2>
      <v-switch v-model="draft.flag_mapping.unread_as_seen" color="primary" label="Mark messages you have read as read" hide-details @update:model-value="emit('changed')" />
      <v-switch v-model="draft.flag_mapping.starred_as_flagged" color="primary" label="Flag starred messages" hide-details @update:model-value="emit('changed')" />
      <v-select
        v-if="optionalSystem.length"
        v-model="draft.flag_mapping.folder_labels"
        :items="optionalSystem"
        multiple
        chips
        closable-chips
        label="Also create folders for these Gmail labels"
        hint="Starred, Important and the Gmail categories are only flags by default."
        persistent-hint
        class="mt-4"
        @update:model-value="emit('changed')"
      />
    </v-card>

    <v-dialog v-model="folderDialog" max-width="480">
      <v-card>
        <v-card-title class="text-h6 pt-4">Folder for “{{ editLabel?.name }}”</v-card-title>
        <v-form @submit.prevent="applyFolder">
          <v-card-text>
            <v-text-field v-model="folderName" label="Folder name" autofocus hint="Use / for subfolders, for example Work/Customers" persistent-hint />
            <p class="gas-muted mt-3 mb-0">New mail goes to this folder. Folders that already exist stay as they are.</p>
          </v-card-text>
          <v-card-actions class="px-4 pb-4">
            <v-btn :disabled="!draft.label_mapping[editLabel?.id ?? '']" @click="resetFolder">Use the label name</v-btn>
            <v-spacer />
            <v-btn @click="folderDialog = false">Cancel</v-btn>
            <v-btn type="submit" color="primary" variant="flat">Apply</v-btn>
          </v-card-actions>
        </v-form>
      </v-card>
    </v-dialog>
  </div>
</template>

<style scoped>
.stack { display: grid; gap: 16px; }
.search { max-width: 240px; }
.kind { max-width: 160px; }
.val { max-width: 260px; }
</style>
