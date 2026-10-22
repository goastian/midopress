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
    <h3>Compare</h3>
    <p v-if="!keep" class="muted small">
      The bodies of this image were not kept: image capture was off when it passed, or it was over the capture limit. Turn on "Keep
      images" in the toolbar to keep the next ones, in memory only.
    </p>
    <p v-else-if="loading" class="muted small">Loading images...</p>
    <p v-else-if="error" class="bad small">{{ error }}</p>
    <template v-else>
      <div class="bar">
        <div class="seg" role="group" aria-label="View">
          <button :class="{ on: mode === 'slider' }" :aria-pressed="mode === 'slider'" @click="mode = 'slider'">Slider</button>
          <button :class="{ on: mode === 'side' }" :aria-pressed="mode === 'side'" @click="mode = 'side'">Side by side</button>
        </div>
      </div>

      <div v-if="mode === 'slider'" ref="stage" class="stage" :style="ratioStyle" @pointerdown="down" @pointermove="drag">
        <img :src="after" alt="Sent to the client" draggable="false" />
        <img :src="before" alt="Original" draggable="false" class="top" :style="{ clipPath: `inset(0 ${100 - pos}% 0 0)` }" @load="loaded" />
        <span class="tag l">Original</span>
        <span class="tag r">Sent</span>
        <div
          class="handle"
          role="slider"
          tabindex="0"
          aria-label="Original versus sent"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="Math.round(pos)"
          :style="{ left: pos + '%' }"
          @keydown="key"
        >
          <span class="grip" />
        </div>
      </div>
      <div v-else class="side">
        <figure>
          <figcaption class="muted small">Original</figcaption>
          <img :src="before" alt="Original" @load="loaded" />
        </figure>
        <figure>
          <figcaption class="muted small">Sent</figcaption>
          <img :src="after" alt="Sent to the client" />
        </figure>
      </div>

      <dl class="sizes mono">
        <div><dt>Original</dt><dd>{{ bytes(origBytes) }} <span class="muted">{{ flow.content_type }}</span></dd></div>
        <div><dt>Sent</dt><dd>{{ bytes(resBytes) }} <span class="muted">{{ sentType }}</span> <span v-if="saved > 0" class="ok">-{{ percent(saved) }}</span></dd></div>
        <div v-if="cmp?.width"><dt>Dimensions</dt><dd>{{ cmp.width }} x {{ cmp.height }}</dd></div>
        <div v-if="cmp?.comparable">
          <dt>DSSIM</dt>
          <dd>{{ distance(cmp.distance) }} <span class="muted sans">{{ verdict }}</span></dd>
        </div>
      </dl>
      <p v-if="cmp && !cmp.comparable" class="muted small">The pictures differ in size, so the distance could not be computed.</p>
      <p v-else-if="cmpError" class="muted small">Distance not available: {{ cmpError }}</p>
      <p class="muted small">
        DSSIM measures structural difference between the two pictures. 0 means identical; around 0.002 can not be told apart by eye; above 0.01
        the difference is usually visible.
      </p>
    </template>
  </section>
</template>

<style scoped>
h3 {
  font-size: 0.9rem;
  font-weight: 600;
  margin-bottom: 0.4rem;
}
.cmp {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.stage {
  position: relative;
  width: 100%;
  max-height: 70vh;
  margin: 0 auto;
  border: 1px solid var(--border);
  background: repeating-conic-gradient(var(--surface-2) 0 25%, var(--surface) 0 50%) 0 0 / 16px 16px;
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
  top: 6px;
  font-size: 0.72rem;
  padding: 0.05rem 0.4rem;
  background: var(--surface);
  border: 1px solid var(--border);
  pointer-events: none;
}
.tag.l {
  left: 6px;
}
.tag.r {
  right: 6px;
}
.handle {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 28px;
  margin-left: -14px;
  display: flex;
  justify-content: center;
  cursor: ew-resize;
  outline: none;
}
.grip {
  width: 2px;
  height: 100%;
  background: var(--text);
  box-shadow: 0 0 0 1px var(--surface);
}
.handle:focus-visible .grip {
  width: 4px;
  background: var(--accent);
}
.side {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.5rem;
}
.side figure {
  margin: 0;
  min-width: 0;
}
.side img {
  display: block;
  max-width: 100%;
  max-height: 60vh;
  margin-top: 0.2rem;
  border: 1px solid var(--border);
}
@media (max-width: 560px) {
  .side {
    grid-template-columns: minmax(0, 1fr);
  }
}
.sizes {
  margin: 0;
  font-size: 0.82rem;
}
.sizes > div {
  display: flex;
  gap: 0.75rem;
  padding: 0.2rem 0;
  border-bottom: 1px solid var(--border);
}
.sizes dt {
  width: 6.5rem;
  flex: none;
  color: var(--muted);
}
.sizes dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}
.sans {
  font-family: var(--font);
}
.seg {
  display: inline-flex;
  border: 1px solid var(--border);
}
.seg button {
  appearance: none;
  border: 0;
  background: none;
  color: var(--muted);
  font: inherit;
  font-size: 0.82rem;
  padding: 0.2rem 0.6rem;
  cursor: pointer;
}
.seg button + button {
  border-left: 1px solid var(--border);
}
.seg button.on {
  background: var(--surface-2);
  color: var(--text);
}
</style>
