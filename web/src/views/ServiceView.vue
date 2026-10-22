<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, download } from '../api/client'
import { halt, settings, stats } from '../composables/live'
import { usePoll } from '../composables/poll'
import { bytes, count, dateTime, duration, ratio } from '../composables/format'
import { toast, toastError } from '../composables/toast'
import type { Settings } from '../api/types'
import ServiceConfigNode from '../components/ServiceConfigNode.vue'
import UiModal from '../components/UiModal.vue'
import UiToggle from '../components/UiToggle.vue'

type Action = 'reload' | 'cache' | 'stats' | 'shutdown'

const { data: about, error: aboutError } = usePoll(() => api.about())
const { data: cfg, error: cfgError } = usePoll(() => api.config())

const confirming = ref<Action | null>(null)
const busy = ref(false)
const stopped = ref(false)
const showModules = ref(false)
const moduleFilter = ref('')
const savingKey = ref<keyof Settings | ''>('')

const actions: Record<Action, { title: string; short: string; button: string; body: string; danger?: boolean }> = {
  reload: {
    title: 'Reload configuration',
    short: 'Reload',
    button: 'Reload',
    body: 'Reads the configuration file again and applies rules, profiles, the default profile, minification, cache TTLs, mitm.passthrough and the log level. Everything else needs a restart.',
  },
  cache: {
    title: 'Clear cache',
    short: 'Clear cache',
    button: 'Clear cache',
    body: 'Empties the memory and disk cache. The next requests go to the origin servers again, so they will be slower for a while.',
    danger: true,
  },
  stats: {
    title: 'Reset statistics',
    short: 'Reset',
    button: 'Reset statistics',
    body: 'Sets the counters, latency and the charts back to zero. Nothing else changes.',
    danger: true,
  },
  shutdown: {
    title: 'Stop the service',
    short: 'Stop',
    button: 'Stop MidoPress',
    body: 'MidoPress stops answering after this request: the proxy and this interface go away, and every device using it loses its connection. It has to be started again from the host.',
    danger: true,
  },
}

async function run() {
  const a = confirming.value
  if (!a) return
  busy.value = true
  try {
    if (a === 'reload') await api.reload()
    else if (a === 'cache') await api.clearCache()
    else if (a === 'stats') await api.resetStats()
    else {
      await api.shutdown()
      halt()
      stopped.value = true
    }
    confirming.value = null
    if (a === 'reload') toast('Configuration reloaded')
    else if (a === 'cache') toast('Cache cleared')
    else if (a === 'stats') toast('Statistics reset')
    if (a === 'reload') void api.config().then((c) => (cfg.value = c)).catch(() => {})
  } catch (e) {
    toastError(e)
    confirming.value = null
  } finally {
    busy.value = false
  }
}

const switches: { key: keyof Settings; label: string; text: string }[] = [
  { key: 'flows', label: 'Live flows', text: 'Keeps the last requests, with their URL path, in memory only. Off throws them away.' },
  { key: 'capture', label: 'Capture images', text: 'Keeps the bodies of images, within the capture limit, so a flow can be compared with its original.' },
  { key: 'domains', label: 'Domain aggregation', text: 'Counts traffic per host name (never paths) for the top domains list.' },
]

async function toggle(key: keyof Settings, value: boolean) {
  savingKey.value = key
  try {
    settings.value = await api.saveSettings({ [key]: value })
  } catch (e) {
    toastError(e)
  } finally {
    savingKey.value = ''
  }
}

function reloadPage() {
  window.location.reload()
}

async function metrics() {
  try {
    await download('/metrics', 'midopress-metrics.txt')
  } catch (e) {
    toastError(e)
  }
}

const hit = computed(() => (stats.value ? ratio(stats.value.cache.hit_rate) : '-'))
const uptime = computed(() => {
  const s = stats.value?.uptime_seconds ?? about.value?.uptime_seconds
  return s == null ? '-' : duration(s)
})
const native = computed(() => Object.entries(about.value?.native ?? {}))
const modules = computed(() => {
  const q = moduleFilter.value.trim().toLowerCase()
  return (about.value?.modules ?? []).filter((m) => !q || m.path.toLowerCase().includes(q))
})

function yaml(v: unknown, indent = 0): string {
  const pad = '  '.repeat(indent)
  if (v === null || v === undefined) return 'null'
  if (typeof v !== 'object') return typeof v === 'string' ? JSON.stringify(v) : String(v)
  const list = Array.isArray(v) ? v.map((x, i) => [String(i), x] as const) : Object.entries(v)
  if (!list.length) return Array.isArray(v) ? '[]' : '{}'
  return list
    .map(([k, x]) => {
      const lead = Array.isArray(v) ? `${pad}- ` : `${pad}${k}:`
      if (x !== null && typeof x === 'object' && (Array.isArray(x) ? x.length : Object.keys(x).length)) {
        return `${lead}\n${yaml(x, indent + 1)}`
      }
      return Array.isArray(v) ? `${lead}${yaml(x)}` : `${lead} ${yaml(x)}`
    })
    .join('\n')
}

async function copyConfig() {
  if (!cfg.value) return
  try {
    await navigator.clipboard.writeText(yaml(cfg.value.config) + '\n')
    toast('Configuration copied')
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <div class="page svc">
    <div class="page-head">
      <div>
        <h1>Service</h1>
        <p class="small">State of this MidoPress, and what you can do to it.</p>
      </div>
    </div>

    <section class="card">
      <header><h2>Status</h2></header>
      <div v-if="aboutError" class="notice err">{{ aboutError }}</div>
      <dl v-else-if="about" class="kv">
        <dt>Version</dt><dd class="num">{{ about.version }}</dd>
        <dt>Go</dt><dd class="num">{{ about.go_version }}</dd>
        <dt>Platform</dt><dd>{{ about.os }}/{{ about.arch }}</dd>
        <dt>Uptime</dt><dd class="num">{{ uptime }}</dd>
        <dt>Started</dt><dd class="num">{{ dateTime(about.started) }}</dd>
        <dt>Proxy address</dt><dd class="mono">{{ about.proxy_addr }}</dd>
        <dt>TLS</dt><dd>{{ about.tls ? 'on' : 'off' }}</dd>
        <dt>MITM</dt><dd>{{ about.mitm ? 'on, HTTPS is decrypted' : 'off, HTTPS passes through' }}</dd>
        <dt>Authentication</dt><dd>{{ about.auth_mode }}</dd>
        <dt>State directory</dt><dd class="mono">{{ about.state_dir }}</dd>
        <dt>Image backend</dt><dd>{{ about.backend }}<span class="muted"> (available: {{ about.backends.join(', ') }})</span></dd>
        <dt>Native codecs</dt>
        <dd>
          <span v-for="[name, ok] in native" :key="name" class="codec" :class="{ yes: ok }">{{ name }} {{ ok ? 'yes' : 'no' }}</span>
        </dd>
      </dl>
      <div v-else class="muted">Loading</div>
    </section>

    <section class="card">
      <header><h2>Actions</h2></header>
      <ul class="acts">
        <li v-for="(a, k) in actions" :key="k">
          <div class="grow">
            <div class="name">{{ a.title }}</div>
            <div class="hint">{{ a.body }}</div>
          </div>
          <button class="btn small" :class="{ danger: a.danger }" @click="confirming = k">{{ a.short }}</button>
        </li>
      </ul>
    </section>

    <div class="grid cols-2">
      <section class="card">
        <header><h2>Privacy and runtime</h2></header>
        <p class="hint" style="margin-bottom: 0.5rem">Applied at once, kept only until MidoPress stops.</p>
        <ul class="acts">
          <li v-for="s in switches" :key="s.key">
            <div class="grow">
              <div class="name">{{ s.label }}</div>
              <div class="hint">{{ s.text }}</div>
            </div>
            <UiToggle
              :model-value="settings[s.key]"
              :disabled="savingKey === s.key"
              :aria-label="s.label"
              @update:model-value="toggle(s.key, $event)"
            />
          </li>
        </ul>
      </section>

      <section class="card">
        <header><h2>Cache</h2></header>
        <dl v-if="stats" class="kv">
          <dt>Memory</dt><dd class="num">{{ bytes(stats.cache.memory_bytes) }} in {{ count(stats.cache.memory_items) }} items</dd>
          <dt>Disk</dt><dd class="num">{{ bytes(stats.cache.disk_bytes) }} in {{ count(stats.cache.disk_items) }} items</dd>
          <dt>Hit rate</dt><dd class="num">{{ hit }}<span class="muted"> ({{ count(stats.cache.hits) }} hits, {{ count(stats.cache.misses) }} misses)</span></dd>
          <dt>Evictions</dt><dd class="num">{{ count(stats.cache.evictions) }}</dd>
          <dt>Expired</dt><dd class="num">{{ count(stats.cache.expired) }}</dd>
          <dt>Disk errors</dt><dd class="num" :class="{ bad: stats.cache.disk_errors > 0 }">{{ count(stats.cache.disk_errors) }}</dd>
        </dl>
        <div v-else class="muted">Waiting for statistics</div>
      </section>
    </div>

    <section class="card">
      <header>
        <h2>Effective configuration</h2>
        <div class="row tight">
          <span class="hint">Read only, secrets are redacted</span>
          <button class="btn small" :disabled="!cfg" @click="copyConfig">Copy as YAML</button>
        </div>
      </header>
      <div v-if="cfgError" class="notice err">{{ cfgError }}</div>
      <div v-else-if="cfg" class="tree mono">
        <ServiceConfigNode v-for="(v, k) in cfg.config" :key="k" :name="String(k)" :value="v" />
      </div>
      <div v-else class="muted">Loading</div>
    </section>

    <section class="card">
      <header>
        <h2>Metrics</h2>
        <button class="btn small" @click="metrics">Download metrics</button>
      </header>
      <p class="hint">Prometheus text format from /api/v1/metrics. It needs the admin token, so it is fetched here instead of linked.</p>
    </section>

    <section class="card">
      <header>
        <h2>About and licenses</h2>
        <button class="btn small" :aria-expanded="showModules" @click="showModules = !showModules">
          {{ showModules ? 'Hide' : 'Show' }} third-party modules{{ about ? ` (${about.modules.length})` : '' }}
        </button>
      </header>
      <p class="small">
        MidoPress, the image and text saving proxy of Midori. The admin API is described in <code>docs/09-admin-api.md</code>.
      </p>
      <div v-if="showModules" class="mods">
        <input v-model="moduleFilter" class="input" type="search" placeholder="Filter modules" aria-label="Filter modules" />
        <ul class="modlist mono">
          <li v-for="m in modules" :key="m.path"><span class="grow">{{ m.path }}</span><span class="muted num">{{ m.version }}</span></li>
          <li v-if="!modules.length" class="muted">No module matches.</li>
        </ul>
      </div>
    </section>

    <UiModal v-if="confirming" :title="actions[confirming].title" @close="!busy && (confirming = null)">
      <p>{{ actions[confirming].body }}</p>
      <template #footer>
        <button class="btn" :disabled="busy" @click="confirming = null">Cancel</button>
        <button class="btn" :class="actions[confirming].danger ? 'danger' : 'primary'" :disabled="busy" @click="run">
          {{ actions[confirming].button }}
        </button>
      </template>
    </UiModal>

    <Teleport to="body">
      <div v-if="stopped" class="stopped" role="alertdialog" aria-labelledby="stopped-t">
        <div class="box">
          <h1 id="stopped-t">MidoPress stopped</h1>
          <p class="muted">
            The service accepted the request and is shutting down. The proxy and this interface are no longer available.
            Start MidoPress again from the host, then reload this page.
          </p>
          <button class="btn" @click="reloadPage">Reload page</button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.svc {
  max-width: 1100px;
}
.card > header {
  margin-bottom: 0.6rem;
}
.kv {
  display: grid;
  grid-template-columns: 9rem minmax(0, 1fr);
  gap: 0.25rem 1rem;
  margin: 0;
  font-size: 0.9rem;
}
.kv dt {
  color: var(--muted);
}
.kv dd {
  margin: 0;
  word-break: break-all;
}
@media (max-width: 480px) {
  .kv {
    grid-template-columns: minmax(0, 1fr);
    gap: 0;
  }
  .kv dd {
    margin-bottom: 0.45rem;
  }
}
.codec {
  margin-right: 0.9rem;
  color: var(--muted);
}
.codec.yes {
  color: var(--accent-strong);
}
.acts {
  list-style: none;
  margin: 0;
  padding: 0;
}
.acts li {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 0.6rem 0;
  border-top: 1px solid var(--border);
}
.acts li:first-child {
  border-top: 0;
}
.name {
  font-weight: 600;
  font-size: 0.92rem;
}
@media (max-width: 560px) {
  .acts li {
    flex-wrap: wrap;
  }
}
.tree {
  font-size: 0.82rem;
  line-height: 1.55;
  max-height: 480px;
  overflow: auto;
}
.mods {
  margin-top: 0.7rem;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.modlist {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 320px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  font-size: 0.8rem;
}
.modlist li {
  display: flex;
  gap: 1rem;
  padding: 0.25rem 0.6rem;
  border-top: 1px solid var(--border);
  word-break: break-all;
}
.modlist li:first-child {
  border-top: 0;
}
.stopped {
  position: fixed;
  inset: 0;
  z-index: 100;
  background: var(--bg);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1.5rem;
}
.stopped .box {
  max-width: 440px;
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
  align-items: flex-start;
}
</style>
