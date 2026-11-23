<script setup lang="ts">
import type { StateView } from '../utils/status'

defineProps<{ state: StateView }>()
</script>

<template>
  <v-chip :color="state.color === 'default' ? undefined : state.color" variant="tonal" label>
    <v-icon :icon="state.icon" start size="small" aria-hidden="true" />
    {{ state.label }}
  </v-chip>
</template>
