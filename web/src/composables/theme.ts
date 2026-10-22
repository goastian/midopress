import { ref } from 'vue'

export type Theme = 'system' | 'light' | 'dark'

const KEY = 'midopress.theme'
const saved = localStorage.getItem(KEY)
export const theme = ref<Theme>(saved === 'light' || saved === 'dark' ? saved : 'system')

const query = window.matchMedia('(prefers-color-scheme: dark)')

function apply() {
  const root = document.documentElement
  if (theme.value === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', theme.value)
  const paper = getComputedStyle(root).getPropertyValue('--paper').trim()
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', paper)
}

export function setTheme(t: Theme) {
  theme.value = t
  if (t === 'system') localStorage.removeItem(KEY)
  else localStorage.setItem(KEY, t)
  apply()
}

export function cycleTheme() {
  const order: Theme[] = ['system', 'light', 'dark']
  setTheme(order[(order.indexOf(theme.value) + 1) % order.length])
}

export function initTheme() {
  apply()
  query.addEventListener('change', apply)
}
