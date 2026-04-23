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
  <el-container style="height: 100vh">
    <el-header style="display: flex; align-items: center; justify-content: space-between">
      <span style="font-size: 16px; font-weight: 600">frps</span>
      <el-button text @click="handleLogout">退出登录</el-button>
    </el-header>

    <el-container>
      <el-aside width="200px">
        <el-menu router :default-active="$route.path">
          <el-menu-item :index="{ name: 'GroupConfig' }">
            <el-icon><Connection /></el-icon>
            <span>分组配置</span>
          </el-menu-item>
        </el-menu>
      </el-aside>

      <el-main>
        <RouterView />
      </el-main>
    </el-container>
  </el-container>
</template>
