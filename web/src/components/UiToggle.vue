<script setup lang="ts">
import { useId } from 'vue'

defineProps<{ label?: string; disabled?: boolean }>()
const model = defineModel<boolean>({ default: false })
const id = useId()
</script>

<template>
  <div class="onoff" role="group" :aria-labelledby="label ? id : undefined">
    <span v-if="label || $slots.default" :id="id" class="name"><slot>{{ label }}</slot></span>
    <span class="seg">
      <button type="button" :aria-pressed="model" :disabled="disabled" @click="model = true">On</button>
      <button type="button" :aria-pressed="!model" :disabled="disabled" @click="model = false">Off</button>
    </span>
  </div>
</template>

<style scoped>
.onoff {
  display: inline-flex;
  align-items: center;
  gap: 0.6rem;
}
.name {
  font-size: var(--fs-sm);
}
</style>
