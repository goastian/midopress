import { createRouter, createWebHashHistory } from 'vue-router'
import { getToken } from '../api/client'

export const routes = [
  { path: '/', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: 'Dashboard', icon: 'dashboard' } },
  { path: '/traffic', name: 'traffic', component: () => import('../views/TrafficView.vue'), meta: { title: 'Traffic', icon: 'traffic' } },
  { path: '/devices', name: 'devices', component: () => import('../views/DevicesView.vue'), meta: { title: 'Devices', icon: 'devices' } },
  { path: '/rules', name: 'rules', component: () => import('../views/RulesView.vue'), meta: { title: 'Rules & profiles', icon: 'rules' } },
  { path: '/certificate', name: 'certificate', component: () => import('../views/CertificateView.vue'), meta: { title: 'Certificate', icon: 'shield' } },
  { path: '/logs', name: 'logs', component: () => import('../views/LogsView.vue'), meta: { title: 'Logs', icon: 'logs' } },
  { path: '/service', name: 'service', component: () => import('../views/ServiceView.vue'), meta: { title: 'Service', icon: 'service' } },
  { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { public: true } },
]

// Hash history: the admin server only has to serve index.html, and the token
// of a link such as  http://127.0.0.1:9998/#/login?token=...  never reaches a log.
const router = createRouter({ history: createWebHashHistory(), routes })

router.beforeEach((to) => {
  if (!to.meta.public && !getToken()) return { name: 'login', query: { next: to.fullPath } }
})

export default router
