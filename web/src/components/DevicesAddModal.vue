<script setup lang="ts">
import { computed, ref } from 'vue'
import { getToken } from '../api/client'
import type { ApiError, NewDevice, ValidationIssue } from '../api/types'
import UiModal from './UiModal.vue'
import PagesCopyButton from './PagesCopyButton.vue'

defineProps<{ profiles: string[]; defaultProfile: string }>()
const emit = defineEmits<{ close: []; created: [id: string]; notokens: [] }>()

const hostname = window.location.hostname
const idPattern = /^[A-Za-z0-9._-]{1,64}$/
const id = ref('')
const profile = ref('')
const host = ref('')
const busy = ref(false)
const failure = ref('')
const serverIssues = ref<ValidationIssue[]>([])
const created = ref<NewDevice | null>(null)
const copied = ref(false)
const warnedClose = ref(false)

const idError = computed(() => {
  if (id.value === '') return ''
  if (!idPattern.test(id.value)) return 'Use letters, digits, dot, dash or underscore, 1 to 64 characters'
  return serverIssues.value.find((i) => i.field === 'id')?.message ?? ''
})
const profileError = computed(() => serverIssues.value.find((i) => i.field === 'profile')?.message ?? '')
const canSubmit = computed(() => idPattern.test(id.value) && !busy.value)
const qrSrc = computed(() =>
  created.value?.config_qr_svg
    ? 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(created.value.config_qr_svg)
    : '',
)

async function submit() {
  if (!canSubmit.value) return
  busy.value = true
  failure.value = ''
  serverIssues.value = []
  try {
    const body: Record<string, string> = { id: id.value, profile: profile.value }
    if (host.value.trim()) body.host = host.value.trim()
    const res = await fetch('/api/v1/devices', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${getToken()}` },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      let err: ApiError = { error: res.statusText, code: 'http' }
      try {
        err = await res.json()
      } catch {
        /* not JSON */
      }
      if (err.code === 'no_tokens') {
        emit('notokens')
        emit('close')
        return
      }
      serverIssues.value = err.issues ?? []
      if (err.code === 'exists') serverIssues.value = [{ index: -1, field: 'id', column: 0, message: err.error }]
      else if (!serverIssues.value.length) failure.value = err.error
      return
    }
    created.value = (await res.json()) as NewDevice
    emit('created', created.value.id)
  } catch (e) {
    failure.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

function close() {
  if (created.value && !copied.value && !warnedClose.value) {
    warnedClose.value = true
    return
  }
  emit('close')
}
</script>

<template>
  <UiModal :title="created ? `Device ${created.id}` : 'Add device'" @close="close">
    <form v-if="!created" class="stack-sm" @submit.prevent="submit">
      <div class="field">
        <label for="dev-id">Device id</label>
        <input id="dev-id" v-model="id" class="input mono" :class="{ bad: idError }" autocomplete="off" autofocus placeholder="pixel-7" />
        <span v-if="idError" class="error-text">{{ idError }}</span>
        <span v-else class="hint">Letters, digits, . - _ up to 64 characters.</span>
      </div>
      <div class="field">
        <label for="dev-profile">Profile</label>
        <select id="dev-profile" v-model="profile" class="select">
          <option value="">Default ({{ defaultProfile }})</option>
          <option v-for="p in profiles" :key="p" :value="p">{{ p }}</option>
        </select>
        <span v-if="profileError" class="error-text">{{ profileError }}</span>
      </div>
      <div class="field">
        <label for="dev-host">Proxy host (optional)</label>
        <input id="dev-host" v-model="host" class="input mono" autocomplete="off" :placeholder="hostname" />
        <span class="hint">Name or address the extension uses to reach the proxy. Leave empty to use {{ hostname }}, the name this page was opened with.</span>
      </div>
      <p v-if="failure" class="notice err">{{ failure }}</p>
      <button type="submit" hidden />
    </form>

    <div v-else class="stack-sm">
      <p class="notice warn">
        This token is shown once and cannot be shown again. Copy it now or scan the code; if it is lost, revoke the device and add it again.
      </p>
      <div class="field">
        <span class="label">Token</span>
        <div class="row tight">
          <code class="grow mono tok">{{ created.token }}</code>
          <PagesCopyButton :value="created.token" @copied="copied = true" />
        </div>
      </div>
      <div class="field">
        <span class="label">Configuration for the Midori extension</span>
        <div class="row tight">
          <code class="grow mono tok">{{ created.config_url }}</code>
          <PagesCopyButton :value="created.config_url" @copied="copied = true" />
        </div>
      </div>
      <div v-if="qrSrc" class="qr">
        <img :src="qrSrc" alt="QR code with the extension configuration" width="220" height="220" />
        <span class="hint">Scan it from the extension to load host, port, user and token.</span>
      </div>
      <p v-if="warnedClose" class="error-text">The token was not copied. Close again to discard it.</p>
    </div>

    <template #footer>
      <template v-if="!created">
        <button class="btn" type="button" @click="emit('close')">Cancel</button>
        <button class="btn primary" type="button" :disabled="!canSubmit" @click="submit">{{ busy ? 'Adding' : 'Add device' }}</button>
      </template>
      <button v-else class="btn primary" type="button" @click="emit('close')">Done</button>
    </template>
  </UiModal>
</template>

<style scoped src="./PagesOps.css"></style>
<style scoped>
.tok { overflow-wrap: anywhere; padding: 0.3rem 0.5rem; user-select: all; }
.qr { display: flex; flex-direction: column; align-items: center; gap: 0.4rem; }
.qr img { background: #fff; border: 1px solid var(--border); padding: 6px; max-width: 100%; height: auto; image-rendering: pixelated; }
</style>
