<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiFailure, setToken } from '../api/client'
import { start } from '../composables/live'
import BrandMark from '../components/BrandMark.vue'

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
    <form @submit.prevent="enter(token)">
      <div class="brand"><BrandMark /><span>MidoPress</span></div>
      <h1>Sign in to the admin panel</h1>
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
}
form {
  width: min(380px, 100%);
  display: flex;
  flex-direction: column;
  gap: 1rem;
  border-top: 2px solid var(--ink);
  padding-top: 1.25rem;
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  font-weight: 600;
}
h1 {
  font-size: var(--fs-xl);
  line-height: 1.2;
}
</style>
