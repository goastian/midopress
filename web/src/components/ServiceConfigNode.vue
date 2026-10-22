<script setup lang="ts">
defineOptions({ name: 'ServiceConfigNode' })
defineProps<{ name: string; value: unknown; depth?: number }>()

function isBranch(v: unknown): v is Record<string, unknown> | unknown[] {
  return v !== null && typeof v === 'object'
}
function entries(v: Record<string, unknown> | unknown[]): [string, unknown][] {
  return Array.isArray(v) ? v.map((x, i) => [String(i), x]) : Object.entries(v)
}
function scalar(v: unknown): string {
  if (v === null || v === undefined) return 'null'
  return typeof v === 'string' ? JSON.stringify(v) : String(v)
}
</script>

<template>
  <details v-if="isBranch(value) && entries(value).length" class="node" :open="(depth ?? 0) < 1">
    <summary>
      <span class="k">{{ name }}</span>
      <span class="muted"> {{ Array.isArray(value) ? `[${value.length}]` : '' }}</span>
    </summary>
    <div class="kids">
      <ServiceConfigNode v-for="[k, v] in entries(value)" :key="k" :name="k" :value="v" :depth="(depth ?? 0) + 1" />
    </div>
  </details>
  <div v-else class="leaf">
    <span class="k">{{ name }}</span><span class="muted">: </span>
    <span class="v" :class="{ red: value === '<redacted>' }">{{ isBranch(value) ? (Array.isArray(value) ? '[]' : '{}') : scalar(value) }}</span>
  </div>
</template>

<style scoped>
.node > summary {
  cursor: pointer;
  list-style: none;
  padding: 0.05rem 0;
}
.node > summary::before {
  content: '+';
  display: inline-block;
  width: 1rem;
  color: var(--ink-2);
}
.node[open] > summary::before {
  content: '-';
}
.node > summary::-webkit-details-marker {
  display: none;
}
.kids {
  margin-left: 0.5rem;
  padding-left: 0.6rem;
  border-left: 1px solid var(--rule);
}
.leaf {
  padding: 0.05rem 0 0.05rem 1rem;
  word-break: break-all;
}
.k {
  color: var(--ink);
}
.v {
  color: var(--ink);
}
.v.red {
  color: var(--ink-2);
}
</style>
