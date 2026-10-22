<script setup lang="ts">
import { computed } from 'vue'
import { useTween } from '../composables/tween'

const props = defineProps<{ value: number; format: (n: number) => string }>()
const shown = useTween(() => props.value)
const text = computed(() => props.format(shown.value))
</script>

<template>
  <span class="num">{{ text }}</span>
</template>
