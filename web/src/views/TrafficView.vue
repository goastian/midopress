<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { ApiFailure, api } from '../api/client'
import type { Flow, ValidationIssue } from '../api/types'
import { on, settings } from '../composables/live'
import { bytes, clock, ms, percent } from '../composables/format'
import { toastError } from '../composables/toast'
import TrafficDrawer from '../components/TrafficDrawer.vue'
import UiToggle from '../components/UiToggle.vue'

const CAP = 500
const kinds = ['image', 'text', 'font', 'other']

const flows = shallowRef<Flow[]>([])
const total = ref(0)
const filter = ref('')
const kind = ref('')
const issues = ref<ValidationIssue[]>([])
const loadError = ref('')
const paused = ref(false)
const missed = ref(0)
const selected = ref<Flow | null>(null)
const busy = ref(false)

let queue: Flow[] = []
let buffered: Flow[] = []
let flushTimer: ReturnType<typeof setTimeout> | undefined
let debounce: ReturnType<typeof setTimeout> | undefined
let refetchTimer: ReturnType<typeof setTimeout> | undefined
let seq = 0

const filtered = computed(() => filter.value.trim() !== '')

async function load() {
  const my = ++seq
  if (!settings.value.flows) {
    flows.value = []
    total.value = 0
    return
  }
  try {
    const r = await api.flows({ filter: filter.value.trim(), kind: kind.value, limit: CAP })
    if (my !== seq) return
    flows.value = r.flows
    total.value = r.total
    issues.value = []
    loadError.value = ''
    queue = []
    buffered = []
    missed.value = 0
  } catch (e) {
    if (my !== seq) return
    if (e instanceof ApiFailure && e.status === 422) {
      issues.value = e.body.issues?.length ? e.body.issues : [{ index: -1, field: 'filter', column: 0, message: e.message }]
    } else {
      issues.value = []
      loadError.value = e instanceof Error ? e.message : String(e)
    }
  }
}

function later() {
  clearTimeout(debounce)
  debounce = setTimeout(load, 300)
}
watch([filter], later)
watch(kind, load)
watch(() => settings.value.flows, load)

function merge(list: Flow[]) {
  if (!list.length) return
  const have = new Set(flows.value.map((f) => f.id))
  const fresh = list.filter((f) => !have.has(f.id)).reverse()
  total.value += fresh.length
  flows.value = [...fresh, ...flows.value].slice(0, CAP)
}

function flush() {
  flushTimer = undefined
  const q = queue
  queue = []
  merge(q)
}

on('flow', (f) => {
  if (!settings.value.flows) return
  if (kind.value && f.kind !== kind.value) return
  if (paused.value) {
    missed.value++
    if (!filtered.value) buffered.push(f)
    return
  }
  if (filtered.value) {
    if (!refetchTimer) refetchTimer = setTimeout(() => ((refetchTimer = undefined), void load()), 1000)
    return
  }
  queue.push(f)
  flushTimer ??= setTimeout(flush, 200)
})

function resume() {
  paused.value = false
  if (filtered.value) void load()
  else {
    merge(buffered)
    buffered = []
    missed.value = 0
  }
}

async function setSetting(patch: { flows?: boolean; capture?: boolean }) {
  busy.value = true
  try {
    settings.value = await api.saveSettings(patch)
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function clearList() {
  try {
    await api.clearFlows()
    flows.value = []
    total.value = 0
    queue = []
    buffered = []
    missed.value = 0
  } catch (e) {
    toastError(e)
  }
}

async function exportHar() {
  try {
    await api.exportHar()
  } catch (e) {
    toastError(e)
  }
}

function clearFilters() {
  filter.value = ''
  kind.value = ''
}

onMounted(load)
onBeforeUnmount(() => {
  clearTimeout(flushTimer)
  clearTimeout(debounce)
  clearTimeout(refetchTimer)
})

const cursorHint = computed(() => {
  const i = issues.value[0]
  if (!i || !i.column) return ''
  return ' '.repeat(Math.max(0, i.column - 1)) + '^'
})

function statusClass(s: number) {
  return s >= 500 ? 'bad' : s >= 400 ? 'warnc' : s >= 300 ? 'muted' : ''
}
function mime(t: string) {
  return t.split(';')[0].trim()
}
function savedPct(f: Flow) {
  return f.bytes_in > 0 && f.bytes_out < f.bytes_in ? ((f.bytes_in - f.bytes_out) / f.bytes_in) * 100 : 0
}
function path(f: Flow) {
  return f.path || '/'
}
function open(f: Flow) {
  selected.value = f
}
</script>

<template>
  <div class="page traffic">
    <h1>Traffic</h1>

    <div class="switches">
      <div>
        <UiToggle :model-value="settings.flows" :disabled="busy" label="Live list" @update:model-value="setSetting({ flows: $event })" />
        <p class="muted small">Keeps the last responses, with host and path, in memory only. Off discards them.</p>
      </div>
      <div>
        <UiToggle :model-value="settings.capture" :disabled="busy" label="Keep images" @update:model-value="setSetting({ capture: $event })" />
        <p class="muted small">Keeps image bodies in memory to compare before and after. Off discards them.</p>
      </div>
    </div>

    <div v-if="!settings.flows" class="panel empty">
      <p><strong>The live list is off.</strong></p>
      <p class="muted">
        Turning it on records, for each response through the proxy, the method, URL, status, sizes, codec, decision and headers.
        It lives in memory only and is never written to disk. Values of Authorization and cookies are never stored, and you can
        empty the list at any time.
      </p>
      <button class="btn" :disabled="busy" @click="setSetting({ flows: true })">Turn on the live list</button>
    </div>

    <template v-else>
      <div class="toolbar">
        <div class="grow filterbox">
          <input
            v-model="filter"
            class="input mono"
            :class="{ bad: issues.length }"
            type="search"
            placeholder="Filter, for example ~d example\.com &amp; ~t image/"
            aria-label="Filter with the rules language"
            spellcheck="false"
            autocomplete="off"
            :aria-invalid="issues.length > 0"
          />
          <div v-if="issues.length" class="error-text" role="alert">
            <pre v-if="cursorHint" class="mono caret">{{ filter }}
{{ cursorHint }}</pre>
            <div v-for="(i, n) in issues" :key="n">{{ i.column ? `column ${i.column}: ` : '' }}{{ i.message }}</div>
          </div>
        </div>
        <div class="chips" role="group" aria-label="Kind">
          <button :class="{ on: !kind }" :aria-pressed="!kind" @click="kind = ''">all</button>
          <button v-for="k in kinds" :key="k" :class="{ on: kind === k }" :aria-pressed="kind === k" @click="kind = kind === k ? '' : k">{{ k }}</button>
        </div>
        <div class="actions">
          <button v-if="!paused" class="btn small" @click="paused = true">Pause</button>
          <button v-else class="btn small" @click="resume">Resume<span v-if="missed"> ({{ missed }} new)</span></button>
          <button class="btn small" :disabled="!filter && !kind" @click="clearFilters">Clear filter</button>
          <button class="btn small" @click="clearList">Empty list</button>
          <button class="btn small" @click="exportHar">Export HAR</button>
        </div>
      </div>

      <p v-if="loadError" class="error-text">{{ loadError }}</p>
      <p class="muted small count">
        {{ flows.length }} shown<template v-if="!filtered && !kind"> of {{ total }}</template>
        <template v-if="paused"> - paused</template>
      </p>

      <div v-if="!flows.length" class="panel empty muted">
        <template v-if="filtered || kind">Nothing matches this filter.</template>
        <template v-else>Waiting for traffic. Use the proxy from a device and responses will appear here.</template>
      </div>

      <div v-else class="tablewrap">
        <table>
          <thead>
            <tr>
              <th class="hm">Time</th>
              <th class="hs">Method</th>
              <th class="path">URL</th>
              <th>Status</th>
              <th class="hs">Kind</th>
              <th class="hm">Type</th>
              <th class="r">Size</th>
              <th class="hm">Codec</th>
              <th class="hm hl">Profile</th>
              <th class="r hs">Time</th>
              <th class="hs">Decision</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="f in flows"
              :key="f.id"
              tabindex="0"
              :class="{ sel: selected?.id === f.id }"
              @click="open(f)"
              @keydown.enter="open(f)"
              @keydown.space.prevent="open(f)"
            >
              <td class="hm mono muted">{{ clock(f.time) }}</td>
              <td class="hs mono">{{ f.method }}</td>
              <td class="path mono" :title="`${f.scheme}://${f.host}${f.path}`">
                <span class="host">{{ f.host }}</span><span class="muted">{{ path(f) }}</span>
              </td>
              <td class="mono" :class="statusClass(f.status)">{{ f.status }}</td>
              <td class="hs">{{ f.kind }}</td>
              <td class="hm mono muted" :title="f.out_content_type ? `${f.content_type} -> ${f.out_content_type}` : f.content_type">{{ mime(f.out_content_type || f.content_type) }}</td>
              <td class="r mono">
                <template v-if="f.bytes_in && f.bytes_in !== f.bytes_out">
                  <span class="muted hs">{{ bytes(f.bytes_in) }} -&gt;</span> {{ bytes(f.bytes_out) }}
                  <span v-if="savedPct(f) > 0" class="ok">-{{ percent(savedPct(f), 0) }}</span>
                </template>
                <template v-else>{{ bytes(f.bytes_out) }}</template>
              </td>
              <td class="hm mono">{{ f.codec || '' }}</td>
              <td class="hm hl mono muted">{{ f.profile }}</td>
              <td class="r hs mono muted">{{ ms(f.duration_ms) }}</td>
              <td class="hs">
                <span v-if="f.cache_hit" class="tag">cache</span>
                <span v-if="f.decision === 'transformed'" class="ok">transformed</span>
                <span v-else class="muted">{{ f.reason }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <TrafficDrawer v-if="selected" :flow="selected" @close="selected = null" />
  </div>
</template>

<style scoped>
h1 {
  font-size: 1.1rem;
  font-weight: 600;
}
.traffic {
  gap: 0.7rem;
}
.switches {
  display: flex;
  gap: 0.5rem 2rem;
  flex-wrap: wrap;
}
.switches > div {
  max-width: 34rem;
}
.switches p {
  margin-top: 0.15rem;
}
.panel {
  border: 1px solid var(--border);
  background: var(--surface);
  padding: 1rem;
}
.empty {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  align-items: flex-start;
  text-align: left;
  max-width: 44rem;
  font-size: 0.9rem;
}
.toolbar {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
  align-items: flex-start;
}
.filterbox {
  flex: 1 1 18rem;
}
.input {
  min-height: 32px;
  padding: 0.2rem 0.5rem;
  border-radius: 2px;
  font-size: 0.85rem;
}
.caret {
  margin: 0.2rem 0 0.1rem;
  font-size: 0.8rem;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.chips {
  display: inline-flex;
  border: 1px solid var(--border);
  height: 32px;
}
.chips button {
  appearance: none;
  border: 0;
  background: none;
  color: var(--muted);
  font: inherit;
  font-size: 0.82rem;
  padding: 0 0.6rem;
  cursor: pointer;
}
.chips button + button {
  border-left: 1px solid var(--border);
}
.chips button.on {
  background: var(--surface-2);
  color: var(--text);
}
.chips button:focus-visible,
tbody tr:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.actions {
  display: flex;
  gap: 0.35rem;
  flex-wrap: wrap;
}
.btn {
  border-radius: 2px;
  min-height: 32px;
  font-weight: 400;
}
.count {
  margin-top: -0.2rem;
}
.tablewrap {
  overflow-x: auto;
  border: 1px solid var(--border);
  background: var(--surface);
}
table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.82rem;
}
th {
  text-align: left;
  font-weight: 400;
  color: var(--muted);
  padding: 0.3rem 0.6rem;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
  position: sticky;
  top: 0;
  background: var(--surface);
}
td {
  padding: 0.22rem 0.6rem;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
tbody tr {
  cursor: pointer;
}
tbody tr:hover td {
  background: var(--surface-2);
}
tbody tr.sel td {
  background: var(--accent-soft);
}
.r {
  text-align: right;
}
.path {
  width: 100%;
  max-width: 0;
  min-width: 9rem;
  overflow: hidden;
  text-overflow: ellipsis;
}
.warnc {
  color: var(--warn);
}
.tag {
  border: 1px solid var(--border);
  padding: 0 0.3rem;
  margin-right: 0.35rem;
  font-size: 0.75rem;
  color: var(--muted);
}
@media (max-width: 1500px) {
  .hl {
    display: none;
  }
}
@media (max-width: 1100px) {
  .hm {
    display: none;
  }
}
@media (max-width: 700px) {
  .hs {
    display: none;
  }
}
</style>
