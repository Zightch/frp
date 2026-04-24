<script setup lang="ts">
import { ref } from 'vue'
import { RouterView, useRouter } from 'vue-router'
import { authApi } from '@/api'
import { ElMessage } from 'element-plus'
import { useMobile } from '@/composables/useMobile'

const router = useRouter()
const { isMobile } = useMobile()
const drawerVisible = ref(false)

async function handleLogout() {
  const result = await authApi.logout()
  if (result.error) {
    ElMessage.error(result.error)
    return
  }
  ElMessage.success('已退出登录')
  router.replace({ name: 'Login' })
}

function handleMenuSelect() {
  if (isMobile.value) {
    drawerVisible.value = false
  }
}
</script>

<template>
  <el-container class="main-layout">
    <el-header class="main-header">
      <span class="header-title">frps</span>

      <!-- Mobile menu toggle -->
      <el-button v-if="isMobile" text class="menu-toggle" @click="drawerVisible = true">
        <el-icon><Menu /></el-icon>
      </el-button>

      <!-- Desktop logout button -->
      <el-button v-if="!isMobile" text @click="handleLogout">退出登录</el-button>
    </el-header>

    <el-container class="main-body">
      <!-- Desktop Aside -->
      <el-aside v-if="!isMobile" class="main-aside">
        <el-menu router :default-active="$route.path">
          <el-menu-item :index="{ name: 'GroupConfig' }">
            <el-icon><Connection /></el-icon>
            <span>分组配置</span>
          </el-menu-item>
          <el-menu-item :index="{ name: 'CertificateAssets' }">
            <el-icon><Key /></el-icon>
            <span>证书资产</span>
          </el-menu-item>
        </el-menu>
      </el-aside>

      <!-- Mobile Drawer -->
      <el-drawer
        v-model="drawerVisible"
        direction="ltr"
        :show-close="false"
        size="200px"
        class="mobile-drawer"
      >
        <template #header>
          <span class="drawer-title">frps</span>
        </template>
        <el-menu router :default-active="$route.path" @select="handleMenuSelect">
          <el-menu-item :index="{ name: 'GroupConfig' }">
            <el-icon><Connection /></el-icon>
            <span>分组配置</span>
          </el-menu-item>
          <el-menu-item :index="{ name: 'CertificateAssets' }">
            <el-icon><Key /></el-icon>
            <span>证书资产</span>
          </el-menu-item>
        </el-menu>
        <template #footer>
          <el-button text class="drawer-logout" @click="handleLogout">
            <el-icon><SwitchButton /></el-icon>
            <span>退出登录</span>
          </el-button>
        </template>
      </el-drawer>

      <el-main class="main-content">
        <RouterView />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.main-layout {
  height: 100vh;
  min-width: var(--min-content-width);
}

.main-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.header-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.menu-toggle {
  padding: var(--spacing-sm);
}

.main-body {
  height: calc(100vh - var(--header-height));
}

.main-aside {
  width: var(--aside-width);
  overflow: hidden;
}

.main-content {
  overflow: auto;
}

/* Mobile Drawer Styles */
.drawer-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.drawer-logout {
  width: 100%;
  justify-content: flex-start;
  gap: var(--spacing-xs);
  color: var(--color-text-regular);
}

/* Responsive */
@media (max-width: 768px) {
  .main-layout {
    min-width: auto;
  }
}
</style>
