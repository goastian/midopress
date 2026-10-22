<script setup lang="ts">
// A list of labelled bars, each as long as its share of the largest.
import { computed } from 'vue'

export interface BarItem {
  key: string
  label: string
  value: number
  text: string
  sub?: string
}

const props = defineProps<{ items: BarItem[]; empty?: string }>()
const max = computed(() => Math.max(1, ...props.items.map((i) => i.value)))
</script>

<template>
  <div v-if="items.length" class="bars">
    <div v-for="i in items" :key="i.key" class="bar">
      <div class="row between" style="gap: 0.5rem; flex-wrap: nowrap">
        <span class="name">{{ i.label }}</span>
        <span class="num muted small nowrap">{{ i.text }}</span>
      </div>
      <div class="meter"><i :style="{ width: (i.value / max) * 100 + '%' }" /></div>
      <div v-if="i.sub" class="muted small">{{ i.sub }}</div>
    </div>
  </div>
  <div v-else class="empty small">{{ empty ?? 'Nothing yet' }}</div>
</template>

<style scoped>
.bars {
  display: flex;
  flex-direction: column;
  gap: 0.7rem;
}
.bar {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
