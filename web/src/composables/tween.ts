import { onScopeDispose, ref, watch, type Ref } from 'vue'

const reduced = window.matchMedia('(prefers-reduced-motion: reduce)')

/** Follows `source` with a short ease so that a number ticks instead of jumping. */
export function useTween(source: () => number, duration = 300): Ref<number> {
  const shown = ref(source())
  let raf = 0
  watch(source, (to) => {
    cancelAnimationFrame(raf)
    if (reduced.matches || !isFinite(to)) {
      shown.value = to
      return
    }
    const from = shown.value
    const start = performance.now()
    const step = (now: number) => {
      const p = Math.min(1, (now - start) / duration)
      const eased = 1 - (1 - p) ** 3
      shown.value = from + (to - from) * eased
      if (p < 1) raf = requestAnimationFrame(step)
    }
    raf = requestAnimationFrame(step)
  })
  onScopeDispose(() => cancelAnimationFrame(raf))
  return shown
}
