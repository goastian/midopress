<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../api/client'
import type { Device, DeviceQuotaLimits } from '../api/types'
import { bytes, count, dateTime } from '../composables/format'
import UiModal from './UiModal.vue'

const props = defineProps<{ device: Device }>()
const emit = defineEmits<{ close: []; saved: [device: Device] }>()
const useGlobal = ref(!props.device.quotas)
const initial = props.device.quotas ?? props.device.quota?.limits
const requests = ref(String(initial?.requests ?? 0))
const byteLimit = ref(String(initial?.bytes ?? 0))
const concurrency = ref(String(initial?.concurrency ?? 0))
const busy = ref(false)
const failure = ref('')

function integer(value: string, maximum = Number.MAX_SAFE_INTEGER): boolean {
  return /^\d+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) <= maximum
}
const valid = computed(() => useGlobal.value || integer(requests.value) && integer(byteLimit.value) && integer(concurrency.value, 10000))

function displayLimit(value: number, asBytes = false): string {
  return value === 0 ? 'unlimited' : asBytes ? bytes(value) : count(value)
}

async function save() {
  if (!valid.value || busy.value) return
  busy.value = true
  failure.value = ''
  const quotas: DeviceQuotaLimits | null = useGlobal.value ? null : {
    requests: Number(requests.value), bytes: Number(byteLimit.value), concurrency: Number(concurrency.value),
  }
  try {
    const updated = await api.setDeviceQuotas(props.device.id, quotas)
    emit('saved', updated)
  } catch (e) {
    failure.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal :title="`Quotas for ${device.id}`" @close="emit('close')">
    <div class="stack-sm">
      <template v-if="device.quota">
        <p class="hint">Current window, in your local time: {{ dateTime(device.quota.window_start) }} – {{ dateTime(device.quota.window_end) }}.</p>
        <dl class="usage">
          <dt>Requests charged</dt><dd>{{ count(device.quota.requests) }} / {{ displayLimit(device.quota.limits.requests) }}</dd>
          <dt>Bytes charged</dt><dd>{{ bytes(device.quota.bytes) }} / {{ displayLimit(device.quota.limits.bytes, true) }}</dd>
          <dt>Active requests</dt><dd>{{ device.quota.active }} / {{ displayLimit(device.quota.limits.concurrency) }}</dd>
          <dt>Active CONNECT tunnels</dt><dd>{{ device.quota.tunnels }} / {{ displayLimit(device.quota.limits.concurrency) }}</dd>
        </dl>
      </template>
      <p v-else class="hint">Quota usage and the current global limits were not reported by this server.</p>
      <form class="stack-sm" @submit.prevent="save">
        <label class="row tight"><input v-model="useGlobal" type="checkbox" :disabled="busy" /> Use global limits</label>
        <p class="hint">Zero means unlimited. Changing limits keeps the usage already charged in this window.</p>
        <fieldset class="stack-sm quota-fields" :disabled="useGlobal || busy">
          <div class="field">
            <label for="quota-requests">Request limit per window</label>
            <input id="quota-requests" v-model="requests" class="input mono" type="number" min="0" :max="Number.MAX_SAFE_INTEGER" step="1" inputmode="numeric" />
          </div>
          <div class="field">
            <label for="quota-bytes">Byte limit per window</label>
            <input id="quota-bytes" v-model="byteLimit" class="input mono" type="number" min="0" :max="Number.MAX_SAFE_INTEGER" step="1" inputmode="numeric" />
            <span class="hint">Enter bytes. 1 GiB = 1,073,741,824 bytes.</span>
          </div>
          <div class="field">
            <label for="quota-concurrency">Concurrency limit per pool</label>
            <input id="quota-concurrency" v-model="concurrency" class="input mono" type="number" min="0" max="10000" step="1" inputmode="numeric" />
          </div>
        </fieldset>
        <p class="hint">Requests and CONNECT tunnels have independent concurrency pools. This limit applies to each pool.</p>
        <p v-if="!valid" class="error-text">Use whole nonnegative values; concurrency cannot exceed 10,000.</p>
        <p v-if="failure" role="alert" class="notice err">{{ failure }}</p>
        <button type="submit" hidden />
      </form>
    </div>
    <template #footer>
      <button class="btn" :disabled="busy" @click="emit('close')">Cancel</button>
      <button class="btn primary" :disabled="busy || !valid" @click="save">{{ busy ? 'Saving' : 'Save limits' }}</button>
    </template>
  </UiModal>
</template>

<style scoped>
.quota-fields { border: 0; padding: 0; margin: 0; min-width: 0; }
.usage { display: grid; grid-template-columns: 1fr auto; gap: 0.3rem 0.8rem; margin: 0; }
.usage dt { color: var(--ink-2); }
.usage dd { margin: 0; text-align: right; font-variant-numeric: tabular-nums; }
</style>
