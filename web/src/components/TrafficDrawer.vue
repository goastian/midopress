<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '../api/client'
import type { Flow } from '../api/types'
import { bytes, dateTime, distance, ms, percent } from '../composables/format'
import UiIcon from './UiIcon.vue'
import TrafficCompare from './TrafficCompare.vue'

const props = defineProps<{ flow: Flow }>()
const emit = defineEmits<{ close: [] }>()

const full = ref<Flow | null>(null)
const error = ref('')
const panel = ref<HTMLElement | null>(null)
const opener = document.activeElement as HTMLElement | null

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

function key(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => {
  window.addEventListener('keydown', key)
  panel.value?.focus()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', key)
  opener?.focus?.()
})

function rows(h: Record<string, string[]> | null | undefined) {
  return Object.entries(h ?? {}).sort((a, b) => a[0].localeCompare(b[0]))
}
</script>

<template>
  <Teleport to="body">
    <div class="back" @mousedown.self="emit('close')">
      <aside ref="panel" class="drawer" role="dialog" aria-modal="true" aria-label="Flow detail" tabindex="-1">
        <header>
          <div class="grow">
            <div class="mono url">{{ f.method }} {{ url }}</div>
          </div>
          <button class="x" aria-label="Close" @click="emit('close')"><UiIcon name="x" :size="18" /></button>
        </header>

        <div class="body">
          <dl class="kv mono">
            <div><dt>Status</dt><dd>{{ f.status }}</dd></div>
            <div><dt>Time</dt><dd>{{ dateTime(f.time) }}</dd></div>
            <div><dt>Client</dt><dd>{{ f.client.device ? f.client.device + ' ' : '' }}{{ f.client.addr }}</dd></div>
            <div><dt>Kind</dt><dd>{{ f.kind }} <span class="muted">{{ f.content_type }}</span></dd></div>
            <div>
              <dt>Size</dt>
              <dd>
                {{ bytes(f.bytes_in) }} to {{ bytes(f.bytes_out) }}
                <span v-if="saved > 0" class="ok">-{{ percent(saved) }}</span>
                <span v-else-if="saved < 0" class="bad">+{{ percent(-saved) }}</span>
              </dd>
            </div>
            <div v-if="f.out_content_type"><dt>Sent as</dt><dd>{{ f.out_content_type }}</dd></div>
            <div><dt>Codec</dt><dd>{{ f.codec || '-' }}</dd></div>
            <div><dt>Profile</dt><dd>{{ f.profile || '-' }}</dd></div>
            <div><dt>Duration</dt><dd>{{ ms(f.duration_ms) }}</dd></div>
            <div>
              <dt>Decision</dt>
              <dd>
                <span :class="{ ok: f.decision === 'transformed' }">{{ f.decision }}</span>
                <span v-if="f.reason && f.reason !== f.decision" class="muted"> ({{ f.reason }})</span>
                <span v-if="f.cache_hit" class="muted"> served from cache</span>
              </dd>
            </div>
            <div v-if="f.metric"><dt>Metric</dt><dd>{{ f.metric }} {{ distance(f.measure) }}</dd></div>
          </dl>

          <TrafficCompare v-if="f.kind === 'image' && (f.decision === 'transformed' || f.has_original)" :key="f.id + f.has_result" :flow="f" />
          <p v-else-if="f.kind === 'image'" class="muted small">This image was left alone, so there is no before and after.</p>

          <p v-if="error" class="bad small">Headers not loaded: {{ error }}</p>
          <template v-for="sec in [{ t: 'Request headers', h: f.request_headers }, { t: 'Response headers', h: f.response_headers }]" :key="sec.t">
            <details class="hdrs" :open="false">
              <summary>{{ sec.t }} <span class="muted">({{ rows(sec.h).length }})</span></summary>
              <p v-if="!rows(sec.h).length" class="muted small">{{ full ? 'None recorded.' : 'Loading...' }}</p>
              <dl v-else class="mono">
                <template v-for="[k, vs] in rows(sec.h)" :key="k">
                  <div v-for="(v, i) in vs" :key="k + i">
                    <dt>{{ k }}</dt>
                    <dd><em v-if="v === '<hidden>'" class="muted">hidden</em><template v-else>{{ v }}</template></dd>
                  </div>
                </template>
              </dl>
            </details>
          </template>
        </div>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.back {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: rgb(0 0 0 / 30%);
  display: flex;
  justify-content: flex-end;
}
.drawer {
  width: min(620px, 100%);
  height: 100%;
  background: var(--surface);
  border-left: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  outline: none;
}
header {
  display: flex;
  gap: 0.5rem;
  align-items: flex-start;
  padding: 0.6rem 0.8rem;
  border-bottom: 1px solid var(--border);
}
.url {
  font-size: 0.82rem;
  overflow-wrap: anywhere;
}
.x {
  appearance: none;
  border: 1px solid var(--border);
  background: none;
  color: var(--text);
  width: 32px;
  height: 32px;
  display: grid;
  place-items: center;
  cursor: pointer;
  flex: none;
}
.body {
  overflow-y: auto;
  padding: 0.6rem 0.8rem 1.5rem;
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
}
.kv,
.hdrs dl {
  margin: 0;
  font-size: 0.82rem;
}
.kv > div,
.hdrs dl > div {
  display: flex;
  gap: 0.75rem;
  padding: 0.2rem 0;
  border-bottom: 1px solid var(--border);
}
dt {
  width: 6.5rem;
  flex: none;
  color: var(--muted);
}
.hdrs dt {
  width: 11rem;
  overflow-wrap: anywhere;
}
dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}
.hdrs summary {
  cursor: pointer;
  font-size: 0.9rem;
  font-weight: 600;
  padding: 0.2rem 0;
}
@media (max-width: 560px) {
  .hdrs dl > div {
    flex-direction: column;
    gap: 0;
  }
  .hdrs dt {
    width: auto;
  }
}
</style>
