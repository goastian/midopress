<script setup lang="ts">
defineProps<{ name: 'x' | 'check' | 'cross' | 'chevron'; size?: number }>()

const paths = {
  x: 'M5 5l10 10M15 5 5 15',
  check: 'M4 10.5l4 4 8-9',
  cross: 'M6 6l8 8M14 6l-8 8',
  chevron: 'M7 4l6 6-6 6',
}
</script>

<template>
  <svg :width="size ?? 16" :height="size ?? 16" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="square" aria-hidden="true" style="flex: none">
    <path :d="paths[name]" />
  </svg>
</template>
