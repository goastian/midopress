import { createRouter, createWebHashHistory } from 'vue-router'
import { getToken } from '../api/client'

export const routes = [
  { path: '/', name: 'overview', component: () => import('../views/DashboardView.vue'), meta: { title: 'Overview', nav: true } },
  { path: '/traffic', name: 'traffic', component: () => import('../views/TrafficView.vue'), meta: { title: 'Traffic', nav: true } },
  { path: '/devices', name: 'devices', component: () => import('../views/DevicesView.vue'), meta: { title: 'Devices', nav: true } },
  { path: '/rules', name: 'policy', component: () => import('../views/RulesView.vue'), meta: { title: 'Policy', nav: true } },
  { path: '/connect', name: 'connect', component: () => import('../views/ConnectView.vue'), meta: { title: 'Connect', nav: true } },
  { path: '/logs', name: 'logs', component: () => import('../views/LogsView.vue'), meta: { title: 'Logs', nav: true } },
  { path: '/service', name: 'service', component: () => import('../views/ServiceView.vue'), meta: { title: 'Service', nav: true } },
  { path: '/certificate', redirect: '/connect' },
  { path: '/policy', redirect: '/rules' },
  { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { public: true, title: 'Sign in' } },
]

// Hash history: the admin server only has to serve index.html, and the token
// of a link such as  http://127.0.0.1:9998/#/login?token=...  never reaches a log.
const router = createRouter({ history: createWebHashHistory(), routes })

router.beforeEach((to) => {
  if (!to.meta.public && !getToken()) return { name: 'login', query: { next: to.fullPath } }
})

export default router
