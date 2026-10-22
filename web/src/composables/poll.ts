import { onMounted, onScopeDispose, ref, type Ref } from 'vue'

/**
 * Loads something now and then. `load` runs at once and every `every`
 * milliseconds while the page is visible; call the returned `reload` after a
 * change.
 */
export function usePoll<T>(load: () => Promise<T>, every = 0) {
  const data = ref<T | null>(null) as Ref<T | null>
  const error = ref('')
  const loading = ref(true)
  let timer: ReturnType<typeof setInterval> | undefined

  async function reload() {
    try {
      data.value = await load()
      error.value = ''
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    void reload()
    if (every > 0) timer = setInterval(() => !document.hidden && void reload(), every)
  })
  onScopeDispose(() => clearInterval(timer))
  return { data, error, loading, reload }
}
