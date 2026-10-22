<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '../api/client'
import type { Flow, FlowCompare } from '../api/types'
import { bytes, distance, percent } from '../composables/format'

const props = defineProps<{ flow: Flow }>()

const before = ref('')
const after = ref('')
const cmp = ref<FlowCompare | null>(null)
const cmpError = ref('')
const error = ref('')
const loading = ref(true)
const mode = ref<'slider' | 'side'>('slider')
const pos = ref(50)
const natural = ref<[number, number] | null>(null)
const stage = ref<HTMLElement | null>(null)
let dead = false

const keep = computed(() => props.flow.has_original && props.flow.has_result)

onMounted(async () => {
  if (!keep.value) {
    loading.value = false
    return
  }
  const id = props.flow.id
  const [a, b, c] = await Promise.allSettled([api.flowBody(id, 'original'), api.flowBody(id, 'transformed'), api.flowCompare(id)])
  if (dead) {
    if (a.status === 'fulfilled') URL.revokeObjectURL(a.value)
    if (b.status === 'fulfilled') URL.revokeObjectURL(b.value)
    return
  }
  if (a.status === 'fulfilled') before.value = a.value
  if (b.status === 'fulfilled') after.value = b.value
  if (a.status === 'rejected' || b.status === 'rejected') {
    const r = (a.status === 'rejected' ? a : b) as PromiseRejectedResult
    error.value = r.reason instanceof Error ? r.reason.message : String(r.reason)
  }
  if (c.status === 'fulfilled') cmp.value = c.value
  else cmpError.value = c.reason instanceof Error ? c.reason.message : String(c.reason)
  loading.value = false
})

onBeforeUnmount(() => {
  dead = true
  if (before.value) URL.revokeObjectURL(before.value)
  if (after.value) URL.revokeObjectURL(after.value)
})

function loaded(e: Event) {
  const img = e.target as HTMLImageElement
  if (!natural.value && img.naturalWidth) natural.value = [img.naturalWidth, img.naturalHeight]
}

const ratioStyle = computed(() => {
  const d = natural.value ?? (cmp.value?.width ? ([cmp.value.width, cmp.value.height] as const) : null)
  return d ? { aspectRatio: `${d[0]} / ${d[1]}` } : { aspectRatio: '4 / 3' }
})

function setFrom(e: PointerEvent) {
  const r = stage.value?.getBoundingClientRect()
  if (!r || !r.width) return
  pos.value = Math.max(0, Math.min(100, ((e.clientX - r.left) / r.width) * 100))
}
function down(e: PointerEvent) {
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  setFrom(e)
}
function drag(e: PointerEvent) {
  if ((e.currentTarget as HTMLElement).hasPointerCapture(e.pointerId)) setFrom(e)
}
function key(e: KeyboardEvent) {
  const step = e.shiftKey ? 10 : 2
  if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') pos.value = Math.max(0, pos.value - step)
  else if (e.key === 'ArrowRight' || e.key === 'ArrowUp') pos.value = Math.min(100, pos.value + step)
  else if (e.key === 'Home') pos.value = 0
  else if (e.key === 'End') pos.value = 100
  else return
  e.preventDefault()
}

const origBytes = computed(() => cmp.value?.original_bytes || props.flow.bytes_in)
const resBytes = computed(() => cmp.value?.result_bytes || props.flow.bytes_out)
const saved = computed(() => (origBytes.value > 0 ? ((origBytes.value - resBytes.value) / origBytes.value) * 100 : 0))
const sentType = computed(() => props.flow.out_content_type || props.flow.content_type)

const verdict = computed(() => {
  const d = cmp.value?.distance
  if (d == null) return ''
  if (d < 0.00001) return 'Identical'
  if (d <= 0.002) return 'Indistinguishable by eye'
  if (d <= 0.005) return 'Very close, differences show only on close inspection'
  if (d <= 0.01) return 'Small differences, visible when zoomed in'
  return 'Visible differences'
})
</script>

<template>
  <section class="cmp">
    <div class="sec-head">
      <h3>Comparison</h3>
      <span v-if="keep && !loading && !error" class="seg" role="group" aria-label="View">
        <button type="button" :aria-pressed="mode === 'slider'" @click="mode = 'slider'">Slider</button>
        <button type="button" :aria-pressed="mode === 'side'" @click="mode = 'side'">Side by side</button>
      </span>
    </div>
    <p v-if="!keep" class="muted small">
      The bodies of this image were not kept: image capture was off when it passed, or it was over the capture limit. Turn on "Keep
      images" at the top of Traffic to keep the next ones, in memory only.
    </p>
    <div v-else-if="loading" class="skeleton" role="status" aria-label="Loading images"><i /><i /><i /></div>
    <p v-else-if="error" class="bad small">{{ error }}</p>
    <template v-else>
      <div v-if="mode === 'slider'" ref="stage" class="stage" :style="ratioStyle" @pointerdown="down" @pointermove="drag">
        <img :src="after" alt="Sent to the client" draggable="false" />
        <img :src="before" alt="Original" draggable="false" class="top" :style="{ clipPath: `inset(0 ${100 - pos}% 0 0)` }" @load="loaded" />
        <span class="tag l mono">Original {{ bytes(origBytes) }}</span>
        <span class="tag r mono">Sent {{ bytes(resBytes) }}<template v-if="saved > 0"> -{{ percent(saved, 0) }}</template></span>
        <div
          class="handle"
          role="slider"
          tabindex="0"
          aria-label="Divider between original and sent"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="Math.round(pos)"
          :style="{ left: pos + '%' }"
          @keydown="key"
        >
          <span class="line" />
          <span class="knob">
            <svg width="16" height="12" viewBox="0 0 16 12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M5 2 1 6l4 4M11 2l4 4-4 4" /></svg>
          </span>
        </div>
      </div>
      <div v-else class="side">
        <figure>
          <figcaption class="mono xs"><b>Original</b> <span class="muted">{{ bytes(origBytes) }} {{ flow.content_type }}</span></figcaption>
          <img :src="before" alt="Original" @load="loaded" />
        </figure>
        <figure>
          <figcaption class="mono xs"><b>Sent</b> <span class="muted">{{ bytes(resBytes) }} {{ sentType }}</span> <span v-if="saved > 0" class="ok">-{{ percent(saved, 0) }}</span></figcaption>
          <img :src="after" alt="Sent to the client" />
        </figure>
      </div>

      <p v-if="cmp?.comparable" class="verdict">
        <span class="mono">DSSIM {{ distance(cmp.distance) }}</span>
        <span :class="cmp.distance <= 0.005 ? 'ok' : cmp.distance > 0.01 ? 'bad' : ''">{{ verdict }}</span>
        <span v-if="cmp.width" class="mono muted">{{ cmp.width }} x {{ cmp.height }}</span>
      </p>
      <p v-else-if="cmp" class="muted small">The pictures differ in size, so the distance could not be computed.</p>
      <p v-else-if="cmpError" class="muted small">Distance not available: {{ cmpError }}</p>
      <p class="hint">
        DSSIM measures structural difference. 0 is identical; around 0.002 can not be told apart by eye; above 0.01 the difference is usually visible.
      </p>
    </template>
  </section>
</template>

<style scoped>
.cmp {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
}
.cmp .sec-head {
  margin-bottom: 0;
}
.stage {
  position: relative;
  width: 100%;
  max-height: 62vh;
  margin: 0 auto;
  border: 1px solid var(--rule-strong);
  background: var(--sunken);
  overflow: hidden;
  touch-action: pan-y;
  cursor: ew-resize;
  user-select: none;
}
.stage img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: contain;
  pointer-events: none;
}
.tag {
  position: absolute;
  top: 8px;
  padding: 0.1rem 0.45rem;
  background: var(--paper);
  color: var(--ink);
  border-color: var(--ink);
  pointer-events: none;
}
.tag.l {
  left: 8px;
}
.tag.r {
  right: 8px;
}
.handle {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 40px;
  margin-left: -20px;
  display: flex;
  justify-content: center;
  align-items: center;
  cursor: ew-resize;
  outline: none;
}
.line {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 4px;
  background: var(--ink);
  border-left: 1px solid var(--paper);
  border-right: 1px solid var(--paper);
}
.knob {
  position: relative;
  display: grid;
  place-items: center;
  width: 26px;
  height: 36px;
  background: var(--ink);
  color: var(--inverse);
  border: 1px solid var(--paper);
  border-radius: var(--radius-lg);
}
.handle:focus-visible .knob {
  outline: 2px solid var(--ink);
  outline-offset: 2px;
}
.side {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
}
.side figure {
  margin: 0;
  min-width: 0;
}
.side img {
  display: block;
  max-width: 100%;
  max-height: 60vh;
  margin-top: 0.3rem;
  border: 1px solid var(--rule-strong);
}
.verdict {
  display: flex;
  gap: 0.2rem 1rem;
  flex-wrap: wrap;
  align-items: baseline;
  font-size: var(--fs-sm);
  padding: 0.4rem 0;
  border-top: 1px solid var(--rule);
  border-bottom: 1px solid var(--rule);
}
@media (max-width: 560px) {
  .side {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
