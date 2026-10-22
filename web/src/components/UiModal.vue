<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import UiIcon from './UiIcon.vue'

defineProps<{ title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()

const box = ref<HTMLElement | null>(null)
const opener = document.activeElement as HTMLElement | null

function key(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => {
  window.addEventListener('keydown', key)
  box.value?.focus()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', key)
  opener?.focus?.()
})
</script>

<template>
  <Teleport to="body">
    <div class="overlay" @mousedown.self="emit('close')">
      <div ref="box" class="modal" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1">
        <div class="row between" style="margin-bottom: 0.9rem; flex-wrap: nowrap">
          <h2>{{ title }}</h2>
          <button class="btn ghost small" aria-label="Close" @click="emit('close')"><UiIcon name="x" /></button>
        </div>
        <slot />
        <div v-if="$slots.footer" class="row" style="justify-content: flex-end; margin-top: 1.1rem">
          <slot name="footer" />
        </div>
      </div>
    </div>
  </Teleport>
</template>
