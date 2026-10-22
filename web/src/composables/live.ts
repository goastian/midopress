import { onScopeDispose, ref, shallowRef } from 'vue'
import { api, getToken, onAuthChange, subscribe } from '../api/client'
import type { ServerEvent, Settings, StatsSnapshot } from '../api/types'

type Listener<T extends ServerEvent['event']> = (data: Extract<ServerEvent, { event: T }>['data']) => void

// One stream for the whole interface. The pages read the latest numbers from
// here and register for the events they care about.
export const stats = shallowRef<StatsSnapshot | null>(null)
export const settings = ref<Settings>({ flows: false, domains: false, capture: false })
export const connected = ref(false)

const listeners = new Map<string, Set<(d: never) => void>>()
let stop: (() => void) | null = null

function dispatch(e: ServerEvent) {
  if (e.event === 'stats') stats.value = e.data
  if (e.event === 'settings') settings.value = e.data
  listeners.get(e.event)?.forEach((fn) => fn(e.data as never))
}

export function start() {
  if (stop || !getToken()) return
  stop = subscribe(dispatch, (ok) => (connected.value = ok))
  api.stats().then((s) => (stats.value = s)).catch(() => {})
  api.settings().then((s) => (settings.value = s)).catch(() => {})
}

export function halt() {
  stop?.()
  stop = null
  connected.value = false
  stats.value = null
}

onAuthChange(() => (getToken() ? start() : halt()))

/** Calls fn for each event of that kind until the component goes away. */
export function on<T extends ServerEvent['event']>(event: T, fn: Listener<T>) {
  let set = listeners.get(event)
  if (!set) listeners.set(event, (set = new Set()))
  set.add(fn as never)
  onScopeDispose(() => set.delete(fn as never))
}
