<script setup lang="ts">
import { ref } from 'vue'
import { toast, toastError } from '../composables/toast'

const props = defineProps<{ value: string; label?: string }>()
const emit = defineEmits<{ copied: [] }>()
const done = ref(false)

async function copy() {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(props.value)
    } else {
      const ta = document.createElement('textarea')
      ta.value = props.value
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      ta.remove()
      if (!ok) throw new Error('Copy is not available here, select the text and copy it by hand')
    }
    done.value = true
    emit('copied')
    toast('Copied', 'ok', 1500)
    setTimeout(() => (done.value = false), 1500)
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <button type="button" class="btn small" @click="copy">{{ done ? 'Copied' : (label ?? 'Copy') }}</button>
</template>

<style scoped src="./PagesOps.css"></style>
