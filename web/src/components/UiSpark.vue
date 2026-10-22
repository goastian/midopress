<script setup lang="ts">
// An area chart of two series that share a scale: what came in (grey) and what
// was sent (green). The gap between them is what was saved.
import { computed } from 'vue'
import type { SeriesPoint } from '../api/types'

const props = defineProps<{ points: SeriesPoint[]; height?: number }>()

const W = 600
const H = computed(() => props.height ?? 140)

const model = computed(() => {
  const pts = props.points
  const n = pts.length
  const top = Math.max(1, ...pts.map((p) => Math.max(p.bytes_in, p.bytes_out)))
  const x = (i: number) => (n <= 1 ? 0 : (i / (n - 1)) * W)
  const y = (v: number) => H.value - 4 - (v / top) * (H.value - 12)
  const line = (key: 'bytes_in' | 'bytes_out') =>
    pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)} ${y(p[key]).toFixed(1)}`).join(' ')
  const area = (key: 'bytes_in' | 'bytes_out') =>
    n ? `${line(key)} L${W} ${H.value} L0 ${H.value} Z` : ''
  return { top, inLine: line('bytes_in'), outLine: line('bytes_out'), inArea: area('bytes_in'), outArea: area('bytes_out') }
})

defineExpose({ top: () => model.value.top })
</script>

<template>
  <svg :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" class="spark" role="img" aria-label="Traffic over time">
    <line v-for="g in [0.25, 0.5, 0.75]" :key="g" x1="0" :x2="W" :y1="H * g" :y2="H * g" class="grid" />
    <path :d="model.inArea" class="in-area" />
    <path :d="model.inLine" class="in-line" />
    <path :d="model.outArea" class="out-area" />
    <path :d="model.outLine" class="out-line" />
  </svg>
</template>

<style scoped>
.spark {
  width: 100%;
  height: v-bind("H + 'px'");
  display: block;
}
.grid {
  stroke: var(--border);
  stroke-width: 1;
  stroke-dasharray: 3 5;
  vector-effect: non-scaling-stroke;
}
.in-area {
  fill: color-mix(in srgb, var(--muted) 16%, transparent);
}
.in-line {
  fill: none;
  stroke: var(--muted);
  stroke-width: 1.5;
  vector-effect: non-scaling-stroke;
}
.out-area {
  fill: color-mix(in srgb, var(--accent) 28%, transparent);
}
.out-line {
  fill: none;
  stroke: var(--accent);
  stroke-width: 2;
  vector-effect: non-scaling-stroke;
}
</style>
