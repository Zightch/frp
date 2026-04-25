<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

defineOptions({
  name: 'SettingsView'
})

const route = useRoute()
const router = useRouter()

const tabs = [
  { name: 'SettingsEntryCertificates', label: '入口证书' }
]

const activeTab = computed(() => {
  return route.name as string
})

function handleTabChange(name: string | number) {
  if (typeof name === 'string') {
    router.push({ name })
  }
}
</script>

<template>
  <el-container direction="vertical" class="settings-view">
    <div class="page-header">
      <el-row justify="space-between" align="middle">
        <h1>系统设置</h1>
      </el-row>
    </div>

    <el-main class="content-main">
      <el-tabs :model-value="activeTab" @tab-change="handleTabChange">
        <el-tab-pane
          v-for="tab in tabs"
          :key="tab.name"
          :name="tab.name"
          :label="tab.label"
        />
      </el-tabs>

      <div class="tab-content">
        <RouterView />
      </div>
    </el-main>
  </el-container>
</template>

<style scoped>
.settings-view {
  height: 100%;
}

.page-header {
  padding: 0;
}

.page-header h1 {
  margin: 0;
  font-size: 18px;
  color: var(--el-text-color-primary);
}

.content-main {
  padding: var(--el-main-padding) 0 0 0;
  flex: 1;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.tab-content {
  flex: 1;
  overflow: auto;
  margin-top: var(--spacing-md);
}
</style>
