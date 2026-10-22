<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, ApiFailure } from '../api/client'
import type { Bucket, Client, Device } from '../api/types'
import { on } from '../composables/live'
import { usePoll } from '../composables/poll'
import { toast, toastError } from '../composables/toast'
import { ago, bytes, count, dateTime, percent } from '../composables/format'
import UiModal from '../components/UiModal.vue'
import DevicesAddModal from '../components/DevicesAddModal.vue'
import PagesCopyButton from '../components/PagesCopyButton.vue'

const now = ref(Date.now())
let tick: ReturnType<typeof setInterval>
onMounted(() => (tick = setInterval(() => (now.value = Date.now()), 5000)))
onUnmounted(() => clearInterval(tick))

const clients = ref<Client[]>([])
const clientsError = ref('')
const clientsLoaded = ref(false)
async function loadClients() {
  try {
    clients.value = await api.clients()
    clientsError.value = ''
  } catch (e) {
    clientsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    clientsLoaded.value = true
  }
}
usePoll(async () => {
  if (!document.hidden) await loadClients()
  return null
}, 5000)
on('client', (c) => {
  const i = clients.value.findIndex((x) => x.key === c.key)
  if (i >= 0) clients.value.splice(i, 1, c)
  else clients.value.push(c)
})

const sortedClients = computed(() =>
  [...clients.value].sort((a, b) => Number(b.active) - Number(a.active) || b.last_seen.localeCompare(a.last_seen)),
)
const open = ref(new Set<string>())
function toggle(key: string) {
  if (open.value.has(key)) open.value.delete(key)
  else open.value.add(key)
  open.value = new Set(open.value)
}

const noTokens = ref(false)
const { data: devices, error: devicesError, reload: reloadDevices } = usePoll<Device[]>(async () => {
  try {
    const list = await api.devices()
    noTokens.value = false
    return list
  } catch (e) {
    if (e instanceof ApiFailure && e.body.code === 'no_tokens') {
      noTokens.value = true
      return []
    }
    throw e
  }
}, 10000)
const { data: cfg } = usePoll(() => api.config())
const profileNames = computed(() => cfg.value?.profiles.map((p) => p.name) ?? [])
const defaultProfile = computed(() => cfg.value?.default_profile ?? 'default')

function shortUA(ua: string): string {
  if (!ua) return '-'
  const os = /\(([^;)]+)/.exec(ua)?.[1]?.trim() ?? ''
  const names: [string, string][] = [['Edg', 'Edge'], ['OPR', 'Opera'], ['Midori', 'Midori'], ['Firefox', 'Firefox'], ['FxiOS', 'Firefox'], ['CriOS', 'Chrome'], ['Chrome', 'Chrome'], ['Version', 'Safari'], ['Safari', 'Safari']]
  for (const [token, name] of names) {
    const m = new RegExp(`${token}/(\\d+)`).exec(ua)
    if (m) return `${name} ${m[1]}${os ? ', ' + os : ''}`
  }
  return ua.length > 36 ? ua.slice(0, 35) + '...' : ua
}
function savedPct(b: { bytes_in: number; bytes_out: number }) {
  return b.bytes_in > 0 ? ((b.bytes_in - b.bytes_out) / b.bytes_in) * 100 : null
}
function kinds(c: Client): [string, Bucket][] {
  return Object.entries(c.by_kind ?? {}).sort((a, b) => b[1].bytes_in - a[1].bytes_in)
}

async function setProfile(d: Device, ev: Event) {
  const profile = (ev.target as HTMLSelectElement).value
  try {
    await api.setDeviceProfile(d.id, profile)
    toast(`${d.id} now uses ${profile || 'the default profile'}`)
  } catch (e) {
    toastError(e)
  }
  await reloadDevices()
}

const adding = ref(false)
const revoking = ref<Device | null>(null)
async function revoke() {
  const d = revoking.value
  if (!d) return
  try {
    await api.revokeDevice(d.id)
    toast(`Revoked ${d.id}`)
    revoking.value = null
    await reloadDevices()
  } catch (e) {
    toastError(e)
  }
}
function onNoTokens() {
  noTokens.value = true
}

const setupYaml = `auth:
  mode: token
  devices_file: /var/lib/midopress/devices.json`
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>Devices</h1>
        <p>Who is using the proxy now, and the devices that hold an access token.</p>
      </div>
      <button class="btn primary" @click="adding = true">Add device</button>
    </div>

    <section class="sec">
      <div class="sec-head">
        <h2>Connected now</h2>
        <span class="sub">{{ sortedClients.filter((c) => c.active).length }} active, {{ sortedClients.length }} seen recently</span>
      </div>
      <p v-if="clientsError" class="notice err">{{ clientsError }}</p>
      <p v-else-if="clientsLoaded && !sortedClients.length" class="muted small">Nobody has used the proxy recently.</p>
      <div v-else-if="sortedClients.length" class="table-wrap">
        <table class="tbl">
          <thead>
            <tr>
              <th></th>
              <th>Device or client</th>
              <th class="hide-sm">Address</th>
              <th class="hide-sm">User agent</th>
              <th>Profile</th>
              <th class="r">Requests</th>
              <th class="r hide-sm">In flight</th>
              <th class="r hide-sm">In</th>
              <th class="r hide-sm">Out</th>
              <th class="r">Saved</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="c in sortedClients" :key="c.key">
              <tr>
                <td>
                  <button class="exp" :aria-expanded="open.has(c.key)" :aria-label="`Details of ${c.key}`" @click="toggle(c.key)">
                    <i :class="{ down: open.has(c.key) }" />
                  </button>
                </td>
                <td class="nowrap">
                  <span class="dot" :class="{ on: c.active }" :title="c.active ? 'active' : 'idle'" />
                  <span class="mono key">{{ c.key }}</span>
                </td>
                <td class="mono nowrap hide-sm">{{ c.addr }}</td>
                <td class="ua hide-sm" :title="c.user_agent">{{ shortUA(c.user_agent) }}</td>
                <td class="mono">{{ c.profile || '-' }}</td>
                <td class="r">{{ count(c.requests) }}</td>
                <td class="r hide-sm">{{ c.in_flight }}</td>
                <td class="r nowrap hide-sm">{{ bytes(c.bytes_in) }}</td>
                <td class="r nowrap hide-sm">{{ bytes(c.bytes_out) }}</td>
                <td class="r nowrap">{{ bytes(c.saved) }} <span class="muted">{{ percent(savedPct(c), 0) }}</span></td>
                <td class="nowrap" :title="dateTime(c.last_seen)">{{ ago(c.last_seen, now) }}</td>
              </tr>
              <tr v-if="open.has(c.key)" class="sub">
                <td></td>
                <td colspan="10">
                  <p v-if="!kinds(c).length" class="muted small">No traffic by kind yet.</p>
                  <table v-else class="tbl inner">
                    <thead>
                      <tr>
                        <th>Kind</th>
                        <th class="r">Requests</th>
                        <th class="r">Changed</th>
                        <th class="r">In</th>
                        <th class="r">Out</th>
                        <th class="r">Saved</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="[kind, b] in kinds(c)" :key="kind">
                        <td>{{ kind }}</td>
                        <td class="r">{{ count(b.requests) }}</td>
                        <td class="r">{{ count(b.transformed) }}</td>
                        <td class="r">{{ bytes(b.bytes_in) }}</td>
                        <td class="r">{{ bytes(b.bytes_out) }}</td>
                        <td class="r">{{ percent(savedPct(b)) }}</td>
                      </tr>
                    </tbody>
                  </table>
                  <p class="muted small first">First seen {{ dateTime(c.first_seen) }}</p>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </section>

    <section class="sec">
      <div class="sec-head">
        <h2>Devices with tokens</h2>
        <span v-if="!noTokens" class="sub">{{ devices?.length ?? 0 }} devices</span>
      </div>

      <div v-if="noTokens" class="notice info setup">
        <p><strong>This proxy does not use device tokens</strong> (auth.mode is not token), so no devices can be managed here. To give each device its own token, enable token authentication in the configuration file and restart MidoPress:</p>
        <pre class="pre">{{ setupYaml }}</pre>
        <div class="row tight">
          <PagesCopyButton :value="setupYaml" label="Copy snippet" />
          <span class="muted small">The devices file is created when the first device is added. From the command line: <code>midopress device add -file devices.json -id phone</code>.</span>
        </div>
      </div>
      <p v-else-if="devicesError" class="notice err">{{ devicesError }}</p>
      <p v-else-if="devices && !devices.length" class="muted small">No devices yet. Add one to get a token for the Midori extension.</p>
      <div v-else-if="devices" class="table-wrap">
        <table class="tbl">
          <thead>
            <tr>
              <th>Id</th>
              <th>Profile</th>
              <th class="hide-sm">Created</th>
              <th>Last seen</th>
              <th class="r hide-sm">Requests</th>
              <th class="r">Saved</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in devices" :key="d.id">
              <td class="nowrap"><span class="dot" :class="{ on: d.active }" /><span class="mono key">{{ d.id }}</span></td>
              <td>
                <select class="select compact" :value="d.profile" :aria-label="`Profile of ${d.id}`" @change="setProfile(d, $event)">
                  <option value="">default ({{ defaultProfile }})</option>
                  <option v-for="p in profileNames" :key="p" :value="p">{{ p }}</option>
                  <option v-if="d.profile && !profileNames.includes(d.profile)" :value="d.profile">{{ d.profile }}</option>
                </select>
              </td>
              <td class="nowrap hide-sm">{{ dateTime(d.created) }}</td>
              <td class="nowrap">{{ ago(d.last_seen, now) }}</td>
              <td class="r hide-sm">{{ count(d.requests) }}</td>
              <td class="r nowrap">{{ bytes(d.saved) }}</td>
              <td class="r"><button class="btn small danger" @click="revoking = d">Revoke</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <DevicesAddModal v-if="adding" :profiles="profileNames" :default-profile="defaultProfile" @close="adding = false" @created="reloadDevices" @notokens="onNoTokens" />

    <UiModal v-if="revoking" :title="`Revoke ${revoking.id}`" @close="revoking = null">
      <p>The token of <code>{{ revoking.id }}</code> stops working at once and cannot be restored. The device will be refused until you add it again with a new token.</p>
      <template #footer>
        <button class="btn" @click="revoking = null">Cancel</button>
        <button class="btn danger" @click="revoke">Revoke device</button>
      </template>
    </UiModal>
  </div>
</template>

<style scoped src="../components/PagesOps.css"></style>
<style scoped>
.key { margin-left: 0.4rem; }
.ua { max-width: 16rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.exp { appearance: none; background: none; border: 0; padding: 0.3rem; cursor: pointer; display: flex; }
.exp i { width: 0; height: 0; border-left: 5px solid var(--muted); border-top: 4px solid transparent; border-bottom: 4px solid transparent; }
.exp i.down { border-left: 4px solid transparent; border-right: 4px solid transparent; border-top: 5px solid var(--muted); border-bottom: 0; }
.inner { max-width: 640px; }
.inner th, .inner td { padding: 0.2rem 0.5rem; }
.first { margin-top: 0.4rem; }
.setup { display: flex; flex-direction: column; gap: 0.6rem; }
</style>
