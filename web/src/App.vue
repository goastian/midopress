<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { getToken, onAuthChange, setToken } from './api/client'
import { connected, halt, start, stats } from './composables/live'
import { bytes, duration } from './composables/format'
import { cycleTheme, setTheme, theme, type Theme } from './composables/theme'
import { routes } from './router'
import BrandMark from './components/BrandMark.vue'
import UiNum from './components/UiNum.vue'
import ToastHost from './components/ToastHost.vue'

const route = useRoute()
const router = useRouter()
const nav = routes.filter((r) => r.meta && 'nav' in r.meta)
const showShell = computed(() => !route.meta.public)
const themes: { id: Theme; label: string }[] = [
  { id: 'system', label: 'System' },
  { id: 'light', label: 'Light' },
  { id: 'dark', label: 'Dark' },
]

const version = computed(() => {
  const v = stats.value?.version ?? ''
  const short = v.replace(/^v/, '').split(/[+-]/)[0]
  return 'v' + short
})

const reqPerSec = computed(() => {
  const s = stats.value?.series.seconds ?? []
  const done = s.slice(-6, -1)
  if (!done.length) return 0
  return done.reduce((n, p) => n + p.requests, 0) / done.length
})

onMounted(() => {
  if (getToken()) start()
})
onAuthChange(() => {
  if (!getToken() && !route.meta.public) router.replace({ name: 'login' })
})
watch(
  () => route.meta.title,
  (t) => (document.title = t ? `${t} - MidoPress` : 'MidoPress'),
  { immediate: true },
)

function logout() {
  halt()
  setToken('')
  router.replace({ name: 'login' })
}
</script>

<template>
  <RouterView v-if="!showShell" />
  <div v-else class="shell">
    <header class="top">
      <RouterLink to="/" class="brand"><BrandMark /><span>MidoPress</span></RouterLink>
      <nav class="tabs" aria-label="Main">
        <RouterLink v-for="r in nav" :key="r.name" :to="r.path" class="tab">{{ r.meta?.title }}</RouterLink>
      </nav>
      <div class="tools">
        <span class="seg themes" role="group" aria-label="Theme">
          <button v-for="t in themes" :key="t.id" type="button" :aria-pressed="theme === t.id" @click="setTheme(t.id)">{{ t.label }}</button>
        </span>
        <button type="button" class="btn small theme-cycle" :aria-label="`Theme: ${theme}. Change theme`" @click="cycleTheme">{{ themes.find((t) => t.id === theme)?.label }}</button>
        <button type="button" class="btn small ghost" @click="logout">Sign out</button>
      </div>
    </header>

    <main class="main"><RouterView /></main>

    <footer class="status mono" aria-label="Status">
      <span class="item"><span class="dot" :class="connected ? 'on' : 'off'" /> {{ connected ? 'live' : 'reconnecting' }}</span>
      <template v-if="stats">
        <span class="item"><b>{{ stats.connections.clients_active }}</b> {{ stats.connections.clients_active === 1 ? 'client' : 'clients' }}</span>
        <span class="item"><b>{{ stats.connections.requests_in_flight }}</b> in flight</span>
        <span class="item hide-sm"><b>{{ reqPerSec.toFixed(1) }}</b> req/s</span>
        <span class="item saved">saved <b><UiNum :value="stats.totals.saved" :format="bytes" /></b></span>
        <span class="spacer" />
        <span class="item hide-sm">up {{ duration(stats.uptime_seconds) }}</span>
        <span class="item hide-xs">{{ version }}</span>
      </template>
    </footer>
  </div>
  <ToastHost />
</template>

<style scoped>
.shell {
  min-height: 100dvh;
}
.top {
  position: sticky;
  top: 0;
  z-index: 20;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: stretch;
  gap: 0 1.75rem;
  min-height: var(--topbar-h);
  padding: 0 1.5rem;
  background: var(--paper);
  border-bottom: 1px solid var(--rule);
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--ink);
  text-decoration: none;
}
.tabs {
  display: flex;
  align-items: stretch;
  gap: 1.35rem;
  overflow-x: auto;
  scrollbar-width: none;
}
.tabs::-webkit-scrollbar {
  display: none;
}
.tab {
  display: flex;
  align-items: center;
  color: var(--ink-2);
  text-decoration: none;
  font-size: var(--fs-md);
  white-space: nowrap;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}
.tab:hover {
  color: var(--ink);
}
.tab.router-link-exact-active {
  color: var(--ink);
  font-weight: 500;
  border-bottom-color: var(--ink);
}
.tab:focus-visible {
  outline-offset: -2px;
}
.tools {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.theme-cycle {
  display: none;
}
.main {
  min-width: 0;
}
.status {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 30;
  height: var(--statusbar-h);
  display: flex;
  align-items: center;
  gap: 1.1rem;
  padding: 0 1.5rem;
  background: var(--paper);
  border-top: 1px solid var(--rule);
  font-size: var(--fs-xs);
  color: var(--ink-2);
  overflow: hidden;
  white-space: nowrap;
}
.status b {
  font-weight: 500;
  color: var(--ink);
}
.status .saved b {
  color: var(--leaf);
}
.status .item {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}
.status .spacer {
  flex: 1 1 auto;
}

@media (max-width: 720px) {
  .top {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas: 'brand tools' 'tabs tabs';
    padding: 0 1rem;
    gap: 0;
  }
  .brand {
    grid-area: brand;
    min-height: var(--topbar-h);
  }
  .tools {
    grid-area: tools;
    gap: 0.25rem;
  }
  .tabs {
    grid-area: tabs;
    gap: 1.25rem;
    margin: 0 -1rem;
    padding: 0 1rem;
    border-top: 1px solid var(--rule);
  }
  .tab {
    min-height: 40px;
  }
  .themes {
    display: none;
  }
  .theme-cycle {
    display: inline-flex;
  }
  .status {
    padding: 0 1rem;
    gap: 0.9rem;
  }
  .hide-sm {
    display: none !important;
  }
}
@media (max-width: 420px) {
  .hide-xs {
    display: none !important;
  }
}
</style>
