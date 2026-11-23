<script setup lang="ts">
import { computed } from 'vue'
import type { Failure } from '../services/client'
import { formatDateTime, formatRelative } from '../utils/format'
import { failureCategories, failureStates } from '../utils/status'
import StateChip from './StateChip.vue'

const props = defineProps<{ items: Failure[]; selected: string[] }>()
const emit = defineEmits<{ 'update:selected': [string[]]; open: [Failure] }>()

const allChecked = computed(() => props.items.length > 0 && props.items.every((f) => props.selected.includes(f.id)))
const someChecked = computed(() => !allChecked.value && props.items.some((f) => props.selected.includes(f.id)))

function toggle(id: string, on: boolean) {
  emit('update:selected', on ? [...new Set([...props.selected, id])] : props.selected.filter((s) => s !== id))
}
function toggleAll(on: boolean) {
  const ids = props.items.map((f) => f.id)
  emit('update:selected', on ? [...new Set([...props.selected, ...ids])] : props.selected.filter((s) => !ids.includes(s)))
}
</script>

<template>
  <div class="wrap">
    <table class="t">
      <thead>
        <tr>
          <th class="chk">
            <v-checkbox-btn
              :model-value="allChecked"
              :indeterminate="someChecked"
              aria-label="Select all failures on this page"
              data-testid="select-all"
              @update:model-value="toggleAll(!!$event)"
            />
          </th>
          <th>Message</th>
          <th>Problem</th>
          <th>Folders</th>
          <th class="num">Attempts</th>
          <th>Last attempt</th>
          <th>Status</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="f in items" :key="f.id" :class="{ picked: selected.includes(f.id) }" data-testid="failure-row">
          <td class="chk">
            <v-checkbox-btn
              :model-value="selected.includes(f.id)"
              :aria-label="`Select ${f.subject || 'message without subject'}`"
              @update:model-value="toggle(f.id, !!$event)"
            />
          </td>
          <td class="msg">
            <button type="button" class="link" @click="emit('open', f)">{{ f.subject || '(no subject)' }}</button>
            <div class="gas-muted small">{{ f.sender || 'Unknown sender' }} · {{ formatDateTime(f.message_date) }}</div>
          </td>
          <td class="prob">
            <strong>{{ failureCategories[f.category] ?? f.category }}</strong>
            <div class="gas-muted small">{{ f.description }}</div>
          </td>
          <td class="small">
            <div class="folders">
              <span v-for="d in f.completed_folders ?? []" :key="'d' + d" class="mono ok" title="Delivered"><span class="sr-only">Delivered to </span>✓ {{ d }}</span>
              <span v-for="d in f.failed_folders ?? []" :key="'f' + d" class="mono bad" title="Not delivered"><span class="sr-only">Not delivered to </span>✗ {{ d }}</span>
              <span v-if="!(f.completed_folders?.length || f.failed_folders?.length)" class="gas-muted">–</span>
            </div>
          </td>
          <td class="num tnum">{{ f.attempt_count }}</td>
          <td class="small" :title="formatDateTime(f.last_attempted_at)">{{ formatRelative(f.last_attempted_at) }}</td>
          <td><StateChip :state="failureStates[f.status] ?? failureStates.pending!" /></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.wrap { overflow-x: auto; }
.t { width: 100%; border-collapse: collapse; }
th { text-align: left; font-weight: 600; font-size: 0.8125rem; padding: 8px; color: rgb(var(--v-theme-on-surface-variant)); border-bottom: 1px solid rgb(var(--v-border-color)); white-space: nowrap; }
td { padding: 8px; vertical-align: top; border-bottom: 1px solid rgb(var(--v-border-color)); }
.chk { width: 44px; }
.num { text-align: right; }
.small { font-size: 0.8125rem; }
.msg { max-width: 300px; }
.prob { max-width: 280px; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
.folders { display: flex; flex-direction: column; gap: 2px; }
.ok { color: rgb(var(--v-theme-success)); }
.bad { color: rgb(var(--v-theme-error)); }
tr.picked { background: rgb(var(--v-theme-inset)); }
.link { background: none; border: 0; padding: 0; color: rgb(var(--v-theme-primary)); font: inherit; font-weight: 600; cursor: pointer; text-align: left; text-decoration: underline; text-underline-offset: 3px; }
</style>
