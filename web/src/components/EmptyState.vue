<script setup lang="ts">
defineProps<{ icon: string; title: string; body: string }>()
</script>

<template>
  <section class="empty" aria-labelledby="empty-title">
    <v-icon :icon="icon" size="32" class="gas-muted" aria-hidden="true" />
    <h2 id="empty-title" class="text-h6">{{ title }}</h2>
    <p class="gas-muted">{{ body }}</p>
    <slot />
  </section>
</template>

<style scoped>
.empty {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  max-width: 52ch;
  padding: 32px 0;
}
.empty p { margin: 0 0 8px; }
</style>
