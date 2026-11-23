<script setup lang="ts">
import { computed } from 'vue'
import type { ActivityDay } from '../services/client'
import { fillDays } from '../utils/activity'

const props = withDefaults(defineProps<{ days: ActivityDay[]; count?: number }>(), { count: 14 })

const bars = computed(() => fillDays(props.days, props.count))
const maxCopied = computed(() => Math.max(1, ...bars.value.map((b) => b.copied)))
const maxFailed = computed(() => Math.max(1, ...bars.value.map((b) => b.failed)))
const totalCopied = computed(() => bars.value.reduce((n, b) => n + b.copied, 0))
const totalFailed = computed(() => bars.value.reduce((n, b) => n + b.failed, 0))
const summary = computed(
  () => `Last ${props.count} days: ${totalCopied.value} messages copied, ${totalFailed.value} failed.`,
)
const W = 560
const slot = computed(() => W / props.count)
</script>

<template>
  <figure class="chart">
    <figcaption class="gas-muted mb-2">{{ summary }}</figcaption>
    <svg :viewBox="`0 0 ${W} 150`" role="img" :aria-label="summary" class="svg">
      <text x="0" y="12" class="axis">Copied per day · peak {{ maxCopied }}</text>
      <g transform="translate(0 18)">
        <line x1="0" :x2="W" y1="62" y2="62" class="base" />
        <rect
          v-for="(b, i) in bars"
          :key="`c-${b.key}`"
          :x="i * slot + 3"
          :width="slot - 6"
          :y="62 - (b.copied / maxCopied) * 60"
          :height="Math.max(b.copied > 0 ? 2 : 0, (b.copied / maxCopied) * 60)"
          class="copied"
          rx="2"
        >
          <title>{{ b.label }}: {{ b.copied }} copied</title>
        </rect>
      </g>
      <text x="0" y="98" class="axis">Failed per day · peak {{ maxFailed }}</text>
      <g transform="translate(0 102)">
        <line x1="0" :x2="W" y1="26" y2="26" class="base" />
        <rect
          v-for="(b, i) in bars"
          :key="`f-${b.key}`"
          :x="i * slot + 3"
          :width="slot - 6"
          :y="26 - (b.failed / maxFailed) * 24"
          :height="Math.max(b.failed > 0 ? 2 : 0, (b.failed / maxFailed) * 24)"
          class="failed"
          rx="2"
        >
          <title>{{ b.label }}: {{ b.failed }} failed</title>
        </rect>
      </g>
      <text x="0" y="146" class="axis">{{ bars[0]?.label }}</text>
      <text :x="W" y="146" class="axis" text-anchor="end">{{ bars[bars.length - 1]?.label }}</text>
    </svg>
    <table class="sr-only">
      <caption>Daily activity</caption>
      <thead><tr><th>Day</th><th>Copied</th><th>Failed</th></tr></thead>
      <tbody>
        <tr v-for="b in bars" :key="b.key"><td>{{ b.label }}</td><td>{{ b.copied }}</td><td>{{ b.failed }}</td></tr>
      </tbody>
    </table>
  </figure>
</template>

<style scoped>
.chart { margin: 0; }
.svg { width: 100%; height: auto; display: block; }
.axis { font: 400 9px var(--gas-sans); fill: rgb(var(--v-theme-on-surface-variant)); }
.base { stroke: rgb(var(--v-border-color)); stroke-width: 1; }
.copied { fill: rgb(var(--v-theme-primary)); }
.failed { fill: rgb(var(--v-theme-error)); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
</style>
