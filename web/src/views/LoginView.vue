<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiFailure, setToken } from '../api/client'
import { start } from '../composables/live'

const route = useRoute()
const router = useRouter()
const token = ref('')
const error = ref('')
const busy = ref(false)

async function enter(value: string) {
  if (!value.trim()) return
  busy.value = true
  error.value = ''
  setToken(value)
  try {
    await api.about()
    start()
    await router.replace(typeof route.query.next === 'string' ? route.query.next : '/')
  } catch (e) {
    setToken('')
    error.value =
      e instanceof ApiFailure && e.status === 401
        ? 'That is not the admin token.'
        : 'The admin server does not answer. Is MidoPress running?'
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  // a link such as  http://127.0.0.1:9998/#/login?token=...  signs in at once
  if (typeof route.query.token === 'string') void enter(route.query.token)
})
</script>

<template>
  <div class="login">
    <form class="card" @submit.prevent="enter(token)">
      <div class="row">
        <img src="/favicon.svg" alt="" width="44" height="44" />
        <div>
          <h1>MidoPress</h1>
          <p class="muted small">Administration</p>
        </div>
      </div>
      <div class="field">
        <label for="token">Admin token</label>
        <input id="token" v-model="token" class="input mono" type="password" autocomplete="current-password" autofocus placeholder="paste the token" />
      </div>
      <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      <button class="btn primary" :disabled="busy || !token.trim()">{{ busy ? 'Checking...' : 'Sign in' }}</button>
      <p class="muted small">
        The token is written when MidoPress starts for the first time. Show it again with
        <code>midopress admin token</code>.
      </p>
    </form>
  </div>
</template>

<style scoped>
.login {
  min-height: 100dvh;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: var(--bg);
}
form {
  width: min(400px, 100%);
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding: 1.4rem;
}
</style>
