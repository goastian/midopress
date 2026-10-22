<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import UiIcon from './UiIcon.vue'

defineProps<{ title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()

function key(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => window.addEventListener('keydown', key))
onUnmounted(() => window.removeEventListener('keydown', key))
</script>

<template>
  <Teleport to="body">
    <div class="overlay" @mousedown.self="emit('close')">
      <div class="modal" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="title">
        <div class="row between" style="margin-bottom: 0.9rem">
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
