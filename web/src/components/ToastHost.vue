<script setup lang="ts">
import { toasts } from '../composables/toast'
</script>

<template>
  <div class="toasts" aria-live="polite" role="status">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind">{{ t.text }}</div>
  </div>
</template>

<style scoped>
.toasts {
  position: fixed;
  right: 1rem;
  bottom: calc(var(--statusbar-h) + 0.75rem);
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  z-index: 100;
  max-width: min(420px, calc(100vw - 2rem));
}
.toast {
  background: var(--ink);
  color: var(--inverse);
  border-radius: var(--radius);
  padding: 0.5rem 0.8rem;
  font-size: var(--fs-sm);
}
.toast.err {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--rule-strong);
  border-left: 3px solid var(--vermilion);
}
@media (prefers-reduced-motion: no-preference) {
  .toast {
    animation: in 0.18s ease-out;
  }
  @keyframes in {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
  }
}
@media (max-width: 720px) {
  .toasts {
    left: 1rem;
    max-width: none;
  }
}
</style>
