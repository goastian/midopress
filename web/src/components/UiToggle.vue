<script setup lang="ts">
defineProps<{ label?: string; disabled?: boolean }>()
const model = defineModel<boolean>({ default: false })
</script>

<template>
  <label class="toggle">
    <input v-model="model" type="checkbox" :disabled="disabled" />
    <span class="track" />
    <span v-if="label || $slots.default"><slot>{{ label }}</slot></span>
  </label>
</template>
