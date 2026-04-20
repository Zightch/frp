import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  // 根路径重定向到分组配置
  {
    path: '/',
    redirect: { name: 'GroupConfig' }
  },

  // 认证相关页面 - 使用 BlankLayout
  {
    path: '/init',
    component: () => import('@/layouts/BlankLayout.vue'),
    children: [
      {
        path: '',
        name: 'Init',
        component: () => import('@/views/InitView.vue')
      }
    ]
  },
  {
    path: '/login',
    component: () => import('@/layouts/BlankLayout.vue'),
    children: [
      {
        path: '',
        name: 'Login',
        component: () => import('@/views/LoginView.vue')
      }
    ]
  },

  // 管理页面 - 使用 MainLayout
  {
    path: '/',
    component: () => import('@/layouts/MainLayout.vue'),
    children: [
      {
        path: 'proxy-groups',
        name: 'GroupConfig',
        component: () => import('@/views/GroupConfigView.vue')
      }
    ]
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

export default router
