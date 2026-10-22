<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'

const open = ref(false)
const root = ref<HTMLElement | null>(null)

const atoms: [string, string, string][] = [
  ['~d', 'host name, regex', '~d example\\.com$'],
  ['~u', 'full URL, regex', '~u \\.(mp4|webm)$'],
  ['~t', 'response content type, regex', '~t image/'],
  ['~c', 'response status, 404 or a class like 4xx', '~c 4xx'],
  ['~m', 'method, regex', '~m POST'],
  ['~h', 'any header, regex on "Name: value"', '~h "User-Agent: curl"'],
  ['~hq', 'request header', '~hq X-MidoPress-Bypass'],
  ['~hs', 'response header', '~hs "Content-Encoding: br"'],
  ['~b', 'body of request or response (first 64 KB)', '~b <svg'],
  ['~q', 'only the request phase, no argument', '~q'],
  ['~s', 'only once there is a response, no argument', '~s'],
  ['~src', 'client IP address or CIDR', '~src 192.168.1.0/24'],
  ['~dst', 'server IP address or CIDR', '~dst 10.0.0.0/8'],
  ['~all', 'always matches', '~all'],
  ['~none', 'never matches', '~none'],
]

function away(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
}
function key(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}
onMounted(() => {
  document.addEventListener('mousedown', away)
  document.addEventListener('keydown', key)
})
onUnmounted(() => {
  document.removeEventListener('mousedown', away)
  document.removeEventListener('keydown', key)
})
</script>

<template>
  <span ref="root" class="help">
    <button type="button" class="btn small" :aria-expanded="open" @click="open = !open">Syntax</button>
    <div v-if="open" class="pop" role="dialog" aria-label="Rule syntax">
      <p class="small">
        A rule matches when its expression is true. Combine atoms with <code>&amp;</code> (and), <code>|</code> (or), <code>!</code> (not) and parentheses. Arguments that contain spaces go in double quotes. Regular expressions are case sensitive except for host, content type and method. Rules run in order.
      </p>
      <div class="table-wrap">
        <table class="tbl">
          <tbody>
            <tr v-for="a in atoms" :key="a[0]">
              <td class="mono nowrap">{{ a[0] }}</td>
              <td>{{ a[1] }}</td>
              <td class="mono">{{ a[2] }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="small muted">Example: <code>~d cdn\.example\.com &amp; ~t image/ &amp; !~c 404</code></p>
    </div>
  </span>
</template>

<style scoped src="./PagesOps.css"></style>
<style scoped>
.help { position: relative; display: inline-block; }
.pop { position: absolute; right: 0; top: calc(100% + 4px); z-index: 20; width: min(560px, calc(100vw - 2rem)); max-height: 70vh; overflow: auto; background: var(--surface); border: 1px solid var(--border); border-radius: 3px; padding: 0.7rem 0.8rem; display: flex; flex-direction: column; gap: 0.5rem; }
@media (max-width: 720px) { .pop { position: fixed; left: 1rem; right: 1rem; top: 20vh; width: auto; } }
</style>
