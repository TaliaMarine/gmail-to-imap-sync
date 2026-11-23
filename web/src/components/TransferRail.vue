<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  phase: string
  discovered: number
  processed: number
  copied: number
  skipped: number
  failed: number
  currentFolder?: string
  /** True while the total is unknown (for example during enumeration). */
  indeterminate?: boolean
}>()

const fraction = computed(() =>
  props.indeterminate || props.discovered <= 0 ? 0 : Math.min(1, props.processed / props.discovered),
)
const percent = computed(() => Math.round(fraction.value * 100))
// Track runs from x=60 (Gmail end) to x=540 (Dovecot end) in a 600-wide box.
const markerX = computed(() => 60 + fraction.value * 480)
const nf = new Intl.NumberFormat()
const summary = computed(() =>
  props.indeterminate
    ? `${props.phase}. Total not known yet.`
    : `${props.phase}. ${percent.value} percent: ${nf.format(props.processed)} of ${nf.format(props.discovered)} processed.`,
)
</script>

<template>
  <figure class="rail" :class="{ indeterminate }" role="group" :aria-label="summary">
    <svg viewBox="0 0 600 84" class="track" aria-hidden="true" focusable="false">
      <text x="0" y="18" class="end origin">Gmail</text>
      <text x="600" y="18" class="end dest" text-anchor="end">Dovecot</text>
      <line x1="60" y1="44" x2="540" y2="44" class="bed" />
      <line v-if="!indeterminate" x1="60" y1="44" :x2="markerX" y2="44" class="done" />
      <g v-for="i in 9" :key="i">
        <line :x1="60 + i * 48" y1="38" :x2="60 + i * 48" y2="50" class="tick" />
      </g>
      <circle v-if="indeterminate" r="6" cy="44" class="marker travel" />
      <circle v-else :cx="markerX" cy="44" r="6" class="marker" />
      <g v-if="failed > 0" class="tray">
        <rect x="270" y="62" width="60" height="16" rx="4" />
        <text x="300" y="74" text-anchor="middle">{{ nf.format(failed) }} set aside</text>
      </g>
    </svg>

    <figcaption class="caption">
      <span class="phase">{{ phase }}</span>
      <span v-if="currentFolder" class="folder">
        into <span class="mono">{{ currentFolder }}</span>
      </span>
    </figcaption>

    <dl class="counts tnum">
      <div><dt>Found</dt><dd>{{ indeterminate ? 'counting…' : nf.format(discovered) }}</dd></div>
      <div><dt>Processed</dt><dd>{{ nf.format(processed) }}</dd></div>
      <div><dt>Copied</dt><dd>{{ nf.format(copied) }}</dd></div>
      <div><dt>Skipped</dt><dd>{{ nf.format(skipped) }}</dd></div>
      <div class="fail" :class="{ some: failed > 0 }"><dt>Failed</dt><dd>{{ nf.format(failed) }}</dd></div>
    </dl>
  </figure>
</template>

<style scoped>
.rail { margin: 0; }
.track { width: 100%; height: auto; display: block; }
.end { font: 500 13px var(--gas-sans); }
.origin { fill: rgb(var(--v-theme-secondary)); }
.dest { fill: rgb(var(--v-theme-primary)); }
.bed { stroke: rgb(var(--v-border-color)); stroke-width: 6; stroke-linecap: round; }
.done { stroke: rgb(var(--v-theme-primary)); stroke-width: 6; stroke-linecap: round; transition: x2 150ms linear; }
.tick { stroke: rgb(var(--v-theme-surface)); stroke-width: 2; }
.marker { fill: rgb(var(--v-theme-surface)); stroke: rgb(var(--v-theme-primary)); stroke-width: 3; }
.travel { animation: travel 2.4s ease-in-out infinite alternate; }
@keyframes travel { from { cx: 60; } to { cx: 540; } }
.tray rect { fill: none; stroke: rgb(var(--v-theme-error)); stroke-width: 1.5; }
.tray text { font: 500 10px var(--gas-sans); fill: rgb(var(--v-theme-error)); }
.caption { display: flex; gap: 8px; align-items: baseline; margin: 4px 0 12px; }
.phase { font-weight: 600; }
.folder { color: rgb(var(--v-theme-on-surface-variant)); }
.counts { display: grid; grid-template-columns: repeat(auto-fit, minmax(110px, 1fr)); gap: 12px; margin: 0; }
.counts div { border-left: 2px solid rgb(var(--v-border-color)); padding-left: 10px; }
.counts dt { font-size: 0.8125rem; color: rgb(var(--v-theme-on-surface-variant)); }
.counts dd { margin: 0; font-size: 1.25rem; font-weight: 600; }
.fail.some { border-left-color: rgb(var(--v-theme-error)); }
.fail.some dd { color: rgb(var(--v-theme-error)); }
@media (prefers-reduced-motion: reduce) {
  .travel { animation: none; cx: 300; }
  .done { transition: none; }
}
</style>
