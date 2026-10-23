<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { api, ApiFailure } from '../api/client'
import type { ConfigView, ProfileInfo, ProfileOverride } from '../api/types'
import { toast } from '../composables/toast'
import UiDrawer from './UiDrawer.vue'

const props = defineProps<{ profile: ProfileInfo | null; profiles: ProfileInfo[] }>()
const emit = defineEmits<{ close: []; saved: [cfg: ConfigView] }>()

type NumKey =
  | 'target_dssim' | 'target_ssim' | 'target_ssimulacra2' | 'attempts' | 'photo_colors' | 'animated_quality' | 'avif_effort'
type BoolKey = 'resize_to_viewport' | 'quantize_png' | 'animated_to_still' | 'minify_js'
type TextKey = 'brotli' | 'gzip' | 'zstd' | 'brotli_static' | 'static_wait_ms'

const isNew = props.profile === null
const builtin = props.profile?.builtin ?? false
const name = ref(props.profile?.name ?? '')
const base = ref(props.profile?.override.base ?? (isNew ? 'balanced' : ''))
const ov = reactive<ProfileOverride>(JSON.parse(JSON.stringify(props.profile?.override ?? {})))
const formats = ref((props.profile?.override.formats ?? []).join(', '))
const issues = ref<Record<string, string>>({})
const general = ref('')
const busy = ref(false)
const confirming = ref(false)

const reference = computed<ProfileInfo | undefined>(() => {
  if (props.profile) return props.profile
  return props.profiles.find((p) => p.name === base.value)
})

const numFields: { key: NumKey; label: string; step: number; hint: string }[] = [
  { key: 'target_dssim', label: 'target_dssim', step: 0.0005, hint: '0.0001 to 1, lower is closer to the original' },
  { key: 'target_ssim', label: 'target_ssim', step: 0.005, hint: '0.5 to 1' },
  { key: 'target_ssimulacra2', label: 'target_ssimulacra2', step: 1, hint: '0 to 100' },
  { key: 'attempts', label: 'attempts', step: 1, hint: '1 to 8 encodes tried per image' },
  { key: 'photo_colors', label: 'photo_colors', step: 1, hint: 'colours above which a PNG counts as a photo' },
  { key: 'animated_quality', label: 'animated_quality', step: 1, hint: '0 to 100, 0 keeps animations as they are' },
  { key: 'avif_effort', label: 'avif_effort', step: 1, hint: '0 lets the engine choose, 1 fastest to 10 smallest AVIF files; WebAssembly never goes above 2' },
]
const boolFields: { key: BoolKey; label: string }[] = [
  { key: 'resize_to_viewport', label: 'resize_to_viewport' },
  { key: 'quantize_png', label: 'quantize_png' },
  { key: 'animated_to_still', label: 'animated_to_still' },
  { key: 'minify_js', label: 'minify_js' },
]
const textFields: { key: TextKey; label: string; hint: string }[] = [
  { key: 'brotli', label: 'text.brotli', hint: '0 to 11' },
  { key: 'gzip', label: 'text.gzip', hint: '1 to 9' },
  { key: 'zstd', label: 'text.zstd', hint: '1 to 22' },
  { key: 'brotli_static', label: 'text.brotli_static', hint: '0 to 11, for cached copies' },
  { key: 'static_wait_ms', label: 'text.static_wait_ms', hint: 'most a request waits for it, in milliseconds' },
]
const noTransformChoices = ['respect', 'images', 'all']

function setNum(key: NumKey, ev: Event) {
  const v = (ev.target as HTMLInputElement).value
  if (v === '') delete ov[key]
  else ov[key] = Number(v)
}
function setBool(key: BoolKey, ev: Event) {
  const v = (ev.target as HTMLSelectElement).value
  if (v === '') delete ov[key]
  else ov[key] = v === 'true'
}
function setText(key: TextKey, ev: Event) {
  const v = (ev.target as HTMLInputElement).value
  const t = { ...(ov.text ?? {}) }
  if (v === '') delete t[key]
  else t[key] = Number(v)
  if (Object.keys(t).length) ov.text = t
  else delete ov.text
}
function setNoTransform(ev: Event) {
  const v = (ev.target as HTMLSelectElement).value
  if (v === '') delete ov.no_transform
  else ov.no_transform = v
}
function boolValue(key: BoolKey) {
  return ov[key] === undefined ? '' : String(ov[key])
}
function effective(key: keyof ProfileInfo | TextKey, text = false): string {
  const r = reference.value
  if (!r) return ''
  const v = text ? r.text[key as TextKey] : r[key as keyof ProfileInfo]
  return Array.isArray(v) ? v.join(', ') : String(v)
}

function payload(): ProfileOverride {
  const out: ProfileOverride = JSON.parse(JSON.stringify(ov))
  const list = formats.value.split(/[\s,]+/).filter(Boolean)
  if (list.length) out.formats = list
  else delete out.formats
  if (!builtin && base.value) out.base = base.value
  else delete out.base
  return out
}

const nameError = computed(() => {
  if (!isNew || !name.value) return ''
  return /^[a-z][a-z0-9_-]{0,31}$/.test(name.value) ? '' : 'Lower case letters, digits, - and _, starting with a letter, up to 32'
})
const canSave = computed(() => !busy.value && (!isNew || (name.value !== '' && !nameError.value)))

function takeIssues(e: unknown) {
  issues.value = {}
  general.value = ''
  if (e instanceof ApiFailure) {
    const known = [...numFields.map((f) => f.key as string), ...boolFields.map((f) => f.key as string), 'formats', 'base', 'name', 'no_transform', ...textFields.map((f) => 'text.' + f.key)]
    for (const i of e.body.issues ?? []) {
      const msg = i.message.replace(/^profile "[^"]*":\s*/, '')
      const key = i.field === 'name' ? 'name' : msg.split(/[\s:]/)[0]
      if (known.includes(key)) issues.value[key] = msg
      else if (/unknown format/.test(msg)) issues.value.formats = msg
      else if (/base/.test(msg)) issues.value.base = msg
      else general.value = general.value ? general.value + '; ' + msg : msg
    }
    if (!Object.keys(issues.value).length && !general.value) general.value = e.message
  } else {
    general.value = e instanceof Error ? e.message : String(e)
  }
}

async function save() {
  busy.value = true
  try {
    const cfg = await api.saveProfile(name.value, payload())
    toast(`Profile ${name.value} saved and applied`)
    emit('saved', cfg)
    emit('close')
  } catch (e) {
    takeIssues(e)
  } finally {
    busy.value = false
  }
}

async function remove() {
  busy.value = true
  try {
    const cfg = await api.deleteProfile(name.value)
    toast(builtin ? `Profile ${name.value} is back to its defaults` : `Profile ${name.value} deleted`)
    emit('saved', cfg)
    emit('close')
  } catch (e) {
    confirming.value = false
    takeIssues(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiDrawer :label="isNew ? 'New profile' : `Profile ${name}`" wide @close="emit('close')">
    <template #head>
      <h2>{{ isNew ? 'New profile' : name }}</h2>
      <p v-if="!isNew" class="muted small">{{ builtin ? 'Built-in profile' : 'Custom profile' }}</p>
    </template>
    <div class="stack-sm">
      <p v-if="builtin" class="muted small">
        Built-in profile. Fields you fill in override the built-in value; empty fields keep it.
      </p>
      <p v-else-if="!isNew" class="muted small">Custom profile. Empty fields are taken from its base.</p>
      <p v-if="profile?.lossless" class="muted small">This profile is lossless: image quality targets are ignored.</p>

      <div v-if="isNew || !builtin" class="head2">
        <div v-if="isNew" class="field">
          <label for="pf-name">Name</label>
          <input id="pf-name" v-model="name" class="input mono" :class="{ bad: nameError || issues.name }" autocomplete="off" placeholder="school-wifi" />
          <span v-if="nameError || issues.name" class="error-text">{{ nameError || issues.name }}</span>
        </div>
        <div class="field">
          <label for="pf-base">Base</label>
          <select id="pf-base" v-model="base" class="select">
            <option v-for="p in profiles.filter((x) => x.name !== name)" :key="p.name" :value="p.name">{{ p.name }}</option>
          </select>
          <span v-if="issues.base" class="error-text">{{ issues.base }}</span>
        </div>
      </div>

      <div class="ledger-wrap">
        <table class="ledger form">
          <thead>
            <tr>
              <th scope="col">Setting</th>
              <th scope="col">Value</th>
              <th scope="col">{{ isNew ? 'Base value' : 'Current' }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in numFields" :key="f.key">
              <td class="mono">{{ f.label }}</td>
              <td>
                <input
                  class="input mono narrow"
                  :class="{ bad: issues[f.key] }"
                  type="number"
                  :step="f.step"
                  :value="ov[f.key] ?? ''"
                  :placeholder="builtin ? 'built-in' : 'inherit'"
                  :aria-label="f.label"
                  @input="setNum(f.key, $event)"
                />
                <div v-if="issues[f.key]" class="error-text">{{ issues[f.key] }}</div>
                <div v-else class="hint">{{ f.hint }}</div>
              </td>
              <td class="mono muted">{{ effective(f.key) }}</td>
            </tr>
            <tr>
              <td class="mono">formats</td>
              <td>
                <input v-model="formats" class="input mono" :class="{ bad: issues.formats }" :placeholder="builtin ? 'built-in' : 'inherit'" aria-label="formats" autocomplete="off" />
                <div v-if="issues.formats" class="error-text">{{ issues.formats }}</div>
                <div v-else class="hint">Comma separated, in order of preference: jxl, avif, webp</div>
              </td>
              <td class="mono muted">{{ effective('formats') }}</td>
            </tr>
            <tr v-for="f in boolFields" :key="f.key">
              <td class="mono">{{ f.label }}</td>
              <td>
                <select class="select compact" :value="boolValue(f.key)" :aria-label="f.label" @change="setBool(f.key, $event)">
                  <option value="">{{ builtin ? 'built-in' : 'inherit' }}</option>
                  <option value="true">on</option>
                  <option value="false">off</option>
                </select>
                <div v-if="issues[f.key]" class="error-text">{{ issues[f.key] }}</div>
              </td>
              <td class="mono muted">{{ effective(f.key) === 'true' ? 'on' : 'off' }}</td>
            </tr>
            <tr>
              <td class="mono">no_transform</td>
              <td>
                <select class="select compact" :value="ov.no_transform ?? ''" aria-label="no_transform" @change="setNoTransform($event)">
                  <option value="">{{ builtin ? 'built-in' : 'inherit' }}</option>
                  <option v-for="c in noTransformChoices" :key="c" :value="c">{{ c }}</option>
                </select>
                <div v-if="issues.no_transform" class="error-text">{{ issues.no_transform }}</div>
                <div v-else class="hint">Cache-Control: no-transform. respect keeps it, images recodes the images that say it, all adds text</div>
              </td>
              <td class="mono muted">{{ effective('no_transform') }}</td>
            </tr>
            <tr v-for="f in textFields" :key="f.key">
              <td class="mono">{{ f.label }}</td>
              <td>
                <input
                  class="input mono narrow"
                  :class="{ bad: issues['text.' + f.key] }"
                  type="number"
                  step="1"
                  :value="ov.text?.[f.key] ?? ''"
                  :placeholder="builtin ? 'built-in' : 'inherit'"
                  :aria-label="f.label"
                  @input="setText(f.key, $event)"
                />
                <div v-if="issues['text.' + f.key]" class="error-text">{{ issues['text.' + f.key] }}</div>
                <div v-else class="hint">{{ f.hint }}</div>
              </td>
              <td class="mono muted">{{ effective(f.key, true) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="general" class="notice err">{{ general }}</p>
      <p v-if="confirming" class="notice warn">
        {{ builtin ? `Reset ${name} to its built-in values? Your changes to it are removed.` : `Delete the profile ${name}? Devices and rules that use it must be changed first.` }}
      </p>
    </div>

    <template #footer>
      <template v-if="confirming">
        <button class="btn" :disabled="busy" @click="confirming = false">Keep</button>
        <button class="btn danger" :disabled="busy" @click="remove">{{ builtin ? 'Reset' : 'Delete' }}</button>
      </template>
      <template v-else>
        <button v-if="!isNew && (!builtin || profile?.modified)" class="btn danger left" :disabled="busy" @click="confirming = true">
          {{ builtin ? 'Reset to defaults' : 'Delete' }}
        </button>
        <button class="btn" @click="emit('close')">Cancel</button>
        <button class="btn primary" :disabled="!canSave" @click="save">{{ busy ? 'Saving' : 'Save and apply' }}</button>
      </template>
    </template>
  </UiDrawer>
</template>

<style scoped>
.head2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 0.8rem; }
.form td { vertical-align: top; padding-top: 0.5rem; padding-bottom: 0.5rem; }
.form td:first-child { padding-top: 0.75rem; white-space: nowrap; }
.form td:last-child { padding-top: 0.75rem; }
.form tbody tr:hover td { background: transparent; }
.narrow { max-width: 11rem; }
.left { margin-right: auto; }
</style>
