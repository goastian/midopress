<script setup lang="ts">
export interface DashRow {
  key: string
  label: string
  cells: string[]
  /** 0 to 1, the length of the bar of the last column */
  bar: number
  barText: string
  green?: boolean
}

defineProps<{ head: string[]; rows: DashRow[]; empty?: string }>()
</script>

<template>
  <div v-if="rows.length" class="wrap">
    <table>
      <thead>
        <tr>
          <th v-for="(h, i) in head" :key="h" :class="{ r: i > 0 }">{{ h }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in rows" :key="r.key">
          <td class="mono name" :title="r.label">{{ r.label }}</td>
          <td v-for="(c, i) in r.cells" :key="i" class="r num mono">{{ c }}</td>
          <td class="barcell">
            <span class="num mono" :class="{ ok: r.green !== false }">{{ r.barText }}</span>
            <span class="track"><i :style="{ width: Math.max(0, Math.min(1, r.bar)) * 100 + '%' }" :class="{ plain: r.green === false }" /></span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
  <div v-else class="none small muted">{{ empty ?? 'Nothing yet' }}</div>
</template>

<style scoped>
.wrap {
  overflow-x: auto;
}
table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}
th {
  text-align: left;
  font-weight: 400;
  color: var(--muted);
  padding: 0.2rem 0.5rem 0.3rem 0;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
td {
  padding: 0.25rem 0.5rem 0.25rem 0;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
tr:last-child td {
  border-bottom: 0;
}
.r {
  text-align: right;
}
.name {
  max-width: 22ch;
  overflow: hidden;
  text-overflow: ellipsis;
}
.barcell {
  min-width: 96px;
  padding-right: 0;
}
.barcell .num {
  display: block;
  text-align: right;
}
.track {
  display: block;
  height: 3px;
  background: var(--surface-2);
  margin-top: 2px;
}
.track i {
  display: block;
  height: 100%;
  background: var(--accent);
}
.track i.plain {
  background: var(--muted);
}
.none {
  padding: 0.4rem 0;
}
</style>
