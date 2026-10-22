<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, triggerRef } from 'vue'
import { api } from '../api/client'
import type { LogLine, LogState } from '../api/types'
import { on } from '../composables/live'
import { clock } from '../composables/format'
import { toast, toastError } from '../composables/toast'

interface Row extends LogLine {
  id: number
  text: string
}

const CAP = 2000
const order = ['DEBUG', 'INFO', 'WARN', 'ERROR']

const rows = shallowRef<Row[]>([])
const held = shallowRef<Row[]>([])
const serverLevel = ref<LogState['level']>('info')
const loaded = ref(false)
const loadError = ref('')
const savingLevel = ref(false)

const query = ref('')
const minLevel = ref('DEBUG')
const paused = ref(false)
const follow = ref(true)
const wrap = ref(false)
const relative = ref(false)
const now = ref(Date.now())

const box = ref<HTMLElement | null>(null)
let nextId = 1
let early: LogLine[] | null = []
let timer: ReturnType<typeof setInterval> | undefined

function fmtVal(v: string): string {
  return /[\s"=]/.test(v) || v === '' ? JSON.stringify(v) : v
}

function toRow(l: LogLine): Row {
  const attrs = Object.entries(l.attrs ?? {})
    .map(([k, v]) => `${k}=${fmtVal(v)}`)
    .join(' ')
  return { ...l, id: nextId++, text: attrs ? `${l.message} ${attrs}` : l.message }
}

function add(list: Row[]) {
  const all = rows.value
  all.push(...list)
  if (all.length > CAP) all.splice(0, all.length - CAP)
  triggerRef(rows)
  if (follow.value) void toBottom()
}

function incoming(l: LogLine) {
  if (early) {
    early.push(l)
    return
  }
  const r = toRow(l)
  if (paused.value) {
    held.value.push(r)
    if (held.value.length > CAP) held.value.splice(0, held.value.length - CAP)
    triggerRef(held)
  } else add([r])
}

on('log', incoming)

async function load() {
  try {
    const st = await api.log()
    serverLevel.value = st.level
    const seen = new Set(st.lines.map((l) => l.time + l.message))
    const initial = st.lines.map(toRow)
    rows.value = initial.slice(-CAP)
    const extra = (early ?? []).filter((l) => !seen.has(l.time + l.message))
    early = null
    loaded.value = true
    if (extra.length) add(extra.map(toRow))
    else void toBottom()
  } catch (e) {
    early = null
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

async function setLevel() {
  savingLevel.value = true
  try {
    const st = await api.setLogLevel(serverLevel.value)
    serverLevel.value = st.level
    toast(`Log level set to ${st.level}`)
  } catch (e) {
    toastError(e)
    void api.log().then((s) => (serverLevel.value = s.level)).catch(() => {})
  } finally {
    savingLevel.value = false
  }
}

const shown = computed(() => {
  const min = order.indexOf(minLevel.value)
  const q = query.value.trim().toLowerCase()
  return rows.value.filter((r) => order.indexOf(r.level) >= min && (!q || r.text.toLowerCase().includes(q)))
})

function resume() {
  paused.value = false
  const h = held.value
  held.value = []
  if (h.length) add(h)
}

function clear() {
  rows.value = []
  held.value = []
  triggerRef(rows)
}

async function toBottom() {
  await nextTick()
  const el = box.value
  if (el) el.scrollTop = el.scrollHeight
}

function onScroll() {
  const el = box.value
  if (!el) return
  follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}

function jump() {
  follow.value = true
  void toBottom()
}

function stamp(iso: string): string {
  if (!relative.value) return clock(iso)
  const s = Math.max(0, Math.round((now.value - new Date(iso).getTime()) / 1000))
  if (s < 60) return `-${s}s`
  if (s < 3600) return `-${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`
  return `-${Math.floor(s / 3600)}h${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
}

function save() {
  const text = shown.value.map((r) => `${r.time} ${r.level.padEnd(5)} ${r.text}`).join('\n') + '\n'
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = 'midopress.log'
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

onMounted(() => {
  void load()
  timer = setInterval(() => (now.value = Date.now()), 1000)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <div class="page logs">
    <div class="page-head">
      <div>
        <h1>Logs</h1>
        <p class="small">The service log, live. URLs are never logged unless <code>log.urls</code> is set.</p>
      </div>
      <div class="level">
        <label for="lv" class="small muted">Service log level</label>
        <select id="lv" v-model="serverLevel" class="select" :disabled="savingLevel" @change="setLevel">
          <option value="debug">debug</option>
          <option value="info">info</option>
          <option value="warn">warn</option>
          <option value="error">error</option>
        </select>
      </div>
    </div>
    <p class="hint">
      The level applies at once and affects what MidoPress keeps and prints, not only this view. Debug is verbose; set it back to info when done.
    </p>

    <div class="bar">
      <input v-model="query" class="input" type="search" placeholder="Filter text" aria-label="Filter text" />
      <select v-model="minLevel" class="select lvl" aria-label="Show from level">
        <option value="DEBUG">show: all</option>
        <option value="INFO">show: info+</option>
        <option value="WARN">show: warn+</option>
        <option value="ERROR">show: error</option>
      </select>
      <div class="row tight">
        <button v-if="!paused" class="btn small" @click="paused = true">Pause</button>
        <button v-else class="btn small primary" @click="resume">Resume{{ held.length ? ` (${held.length})` : '' }}</button>
        <button class="btn small" :class="{ on: wrap }" :aria-pressed="wrap" @click="wrap = !wrap">Wrap</button>
        <button class="btn small" :class="{ on: relative }" :aria-pressed="relative" @click="relative = !relative">
          {{ relative ? 'Relative' : 'Absolute' }}
        </button>
        <button class="btn small" :disabled="!shown.length" @click="save">Download</button>
        <button class="btn small" @click="clear">Clear</button>
      </div>
    </div>

    <div class="status small muted num">
      {{ shown.length }} of {{ rows.length }} lines<span v-if="paused"> - paused, {{ held.length }} waiting</span>
    </div>

    <div class="viewport">
      <div ref="box" class="lines" :class="{ wrap }" tabindex="0" @scroll.passive="onScroll">
        <div v-if="loadError" class="empty">{{ loadError }}</div>
        <div v-else-if="loaded && !shown.length" class="empty">
          {{ rows.length ? 'No line matches the filter.' : 'No lines yet.' }}
        </div>
        <div v-for="r in shown" :key="r.id" class="ln">
          <span class="t num">{{ stamp(r.time) }}</span>
          <span class="lv" :class="r.level.toLowerCase()">{{ r.level.padEnd(5) }}</span>
          <span class="msg"><span class="m">{{ r.message }}</span><template v-for="(v, k) in r.attrs" :key="k">{{ ' ' }}<span class="k">{{ k }}=</span>{{ fmtVal(v) }}</template></span>
        </div>
      </div>
      <button v-if="!follow" class="btn small jump" @click="jump">Jump to latest</button>
    </div>
  </div>
</template>

<style scoped>
.logs {
  gap: 0.6rem;
  height: calc(100dvh - 3rem);
  min-height: 420px;
}
@media (max-width: 860px) {
  .logs {
    height: calc(100dvh - var(--topbar-h) - var(--bottom-nav-h) - 3.5rem);
  }
}
.level {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.level .select {
  width: auto;
}
.bar {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
  align-items: center;
}
.bar .input {
  flex: 1 1 200px;
  width: auto;
}
.bar .select.lvl {
  width: auto;
}
.btn.on {
  border-color: var(--accent);
  color: var(--accent-strong);
}
.viewport {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
}
.lines {
  flex: 1;
  overflow: auto;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  font-family: var(--mono);
  font-size: 0.8rem;
  line-height: 1.5;
  padding: 0.25rem 0;
}
.ln {
  display: flex;
  gap: 0.75rem;
  padding: 0 0.7rem;
  white-space: pre;
  width: max-content;
  min-width: 100%;
}
.ln:hover {
  background: var(--surface-2);
}
.lines.wrap .ln {
  white-space: pre-wrap;
  width: auto;
  word-break: break-word;
}
.t {
  color: var(--muted);
  flex: none;
}
.lv {
  flex: none;
  color: var(--muted);
  white-space: pre;
}
.lv.info {
  color: var(--text);
}
.lv.warn {
  color: var(--warn);
}
.lv.error {
  color: var(--danger);
  font-weight: 600;
}
.msg {
  min-width: 0;
}
.m {
  color: var(--text);
}
.k {
  color: var(--muted);
}
.jump {
  position: absolute;
  right: 1rem;
  bottom: 0.8rem;
  background: var(--surface);
  border-color: var(--accent);
}
.empty {
  font-family: var(--font);
}
</style>
