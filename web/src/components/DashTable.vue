<script setup lang="ts">
export interface DashRow {
  key: string
  label: string
  cells: string[]
  /** 0 to 1, the length of the micro-bar of the last column */
  bar: number
  barText: string
  /** false draws the bar in ink instead of leaf: it is not a saving */
  green?: boolean
}

defineProps<{ head: string[]; rows: DashRow[]; empty?: string; hideCols?: number[] }>()
</script>

<template>
  <div v-if="rows.length" class="ledger-wrap">
    <table class="ledger">
      <thead>
        <tr>
          <th v-for="(h, i) in head" :key="h" :class="[{ r: i > 0 }, hideCols?.includes(i) ? 'hide-xs' : '']" scope="col">{{ h }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in rows" :key="r.key">
          <td class="mono name" :title="r.label">{{ r.label }}</td>
          <td v-for="(c, i) in r.cells" :key="i" class="r mono" :class="{ 'hide-xs': hideCols?.includes(i + 1) }">{{ c }}</td>
          <td class="r pct">
            <span class="mono" :class="{ ok: r.green !== false }">{{ r.barText }}</span>
            <span class="mbar" :class="{ plain: r.green === false }"><i :style="{ width: Math.max(0, Math.min(1, r.bar)) * 100 + '%' }" /></span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-else class="empty small">{{ empty ?? 'Nothing yet.' }}</p>
</template>

<style scoped>
.name {
  max-width: 22ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
td {
  white-space: nowrap;
}
.pct {
  width: 1%;
}
.pct .mono {
  display: inline-block;
  min-width: 4.5ch;
  margin-right: 0.5rem;
}
@media (max-width: 480px) {
  .hide-xs {
    display: none;
  }
  .mbar {
    width: 36px;
  }
}
</style>
