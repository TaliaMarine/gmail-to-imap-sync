<script setup lang="ts">
withDefaults(
  defineProps<{
    modelValue: boolean
    title: string
    confirmLabel: string
    danger?: boolean
    loading?: boolean
  }>(),
  { danger: false, loading: false },
)
const emit = defineEmits<{ 'update:modelValue': [boolean]; confirm: [] }>()
</script>

<template>
  <v-dialog :model-value="modelValue" max-width="520" @update:model-value="emit('update:modelValue', $event)">
    <v-card>
      <v-card-title class="text-h6 pt-4">{{ title }}</v-card-title>
      <v-card-text><slot /></v-card-text>
      <v-card-actions class="px-4 pb-4">
        <v-spacer />
        <v-btn @click="emit('update:modelValue', false)">Cancel</v-btn>
        <v-btn :color="danger ? 'error' : 'primary'" variant="flat" :loading="loading" @click="emit('confirm')">
          {{ confirmLabel }}
        </v-btn>
      </v-card-actions>
    </v-card>
  </v-dialog>
</template>
