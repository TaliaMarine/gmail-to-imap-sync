<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import type { SessionSummary } from '../services/client'
import { elapsedMs, estimateRemaining, formatDuration } from '../utils/format'
import { phaseLabel, sessionStates, syncTypeLabels } from '../utils/status'
import TransferRail from './TransferRail.vue'

const props = defineProps<{ session: SessionSummary; compact?: boolean }>()

// A ticking clock so elapsed time moves between server updates.
const now = ref(Date.now())
const timer = setInterval(() => (now.value = Date.now()), 1000)
onBeforeUnmount(() => clearInterval(timer))

/**
 * Initial imports and rescans list Gmail page by page, so the total is not
 * known until the end: they show counters, not a percentage.
 */
const indeterminate = computed(() => {
  const s = props.session
  if (s.status === 'queued') return true
  if (s.sync_type === 'initial' || s.sync_type === 'reconcile') return true
  return s.discovered <= 0 || s.phase === 'loading_labels' || s.phase === 'reading_history' || s.phase === 'enumerating'
})
const phase = computed(() => (props.session.status === 'queued' ? sessionStates.queued!.label : phaseLabel(props.session.phase)))
const elapsed = computed(() => elapsedMs(props.session.started_at, props.session.finished_at, now.value))
const remaining = computed(() => (indeterminate.value ? undefined : estimateRemaining(props.session, now.value)))
</script>

<template>
  <div>
    <TransferRail
      :phase="phase"
      :discovered="session.discovered"
      :processed="session.processed"
      :copied="session.copied"
      :skipped="session.skipped"
      :failed="session.failed"
      :current-folder="session.current_folder"
      :indeterminate="indeterminate"
    />
    <p v-if="!compact" class="gas-muted tnum mt-3 mb-0">
      {{ syncTypeLabels[session.sync_type] }}
      <template v-if="elapsed !== undefined"> · running for {{ formatDuration(elapsed) }}</template>
      <template v-if="remaining !== undefined"> · about {{ formatDuration(remaining) }} left</template>
      <template v-if="session.cancel_requested"> · stopping after the current messages</template>
    </p>
  </div>
</template>
