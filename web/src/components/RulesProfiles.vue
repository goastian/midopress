<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api/client'
import type { ConfigView, ProfileInfo } from '../api/types'
import { toast, toastError } from '../composables/toast'
import RulesProfileModal from './RulesProfileModal.vue'

const props = defineProps<{ cfg: ConfigView }>()
const emit = defineEmits<{ update: [cfg: ConfigView] }>()

const editing = ref<ProfileInfo | null>(null)
const open = ref(false)
const busy = ref(false)

function edit(p: ProfileInfo | null) {
  editing.value = p
  open.value = true
}

async function setDefault(ev: Event) {
  const select = ev.target as HTMLSelectElement
  const profile = select.value
  busy.value = true
  try {
    emit('update', await api.setDefaultProfile(profile))
    toast(`Default profile is now ${profile}`)
  } catch (e) {
    select.value = props.cfg.default_profile
    toastError(e)
  } finally {
    busy.value = false
  }
}

function kind(p: ProfileInfo) {
  if (!p.builtin) return 'custom'
  return p.modified ? 'built-in, changed' : 'built-in'
}
function animated(p: ProfileInfo) {
  if (p.animated_to_still) return 'still'
  return p.animated_quality > 0 ? `q${p.animated_quality}` : 'as is'
}
</script>

<template>
  <section class="sec">
    <div class="sec-head">
      <h2>Profiles</h2>
      <div class="row tight">
        <label class="small muted" for="def-profile">Default profile</label>
        <select id="def-profile" class="select compact" :value="cfg.default_profile" :disabled="busy" @change="setDefault">
          <option v-for="p in cfg.profiles" :key="p.name" :value="p.name">{{ p.name }}</option>
        </select>
        <span v-if="cfg.managed_by_ui.default_profile" class="tag">set in the interface</span>
        <button class="btn small" @click="edit(null)">New profile</button>
      </div>
    </div>
    <p class="lede">
      The default profile applies to every request that no rule, device or header chooses another one for. Changes are applied at once without a restart.
    </p>
    <div class="ledger-wrap">
      <table class="ledger">
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th class="hide-sm" scope="col">Kind</th>
            <th class="r" scope="col" title="Lower is closer to the original">dssim</th>
            <th scope="col">Formats</th>
            <th class="hide-sm" scope="col">Resize</th>
            <th class="hide-sm" scope="col">PNG palette</th>
            <th class="hide-sm" scope="col">Animated</th>
            <th class="hide-sm" scope="col" title="brotli / gzip / zstd levels">Text br/gz/zstd</th>
            <th scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in cfg.profiles" :key="p.name">
            <td class="nowrap">
              <span class="mono">{{ p.name }}</span>
              <span v-if="p.name === cfg.default_profile" class="tag ok defbadge">default</span>
            </td>
            <td class="muted nowrap hide-sm">{{ kind(p) }}{{ p.lossless ? ', lossless' : '' }}</td>
            <td class="r mono">{{ p.lossless ? '-' : p.target_dssim }}</td>
            <td class="mono nowrap">{{ p.formats.join(' ') }}</td>
            <td class="hide-sm">{{ p.resize_to_viewport ? 'to viewport' : 'no' }}</td>
            <td class="hide-sm">{{ p.quantize_png ? 'yes' : 'no' }}</td>
            <td class="hide-sm">{{ animated(p) }}</td>
            <td class="mono nowrap hide-sm">{{ p.text.brotli }} / {{ p.text.gzip }} / {{ p.text.zstd }}</td>
            <td class="r"><button class="btn small ghost" :aria-label="`Edit profile ${p.name}`" @click="edit(p)">Edit</button></td>
          </tr>
        </tbody>
      </table>
    </div>

    <RulesProfileModal
      v-if="open"
      :profile="editing"
      :profiles="cfg.profiles"
      @close="open = false"
      @saved="emit('update', $event)"
    />
  </section>
</template>

<style scoped>
.defbadge { margin-left: 0.5rem; }
</style>
