<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../api/client'
import { settings, stats } from '../composables/live'
import { bytes, count, ms, percent, ratio } from '../composables/format'
import { toastError } from '../composables/toast'
import type { Bucket } from '../api/types'
import DashChart from '../components/DashChart.vue'
import DashTable, { type DashRow } from '../components/DashTable.vue'
import UiToggle from '../components/UiToggle.vue'

const range = ref<'s' | 'min'>('s')
const busy = ref(false)

const points = computed(() => (range.value === 's' ? stats.value?.series.seconds : stats.value?.series.minutes) ?? [])

function pct(b: Bucket) {
  return b.bytes_in > 0 ? ((b.bytes_in - b.bytes_out) / b.bytes_in) * 100 : 0
}

function bucketRows(m: Record<string, Bucket> | undefined): DashRow[] {
  return Object.entries(m ?? {})
    .sort((a, b) => b[1].bytes_in - a[1].bytes_in)
    .map(([k, b]) => ({
      key: k,
      label: k,
      cells: [count(b.requests), bytes(b.bytes_in), bytes(b.bytes_out)],
      bar: pct(b) / 100,
      barText: percent(pct(b)),
    }))
}

const kinds = computed(() => bucketRows(stats.value?.by_kind))
const codecs = computed(() => bucketRows(stats.value?.by_codec))
const profiles = computed(() => bucketRows(stats.value?.by_profile))

const decisions = computed<DashRow[]>(() => {
  const e = Object.entries(stats.value?.by_decision ?? {}).sort((a, b) => b[1] - a[1])
  const total = e.reduce((s, x) => s + x[1], 0) || 1
  return e.map(([k, n]) => ({ key: k, label: k, cells: [count(n)], bar: n / total, barText: percent((n / total) * 100, 0), green: k === 'transformed' }))
})

const domains = computed<DashRow[]>(() => {
  const d = [...(stats.value?.top_domains ?? [])].sort((a, b) => b.bytes_in - b.bytes_out - (a.bytes_in - a.bytes_out))
  const max = Math.max(1, ...d.map((x) => x.bytes_in - x.bytes_out))
  return d.slice(0, 15).map((x) => ({
    key: x.domain,
    label: x.domain,
    cells: [count(x.requests), bytes(x.bytes_in), bytes(x.bytes_out)],
    bar: (x.bytes_in - x.bytes_out) / max,
    barText: `${bytes(x.bytes_in - x.bytes_out)}  ${percent(pct(x))}`,
  }))
})

const s = computed(() => stats.value)
const transformedShare = computed(() => (s.value && s.value.totals.requests > 0 ? s.value.totals.transformed / s.value.totals.requests : 0))
const cacheTotal = computed(() => (s.value ? s.value.cache.hits + s.value.cache.misses : 0))

async function setDomains(on: boolean) {
  busy.value = true
  try {
    settings.value = await api.saveSettings({ domains: on })
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page dash">
    <h1>Dashboard</h1>

    <div v-if="!s" class="panel muted small">Waiting for the first update...</div>

    <template v-else>
      <dl class="strip">
        <div>
          <dt>Traffic</dt>
          <dd class="mono">{{ bytes(s.totals.bytes_in) }} <span class="muted">to</span> {{ bytes(s.totals.bytes_out) }}</dd>
        </div>
        <div>
          <dt>Saved</dt>
          <dd class="mono ok">{{ bytes(s.totals.saved) }} ({{ percent(s.totals.saved_percent) }})</dd>
        </div>
        <div>
          <dt>Requests</dt>
          <dd class="mono">{{ count(s.totals.requests) }}</dd>
        </div>
        <div>
          <dt>Transformed</dt>
          <dd class="mono">{{ count(s.totals.transformed) }} ({{ ratio(transformedShare, 0) }})</dd>
        </div>
        <div>
          <dt>Cache hit rate</dt>
          <dd class="mono">{{ cacheTotal ? ratio(s.cache.hit_rate) : '-' }}</dd>
        </div>
        <div>
          <dt>Latency p50 / p95 / p99</dt>
          <dd class="mono">{{ s.latency.samples ? `${ms(s.latency.p50_ms)} / ${ms(s.latency.p95_ms)} / ${ms(s.latency.p99_ms)}` : '-' }}</dd>
        </div>
        <div>
          <dt>In flight</dt>
          <dd class="mono">{{ s.connections.requests_in_flight }} req, {{ s.connections.clients_active }} clients, {{ s.connections.tunnels_active }} tunnels</dd>
        </div>
        <div>
          <dt>CPU slots busy</dt>
          <dd class="mono">{{ s.cpu.busy }} / {{ s.cpu.slots }}<span v-if="s.cpu.background_queue" class="muted"> ({{ s.cpu.background_queue }} queued)</span></dd>
        </div>
      </dl>

      <section class="panel">
        <header>
          <h2>Throughput</h2>
          <div class="seg" role="group" aria-label="Chart range">
            <button :class="{ on: range === 's' }" :aria-pressed="range === 's'" @click="range = 's'">Last 2 min</button>
            <button :class="{ on: range === 'min' }" :aria-pressed="range === 'min'" @click="range = 'min'">Last 3 h</button>
          </div>
        </header>
        <DashChart :points="points" :unit="range" />
      </section>

      <div class="cols">
        <section class="panel">
          <header><h2>By kind</h2></header>
          <DashTable :head="['Kind', 'Req', 'In', 'Out', 'Saved']" :rows="kinds" />
        </section>
        <section class="panel">
          <header><h2>By codec sent</h2></header>
          <DashTable :head="['Codec', 'Req', 'In', 'Out', 'Saved']" :rows="codecs" />
        </section>
        <section class="panel">
          <header><h2>By profile</h2></header>
          <DashTable :head="['Profile', 'Req', 'In', 'Out', 'Saved']" :rows="profiles" />
        </section>
        <section class="panel">
          <header>
            <h2>Decisions</h2>
            <span class="muted small">why responses were left alone</span>
          </header>
          <DashTable :head="['Decision', 'Responses', 'Share']" :rows="decisions" />
        </section>
      </div>

      <section class="panel">
        <header>
          <h2>Top domains by bytes saved</h2>
          <UiToggle :model-value="settings.domains" :disabled="busy" label="Track domains" @update:model-value="setDomains" />
        </header>
        <p class="muted small note">Records host names (never paths) in memory while on. Turning it off discards them.</p>
        <DashTable
          v-if="settings.domains && s.top_domains"
          :head="['Domain', 'Req', 'In', 'Out', 'Saved']"
          :rows="domains"
          empty="No traffic yet."
        />
        <p v-else class="muted small">Off. Turn it on to see which sites save the most. Counting starts from that moment.</p>
      </section>

      <section class="panel">
        <header><h2>Cache</h2></header>
        <dl class="kv">
          <div><dt>Memory</dt><dd class="mono">{{ bytes(s.cache.memory_bytes) }} in {{ count(s.cache.memory_items) }} items</dd></div>
          <div><dt>Disk</dt><dd class="mono">{{ bytes(s.cache.disk_bytes) }} in {{ count(s.cache.disk_items) }} items</dd></div>
          <div><dt>Hits / misses</dt><dd class="mono">{{ count(s.cache.hits) }} / {{ count(s.cache.misses) }}</dd></div>
          <div><dt>Evictions</dt><dd class="mono">{{ count(s.cache.evictions) }}</dd></div>
          <div><dt>Expired</dt><dd class="mono">{{ count(s.cache.expired) }}</dd></div>
          <div><dt>Disk errors</dt><dd class="mono" :class="{ bad: s.cache.disk_errors > 0 }">{{ count(s.cache.disk_errors) }}</dd></div>
        </dl>
      </section>
    </template>
  </div>
</template>

<style scoped>
h1 {
  font-size: 1.1rem;
  font-weight: 600;
}
.dash {
  gap: 0.8rem;
}
.panel {
  border: 1px solid var(--border);
  background: var(--surface);
  padding: 0.6rem 0.8rem 0.7rem;
  min-width: 0;
}
.panel > header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
  margin-bottom: 0.5rem;
}
h2 {
  font-size: 0.9rem;
  font-weight: 600;
  letter-spacing: 0;
}
.strip {
  margin: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(215px, 1fr));
  border-top: 1px solid var(--border);
  border-left: 1px solid var(--border);
  background: var(--surface);
}
.strip > div {
  padding: 0.4rem 0.7rem 0.5rem;
  min-width: 0;
  border-right: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
@media (max-width: 700px) {
  .strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
dt {
  color: var(--muted);
  font-size: 0.78rem;
}
dd {
  margin: 0;
  font-size: 0.9rem;
  overflow-wrap: anywhere;
}
.cols {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 380px), 1fr));
  gap: 0.8rem;
}
.note {
  margin-bottom: 0.4rem;
}
.kv {
  margin: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  column-gap: 1.5rem;
}
.kv > div {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.25rem 0;
  border-bottom: 1px solid var(--border);
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
.seg button:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
</style>
