import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    redirect: '/proxy-groups'
  },
  {
    path: '/init',
    name: 'Init',
    component: () => import('@/views/InitView.vue')
  },
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/LoginView.vue')
  },
  {
    path: '/proxy-groups',
    name: 'ProxyGroups',
    component: () => import('@/views/ProxyGroupsView.vue')
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

export default router
