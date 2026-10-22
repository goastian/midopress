<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api/client'
import type { Flow } from '../api/types'
import { bytes, dateTime, distance, ms, percent } from '../composables/format'
import UiDrawer from './UiDrawer.vue'
import TrafficCompare from './TrafficCompare.vue'

const props = defineProps<{ flow: Flow }>()
const emit = defineEmits<{ close: [] }>()

type Tab = 'request' | 'response' | 'decision'
const tabs: { id: Tab; label: string }[] = [
  { id: 'request', label: 'Request' },
  { id: 'response', label: 'Response' },
  { id: 'decision', label: 'Decision' },
]
const tab = ref<Tab>('decision')
const full = ref<Flow | null>(null)
const error = ref('')

watch(
  () => props.flow.id,
  async (id) => {
    full.value = null
    error.value = ''
    try {
      const f = await api.flow(id)
      if (f.id === props.flow.id) full.value = f
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  },
  { immediate: true },
)

const f = computed(() => full.value ?? props.flow)
const url = computed(() => `${f.value.scheme}://${f.value.host}${f.value.path}`)
const saved = computed(() => (f.value.bytes_in > 0 ? ((f.value.bytes_in - f.value.bytes_out) / f.value.bytes_in) * 100 : 0))
const hasImage = computed(() => f.value.kind === 'image' && (f.value.decision === 'transformed' || f.value.has_original))

function rows(h: Record<string, string[]> | null | undefined) {
  return Object.entries(h ?? {}).sort((a, b) => a[0].localeCompare(b[0]))
}

function move(e: KeyboardEvent) {
  const i = tabs.findIndex((t) => t.id === tab.value)
  const next = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : -1
  if (next < 0) return
  tab.value = tabs[(next + tabs.length) % tabs.length].id
  e.preventDefault()
  ;(e.currentTarget as HTMLElement).querySelector<HTMLElement>(`#tab-${tab.value}`)?.focus()
}
</script>

<template>
  <UiDrawer label="Flow detail" wide @close="emit('close')">
    <template #head>
      <div class="mono url"><span class="method">{{ f.method }}</span> {{ url }}</div>
      <p class="summary mono">
        <span :class="f.status >= 500 ? 'bad' : f.status >= 400 ? 'warn' : ''">{{ f.status }}</span>
        <span class="muted">{{ bytes(f.bytes_in) }} to</span> {{ bytes(f.bytes_out) }}
        <span v-if="saved > 0" class="ok">-{{ percent(saved) }}</span>
        <span v-else-if="saved < 0" class="bad">+{{ percent(-saved) }}</span>
        <span class="muted">{{ ms(f.duration_ms) }}</span>
      </p>
    </template>

    <div class="stack">
      <TrafficCompare v-if="hasImage" :key="f.id + f.has_result" :flow="f" />
      <p v-else-if="f.kind === 'image'" class="muted small">This image was left alone, so there is no before and after.</p>

      <div>
        <div class="tabs" role="tablist" aria-label="Detail" @keydown="move">
          <button
            v-for="t in tabs"
            :id="`tab-${t.id}`"
            :key="t.id"
            type="button"
            role="tab"
            :aria-selected="tab === t.id"
            :aria-controls="`panel-${t.id}`"
            :tabindex="tab === t.id ? 0 : -1"
            @click="tab = t.id"
          >{{ t.label }}</button>
        </div>

        <div :id="`panel-${tab}`" role="tabpanel" :aria-labelledby="`tab-${tab}`" class="panel">
          <p v-if="error" class="bad small">Headers not loaded: {{ error }}</p>

          <template v-if="tab === 'request'">
            <dl class="dl">
              <dt>Time</dt><dd class="mono">{{ dateTime(f.time) }}</dd>
              <dt>Client</dt><dd class="mono">{{ f.client.device ? f.client.device + ' ' : '' }}{{ f.client.addr }}</dd>
              <dt>Method</dt><dd class="mono">{{ f.method }}</dd>
              <dt>URL</dt><dd class="mono">{{ url }}</dd>
            </dl>
            <h3>Headers <span class="muted">({{ rows(f.request_headers).length }})</span></h3>
            <p v-if="!rows(f.request_headers).length" class="muted small">{{ full ? 'None recorded.' : 'Loading...' }}</p>
            <dl v-else class="dl headers">
              <template v-for="[k, vs] in rows(f.request_headers)" :key="k">
                <template v-for="(v, i) in vs" :key="k + i">
                  <dt class="mono">{{ k }}</dt>
                  <dd class="mono"><span v-if="v === '<hidden>'" class="muted">hidden</span><template v-else>{{ v }}</template></dd>
                </template>
              </template>
            </dl>
          </template>

          <template v-else-if="tab === 'response'">
            <dl class="dl">
              <dt>Status</dt><dd class="mono" :class="f.status >= 500 ? 'bad' : f.status >= 400 ? 'warn' : ''">{{ f.status }}</dd>
              <dt>Kind</dt><dd class="mono">{{ f.kind }}</dd>
              <dt>Received as</dt><dd class="mono">{{ f.content_type || '-' }}</dd>
              <dt v-if="f.out_content_type">Sent as</dt><dd v-if="f.out_content_type" class="mono">{{ f.out_content_type }}</dd>
              <dt>Size</dt><dd class="mono">{{ bytes(f.bytes_in) }} to {{ bytes(f.bytes_out) }}</dd>
            </dl>
            <h3>Headers <span class="muted">({{ rows(f.response_headers).length }})</span></h3>
            <p v-if="!rows(f.response_headers).length" class="muted small">{{ full ? 'None recorded.' : 'Loading...' }}</p>
            <dl v-else class="dl headers">
              <template v-for="[k, vs] in rows(f.response_headers)" :key="k">
                <template v-for="(v, i) in vs" :key="k + i">
                  <dt class="mono">{{ k }}</dt>
                  <dd class="mono"><span v-if="v === '<hidden>'" class="muted">hidden</span><template v-else>{{ v }}</template></dd>
                </template>
              </template>
            </dl>
          </template>

          <dl v-else class="dl">
            <dt>Decision</dt>
            <dd>
              <span :class="{ ok: f.decision === 'transformed' }">{{ f.decision }}</span>
              <span v-if="f.reason && f.reason !== f.decision" class="muted"> ({{ f.reason }})</span>
            </dd>
            <dt>Codec sent</dt><dd class="mono">{{ f.codec || '-' }}</dd>
            <dt>Profile</dt><dd class="mono">{{ f.profile || '-' }}</dd>
            <dt>Duration</dt><dd class="mono">{{ ms(f.duration_ms) }}</dd>
            <dt>Cache</dt><dd>{{ f.cache_hit ? 'served from cache' : 'not from cache' }}</dd>
            <template v-if="f.metric">
              <dt>Quality metric</dt><dd class="mono">{{ f.metric }} {{ distance(f.measure) }}</dd>
            </template>
          </dl>
        </div>
      </div>
    </div>
  </UiDrawer>
</template>

<style scoped>
.url {
  overflow-wrap: anywhere;
  font-size: var(--fs-sm);
}
.method {
  color: var(--ink-2);
}
.summary {
  display: flex;
  gap: 0.2rem 0.75rem;
  flex-wrap: wrap;
  margin-top: 0.25rem;
  font-size: var(--fs-xs);
}
.tabs {
  display: flex;
  gap: 1.25rem;
  border-bottom: 1px solid var(--rule);
}
.tabs button {
  appearance: none;
  background: none;
  border: 0;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  padding: 0.4rem 0;
  font: inherit;
  font-size: var(--fs-sm);
  color: var(--ink-2);
  cursor: pointer;
  min-height: 34px;
}
.tabs button:hover {
  color: var(--ink);
}
.tabs button[aria-selected='true'] {
  color: var(--ink);
  font-weight: 500;
  border-bottom-color: var(--ink);
}
.tabs button:focus-visible {
  outline-offset: -2px;
}
.panel {
  padding-top: 0.75rem;
}
.panel h3 {
  margin: 1.1rem 0 0.4rem;
}
.headers {
  font-size: var(--fs-xs);
}
.headers dt,
.headers dd {
  padding: 0.2rem 0;
}
.headers dt {
  min-width: 11rem;
}
@media (max-width: 720px) {
  .headers dt {
    min-width: 0;
  }
}
</style>
