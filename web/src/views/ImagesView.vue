<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api/client'
import type { ImageAPIInfo, ImageCredential } from '../api/types'
import { bytes } from '../composables/format'

const info = ref<ImageAPIInfo | null>(null)
const resource = ref('')
const profile = ref('balanced')
const profiles = ref(['lossless', 'balanced', 'saver', 'extreme'])
const format = ref('auto')
const width = ref(0)
const dpr = ref(1)
const busy = ref(false)
const error = ref('')
const original = ref('')
const optimized = ref('')
const fallback = ref('')
const showOriginal = ref(false)
const credential = ref<ImageCredential | null>(null)
const credentialHosts = ref('www.gstatic.com, i.ytimg.com')
const credentialWidth = ref(1024)
const credentialDPR = ref(2)
const credentialMinutes = ref(30)
const credentialBusy = ref(false)
const credentialError = ref('')
const credentialCopied = ref(false)
const report = ref<{ before: number; after: number; ms: number; cache: string; decision: string; metric: string; measure: string; backend: string } | null>(null)
let controller: AbortController | null = null
const saved = computed(() => report.value ? ((1 - report.value.after / report.value.before) * 100).toFixed(1) : '0')
const messages: Record<string, string> = {
  session_resource: 'This resource depends on a session or disallows transformation. Open the original in its site.',
  destination_refused: 'The destination is outside the permitted public network.',
  invalid_signature: 'The signed URL expired or was changed. Run the comparison again.',
  byte_limit: 'The image exceeds the byte limit.',
  unsupported_or_pixel_limit: 'The image is unsupported, invalid, or exceeds the pixel limit.',
  timeout: 'The image exceeded the time limit. The worker was stopped.',
  busy: 'All image workers are busy. Try again shortly.',
  origin_failed: 'The origin did not return an accessible public image.',
}

onMounted(async () => {
  try {
    const [status, config] = await Promise.all([api.images(), api.config()])
    info.value = status
    profiles.value = config.profiles.map(p => p.name)
  } catch (e) { error.value = e instanceof Error ? e.message : 'Unable to load image settings.' }
})

function clearImages() {
  for (const url of [original.value, optimized.value]) if (url) URL.revokeObjectURL(url)
  original.value = optimized.value = ''
}

onUnmounted(() => { controller?.abort(); clearImages() })

async function load(url: string, signal: AbortSignal) {
  const start = performance.now()
  const res = await fetch(url, {
    credentials: 'omit', referrerPolicy: 'no-referrer', signal, cache: 'no-store',
    headers: { Accept: 'image/avif,image/webp,image/png,image/jpeg,image/gif' },
  })
  if (!res.ok) {
    let code = ''
    try { code = (await res.json()).code } catch { /* the endpoint may be unavailable */ }
    throw new Error(messages[code] ?? `Image unavailable (HTTP ${res.status}).`)
  }
  const blob = await res.blob()
  return { blob, headers: res.headers, ms: performance.now() - start }
}

async function compare() {
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  clearImages()
  busy.value = true
  error.value = ''
  report.value = null
  fallback.value = ''
  showOriginal.value = true
  try {
    const signed = await api.signImage({ url: resource.value.trim(), profile: profile.value, format: format.value,
      width: width.value, dpr: dpr.value, version: '1' })
    fallback.value = signed.fallback_url
    const before = await load(signed.original_url, signal)
    if (signal.aborted) return
    original.value = URL.createObjectURL(before.blob)
    const after = await load(signed.optimized_url, signal)
    if (signal.aborted) return
    optimized.value = URL.createObjectURL(after.blob)
    showOriginal.value = false
    report.value = { before: before.blob.size, after: after.blob.size, ms: after.ms,
      cache: after.headers.get('X-MidoPress-Cache') ?? '', decision: after.headers.get('X-MidoPress-Decision') ?? '',
      metric: after.headers.get('X-MidoPress-Metric') ?? '', measure: after.headers.get('X-MidoPress-Measure') ?? '',
      backend: after.headers.get('X-MidoPress-Backend') ?? '' }
  } catch (e) {
    if (!signal.aborted) {
      error.value = e instanceof Error ? e.message : 'Image unavailable.'
      showOriginal.value = true
    }
  } finally { if (!signal.aborted) busy.value = false }
}

async function mintCredential() {
  credentialBusy.value = true
  credentialError.value = ''
  credential.value = null
  credentialCopied.value = false
  try {
    credential.value = await api.imageCredential({ hosts: credentialHosts.value.split(/[\s,]+/).filter(Boolean),
      profiles: [profile.value], max_width: credentialWidth.value, max_dpr: credentialDPR.value,
      ttl_seconds: credentialMinutes.value * 60 })
  } catch (e) { credentialError.value = e instanceof Error ? e.message : 'Unable to issue credential.' }
  finally { credentialBusy.value = false }
}
async function copyCredential() {
  if (!credential.value) return
  try { await navigator.clipboard.writeText(credential.value.token); credentialCopied.value = true }
  catch { credentialError.value = 'Clipboard unavailable. Select and copy the credential field.' }
}
</script>

<template>
  <div class="page">
    <div class="page-head"><div><h1>Images</h1><p>Compare a public image with its optimized version. No proxy or CA installation required.</p></div></div>
    <div v-if="info && !info.enabled" class="notice">The image API is disabled. Enable <code>image_api.enabled</code> in the server configuration and restart.</div>
    <form class="sec" @submit.prevent="compare">
      <label class="field">Public image URL<input v-model="resource" class="input" type="url" required placeholder="https://example.com/photo.jpg" :disabled="busy" /></label>
      <div class="options">
        <label class="field">Profile<select v-model="profile" class="input"><option v-for="p in profiles" :key="p">{{ p }}</option></select></label>
        <label class="field">Format<select v-model="format" class="input"><option value="auto">Browser formats</option><option value="jpeg">JPEG</option><option value="png">PNG</option><option value="webp">WebP</option><option value="avif">AVIF</option><option value="jxl">JPEG XL</option></select></label>
        <label class="field">Visible width (CSS px, 0 = original)<input v-model.number="width" class="input" type="number" min="0" :max="info?.max_width ?? 4096" step="1" required /></label>
        <label class="field">DPR<input v-model.number="dpr" class="input" type="number" min="1" :max="info?.max_dpr ?? 3" step="0.25" required /></label>
      </div>
      <p class="hint">Public images only. Session cookies and navigation credentials are excluded. Resizing and the extreme profile can visibly change an image.</p>
      <p v-if="info" class="hint">Limits: {{ bytes(info.max_bytes) }}, {{ info.max_pixels.toLocaleString() }} pixels, {{ info.timeout_ms / 1000 }} seconds. Width × DPR must stay within {{ info.max_width }} px.</p>
      <button class="btn primary" type="submit" :disabled="busy || !info?.enabled">{{ busy ? 'Comparing…' : 'Compare image' }}</button>
    </form>
    <div v-if="error" class="notice err" role="alert">{{ error }} <a v-if="fallback" :href="fallback" target="_blank" rel="noopener noreferrer">Open original</a></div>
    <section v-if="original" class="sec">
      <div class="sec-head"><h2>Comparison</h2><div class="seg" role="group" aria-label="Image version"><button :aria-pressed="showOriginal" @click="showOriginal = true">Original</button><button :aria-pressed="!showOriginal" :disabled="!optimized" @click="showOriginal = false">Optimized</button></div></div>
      <dl v-if="report" class="metrics mono"><div><dt>Original</dt><dd>{{ bytes(report.before) }}</dd></div><div><dt>Result</dt><dd>{{ bytes(report.after) }}</dd></div><div><dt>Saved</dt><dd>{{ saved }}%</dd></div><div><dt>Time</dt><dd>{{ report.ms.toFixed(0) }} ms</dd></div><div><dt>Cache / decision</dt><dd>{{ report.cache }} / {{ report.decision }}</dd></div><div><dt>Codec execution</dt><dd>{{ report.backend }}</dd></div><div v-if="report.metric"><dt>{{ report.metric }}</dt><dd>{{ report.measure }}</dd></div></dl>
      <div class="preview"><img :src="showOriginal || !optimized ? original : optimized" :alt="showOriginal ? 'Original image' : 'Optimized image'" @error="error = 'This browser cannot display the chosen format. Select Browser formats or open the original.'; showOriginal = true" /></div>
      <p class="hint">The same display size is used for both versions. Metric scores describe the configured comparison; they do not guarantee identical perception for every viewer.</p>
    </section>
    <form class="sec" @submit.prevent="mintCredential">
      <div class="sec-head"><h2>Browser extension credential</h2></div>
      <p class="hint">Issue access for public images from specific hosts and the selected profile. The extension receives this limited credential; the administrator token and signing key stay on the server.</p>
      <label class="field">Allowed image hosts<input v-model="credentialHosts" class="input" required placeholder="images.example.com, *.cdn.example.com" /></label>
      <div class="options">
        <label class="field">Maximum CSS width<input v-model.number="credentialWidth" class="input" type="number" min="1" :max="info?.max_width ?? 4096" required /></label>
        <label class="field">Maximum DPR<input v-model.number="credentialDPR" class="input" type="number" min="1" :max="info?.max_dpr ?? 3" step="0.25" required /></label>
        <label class="field">Expires in minutes<input v-model.number="credentialMinutes" class="input" type="number" min="1" max="60" required /></label>
      </div>
      <button class="btn" :disabled="credentialBusy || !info?.enabled">{{ credentialBusy ? 'Issuing…' : 'Issue extension credential' }}</button>
      <p v-if="credentialError" role="alert" class="notice err">{{ credentialError }}</p>
      <div v-if="credential" class="stack">
        <label class="field">Extension API origin<input class="input mono" :value="credential.public_url" readonly /></label>
        <label class="field">Limited credential<input class="input mono" type="password" :value="credential.token" readonly autocomplete="off" /></label>
        <button class="btn" type="button" @click="copyCredential">{{ credentialCopied ? 'Copied' : 'Copy limited credential' }}</button>
        <p class="hint">Profile {{ credential.scope.profiles.join(', ') }}. Expires {{ new Date(credential.scope.expires * 1000).toLocaleString() }}. Up to 120 signatures per minute; requests outside the scope retain the original image.</p>
      </div>
    </form>
  </div>
</template>

<style scoped>
.field { display: flex; flex-direction: column; gap: .4rem; font-size: var(--fs-sm); }
.options { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1rem; margin: 1rem 0; }
.hint { margin: .8rem 0; }
.metrics { display: flex; flex-wrap: wrap; gap: 1rem 2rem; padding: 0 0 1rem; }
.metrics dt { color: var(--ink-2); font-size: var(--fs-xs); }
.metrics dd { margin: .3rem 0 0; }
.preview { display: flex; align-items: center; justify-content: center; height: min(70vh, 640px); min-height: 180px; background: var(--paper-2); border: 1px solid var(--rule); padding: 1rem; }
.preview img { display: block; width: 100%; height: 100%; object-fit: contain; }
@media (max-width: 720px) { .options { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
</style>
