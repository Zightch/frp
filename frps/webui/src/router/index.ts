import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { WEBUI_HISTORY_BASE } from '@/runtime/basePath'

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
      },
      {
        path: 'rate-policies',
        name: 'RatePolicy',
        component: () => import('@/views/RatePolicyView.vue')
      },
      {
        path: 'certificate-assets',
        name: 'CertificateAssets',
        component: () => import('@/views/CertificateAssetsView.vue')
      },
      {
        path: 'settings',
        component: () => import('@/views/SettingsView.vue'),
        children: [
          {
            path: '',
            name: 'Settings',
            redirect: { name: 'SettingsEntryCertificates' }
          },
          {
            path: 'entry-certificates',
            name: 'SettingsEntryCertificates',
            component: () => import('@/views/settings/EntryCertificates.vue')
          }
        ]
      }
    ]
  }
]

const router = createRouter({
  history: createWebHistory(WEBUI_HISTORY_BASE),
  routes
})

export default router
