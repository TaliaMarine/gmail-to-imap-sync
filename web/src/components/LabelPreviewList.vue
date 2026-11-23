<script setup lang="ts">
import type { LabelView } from '../services/client'

defineProps<{ labels: LabelView[]; mapped: Record<string, string> }>()
const emit = defineEmits<{
  'toggle-exclude': [id: string, excluded: boolean]
  'edit-folder': [label: LabelView]
}>()
</script>

<template>
  <ul class="labels" aria-label="Gmail labels and the folders they become">
    <li v-for="l in labels" :key="l.id" class="row" :class="{ off: l.excluded, clash: l.collides_with?.length }" :data-label="l.id">
      <v-switch
        :model-value="!l.excluded"
        density="compact"
        hide-details
        color="primary"
        :aria-label="`Copy mail labelled ${l.name}`"
        @update:model-value="emit('toggle-exclude', l.id, !$event)"
      />
      <div class="name">
        <span>{{ l.name }}</span>
        <v-chip size="x-small" label class="ml-2">{{ l.system ? 'Gmail' : 'Yours' }}</v-chip>
      </div>
      <v-icon icon="mdi-arrow-right" size="small" class="gas-muted" aria-hidden="true" />
      <div class="dest">
        <span v-if="l.excluded" class="gas-muted">Not copied</span>
        <span v-else-if="l.folder" class="mono" data-testid="folder">{{ l.folder }}</span>
        <span v-else class="gas-muted">No folder (sets flags only)</span>
        <v-chip v-if="mapped[l.id]" size="x-small" label color="secondary" variant="tonal" class="ml-2">Renamed</v-chip>
        <v-chip v-if="l.collides_with?.length" size="x-small" label color="warning" variant="tonal" class="ml-2" data-testid="collision">
          <v-icon icon="mdi-alert-outline" start size="x-small" aria-hidden="true" />
          Same folder as {{ l.collides_with.length }} other {{ l.collides_with.length === 1 ? 'label' : 'labels' }}
        </v-chip>
      </div>
      <v-btn
        v-if="!l.excluded && (l.folder || !l.system)"
        size="small"
        icon="mdi-pencil-outline"
        :aria-label="`Choose the folder for ${l.name}`"
        @click="emit('edit-folder', l)"
      />
      <span v-else />
    </li>
  </ul>
</template>

<style scoped>
.labels { list-style: none; margin: 0; padding: 0; }
.row {
  display: grid;
  grid-template-columns: 56px minmax(120px, 1fr) 20px minmax(140px, 1.2fr) 40px;
  align-items: center;
  gap: 8px;
  padding: 2px 8px;
  min-height: 44px;
  border-bottom: 1px solid rgb(var(--v-border-color));
}
.row.off .name { color: rgb(var(--v-theme-on-surface-variant)); }
.row.clash { box-shadow: inset 3px 0 0 rgb(var(--v-theme-warning)); }
.name { display: flex; align-items: center; min-width: 0; }
.dest { display: flex; align-items: center; flex-wrap: wrap; min-width: 0; }
@media (max-width: 700px) {
  .row { grid-template-columns: 56px 1fr 40px; }
  .row > :nth-child(3) { display: none; }
  .row .dest { grid-column: 2 / 4; }
}
</style>
