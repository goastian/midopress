<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, ApiFailure } from '../api/client'
import type { ConfigView, TestRequest, TestResult, ValidationIssue } from '../api/types'

defineProps<{ cfg: ConfigView }>()

const url = ref('')
const method = ref('GET')
const contentType = ref('')
const status = ref('')
const headers = ref('')
const deviceProfile = ref('')
const src = ref('')
const busy = ref(false)
const result = ref<TestResult | null>(null)
const issues = ref<ValidationIssue[]>([])
const failure = ref('')

const fieldError = (f: string) => issues.value.find((i) => i.field === f)?.message ?? ''
const canRun = computed(() => url.value.trim() !== '' && !busy.value)

const sourceText: Record<string, string> = {
  device: 'the device profile',
  header: 'the X-MidoPress-Profile request header',
  rule: 'a matching rule',
  'save-data': 'the Save-Data request header',
  default: 'the default profile',
}

function parseHeaders(): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of headers.value.split('\n')) {
    const at = line.indexOf(':')
    if (at > 0) out[line.slice(0, at).trim()] = line.slice(at + 1).trim()
  }
  return Object.keys(out).length ? out : undefined
}

async function run() {
  busy.value = true
  issues.value = []
  failure.value = ''
  const req: TestRequest = { url: url.value.trim(), method: method.value }
  if (contentType.value.trim()) req.content_type = contentType.value.trim()
  if (status.value.trim()) req.status = Number(status.value)
  const h = parseHeaders()
  if (h) req.headers = h
  if (deviceProfile.value) req.device_profile = deviceProfile.value
  if (src.value.trim()) req.src = src.value.trim()
  try {
    result.value = await api.testUrl(req)
  } catch (e) {
    result.value = null
    if (e instanceof ApiFailure && e.body.issues?.length) issues.value = e.body.issues
    else failure.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="sec">
    <div class="sec-head">
      <h2>Test a URL</h2>
      <span class="sub">Uses the saved rules and profiles</span>
    </div>
    <form class="form" @submit.prevent="run">
      <div class="field wide">
        <label for="t-url">URL</label>
        <input id="t-url" v-model="url" class="input mono" :class="{ bad: fieldError('url') }" spellcheck="false" autocomplete="off" placeholder="https://cdn.example.com/photo.jpg" />
        <span v-if="fieldError('url')" class="error-text">{{ fieldError('url') }}</span>
      </div>
      <div class="field">
        <label for="t-method">Method</label>
        <select id="t-method" v-model="method" class="select">
          <option v-for="m in ['GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'PATCH']" :key="m">{{ m }}</option>
        </select>
      </div>
      <div class="field">
        <label for="t-ct">Content type</label>
        <input id="t-ct" v-model="contentType" class="input mono" autocomplete="off" placeholder="image/jpeg" />
      </div>
      <div class="field">
        <label for="t-status">Status</label>
        <input id="t-status" v-model="status" class="input mono" type="number" min="100" max="599" placeholder="200" />
      </div>
      <div class="field">
        <label for="t-dev">Device profile</label>
        <select id="t-dev" v-model="deviceProfile" class="select">
          <option value="">none</option>
          <option v-for="p in cfg.profiles" :key="p.name" :value="p.name">{{ p.name }}</option>
        </select>
        <span v-if="fieldError('device_profile')" class="error-text">{{ fieldError('device_profile') }}</span>
      </div>
      <div class="field">
        <label for="t-src">Client address</label>
        <input id="t-src" v-model="src" class="input mono" :class="{ bad: fieldError('src') }" autocomplete="off" placeholder="192.168.1.20" />
        <span v-if="fieldError('src')" class="error-text">{{ fieldError('src') }}</span>
      </div>
      <div class="field wide">
        <label for="t-headers">Request headers, one per line</label>
        <textarea id="t-headers" v-model="headers" class="textarea mono" rows="2" spellcheck="false" placeholder="Save-Data: on" />
      </div>
      <div class="field wide">
        <div class="row tight">
          <button class="btn primary" type="submit" :disabled="!canRun">{{ busy ? 'Testing' : 'Test' }}</button>
          <span class="hint">Without a content type only the conditions on the request count.</span>
        </div>
      </div>
    </form>

    <p v-if="failure" class="notice err res">{{ failure }}</p>
    <p v-for="(i, k) in issues.filter((x) => !['url', 'src', 'device_profile'].includes(x.field))" :key="k" class="notice err res">{{ i.field ? i.field + ': ' : '' }}{{ i.message }}</p>

    <div v-if="result" class="res">
      <dl class="dl">
        <dt>Outcome</dt>
        <dd>
          <strong>{{ result.outcome === 'process' ? 'Processed' : 'Passed through untouched' }}</strong>
          <span v-if="result.action" class="muted"> ({{ result.action }})</span>
        </dd>
        <dt>Profile</dt>
        <dd><span class="mono">{{ result.profile }}</span> <span class="muted">from {{ sourceText[result.profile_source] ?? result.profile_source }}</span></dd>
        <dt>Settings</dt>
        <dd>
          <template v-if="Object.keys(result.set ?? {}).length">
            <span v-for="(v, k) in result.set" :key="k" class="mono kv">{{ k }}={{ v }}</span>
          </template>
          <span v-else class="muted">none changed</span>
        </dd>
        <template v-if="result.note">
          <dt>Note</dt>
          <dd>{{ result.note }}</dd>
        </template>
      </dl>
      <div v-if="result.explanations?.length" class="table-wrap">
        <table class="tbl">
          <thead>
            <tr>
              <th class="r">#</th>
              <th>Rule</th>
              <th>Matched</th>
              <th>Reason</th>
              <th>Action</th>
              <th>Profile</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="x in result.explanations" :key="x.index" :class="{ hit: x.matched }">
              <td class="r">{{ x.index + 1 }}</td>
              <td class="mono match">{{ x.match }}</td>
              <td :class="x.matched ? 'ok' : 'muted'">{{ x.matched ? 'yes' : 'no' }}</td>
              <td>{{ x.reason }}</td>
              <td class="mono">{{ x.matched ? x.action || '-' : '' }}</td>
              <td class="mono">{{ x.matched ? x.profile || '-' : '' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="muted small">There are no rules to try.</p>
    </div>
  </section>
</template>

<style scoped src="./PagesOps.css"></style>
<style scoped>
.form { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 0.7rem 0.9rem; align-items: start; }
.form .wide { grid-column: 1 / -1; }
.res { margin-top: 0.9rem; display: flex; flex-direction: column; gap: 0.8rem; }
p.res { display: block; }
.kv { margin-right: 0.8rem; }
.match { overflow-wrap: anywhere; min-width: 12rem; }
.form textarea { min-height: 0; }
.hit td { background: var(--accent-soft); }
</style>
