<script setup lang="ts">
import { RouterView, useRouter } from 'vue-router'
import { authApi } from '@/api'
import { ElMessage } from 'element-plus'

const router = useRouter()

async function handleLogout() {
  const result = await authApi.logout()
  if (result.error) {
    ElMessage.error(result.error)
    return
  }
  ElMessage.success('已退出登录')
  router.replace('/login')
}
</script>

<template>
  <div class="main-layout">
    <!-- Header -->
    <header class="layout-header">
      <div class="header-brand">frps</div>
      <div class="header-actions">
        <el-button text @click="handleLogout">退出登录</el-button>
      </div>
    </header>

    <!-- Body: Aside + Main -->
    <div class="layout-body">
      <!-- Aside -->
      <aside class="layout-aside">
        <nav class="aside-nav">
          <router-link to="/proxy-groups" class="nav-item">
            <el-icon><Connection /></el-icon>
            <span>接入管理</span>
          </router-link>
        </nav>
      </aside>

      <!-- Main -->
      <main class="layout-main">
        <div class="main-content">
          <RouterView />
        </div>
      </main>
    </div>
  </div>
</template>

<style scoped>
.main-layout {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}

/* Header */
.layout-header {
  height: var(--header-height);
  background: var(--color-bg-white);
  border-bottom: 1px solid var(--color-border-light);
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 var(--spacing-lg);
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  z-index: 100;
}

.header-brand {
  font-size: var(--font-size-h2);
  font-weight: var(--font-weight-bold);
  color: var(--color-text-primary);
}

.header-actions {
  display: flex;
  align-items: center;
  gap: var(--spacing-sm);
}

/* Body */
.layout-body {
  display: flex;
  flex: 1;
  margin-top: var(--header-height);
}

/* Aside */
.layout-aside {
  width: var(--aside-width);
  background: var(--color-bg-white);
  border-right: 1px solid var(--color-border-light);
  position: fixed;
  top: var(--header-height);
  left: 0;
  bottom: 0;
  overflow-y: auto;
}

.aside-nav {
  padding: var(--spacing-base) 0;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: var(--spacing-sm);
  padding: var(--spacing-md) var(--spacing-lg);
  color: var(--color-text-regular);
  text-decoration: none;
  transition: all 0.2s;
}

.nav-item:hover {
  background: var(--color-bg-fill-lighter);
  color: var(--color-primary);
}

.nav-item.router-link-active {
  background: var(--color-primary-light-9);
  color: var(--color-primary);
  border-right: 2px solid var(--color-primary);
}

/* Main */
.layout-main {
  flex: 1;
  margin-left: var(--aside-width);
  background: var(--color-bg-page);
  min-height: calc(100vh - var(--header-height));
}

.main-content {
  max-width: var(--content-max-width);
  margin: 0 auto;
  padding: var(--content-padding);
}
</style>
