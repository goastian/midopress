<script setup lang="ts">
defineProps<{ label: string; value: string; sub?: string; tone?: 'green' | 'red' | 'amber' | '' }>()
</script>

<template>
  <div class="card stat">
    <div class="label">{{ label }}</div>
    <div class="value num" :class="tone">{{ value }}</div>
    <div v-if="sub" class="sub muted small">{{ sub }}</div>
    <slot />
  </div>
</template>

<style scoped>
.stat {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}
.label {
  font-size: 0.8rem;
  font-weight: 500;
  color: var(--muted);
}
.value {
  font-size: 1.6rem;
  font-weight: 600;
  letter-spacing: -0.02em;
}
.value.green {
  color: var(--accent-strong);
}
.value.red {
  color: var(--danger);
}
.value.amber {
  color: var(--warn);
}
</style>
