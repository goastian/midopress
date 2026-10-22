<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import UiIcon from './UiIcon.vue'

defineProps<{ label: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()

const panel = ref<HTMLElement | null>(null)
const opener = document.activeElement as HTMLElement | null

function key(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => {
  window.addEventListener('keydown', key)
  panel.value?.focus()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', key)
  opener?.focus?.()
})
</script>

<template>
  <Teleport to="body">
    <div class="back" @mousedown.self="emit('close')">
      <aside ref="panel" class="drawer" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="label" tabindex="-1">
        <header>
          <div class="grow"><slot name="head" /></div>
          <button class="btn ghost small" aria-label="Close" @click="emit('close')"><UiIcon name="x" :size="18" /></button>
        </header>
        <div class="body"><slot /></div>
        <footer v-if="$slots.footer"><slot name="footer" /></footer>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.back {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: var(--scrim);
  display: flex;
  justify-content: flex-end;
}
.drawer {
  width: min(640px, 100%);
  height: 100%;
  background: var(--surface);
  border-left: 1px solid var(--rule-strong);
  box-shadow: var(--shadow-pop);
  display: flex;
  flex-direction: column;
  outline: none;
}
.drawer.wide {
  width: min(820px, 100%);
}
@media (prefers-reduced-motion: no-preference) {
  .drawer {
    animation: slide 0.16s ease-out;
  }
  @keyframes slide {
    from {
      transform: translateX(24px);
      opacity: 0;
    }
  }
}
header {
  display: flex;
  gap: 0.5rem;
  align-items: flex-start;
  padding: 0.85rem 1rem;
  border-bottom: 1px solid var(--rule);
}
.body {
  overflow-y: auto;
  padding: 1rem 1rem 1.5rem;
  flex: 1 1 auto;
  min-height: 0;
}
footer {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  padding: 0.75rem 1rem;
  border-top: 1px solid var(--rule);
}
</style>
