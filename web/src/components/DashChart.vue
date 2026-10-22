<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import type { SeriesPoint } from '../api/types'
import { bytes } from '../composables/format'

const props = defineProps<{ points: SeriesPoint[]; unit: 's' | 'min' }>()

const H = 190
const PAD = { l: 58, r: 8, t: 8, b: 20 }
const box = ref<HTMLElement | null>(null)
const W = ref(600)
const hover = ref<number | null>(null)
let ro: ResizeObserver | undefined

onMounted(() => {
  if (!box.value) return
  W.value = box.value.clientWidth || 600
  ro = new ResizeObserver(() => (W.value = box.value?.clientWidth || W.value))
  ro.observe(box.value)
})
onBeforeUnmount(() => ro?.disconnect())

const span = computed(() => (props.unit === 's' ? 120 : 180))
const last = computed(() => props.points[props.points.length - 1]?.t ?? 0)
const first = computed(() => last.value - (span.value - 1) * (props.unit === 's' ? 1 : 60))
const step = computed(() => (props.unit === 's' ? 1 : 60))

const yMax = computed(() => {
  const m = Math.max(0, ...props.points.map((p) => p.bytes_in))
  if (m <= 0) return 1000
  const pow = 10 ** Math.floor(Math.log10(m))
  const n = m / pow
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * pow
})

const iw = computed(() => Math.max(10, W.value - PAD.l - PAD.r))
const ih = H - PAD.t - PAD.b
const x = (t: number) => PAD.l + ((t - first.value) / ((span.value - 1) * step.value)) * iw.value
const y = (v: number) => PAD.t + ih - (v / yMax.value) * ih

function line(key: 'bytes_in' | 'bytes_out') {
  return props.points.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p[key]).toFixed(1)}`).join('')
}
const area = computed(() => {
  if (!props.points.length) return ''
  const top = props.points.map((p) => `${x(p.t).toFixed(1)},${y(p.bytes_in).toFixed(1)}`)
  const bot = props.points.map((p) => `${x(p.t).toFixed(1)},${y(p.bytes_out).toFixed(1)}`).reverse()
  return `M${top.join('L')}L${bot.join('L')}Z`
})

const yTicks = computed(() => [0, 0.5, 1].map((f) => ({ v: yMax.value * f, y: y(yMax.value * f) })))
const xTicks = computed(() => {
  const marks: [number, string][] =
    props.unit === 's'
      ? [[119, '-2m'], [90, '-90s'], [60, '-60s'], [30, '-30s'], [0, 'now']]
      : [[179, '-3h'], [150, '-150m'], [120, '-2h'], [90, '-90m'], [60, '-1h'], [30, '-30m'], [0, 'now']]
  return marks.map(([back, label]) => ({ x: x(first.value + (span.value - 1 - back) * step.value), label }))
})

function move(e: PointerEvent) {
  const r = (e.currentTarget as SVGElement).getBoundingClientRect()
  const px = e.clientX - r.left
  const idx = Math.round(((px - PAD.l) / iw.value) * (span.value - 1))
  const t = first.value + idx * step.value
  const i = props.points.findIndex((p) => p.t === t)
  hover.value = i >= 0 ? i : null
}

const tip = computed(() => {
  if (hover.value == null) return null
  const p = props.points[hover.value]
  if (!p) return null
  const d = new Date(p.t * 1000)
  const time = props.unit === 's' ? d.toLocaleTimeString(undefined, { hour12: false }) : d.toLocaleTimeString(undefined, { hour12: false, hour: '2-digit', minute: '2-digit' })
  const px = x(p.t)
  return { p, time, px, left: px > W.value * 0.6 }
})
const perUnit = computed(() => (props.unit === 's' ? '/s' : '/min'))
</script>

<template>
  <div ref="box" class="chart">
    <svg :width="W" :height="H" role="img" aria-label="Bytes received from origins and sent to clients over time" @pointermove="move" @pointerleave="hover = null">
      <g class="grid">
        <line v-for="t in yTicks" :key="t.v" :x1="PAD.l" :x2="W - PAD.r" :y1="t.y" :y2="t.y" />
      </g>
      <g class="axis">
        <text v-for="t in yTicks" :key="t.v" :x="PAD.l - 6" :y="t.y + 3" text-anchor="end">{{ bytes(t.v, 0) }}{{ perUnit }}</text>
        <text v-for="t in xTicks" :key="t.label + t.x" :x="t.x" :y="H - 6" :text-anchor="t.label === 'now' ? 'end' : t.x < PAD.l + 4 ? 'start' : 'middle'">{{ t.label }}</text>
      </g>
      <path class="saved" :d="area" />
      <path class="in" :d="line('bytes_in')" />
      <path class="out" :d="line('bytes_out')" />
      <g v-if="tip">
        <line class="cursor" :x1="tip.px" :x2="tip.px" :y1="PAD.t" :y2="H - PAD.b" />
        <circle class="dot-in" :cx="tip.px" :cy="y(tip.p.bytes_in)" r="3" />
        <circle class="dot-out" :cx="tip.px" :cy="y(tip.p.bytes_out)" r="3" />
      </g>
    </svg>
    <div v-if="tip" class="tip mono" :style="tip.left ? { right: W - tip.px + 8 + 'px' } : { left: tip.px + 8 + 'px' }">
      <div>{{ tip.time }}</div>
      <div>in {{ bytes(tip.p.bytes_in) }}</div>
      <div>out {{ bytes(tip.p.bytes_out) }}</div>
      <div class="ok">saved {{ bytes(tip.p.bytes_in - tip.p.bytes_out) }}</div>
      <div>{{ tip.p.requests }} req</div>
    </div>
    <div class="legend small muted">
      <span><i class="k in" /> from origins</span>
      <span><i class="k out" /> sent to clients</span>
      <span><i class="k saved" /> saved</span>
    </div>
  </div>
</template>

<style scoped>
.chart {
  position: relative;
  min-width: 0;
}
svg {
  display: block;
  touch-action: pan-y;
  font-family: var(--mono);
}
.grid line {
  stroke: var(--border);
  stroke-width: 1;
}
.axis text {
  fill: var(--muted);
  font-size: 10px;
}
path {
  fill: none;
  stroke-width: 1.5;
  stroke-linejoin: round;
}
.in {
  stroke: var(--muted);
}
.out {
  stroke: var(--text);
}
.saved {
  fill: color-mix(in srgb, var(--accent) 22%, transparent);
  stroke: none;
}
.cursor {
  stroke: var(--muted);
  stroke-dasharray: 2 3;
}
.dot-in {
  fill: var(--muted);
}
.dot-out {
  fill: var(--text);
}
.tip {
  position: absolute;
  top: 6px;
  pointer-events: none;
  background: var(--surface);
  border: 1px solid var(--border);
  padding: 0.25rem 0.45rem;
  font-size: 0.75rem;
  line-height: 1.35;
  white-space: nowrap;
}
.legend {
  display: flex;
  gap: 1rem;
  flex-wrap: wrap;
  margin-top: 0.3rem;
}
.k {
  display: inline-block;
  width: 14px;
  height: 0;
  border-top: 2px solid;
  vertical-align: middle;
  margin-right: 0.25rem;
}
.k.in {
  border-color: var(--muted);
}
.k.out {
  border-color: var(--text);
}
.k.saved {
  height: 8px;
  border: 0;
  background: color-mix(in srgb, var(--accent) 30%, transparent);
}
</style>
