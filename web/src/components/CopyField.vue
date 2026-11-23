<script setup lang="ts">
import { ref } from 'vue'

const props = defineProps<{ value: string; label: string }>()
const copied = ref(false)

async function copy() {
  try {
    await navigator.clipboard.writeText(props.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 2000)
  } catch {
    // Clipboard blocked: the field is still selectable.
  }
}
</script>

<template>
  <v-text-field :model-value="value" :label="label" readonly hide-details class="copy-field">
    <template #append-inner>
      <v-btn
        size="small"
        :prepend-icon="copied ? 'mdi-check' : 'mdi-content-copy'"
        :aria-label="`Copy ${label}`"
        @click="copy"
      >
        {{ copied ? 'Copied' : 'Copy' }}
      </v-btn>
    </template>
  </v-text-field>
</template>

<style scoped>
.copy-field :deep(input) { font-family: var(--gas-mono); font-size: 0.8125rem; }
</style>
