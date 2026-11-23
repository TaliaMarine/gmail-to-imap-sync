<script setup lang="ts">
import { computed } from 'vue'
import type { SyncSettings } from '../services/client'
import { intervalOptions } from '../utils/syncSettings'

const props = defineProps<{ draft: SyncSettings; section: 'schedule' | 'advanced' }>()

const intervals = computed(() => {
  const cur = props.draft.schedule_interval_seconds
  return intervalOptions.some((o) => o.value === cur) ? intervalOptions : [...intervalOptions, { title: `Every ${Math.round(cur / 60)} minutes`, value: cur }]
})
const percent = computed({
  get: () => Math.round(props.draft.rate_fraction * 100),
  set: (v: number) => (props.draft.rate_fraction = v / 100),
})
</script>

<template>
  <v-card class="pa-4 form">
    <template v-if="section === 'schedule'">
      <v-switch v-model="draft.enabled" color="primary" label="Sync on a schedule" hide-details />
      <v-select v-model="draft.schedule_interval_seconds" :items="intervals" label="How often" :disabled="!draft.enabled" class="mt-3" />
      <v-text-field
        v-model="draft.initial_sync_after"
        type="date"
        label="Only import mail received after"
        hint="Optional. Applies to the first import; leave empty to import everything."
        persistent-hint
        clearable
        class="mt-3"
      />
      <h3 class="text-subtitle-1 mt-6 mb-2">Special folders</h3>
      <div class="three">
        <v-text-field v-model="draft.inbox_folder" label="Inbox folder" />
        <v-text-field v-model="draft.sent_folder" label="Sent folder" />
        <v-text-field v-model="draft.archive_folder" label="Archive folder" hint="Mail with no label" persistent-hint />
      </div>
    </template>
    <template v-else>
      <div class="two">
        <v-text-field v-model.number="draft.worker_count" type="number" min="1" max="64" label="Messages handled at once" hint="1 to 64" persistent-hint />
        <v-text-field v-model.number="draft.batch_size" type="number" min="1" max="500" label="Batch size" hint="Messages listed per request, 1 to 500" persistent-hint />
      </div>
      <p class="mt-6 mb-1">Share of Gmail’s request quota to use: <strong class="tnum">{{ percent }}%</strong></p>
      <v-slider v-model="percent" min="5" max="100" step="5" color="primary" hide-details aria-label="Share of Gmail request quota" />
      <v-text-field
        v-model.number="draft.retry_max_attempts"
        type="number"
        min="0"
        max="50"
        label="Automatic retries per failed message"
        hint="0 turns automatic retries off. You can always retry by hand."
        persistent-hint
        class="mt-6"
      />
      <v-switch v-model="draft.subscribe_folders" color="primary" label="Subscribe to the folders it creates" hide-details class="mt-3" />
    </template>
  </v-card>
</template>

<style scoped>
.form { max-width: 720px; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.three { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 16px; }
</style>
