<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../api/client'
import { settings, stats } from '../composables/live'
import { bytes, count, ms, percent, ratio } from '../composables/format'
import { toastError } from '../composables/toast'
import type { Bucket, ReasonHost } from '../api/types'
import DashChart from '../components/DashChart.vue'
import DashTable, { type DashRow } from '../components/DashTable.vue'
import UiNum from '../components/UiNum.vue'
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

// What was left alone, by reason and kind, with the bytes: the bytes of the
// images, text and fonts here are the ones that were not saved.
const leftAlone = computed<DashRow[]>(() => {
  const e = (stats.value?.by_reason ?? []).filter((r) => r.reason !== 'transformed')
  const total = e.reduce((s, x) => s + x.bytes_in, 0) || 1
  return e.slice(0, 14).map((r) => ({
    key: r.reason + '/' + r.kind,
    label: `${r.reason} · ${r.kind}`,
    cells: [count(r.requests), bytes(r.bytes_in)],
    bar: r.bytes_in / total,
    barText: percent((r.bytes_in / total) * 100, 0),
    green: false,
  }))
})

// The reasons that need a cause: why it was busy, which type is not handled.
const causes = computed<DashRow[]>(() => {
  const e = stats.value?.by_cause ?? []
  const total = e.reduce((s, x) => s + x.bytes_in, 0) || 1
  return e.slice(0, 12).map((c) => ({
    key: c.reason + '/' + c.cause,
    label: `${c.reason}: ${c.cause}`,
    cells: [count(c.requests), bytes(c.bytes_in)],
    bar: c.bytes_in / total,
    barText: percent((c.bytes_in / total) * 100, 0),
    green: false,
  }))
})

// Which hosts a reason came from, asked of the server when one is picked.
const reasons = computed(() => [...new Set((stats.value?.by_reason ?? []).filter((r) => r.reason !== 'transformed').map((r) => r.reason))])
const pickedReason = ref('')
const byHost = ref<ReasonHost[]>([])
const byHostOn = ref(true)
async function loadHosts() {
  if (!pickedReason.value) {
    byHost.value = []
    return
  }
  try {
    const m = await api.missed(pickedReason.value, 15)
    byHost.value = m.hosts
    byHostOn.value = m.enabled
  } catch (e) {
    toastError(e)
  }
}
watch(pickedReason, loadHosts)
watch(() => settings.value.domains, loadHosts)
const hostRows = computed<DashRow[]>(() => {
  const max = Math.max(1, ...byHost.value.map((h) => h.bytes_in))
  return byHost.value.map((h) => ({
    key: h.domain,
    label: h.domain,
    cells: [count(h.requests), bytes(h.bytes_in), h.kinds.map((k) => k.kind).join(', ')],
    bar: h.bytes_in / max,
    barText: '',
    green: false,
  }))
})

const missed = computed<DashRow[]>(() => {
  const d = stats.value?.top_missed ?? []
  const max = Math.max(1, ...d.map((x) => x.missed_bytes + x.media_bytes))
  return d.map((x) => ({
    key: x.domain,
    label: x.domain,
    cells: [
      count(x.requests),
      bytes(x.missed_bytes),
      bytes(x.media_bytes),
      x.reasons
        .slice(0, 2)
        .map((r) => `${r.reason} ${count(r.requests)}`)
        .join(', '),
    ],
    bar: (x.missed_bytes + x.media_bytes) / max,
    barText: percent(x.bytes_in > 0 ? ((x.missed_bytes + x.media_bytes) / x.bytes_in) * 100 : 0, 0),
    green: false,
  }))
})

const domains = computed<DashRow[]>(() => {
  const d = [...(stats.value?.top_domains ?? [])].sort((a, b) => b.bytes_in - b.bytes_out - (a.bytes_in - a.bytes_out))
  const max = Math.max(1, ...d.map((x) => x.bytes_in - x.bytes_out))
  return d.slice(0, 15).map((x) => ({
    key: x.domain,
    label: x.domain,
    cells: [count(x.requests), bytes(x.bytes_in), bytes(x.bytes_out), bytes(x.bytes_in - x.bytes_out)],
    bar: (x.bytes_in - x.bytes_out) / max,
    barText: percent(pct(x), 0),
  }))
})

const s = computed(() => stats.value)
const t = computed(() => s.value?.totals)

// What the proxy can shrink (images, text, fonts), what it passes as it comes
// (video and audio, which are most of the bytes of a phone) and what it never
// sees (the HTTPS tunnels it does not open). The headline is the first: mixing
// it with the others turns it into an average of things that are not alike.
function sum(kinds: string[]): Bucket {
  const k = stats.value?.by_kind ?? {}
  return kinds.reduce<Bucket>(
    (a, n) => {
      const b = k[n]
      return b ? { requests: a.requests + b.requests, transformed: a.transformed + b.transformed, bytes_in: a.bytes_in + b.bytes_in, bytes_out: a.bytes_out + b.bytes_out } : a
    },
    { requests: 0, transformed: 0, bytes_in: 0, bytes_out: 0 },
  )
}
const comp = computed(() => sum(['image', 'text', 'font']))
const compSaved = computed(() => comp.value.bytes_in - comp.value.bytes_out)
const compPercent = computed(() => (comp.value.bytes_in > 0 ? (compSaved.value / comp.value.bytes_in) * 100 : 0))
const media = computed(() => sum(['media']))
const otherKind = computed(() => sum(['other']))
const tunnel = computed(() => (s.value?.connections.tunnel_bytes_down ?? 0) + (s.value?.connections.tunnel_bytes_up ?? 0))
const everything = computed(() => (t.value?.bytes_in ?? 0) + (s.value?.connections.tunnel_bytes_down ?? 0))
function share(n: number) {
  return everything.value > 0 ? percent((n / everything.value) * 100, 0) : '-'
}
const keptShare = computed(() => (comp.value.bytes_in > 0 ? (comp.value.bytes_out / comp.value.bytes_in) * 100 : 100))
const transformedShare = computed(() => (t.value && t.value.requests > 0 ? t.value.transformed / t.value.requests : 0))
const cacheTotal = computed(() => (s.value ? s.value.cache.hits + s.value.cache.misses : 0))

const latency = computed(() => {
  const l = s.value?.latency
  if (!l || !l.samples) return null
  const top = Math.max(1, l.p99_ms * 1.1)
  const at = (v: number) => Math.min(100, (v / top) * 100)
  return { l, p50: at(l.p50_ms), p95: at(l.p95_ms), p99: at(l.p99_ms) }
})

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
  <div class="page">
    <h1>Overview</h1>

    <div v-if="!s || !t" class="skeleton" aria-label="Loading" role="status">
      <i /><i /><i />
    </div>

    <template v-else>
      <section class="hero" aria-labelledby="hero-line">
        <p v-if="comp.bytes_in === 0" id="hero-line" class="statement">
          <span class="fig zero"><UiNum :value="0" :format="bytes" /></span>
          <span class="of">saved on images, text and fonts so far.</span>
        </p>
        <p v-else id="hero-line" class="statement">
          <span class="fig"><UiNum :value="compSaved" :format="bytes" /></span>
          <span class="of">
            saved of <UiNum :value="comp.bytes_in" :format="bytes" /> of images, text and fonts,
            <UiNum :value="compPercent" :format="(n) => percent(n, 0)" />
          </span>
        </p>

        <template v-if="comp.bytes_in > 0">
          <div class="reduction" role="img" :aria-label="`Of ${bytes(comp.bytes_in)} of images, text and fonts, ${bytes(comp.bytes_out)} was sent and ${bytes(compSaved)} saved`">
            <i class="sent" :style="{ width: keptShare + '%' }" />
            <i class="gain" :style="{ width: 100 - keptShare + '%' }" />
          </div>
          <div class="key mono">
            <span class="k-sent">sent <b>{{ bytes(comp.bytes_out) }}</b></span>
            <span v-if="keptShare >= 28 && keptShare <= 70" class="k-gain" :style="{ left: keptShare + '%' }">saved <b>{{ bytes(compSaved) }}</b></span>
            <span v-else class="k-gain end">saved <b>{{ bytes(compSaved) }}</b></span>
          </div>
        </template>
        <p v-else-if="t.bytes_in === 0 && tunnel === 0" class="empty">
          Nothing has gone through the proxy yet. Open <RouterLink to="/connect">Connect</RouterLink> and paste the address into a device.
        </p>

        <dl class="classes" aria-label="Where the bytes are">
          <div>
            <dt>Images, text, fonts</dt>
            <dd class="mono"><b>{{ bytes(comp.bytes_in) }}</b> <span class="muted">{{ share(comp.bytes_in) }} of all bytes, shrunk {{ percent(compPercent, 0) }}</span></dd>
          </div>
          <div>
            <dt>Video and audio</dt>
            <dd class="mono"><b>{{ bytes(media.bytes_in) }}</b> <span class="muted">{{ share(media.bytes_in) }} of all bytes, passed as it comes</span></dd>
          </div>
          <div>
            <dt>Not inspected</dt>
            <dd class="mono"><b>{{ bytes(tunnel) }}</b> <span class="muted">{{ share(s.connections.tunnel_bytes_down) }} of all bytes, HTTPS tunnels that are never opened</span></dd>
          </div>
        </dl>

        <p class="tally small muted">
          <span class="mono">{{ count(t.requests) }}</span> requests,
          <span class="mono">{{ count(t.transformed) }}</span> changed ({{ ratio(transformedShare, 0) }}),
          cache hit rate <span class="mono">{{ cacheTotal ? ratio(s.cache.hit_rate, 0) : '-' }}</span>.
          Everything the proxy opened, video and other files included: <span class="mono">{{ bytes(t.saved) }}</span> saved of
          <span class="mono">{{ bytes(t.bytes_in) }}</span> ({{ percent(t.saved_percent, 1) }}), of which
          <span class="mono">{{ bytes(otherKind.bytes_in) }}</span> other files.
        </p>
      </section>

      <section class="sec">
        <div class="sec-head">
          <h2>Throughput</h2>
          <span class="seg" role="group" aria-label="Chart range">
            <button type="button" :aria-pressed="range === 's'" @click="range = 's'">Last 2 min</button>
            <button type="button" :aria-pressed="range === 'min'" @click="range = 'min'">Last 3 h</button>
          </span>
        </div>
        <DashChart :points="points" :unit="range" />
      </section>

      <div class="cols">
        <div class="stack-lg">
          <section class="sec">
            <div class="sec-head"><h2>By kind</h2></div>
            <DashTable :head="['Kind', 'Req', 'In', 'Out', 'Saved']" :rows="kinds" :hide-cols="[2]" />
          </section>
          <section class="sec">
            <div class="sec-head"><h2>By codec sent</h2></div>
            <DashTable :head="['Codec', 'Req', 'In', 'Out', 'Saved']" :rows="codecs" :hide-cols="[2]" />
          </section>
        </div>
        <div class="stack-lg">
          <section class="sec">
            <div class="sec-head"><h2>By profile</h2></div>
            <DashTable :head="['Profile', 'Req', 'In', 'Out', 'Saved']" :rows="profiles" :hide-cols="[2]" />
          </section>
          <section class="sec">
            <div class="sec-head">
              <h2>Left alone</h2>
              <span class="sub">why bytes were not shrunk</span>
            </div>
            <DashTable :head="['Reason · kind', 'Responses', 'Bytes', 'Share']" :rows="leftAlone" :hide-cols="[1]" empty="Everything the proxy opened was shrunk." />
          </section>
        </div>
      </div>

      <div class="cols">
        <div class="stack-lg">
          <section class="sec">
            <div class="sec-head">
              <h2>Latency</h2>
              <span v-if="latency" class="sub">from {{ count(latency.l.samples) }} responses</span>
            </div>
            <template v-if="latency">
              <p class="pxx mono">
                <span><span class="muted">p50</span> {{ ms(latency.l.p50_ms) }}</span>
                <span><span class="muted">p95</span> {{ ms(latency.l.p95_ms) }}</span>
                <span><span class="muted">p99</span> {{ ms(latency.l.p99_ms) }}</span>
              </p>
              <div class="dist" aria-hidden="true">
                <i class="t p50" :style="{ left: latency.p50 + '%' }" />
                <i class="t p95" :style="{ left: latency.p95 + '%' }" />
                <i class="t p99" :style="{ left: latency.p99 + '%' }" />
              </div>
            </template>
            <p v-else class="empty small">No responses measured yet.</p>
          </section>
          <section class="sec">
            <div class="sec-head"><h2>Connections</h2></div>
            <dl class="dl keep">
              <dt>In flight</dt><dd class="mono">{{ s.connections.requests_in_flight }} requests</dd>
              <dt>Clients, last 2 min</dt><dd class="mono">{{ s.connections.clients_active }}</dd>
              <dt>Tunnels</dt><dd class="mono">{{ s.connections.tunnels_active }} open, {{ count(s.connections.tunnels_total) }} total</dd>
              <dt>Passed untouched (HTTPS tunnels)</dt><dd class="mono">{{ bytes(s.connections.tunnel_bytes_down) }} down, {{ bytes(s.connections.tunnel_bytes_up) }} up<span class="muted">, not shrunk</span></dd>
              <dt>TLS handshakes</dt><dd class="mono">{{ count(s.connections.tls_handshakes) }}<span :class="s.connections.tls_handshake_errors ? 'bad' : 'muted'">, {{ count(s.connections.tls_handshake_errors) }} failed</span></dd>
              <dt>Upstream errors</dt><dd class="mono" :class="{ bad: s.connections.upstream_errors > 0 }">{{ count(s.connections.upstream_errors) }}</dd>
              <dt>Denied</dt><dd class="mono" :class="{ bad: s.connections.denied > 0 }">{{ count(s.connections.denied) }}</dd>
              <dt>CPU slots busy</dt><dd class="mono">{{ s.cpu.busy }} of {{ s.cpu.slots }}<span v-if="s.cpu.background_queue" class="muted">, {{ s.cpu.background_queue }} queued</span></dd>
            </dl>
          </section>
        </div>
        <section class="sec">
          <div class="sec-head"><h2>Cache</h2></div>
          <dl class="dl keep">
            <dt>Memory</dt><dd class="mono">{{ bytes(s.cache.memory_bytes) }} in {{ count(s.cache.memory_items) }} items</dd>
            <dt>Disk</dt><dd class="mono">{{ bytes(s.cache.disk_bytes) }} in {{ count(s.cache.disk_items) }} items</dd>
            <dt>Hits and misses</dt><dd class="mono">{{ count(s.cache.hits) }} / {{ count(s.cache.misses) }}</dd>
            <dt>Evictions</dt><dd class="mono">{{ count(s.cache.evictions) }}</dd>
            <dt>Expired</dt><dd class="mono">{{ count(s.cache.expired) }}</dd>
            <dt>Disk errors</dt><dd class="mono" :class="{ bad: s.cache.disk_errors > 0 }">{{ count(s.cache.disk_errors) }}</dd>
          </dl>
        </section>
      </div>

      <section class="sec">
        <div class="sec-head">
          <h2>Causes</h2>
          <span class="sub">busy by memory or CPU, types that are not handled, statuses</span>
        </div>
        <DashTable :head="['Reason: cause', 'Responses', 'Bytes', 'Share']" :rows="causes" :hide-cols="[1]" empty="No cause to report yet." />
      </section>

      <section class="sec">
        <div class="sec-head">
          <h2>Where bytes were not saved</h2>
          <UiToggle :model-value="settings.domains" :disabled="busy" label="Track domains" @update:model-value="setDomains" />
        </div>
        <template v-if="settings.domains && s.top_missed">
          <DashTable
            :head="['Host', 'Req', 'Not saved', 'Video and audio', 'Why', 'Of its bytes']"
            :rows="missed"
            :hide-cols="[3, 4]"
            empty="No traffic yet. Hosts appear here as devices browse."
          />
          <div class="by-host">
            <label class="sub" for="pick-reason">Which hosts had responses left alone because of</label>
            <select id="pick-reason" v-model="pickedReason" class="select compact mono">
              <option value="">pick a reason</option>
              <option v-for="r in reasons" :key="r" :value="r">{{ r }}</option>
            </select>
            <DashTable
              v-if="pickedReason"
              :head="['Host', 'Responses', 'Bytes', 'Kinds', '']"
              :rows="hostRows"
              :hide-cols="[3]"
              empty="No host in the tracked set."
            />
          </div>
        </template>
        <p v-else class="empty">Tracking is off. Turn it on to see which hosts the bytes that were not saved come from, and why. It records host names, never paths, in memory only, and counting starts from that moment.</p>
      </section>

      <section class="sec">
        <div class="sec-head">
          <h2>Top domains by bytes saved</h2>
          <UiToggle :model-value="settings.domains" :disabled="busy" label="Track domains" @update:model-value="setDomains" />
        </div>
        <DashTable
          v-if="settings.domains && s.top_domains"
          :head="['Domain', 'Req', 'In', 'Out', 'Saved', '%']"
          :rows="domains"
          :hide-cols="[2, 3]"
          empty="No traffic yet. Domains appear here as devices browse."
        />
        <p v-else class="empty">Tracking is off. Turn it on to see which sites save the most. It records host names, never paths, in memory only, and counting starts from that moment.</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.stack-lg {
  display: flex;
  flex-direction: column;
  gap: 2rem;
  min-width: 0;
}
.hero {
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
  padding-bottom: 1.5rem;
  border-bottom: 1px solid var(--rule);
}
.statement {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 0.2rem 1rem;
}
.fig {
  font-size: var(--fs-hero);
  line-height: 1;
  font-weight: 500;
  letter-spacing: -0.03em;
  color: var(--leaf);
  font-variant-numeric: tabular-nums;
}
.fig .num,
.of .num {
  font-family: var(--font-sans);
}
.fig.zero {
  color: var(--ink-2);
}
.of {
  font-size: var(--fs-xl);
  line-height: 1.2;
  font-weight: 400;
  letter-spacing: -0.01em;
}
.reduction {
  display: flex;
  height: 14px;
  margin-top: 0.4rem;
  background: var(--rule);
}
.reduction i {
  display: block;
  height: 100%;
}
@media (prefers-reduced-motion: no-preference) {
  .reduction i {
    transition: width 0.3s ease-out;
  }
}
.sent {
  background: var(--ink);
}
.gain {
  background: var(--leaf);
}
.key {
  position: relative;
  height: 1.5rem;
  font-size: var(--fs-xs);
  color: var(--ink-2);
  margin-top: -0.3rem;
}
.key b {
  font-weight: 500;
  color: var(--ink);
}
.key span {
  position: absolute;
  top: 0;
  white-space: nowrap;
  padding: 0 0.4rem;
}
.k-sent {
  left: 0;
  border-left: 1px solid var(--ink);
}
.k-gain {
  border-left: 1px solid var(--leaf);
}
.k-gain.end {
  right: 0;
  border-left: 0;
  border-right: 1px solid var(--leaf);
}
.k-gain b {
  color: var(--leaf);
}
.tally {
  margin-top: 0.4rem;
}
.classes {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.6rem 2rem;
  margin: 0.8rem 0 0.2rem;
  padding-top: 1rem;
  border-top: 1px solid var(--rule);
}
.classes dt {
  font-size: var(--fs-xs);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--ink-2);
  margin-bottom: 0.25rem;
}
.classes dd b {
  display: block;
  font-size: var(--fs-lg);
  font-weight: 500;
  color: var(--ink);
}
.classes dd .muted {
  display: block;
  font-size: var(--fs-xs);
  line-height: 1.4;
}
.by-host {
  margin-top: 1.4rem;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.6rem;
}
@media (max-width: 720px) {
  .classes {
    grid-template-columns: 1fr;
  }
}
.pxx {
  display: flex;
  gap: 0.4rem 2rem;
  flex-wrap: wrap;
  font-size: var(--fs-md);
  margin-bottom: 0.7rem;
}
.dist {
  position: relative;
  height: 14px;
  max-width: 420px;
  border-bottom: 1px solid var(--rule-strong);
}
.dist .t {
  position: absolute;
  bottom: 0;
  width: 1px;
  height: 12px;
  background: var(--ink);
}
.dist .p50 {
  height: 7px;
  background: var(--ink-2);
}
.dist .p99 {
  background: var(--vermilion);
}
</style>
