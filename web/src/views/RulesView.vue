<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api/client'
import type { ConfigView } from '../api/types'
import { usePoll } from '../composables/poll'
import { toast, toastError } from '../composables/toast'
import UiModal from '../components/UiModal.vue'
import RulesProfiles from '../components/RulesProfiles.vue'
import RulesEditor from '../components/RulesEditor.vue'
import RulesTester from '../components/RulesTester.vue'
import PagesCopyButton from '../components/PagesCopyButton.vue'

const { data: cfg, error, reload } = usePoll(() => api.config())

const editorKey = ref(0)
const forgetting = ref(false)

function update(next: ConfigView) {
  cfg.value = next
}

async function forget() {
  try {
    cfg.value = await api.forgetUiSettings()
    editorKey.value++
    forgetting.value = false
    toast('Settings made in the interface were forgotten')
  } catch (e) {
    toastError(e)
  }
}

const managed = (c: ConfigView) => Object.entries(c.managed_by_ui).filter(([, on]) => on).map(([k]) => k.replace('_', ' '))
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>Policy</h1>
        <p>Rules and profiles decide how each request is handled. Everything saved here applies at once, without a restart.</p>
      </div>
    </div>

    <div v-if="error && !cfg" class="notice err row">
      <span>{{ error }}</span>
      <button class="btn small" @click="reload">Retry</button>
    </div>

    <div v-else-if="!cfg" class="skeleton" role="status" aria-label="Loading"><i /><i /><i /></div>

    <template v-if="cfg">
      <RulesProfiles :cfg="cfg" @update="update" />
      <RulesEditor :key="editorKey" :cfg="cfg" @update="update" />
      <RulesTester :cfg="cfg" />

      <section class="sec">
        <div class="sec-head"><h2>Where settings are kept</h2></div>
        <dl class="dl">
          <dt>State file</dt>
          <dd class="row tight">
            <code class="mono grow">{{ cfg.state_file }}</code>
            <PagesCopyButton :value="cfg.state_file" />
          </dd>
          <dt>Made in the interface</dt>
          <dd>
            <template v-if="managed(cfg).length">
              {{ managed(cfg).join(', ') }}. These take precedence over the configuration file, so editing the same setting in the file has no effect until it is forgotten.
            </template>
            <span v-else class="muted">Nothing. The configuration file decides everything.</span>
          </dd>
        </dl>
        <div v-if="managed(cfg).length" class="forget">
          <button class="btn small danger" @click="forgetting = true">Forget everything saved from the interface</button>
        </div>
      </section>
    </template>

    <UiModal v-if="forgetting" title="Forget interface settings" @close="forgetting = false">
      <p>This deletes the state file and goes back to the configuration file: the default profile, the profile changes and the rules made here are removed and the proxy uses the file's values from now on. Unsaved edits of the rules are lost too.</p>
      <template #footer>
        <button class="btn" @click="forgetting = false">Cancel</button>
        <button class="btn danger" @click="forget">Forget settings</button>
      </template>
    </UiModal>
  </div>
</template>

<style scoped>
.forget { margin-top: 0.7rem; }
</style>
