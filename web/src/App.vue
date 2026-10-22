<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { getToken, onAuthChange, setToken } from './api/client'
import { connected, halt, start, stats } from './composables/live'
import { duration } from './composables/format'
import { routes } from './router'
import ToastHost from './components/ToastHost.vue'
import UiIcon from './components/UiIcon.vue'

const route = useRoute()
const router = useRouter()
const nav = routes.filter((r) => r.meta && 'icon' in r.meta)
const showShell = computed(() => !route.meta.public)

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
    <aside class="side">
      <RouterLink to="/" class="brand">
        <img src="/favicon.svg" alt="" width="34" height="34" />
        <span>MidoPress</span>
      </RouterLink>
      <nav class="nav" aria-label="Main">
        <RouterLink v-for="r in nav" :key="r.name" :to="r.path">
          <UiIcon :name="String(r.meta?.icon)" />
          <span>{{ r.meta?.title }}</span>
        </RouterLink>
      </nav>
      <div class="side-foot">
        <div class="row tight small muted">
          <span class="dot" :class="connected ? 'on' : 'off'" />
          <span>{{ connected ? 'Live' : 'Reconnecting' }}</span>
        </div>
        <div v-if="stats" class="small muted">v{{ stats.version }} · up {{ duration(stats.uptime_seconds) }}</div>
        <button class="btn ghost small" style="justify-content: flex-start; padding-left: 0" @click="logout">Sign out</button>
      </div>
    </aside>

    <header class="top">
      <RouterLink to="/" class="brand"><img src="/favicon.svg" alt="" width="28" height="28" /><span>MidoPress</span></RouterLink>
      <div class="row tight small muted">
        <span class="dot" :class="connected ? 'on' : 'off'" />
        <span>{{ connected ? 'Live' : 'Reconnecting' }}</span>
        <button class="btn ghost small" @click="logout">Sign out</button>
      </div>
    </header>

    <main class="main"><RouterView /></main>

    <nav class="bottom" aria-label="Main">
      <RouterLink v-for="r in nav" :key="r.name" :to="r.path">
        <UiIcon :name="String(r.meta?.icon)" :size="22" />
        <span>{{ String(r.meta?.title).split(' ')[0] }}</span>
      </RouterLink>
    </nav>
  </div>
  <ToastHost />
</template>

<style scoped>
.shell {
  min-height: 100dvh;
  display: grid;
  grid-template-columns: var(--nav-w) minmax(0, 1fr);
  grid-template-rows: auto 1fr;
  grid-template-areas: 'side main';
}
.side {
  grid-area: side;
  position: sticky;
  top: 0;
  height: 100dvh;
  background: var(--green-900);
  color: #e8f6ee;
  display: flex;
  flex-direction: column;
  padding: 1rem 0.8rem;
  gap: 1.2rem;
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  font-weight: 800;
  font-size: 1.15rem;
  letter-spacing: -0.02em;
  color: inherit;
  text-decoration: none;
  padding: 0 0.4rem;
}
.nav {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}
.nav a {
  display: flex;
  align-items: center;
  gap: 0.7rem;
  padding: 0.6rem 0.75rem;
  border-radius: var(--radius-sm);
  color: #bfdccb;
  text-decoration: none;
  font-weight: 600;
}
.nav a:hover {
  background: rgb(255 255 255 / 8%);
  color: #fff;
}
.nav a.router-link-exact-active,
.nav a.router-link-active:not([href='#/']) {
  background: var(--green-600);
  color: #fff;
}
.side-foot {
  margin-top: auto;
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  padding: 0 0.4rem;
}
.side-foot .muted,
.side-foot .btn {
  color: #9bbfab;
}
.top,
.bottom {
  display: none;
}
.main {
  grid-area: main;
  padding: 1.5rem 1.6rem 2.5rem;
  min-width: 0;
}

@media (max-width: 860px) {
  .shell {
    display: block;
  }
  .side {
    display: none;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    position: sticky;
    top: 0;
    z-index: 20;
    height: var(--topbar-h);
    padding: 0 0.9rem;
    padding-top: env(safe-area-inset-top);
    background: var(--green-900);
    color: #e8f6ee;
  }
  .top .brand {
    font-size: 1.05rem;
  }
  .top .btn {
    color: #bfdccb;
  }
  .main {
    padding: 1rem 0.9rem calc(var(--bottom-nav-h) + 1.5rem + env(safe-area-inset-bottom));
  }
  .bottom {
    display: flex;
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 20;
    height: calc(var(--bottom-nav-h) + env(safe-area-inset-bottom));
    padding-bottom: env(safe-area-inset-bottom);
    background: var(--surface);
    border-top: 1px solid var(--border);
    overflow-x: auto;
  }
  .bottom a {
    flex: 1 0 56px;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.15rem;
    font-size: 0.66rem;
    font-weight: 600;
    color: var(--muted);
    text-decoration: none;
  }
  .bottom a.router-link-exact-active,
  .bottom a.router-link-active:not([href='#/']) {
    color: var(--accent-strong);
  }
}
</style>
