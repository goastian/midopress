<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import type { SeriesPoint } from '../api/types'
import { bytes } from '../composables/format'

const props = defineProps<{ points: SeriesPoint[]; unit: 's' | 'min' }>()

const H = 232
const PAD = { l: 64, r: 8, t: 14, b: 22 }
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
const step = computed(() => (props.unit === 's' ? 1 : 60))
const first = computed(() => last.value - (span.value - 1) * step.value)

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

const perUnit = computed(() => (props.unit === 's' ? '/s' : '/min'))
const yTicks = computed(() => [0, 0.5, 1].map((f) => ({ v: yMax.value * f, y: y(yMax.value * f) })))
const xTicks = computed(() => {
  const narrow = W.value < 460
  const marks: [number, string][] =
    props.unit === 's'
      ? narrow ? [[119, '-2m'], [60, '-1m'], [0, 'now']] : [[119, '-2m'], [90, '-90s'], [60, '-60s'], [30, '-30s'], [0, 'now']]
      : narrow ? [[179, '-3h'], [120, '-2h'], [60, '-1h'], [0, 'now']] : [[179, '-3h'], [150, '-150m'], [120, '-2h'], [90, '-90m'], [60, '-1h'], [30, '-30m'], [0, 'now']]
  return marks.map(([back, label]) => ({ x: x(first.value + (span.value - 1 - back) * step.value), label }))
})

const end = computed(() => props.points[props.points.length - 1] ?? null)

const peak = computed(() => {
  let best = -1
  let top = 0
  props.points.forEach((p, i) => {
    if (p.bytes_in > top) {
      top = p.bytes_in
      best = i
    }
  })
  if (best < 0) return null
  const p = props.points[best]
  const px = x(p.t)
  const inY = y(p.bytes_in)
  const outY = y(p.bytes_out)
  const right = px < W.value - 140
  const inLeft = px > 130
  return {
    inLabel: { x: inLeft ? px - 8 : px + 8, y: inY + 4, anchor: inLeft ? 'end' : 'start' },
    outLabel: { x: right ? px + 8 : px - 8, y: outY - 7 - (inY + 4) >= 16 ? outY - 7 : outY + 16, anchor: right ? 'start' : 'end' },
  }
})

function tickLabel(v: number) {
  return v === 0 ? '0' : bytes(v, 1).replace('.0 ', ' ') + perUnit.value
}

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
</script>

<template>
  <div ref="box" class="chart">
    <svg
      :width="W"
      :height="H"
      role="img"
      :aria-label="end ? `Bytes per ${unit === 's' ? 'second' : 'minute'}: ${bytes(end.bytes_in)} from origins, ${bytes(end.bytes_out)} sent to clients at the latest point` : 'No traffic yet'"
      @pointermove="move"
      @pointerleave="hover = null"
    >
      <g class="grid">
        <line v-for="t in yTicks" :key="t.v" :x1="PAD.l" :x2="W - PAD.r" :y1="t.y" :y2="t.y" />
      </g>
      <g class="axis">
        <text v-for="t in yTicks" :key="t.v" :x="PAD.l - 8" :y="t.y + 4" text-anchor="end">{{ tickLabel(t.v) }}</text>
        <text v-for="t in xTicks" :key="t.label + t.x" :x="t.x" :y="H - 6" :text-anchor="t.label === 'now' ? 'end' : t.x < PAD.l + 4 ? 'start' : 'middle'">{{ t.label }}</text>
      </g>
      <path class="saved" :d="area" />
      <path class="in" :d="line('bytes_in')" />
      <path class="out" :d="line('bytes_out')" />
      <g v-if="peak" class="label">
        <text class="l-in" :x="peak.inLabel.x" :y="peak.inLabel.y" :text-anchor="peak.inLabel.anchor">from origins</text>
        <text class="l-out" :x="peak.outLabel.x" :y="peak.outLabel.y" :text-anchor="peak.outLabel.anchor">sent to clients</text>
      </g>
      <g v-if="tip">
        <line class="cursor" :x1="tip.px" :x2="tip.px" :y1="PAD.t" :y2="H - PAD.b" />
        <circle class="dot-in" :cx="tip.px" :cy="y(tip.p.bytes_in)" r="3" />
        <circle class="dot-out" :cx="tip.px" :cy="y(tip.p.bytes_out)" r="3" />
      </g>
    </svg>
    <div v-if="tip" class="tip mono" :style="tip.left ? { right: W - tip.px + 10 + 'px' } : { left: tip.px + 10 + 'px' }">
      <div class="muted">{{ tip.time }}</div>
      <div>from origins {{ bytes(tip.p.bytes_in) }}</div>
      <div>sent {{ bytes(tip.p.bytes_out) }}</div>
      <div class="ok">saved {{ bytes(tip.p.bytes_in - tip.p.bytes_out) }}</div>
      <div class="muted">{{ tip.p.requests }} req</div>
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
  font-family: var(--font-mono);
}
.grid line {
  stroke: var(--rule);
  stroke-width: 1;
}
.axis text {
  fill: var(--ink-2);
  font-size: 11px;
}
path {
  fill: none;
  stroke-linejoin: round;
}
.in {
  stroke: var(--ink-2);
  stroke-width: 1.25;
}
.out {
  stroke: var(--ink);
  stroke-width: 1.75;
}
.saved {
  fill: var(--leaf);
  fill-opacity: 0.2;
  stroke: none;
}
.label text {
  font-size: 12px;
}
.l-in {
  fill: var(--ink-2);
}
.l-out {
  fill: var(--ink);
  font-weight: 500;
}
.cursor {
  stroke: var(--ink-3);
  stroke-width: 1;
}
.dot-in {
  fill: var(--paper);
  stroke: var(--ink-2);
  stroke-width: 1.5;
}
.dot-out {
  fill: var(--ink);
}
.tip {
  position: absolute;
  top: 6px;
  pointer-events: none;
  background: var(--surface);
  border: 1px solid var(--rule-strong);
  border-radius: var(--radius);
  padding: 0.3rem 0.5rem;
  font-size: var(--fs-xs);
  line-height: 1.45;
  white-space: nowrap;
}
</style>
