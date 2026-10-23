<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { api, ApiFailure } from '../api/client'
import type { ConfigView, Rule, ValidationIssue } from '../api/types'
import { toast } from '../composables/toast'
import RulesSyntaxHelp from './RulesSyntaxHelp.vue'
import UiIcon from './UiIcon.vue'

const props = defineProps<{ cfg: ConfigView }>()
const emit = defineEmits<{ update: [cfg: ConfigView] }>()

type Action = '' | 'passthrough' | 'passthrough_tls'
interface Entry {
  uid: number
  match: string
  action: Action
  profile: string
  set: { uid: number; key: string; value: string }[]
}

const setKeys: Record<string, 'mode' | 'bool' | 'int' | 'choice'> = {
  minify_html: 'mode',
  minify_css: 'mode',
  minify_js: 'mode',
  minify_svg: 'mode',
  minify_json: 'mode',
  images: 'bool',
  text: 'bool',
  quality: 'int',
  no_transform: 'choice',
}
const keyNames = Object.keys(setKeys)
const modes = ['off', 'safe', 'aggressive', 'true', 'false']
const noTransformChoices = ['respect', 'images', 'all']

let uid = 1
function fromRules(rules: Rule[]): Entry[] {
  return rules.map((r) => ({
    uid: uid++,
    match: r.match,
    action: (r.action ?? '') as Action,
    profile: r.profile ?? '',
    set: Object.entries(r.set ?? {}).map(([key, v]) => ({ uid: uid++, key, value: String(v) })),
  }))
}
function typed(key: string, value: string): string | number | boolean {
  const kind = setKeys[key]
  if (kind === 'int') return Number(value)
  if (kind === 'bool') return value === 'true'
  if (value === 'true') return true
  if (value === 'false') return false
  return value
}
function toRules(list: Entry[]): Rule[] {
  return list.map((e) => {
    const r: Rule = { match: e.match }
    if (e.action) r.action = e.action
    if (e.profile) r.profile = e.profile
    if (e.set.length) r.set = Object.fromEntries(e.set.map((s) => [s.key, typed(s.key, s.value)]))
    return r
  })
}

const draft = ref<Entry[]>(fromRules(props.cfg.rules))
const savedJson = ref(JSON.stringify(toRules(draft.value)))
const draftRules = computed(() => toRules(draft.value))
const draftJson = computed(() => JSON.stringify(draftRules.value))
const dirty = computed(() => draftJson.value !== savedJson.value)

watch(
  () => props.cfg.rules,
  (rules) => {
    const wasDirty = dirty.value
    savedJson.value = JSON.stringify(toRules(fromRules(rules)))
    if (!wasDirty) draft.value = fromRules(rules)
  },
)

const issues = ref<ValidationIssue[]>([])
const checking = ref(false)
const saving = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let seq = 0

async function validate() {
  const mine = ++seq
  checking.value = true
  try {
    const res = await api.validateRules(draftRules.value)
    if (mine === seq) issues.value = res.issues ?? []
  } catch (e) {
    if (mine === seq) issues.value = [{ index: -1, field: '', column: 0, message: e instanceof Error ? e.message : String(e) }]
  } finally {
    if (mine === seq) checking.value = false
  }
}
watch(
  draftJson,
  () => {
    clearTimeout(timer)
    checking.value = true
    timer = setTimeout(validate, 300)
  },
  { immediate: true },
)
onUnmounted(() => clearTimeout(timer))

const general = computed(() => issues.value.filter((i) => i.index < 0 || i.index >= draft.value.length))
function forRule(i: number, field?: string) {
  return issues.value.filter((x) => x.index === i && (field === undefined || (field === 'match' ? x.field === 'match' || x.field === '' : x.field === field)))
}
const otherIssues = (i: number) => issues.value.filter((x) => x.index === i && x.field !== 'match' && x.field !== '' && x.field !== 'profile' && x.field !== 'action')
const hasIssues = computed(() => issues.value.length > 0)
function markOf(i: number): 'ok' | 'bad' | 'wait' {
  if (forRule(i).length || otherIssues(i).length) return 'bad'
  if (checking.value || !draft.value[i].match.trim()) return 'wait'
  return 'ok'
}
const canSave = computed(() => dirty.value && !hasIssues.value && !checking.value && !saving.value)

function add() {
  draft.value.push({ uid: uid++, match: '', action: '', profile: '', set: [] })
}
function remove(i: number) {
  draft.value.splice(i, 1)
}
function move(i: number, d: -1 | 1) {
  const j = i + d
  if (j < 0 || j >= draft.value.length) return
  const [e] = draft.value.splice(i, 1)
  draft.value.splice(j, 0, e)
}
function addSet(e: Entry) {
  const free = keyNames.find((k) => !e.set.some((s) => s.key === k)) ?? keyNames[0]
  e.set.push({ uid: uid++, key: free, value: defaultValue(free) })
}
function defaultValue(key: string) {
  return setKeys[key] === 'int' ? '80' : setKeys[key] === 'bool' ? 'true' : setKeys[key] === 'choice' ? 'images' : 'safe'
}
function changeKey(s: Entry['set'][number], ev: Event) {
  s.key = (ev.target as HTMLSelectElement).value
  s.value = defaultValue(s.key)
}
function discard() {
  draft.value = fromRules(props.cfg.rules)
  savedJson.value = JSON.stringify(toRules(draft.value))
}

async function save() {
  saving.value = true
  try {
    const rules = draftRules.value
    const cfg = await api.saveRules(rules)
    savedJson.value = JSON.stringify(toRules(fromRules(cfg.rules)))
    toast('Rules saved and applied')
    emit('update', cfg)
  } catch (e) {
    if (e instanceof ApiFailure && e.body.issues?.length) issues.value = e.body.issues
    else toast(e instanceof Error ? e.message : String(e), 'err', 6000)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="sec">
    <div class="sec-head">
      <h2>
        Rules
        <span v-if="dirty" class="tag warn dirty">unsaved changes</span>
        <span v-else-if="cfg.managed_by_ui.rules" class="tag dirty">set in the interface</span>
      </h2>
      <div class="row tight">
        <RulesSyntaxHelp />
        <button class="btn small" @click="add">Add rule</button>
      </div>
    </div>
    <p class="lede">
      Rules are tried from top to bottom. A request takes the action, profile and settings of every rule that matches it; later rules refine earlier ones.
    </p>

    <p v-if="!draft.length" class="empty">No rules, so every request uses the default profile. Choose Add rule to match some traffic, for example <code>~t image/</code>.</p>

    <div v-else class="rules-wrap">
      <div class="rules-head xs muted" aria-hidden="true">
        <span class="r">#</span><span /><span>Match expression</span><span>Action</span><span>Profile</span><span />
      </div>
      <ol class="rules">
        <li v-for="(e, i) in draft" :key="e.uid" class="rule" :class="{ invalid: markOf(i) === 'bad' }">
          <span class="n mono r">{{ i + 1 }}</span>
          <span class="mark" :class="markOf(i)" role="img" :aria-label="markOf(i) === 'ok' ? 'valid' : markOf(i) === 'bad' ? 'has a problem' : 'not checked'">
            <UiIcon v-if="markOf(i) === 'ok'" name="check" :size="14" />
            <UiIcon v-else-if="markOf(i) === 'bad'" name="cross" :size="14" />
            <span v-else class="bar" />
          </span>
          <input
            v-model="e.match"
            class="input mono expr"
            :class="{ bad: forRule(i, 'match').length }"
            spellcheck="false"
            autocomplete="off"
            :aria-label="`Match expression of rule ${i + 1}`"
            placeholder="~d example\.com &amp; ~t image/"
          />
          <select v-model="e.action" class="select" :aria-label="`Action of rule ${i + 1}`">
            <option value="">no action</option>
            <option value="passthrough">passthrough</option>
            <option value="passthrough_tls">passthrough_tls</option>
          </select>
          <select v-model="e.profile" class="select" :aria-label="`Profile of rule ${i + 1}`">
            <option value="">profile unchanged</option>
            <option v-for="p in cfg.profiles" :key="p.name" :value="p.name">{{ p.name }}</option>
            <option v-if="e.profile && !cfg.profiles.some((p) => p.name === e.profile)" :value="e.profile">{{ e.profile }}</option>
          </select>
          <div class="ctl">
            <button class="btn small ghost" :disabled="i === 0" :aria-label="`Move rule ${i + 1} up`" @click="move(i, -1)">Up</button>
            <button class="btn small ghost" :disabled="i === draft.length - 1" :aria-label="`Move rule ${i + 1} down`" @click="move(i, 1)">Down</button>
            <button class="btn small ghost bad" :aria-label="`Delete rule ${i + 1}`" @click="remove(i)">Delete</button>
          </div>

          <div class="extra">
            <div v-for="(is, k) in forRule(i, 'match')" :key="k" class="issue">
              <div v-if="is.column > 0" class="caret mono" :style="{ paddingLeft: `calc(0.55rem + 1px + ${is.column - 1}ch)` }" aria-hidden="true">^</div>
              <span class="error-text"><template v-if="is.column > 0">column {{ is.column }}: </template>{{ is.message }}</span>
            </div>
            <div v-for="(is, k) in [...forRule(i, 'profile'), ...forRule(i, 'action')]" :key="'pa' + k" class="error-text">{{ is.field }}: {{ is.message }}</div>

            <div v-for="s in e.set" :key="s.uid" class="setrow">
              <span class="muted xs setlabel mono">set</span>
              <select :value="s.key" class="select compact mono" :aria-label="`Setting key of rule ${i + 1}`" @change="changeKey(s, $event)">
                <option v-for="k in keyNames" :key="k" :value="k">{{ k }}</option>
              </select>
              <input v-if="setKeys[s.key] === 'int'" v-model="s.value" class="input mono val" type="number" min="1" max="100" step="1" aria-label="Quality 1 to 100" />
              <select v-else-if="setKeys[s.key] === 'choice'" v-model="s.value" class="select compact val" aria-label="no_transform">
                <option v-for="c in noTransformChoices" :key="c" :value="c">{{ c }}</option>
              </select>
              <select v-else-if="setKeys[s.key] === 'bool'" v-model="s.value" class="select compact val">
                <option value="true">true</option>
                <option value="false">false</option>
              </select>
              <select v-else v-model="s.value" class="select compact val">
                <option v-for="m in modes" :key="m" :value="m">{{ m }}</option>
              </select>
              <button class="btn small ghost" :aria-label="`Remove setting ${s.key}`" @click="e.set.splice(e.set.indexOf(s), 1)">Remove</button>
            </div>
            <div v-for="(is, k) in otherIssues(i)" :key="'o' + k" class="error-text">{{ is.field ? is.field + ': ' : '' }}{{ is.message }}</div>
            <div>
              <button class="btn small ghost" :disabled="e.set.length >= keyNames.length" @click="addSet(e)">Add setting</button>
            </div>
          </div>
        </li>
      </ol>
    </div>

    <p v-for="(g, k) in general" :key="k" class="notice err gen">{{ g.field ? g.field + ': ' : '' }}{{ g.message }}</p>

    <div class="bar-row">
      <span class="small" :class="hasIssues ? 'bad' : 'muted'" role="status">
        <template v-if="hasIssues">{{ issues.length }} {{ issues.length === 1 ? 'problem' : 'problems' }} to fix before saving</template>
        <template v-else-if="checking">Checking</template>
        <template v-else-if="dirty">Valid, not saved yet</template>
        <template v-else>Saved</template>
      </span>
      <span class="grow" />
      <button class="btn" :disabled="!dirty || saving" @click="discard">Discard changes</button>
      <button class="btn primary" :disabled="!canSave" @click="save">{{ saving ? 'Saving' : 'Save and apply' }}</button>
    </div>
  </section>
</template>

<style scoped>
.dirty { margin-left: 0.5rem; vertical-align: 1px; font-weight: 400; }
.rules-wrap { border-bottom: 1px solid var(--rule-strong); }
.rules-head,
.rule {
  display: grid;
  grid-template-columns: 2rem 1.1rem minmax(0, 1fr) 10.5rem 10.5rem 9.5rem;
  gap: 0.35rem 0.6rem;
  align-items: center;
}
.rules-head { padding: 0.3rem 0 0.3rem; border-bottom: 1px solid var(--rule-strong); }
.rules { list-style: none; margin: 0; padding: 0; counter-reset: rule; }
.rule { padding: 0.5rem 0; border-bottom: 1px solid var(--rule); }
.rule:last-child { border-bottom: 0; }
.rule.invalid { background: var(--vermilion-tint); }
.n { color: var(--ink-2); font-size: var(--fs-xs); }
.mark { display: grid; place-items: center; height: 14px; }
.mark.ok { color: var(--leaf); }
.mark.bad { color: var(--vermilion); }
.mark .bar { width: 8px; height: 1px; background: var(--ink-3); }
.expr { min-width: 0; }
.extra { grid-column: 3 / -1; display: flex; flex-direction: column; gap: 0.35rem; }
.extra:empty { display: none; }
.caret { color: var(--vermilion); line-height: 1; font-size: var(--fs-sm); font-weight: 500; }
.issue { display: flex; flex-direction: column; gap: 0.15rem; }
.setrow { display: flex; flex-wrap: wrap; align-items: center; gap: 0.4rem; }
.setlabel { width: 2rem; }
.val { width: 8rem; }
input.val { width: 6rem; }
.ctl { display: flex; gap: 0.1rem; }
.bar-row { display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem; margin-top: 1rem; }
.gen { margin-top: 0.5rem; }
@media (max-width: 960px) {
  .rules-head { display: none; }
  .rule { grid-template-columns: 1.6rem 1.1rem minmax(0, 1fr) minmax(0, 1fr); }
  .rule .expr { grid-column: 3 / -1; }
  .rule > .select { grid-column: auto; width: 100%; }
  .rule > .select:nth-of-type(1) { grid-column: 3; }
  .rule > .select:nth-of-type(2) { grid-column: 4; }
  .rule .ctl { grid-column: 3 / -1; }
  .rule .n { grid-row: 1; }
  .rule .mark { grid-row: 1; }
}
</style>
