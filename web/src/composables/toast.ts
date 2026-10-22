import { reactive } from 'vue'

export interface Toast {
  id: number
  kind: 'ok' | 'err' | 'info'
  text: string
}

export const toasts = reactive<Toast[]>([])
let next = 1

export function toast(text: string, kind: Toast['kind'] = 'ok', ms = 4000) {
  const t = { id: next++, kind, text }
  toasts.push(t)
  setTimeout(() => {
    const i = toasts.findIndex((x) => x.id === t.id)
    if (i >= 0) toasts.splice(i, 1)
  }, ms)
}

export function toastError(e: unknown) {
  toast(e instanceof Error ? e.message : String(e), 'err', 6000)
}
