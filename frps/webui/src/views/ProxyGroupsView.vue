<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { authApi, proxyGroupsApi, tunnelsApi } from '@/api'

const router = useRouter()

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data state
const groups = ref<Array<{ id: string; name: string; token_id: string }>>([])
const tunnels = ref<Array<{ id: string; name: string; group_id: string; type: string; local_addr: string; remote_addr: string }>>([])
const selectedGroupId = ref<string | null>(null)
const loading = ref(false)
const error = ref('')

onMounted(async () => {
  // Check auth state
  const authResult = await authApi.state()
  checking.value = false

  if (!authResult.data) {
    error.value = '无法检查认证状态'
    return
  }

  if (!authResult.data.initialized) {
    router.replace('/init')
    return
  }

  initialized.value = true

  if (!authResult.data.authenticated) {
    router.replace('/login')
    return
  }

  authenticated.value = true

  // Load initial data
  await loadData()
})

async function loadData() {
  loading.value = true
  error.value = ''

  try {
    const [groupsResult, tunnelsResult] = await Promise.all([
      proxyGroupsApi.list(),
      tunnelsApi.list()
    ])

    if (groupsResult.error) {
      error.value = groupsResult.error
      return
    }

    if (tunnelsResult.error) {
      error.value = tunnelsResult.error
      return
    }

    groups.value = groupsResult.data?.groups || []
    tunnels.value = tunnelsResult.data?.tunnels || []

    // Select first group by default
    if (groups.value.length > 0 && !selectedGroupId.value) {
      selectedGroupId.value = groups.value[0].id
    }
  } catch {
    error.value = '加载数据失败'
  } finally {
    loading.value = false
  }
}

// Filter tunnels by selected group
const filteredTunnels = ref<typeof tunnels.value>([])

watch([selectedGroupId, tunnels], ([groupId, allTunnels]) => {
  if (groupId) {
    filteredTunnels.value = allTunnels.filter(t => t.group_id === groupId)
  } else {
    filteredTunnels.value = []
  }
}, { immediate: true })

// Find selected group name
function getGroupName(groupId: string): string {
  const group = groups.value.find(g => g.id === groupId)
  return group?.name || groupId
}

// Row click handler
function handleGroupRowClick(row: { id: string; name: string; token_id: string }) {
  selectedGroupId.value = row.id
}

// Row class name handler
function getGroupRowClass({ row }: { row: { id: string; name: string; token_id: string } }): string {
  return row.id === selectedGroupId.value ? 'selected-row' : ''
}
</script>

<template>
  <div class="proxy-groups-page">
    <!-- Loading / Auth check -->
    <div v-if="checking" class="page-loading">
      <p>检查认证状态...</p>
    </div>

    <!-- Error state -->
    <div v-else-if="error" class="page-error">
      <p>{{ error }}</p>
      <el-button @click="loadData">重试</el-button>
    </div>

    <!-- Main content -->
    <div v-else-if="authenticated" class="page-content">
      <!-- Header -->
      <div class="page-header">
        <h1>接入管理</h1>
        <el-button @click="loadData" :loading="loading">刷新</el-button>
      </div>

      <!-- Loading -->
      <div v-if="loading" class="page-loading">
        <p>加载中...</p>
      </div>

      <!-- Content -->
      <div v-else class="content-grid">
        <!-- Groups section -->
        <div class="section">
          <h2>分组列表</h2>
          <div v-if="groups.length === 0" class="empty-state">
            <p>暂无分组</p>
          </div>
          <el-table v-else :data="groups" @row-click="handleGroupRowClick"
            :row-class-name="getGroupRowClass">
            <el-table-column prop="name" label="名称" />
            <el-table-column prop="token_id" label="Token ID" width="280" />
          </el-table>
        </div>

        <!-- Tunnels section -->
        <div class="section">
          <h2>
            隧道列表
            <span v-if="selectedGroupId" class="subtitle">（{{ getGroupName(selectedGroupId) }}）</span>
          </h2>
          <div v-if="!selectedGroupId" class="empty-state">
            <p>请选择分组查看隧道</p>
          </div>
          <div v-else-if="filteredTunnels.length === 0" class="empty-state">
            <p>该分组暂无隧道</p>
          </div>
          <el-table v-else :data="filteredTunnels">
            <el-table-column prop="name" label="名称" />
            <el-table-column prop="type" label="类型" width="80" />
            <el-table-column prop="local_addr" label="本地地址" />
            <el-table-column prop="remote_addr" label="远端地址" />
          </el-table>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.proxy-groups-page {
  min-height: 100vh;
  background: var(--background-color);
}

.page-loading,
.page-error {
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  gap: 16px;
}

.page-content {
  max-width: 1200px;
  margin: 0 auto;
  padding: 24px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0;
}

.content-grid {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.section {
  background: #fff;
  border-radius: 8px;
  border: 1px solid var(--border-color);
  padding: 20px;
}

.section h2 {
  margin: 0 0 16px 0;
  font-size: 16px;
}

.subtitle {
  font-weight: normal;
  color: var(--text-color-secondary);
  font-size: 14px;
}

.empty-state {
  text-align: center;
  padding: 40px 0;
  color: var(--text-color-secondary);
}

:deep(.selected-row) {
  background-color: #ecf5ff;
}

:deep(.el-table) {
  cursor: pointer;
}
</style>
