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
const fresh = new Set<string>()
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
    fresh.clear()
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
  const added = list.filter((f) => !have.has(f.id)).reverse()
  added.forEach((f) => fresh.add(f.id))
  total.value += added.length
  flows.value = [...added, ...flows.value].slice(0, CAP)
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
  return s >= 500 ? 'bad' : s >= 400 ? 'warn' : ''
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
    <div class="page-head">
      <div>
        <h1>Traffic</h1>
        <p>Every response through the proxy, as it happens. Kept in memory only.</p>
      </div>
      <div class="switches">
        <UiToggle :model-value="settings.flows" :disabled="busy" label="Live list" @update:model-value="setSetting({ flows: $event })" />
        <UiToggle :model-value="settings.capture" :disabled="busy" label="Keep images" @update:model-value="setSetting({ capture: $event })" />
      </div>
    </div>

    <div v-if="!settings.flows" class="off">
      <p><strong>The live list is off.</strong></p>
      <p class="muted">
        Turn it on to record, for each response, the method, URL, status, sizes, codec, decision and headers. It lives in memory only and is
        never written to disk, Authorization and cookie values are never stored, and you can empty the list at any time.
      </p>
      <button class="btn primary" :disabled="busy" @click="setSetting({ flows: true })">Turn on the live list</button>
    </div>

    <template v-else>
      <div class="filterbar">
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
        <span class="seg" role="group" aria-label="Kind">
          <button type="button" :aria-pressed="!kind" @click="kind = ''">all</button>
          <button v-for="k in kinds" :key="k" type="button" :aria-pressed="kind === k" @click="kind = kind === k ? '' : k">{{ k }}</button>
        </span>
        <div class="actions">
          <button v-if="!paused" class="btn small" @click="paused = true">Pause</button>
          <button v-else class="btn small primary" @click="resume">Resume<span v-if="missed"> ({{ missed }} new)</span></button>
          <button class="btn small" :disabled="!filter && !kind" @click="clearFilters">Clear filter</button>
          <button class="btn small" @click="clearList">Empty list</button>
          <button class="btn small" @click="exportHar">Export HAR</button>
        </div>
      </div>

      <p v-if="loadError" class="error-text">{{ loadError }}</p>
      <p class="count mono xs muted">
        {{ flows.length }} shown<template v-if="!filtered && !kind"> of {{ total }}</template>
        <template v-if="paused"> - paused</template>
      </p>

      <p v-if="!flows.length" class="empty">
        <template v-if="filtered || kind">Nothing matches this filter. Clear it to see every response.</template>
        <template v-else>Waiting for traffic. Point a device at the proxy from Connect and its responses will appear here.</template>
      </p>

      <div v-else class="tablewrap">
        <table class="ledger sticky">
          <thead>
            <tr>
              <th class="hm" scope="col">Time</th>
              <th class="hs" scope="col">Method</th>
              <th scope="col">URL</th>
              <th scope="col">Status</th>
              <th class="hs" scope="col">Kind</th>
              <th class="hm" scope="col">Type</th>
              <th class="r hs" scope="col">In</th>
              <th class="r" scope="col">Out</th>
              <th class="r" scope="col">Saved</th>
              <th class="hm" scope="col">Codec</th>
              <th class="hl" scope="col">Profile</th>
              <th class="r hs" scope="col">Took</th>
              <th class="hs" scope="col">Decision</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="f in flows"
              :key="f.id"
              tabindex="0"
              :class="{ sel: selected?.id === f.id, fresh: fresh.has(f.id) }"
              :aria-label="`${f.method} ${f.host}${path(f)}, status ${f.status}`"
              @click="open(f)"
              @keydown.enter="open(f)"
              @keydown.space.prevent="open(f)"
            >
              <td class="hm mono muted">{{ clock(f.time) }}</td>
              <td class="hs mono">{{ f.method }}</td>
              <td class="url mono" :title="`${f.scheme}://${f.host}${f.path}`">
                <span>{{ f.host }}</span><span class="muted">{{ path(f) }}</span>
              </td>
              <td class="mono" :class="statusClass(f.status)">{{ f.status }}</td>
              <td class="hs">{{ f.kind }}</td>
              <td class="hm mono muted" :title="f.out_content_type ? `${f.content_type} -> ${f.out_content_type}` : f.content_type">{{ mime(f.out_content_type || f.content_type) }}</td>
              <td class="r hs mono muted">{{ bytes(f.bytes_in) }}</td>
              <td class="r mono">{{ bytes(f.bytes_out) }}</td>
              <td class="r saved">
                <span class="mono" :class="savedPct(f) > 0 ? 'ok' : 'muted'">{{ savedPct(f) > 0 ? percent(savedPct(f), 0) : '' }}</span>
                <span class="mbar"><i :style="{ width: savedPct(f) + '%' }" /></span>
              </td>
              <td class="hm mono">{{ f.codec || '' }}</td>
              <td class="hl mono muted">{{ f.profile }}</td>
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
.traffic {
  gap: 1rem;
}
.switches {
  display: flex;
  gap: 0.6rem 1.5rem;
  flex-wrap: wrap;
}
.off {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  align-items: flex-start;
  max-width: 62ch;
  padding-top: 0.5rem;
}
.filterbar {
  display: flex;
  gap: 0.5rem 0.75rem;
  flex-wrap: wrap;
  align-items: flex-start;
}
.filterbox {
  flex: 1 1 22rem;
}
.caret {
  margin: 0.2rem 0 0.1rem;
  font-size: var(--fs-xs);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.actions {
  display: flex;
  gap: 0.35rem;
  flex-wrap: wrap;
  align-items: center;
}
.filterbar .seg {
  align-self: stretch;
}
.count {
  margin-top: -0.4rem;
}
.tablewrap {
  overflow: auto;
  max-height: calc(100dvh - var(--topbar-h) - var(--statusbar-h) - 15rem);
  min-height: 14rem;
  border-bottom: 1px solid var(--rule-strong);
}
.ledger {
  font-size: var(--fs-xs);
}
.ledger td,
.ledger th {
  padding: 0.22rem 0.7rem 0.22rem 0;
  white-space: nowrap;
}
.ledger td.mono {
  font-size: var(--fs-xs);
}
.ledger th:first-child,
.ledger td:first-child {
  padding-left: 0.1rem;
}
tbody tr {
  cursor: pointer;
}
tbody tr:focus-visible {
  outline-offset: -2px;
}
.ledger td:first-child {
  border-left: 2px solid transparent;
}
tbody tr.sel td {
  background: var(--sunken);
}
tbody tr.sel td:first-child {
  border-left-color: var(--ink);
}
.url {
  width: 100%;
  max-width: 0;
  min-width: 10rem;
  overflow: hidden;
  text-overflow: ellipsis;
}
.url span:first-child {
  color: var(--ink);
}
.tag {
  margin-right: 0.35rem;
}
.saved .mono {
  display: inline-block;
  min-width: 3.5ch;
  margin-right: 0.4rem;
}
.saved .mbar {
  width: 40px;
  height: 3px;
}
@media (prefers-reduced-motion: no-preference) {
  tr.fresh {
    animation: arrive 0.9s ease-out;
  }
  @keyframes arrive {
    from {
      background: var(--leaf-tint);
    }
  }
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
@media (max-width: 720px) {
  .hs {
    display: none;
  }
  .tablewrap {
    max-height: none;
  }
  .ledger td {
    padding-top: 0.6rem;
    padding-bottom: 0.6rem;
    font-size: var(--fs-sm);
  }
  .url {
    min-width: 6rem;
  }
}
</style>
