<script setup lang="ts">
import { toasts } from '../composables/toast'
</script>

<template>
  <div class="toasts" aria-live="polite">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind">{{ t.text }}</div>
  </div>
</template>

<style scoped>
.toasts {
  position: fixed;
  right: 1rem;
  bottom: calc(1rem + env(safe-area-inset-bottom));
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  z-index: 100;
  max-width: min(420px, calc(100vw - 2rem));
}
.toast {
  background: var(--surface);
  border: 1px solid var(--border);
  border-left: 4px solid var(--accent);
  border-radius: var(--radius-sm);
  padding: 0.65rem 0.9rem;
  box-shadow: var(--shadow);
  font-size: 0.9rem;
}
.toast.err {
  border-left-color: var(--danger);
}
.toast.info {
  border-left-color: var(--info);
}
@media (max-width: 860px) {
  .toasts {
    bottom: calc(var(--bottom-nav-h) + 0.75rem + env(safe-area-inset-bottom));
    right: 0.75rem;
    left: 0.75rem;
    max-width: none;
  }
}
</style>
