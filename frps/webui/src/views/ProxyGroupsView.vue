<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { authApi, proxyGroupsApi, tunnelsApi, type ProxyGroup, type Tunnel } from '@/api'

const router = useRouter()

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data
const groups = ref<ProxyGroup[]>([])
const tunnels = ref<Tunnel[]>([])
const selectedGroupId = ref<number | null>(null)
const loading = ref(false)
const error = ref('')

// Computed
const selectedGroup = computed(() =>
  groups.value.find(g => g.id === selectedGroupId.value)
)

const filteredTunnels = computed(() =>
  selectedGroupId.value
    ? tunnels.value.filter(t => t.group_id === selectedGroupId.value)
    : []
)

// Lifecycle
onMounted(async () => {
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
  await loadData()
})

// Data loading
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

    groups.value = groupsResult.data?.items || []
    tunnels.value = tunnelsResult.data?.items || []

    if (groups.value.length > 0 && selectedGroupId.value === null) {
      selectedGroupId.value = groups.value[0].id
    }
  } catch {
    error.value = '加载数据失败'
  } finally {
    loading.value = false
  }
}

// Format helpers
function formatRemotePort(tunnel: Tunnel): string {
  if (tunnel.remote_type === 'range' && tunnel.remote_end > tunnel.remote_start) {
    return `${tunnel.remote_start}-${tunnel.remote_end}`
  }
  return String(tunnel.remote_start)
}

function formatLocalAddr(tunnel: Tunnel): string {
  const port = tunnel.local_end > tunnel.local_start
    ? `${tunnel.local_start}-${tunnel.local_end}`
    : String(tunnel.local_start)
  return `${tunnel.local_host}:${port}`
}

// Group selection
function handleGroupRowClick(row: ProxyGroup) {
  selectedGroupId.value = row.id
}

function getGroupRowClass({ row }: { row: ProxyGroup }): string {
  return row.id === selectedGroupId.value ? 'selected-row' : ''
}
</script>

<template>
  <div class="management-page">
    <!-- Auth check loading -->
    <div v-if="checking" class="state-message">
      检查认证状态...
    </div>

    <!-- Error state -->
    <div v-else-if="error" class="state-message state-error">
      <span>{{ error }}</span>
      <el-button size="small" @click="loadData">重试</el-button>
    </div>

    <!-- Main content -->
    <template v-else-if="authenticated">
      <!-- Page header -->
      <div class="page-header">
        <h1>接入管理</h1>
        <div class="header-actions">
          <el-button @click="loadData" :loading="loading">刷新</el-button>
        </div>
      </div>

      <!-- Operation bar -->
      <div v-if="selectedGroup" class="operation-bar">
        <div class="operation-info">
          <span class="info-item">
            <span class="info-label">已选中:</span>
            <span class="info-value">{{ selectedGroup.name }}</span>
          </span>
          <span class="info-divider">|</span>
          <span class="info-item">
            <span class="info-label">Token ID:</span>
            <span class="info-value token">{{ selectedGroup.token_id }}</span>
          </span>
        </div>
        <div class="operation-actions">
          <el-button size="small">重置 Token</el-button>
          <el-button size="small">编辑</el-button>
          <el-button size="small" type="danger">删除</el-button>
        </div>
      </div>

      <!-- Loading -->
      <div v-if="loading" class="state-message">
        加载中...
      </div>

      <!-- Content grid -->
      <div v-else class="content-grid">
        <!-- Groups panel -->
        <section class="panel groups-panel">
          <div class="panel-header">
            <h2>分组列表</h2>
            <el-button type="primary" size="small">新建分组</el-button>
          </div>
          <div class="panel-body">
            <div v-if="groups.length === 0" class="empty-message">
              暂无分组，请新建
            </div>
            <el-table
              v-else
              :data="groups"
              height="100%"
              @row-click="handleGroupRowClick"
              :row-class-name="getGroupRowClass"
              highlight-current-row
            >
              <el-table-column prop="name" label="名称" />
              <el-table-column prop="token_id" label="Token ID" width="200" />
              <el-table-column label="状态" width="80" align="center">
                <template #default="{ row }">
                  <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
                    {{ row.enabled ? '启用' : '禁用' }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="100" align="center" fixed="right">
                <template #default>
                  <el-button link type="primary" size="small">编辑</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </section>

        <!-- Tunnels panel -->
        <section class="panel tunnels-panel">
          <div class="panel-header">
            <h2>
              隧道列表
              <span v-if="selectedGroup" class="subtitle">（{{ selectedGroup.name }}）</span>
            </h2>
            <el-button
              v-if="selectedGroupId"
              type="primary"
              size="small"
              :disabled="!selectedGroupId"
            >
              新建隧道
            </el-button>
          </div>
          <div class="panel-body">
            <div v-if="!selectedGroupId" class="empty-message">
              请选择分组查看隧道
            </div>
            <div v-else-if="filteredTunnels.length === 0" class="empty-message">
              该分组暂无隧道
            </div>
            <el-table v-else :data="filteredTunnels" height="100%">
              <el-table-column prop="name" label="名称" />
              <el-table-column prop="protocol" label="协议" width="70" align="center">
                <template #default="{ row }">
                  <el-tag size="small">{{ row.protocol.toUpperCase() }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="远端" width="120">
                <template #default="{ row }">
                  {{ formatRemotePort(row) }}
                </template>
              </el-table-column>
              <el-table-column label="本地">
                <template #default="{ row }">
                  {{ formatLocalAddr(row) }}
                </template>
              </el-table-column>
              <el-table-column label="状态" width="80" align="center">
                <template #default="{ row }">
                  <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
                    {{ row.enabled ? '启用' : '禁用' }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="100" align="center">
                <template #default>
                  <el-button link type="primary" size="small">编辑</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </section>
      </div>
    </template>
  </div>
</template>

<style scoped>
.management-page {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--spacing-sm);
  margin-bottom: var(--spacing-lg);
  flex-shrink: 0;
}

.page-header h1 {
  font-size: var(--font-size-h1);
  font-weight: var(--font-weight-bold);
  color: var(--color-text-primary);
  margin: 0;
}

.header-actions {
  display: flex;
  gap: var(--spacing-sm);
}

.state-message {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  text-align: center;
  padding: var(--spacing-2xl);
  color: var(--color-text-secondary);
}

.state-error {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--spacing-md);
  color: var(--color-danger);
}

.content-grid {
  display: grid;
  grid-template-columns: minmax(320px, 0.95fr) minmax(560px, 1.75fr);
  gap: var(--spacing-lg);
  flex: 1;
  min-height: 0;
  align-items: stretch;
}

.panel {
  background: var(--color-bg-white);
  border-radius: var(--radius-base);
  border: 1px solid var(--color-border-lighter);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-height: 0;
}

.groups-panel {
  min-width: 320px;
}

.tunnels-panel {
  min-width: 560px;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--spacing-base) var(--spacing-lg);
  border-bottom: 1px solid var(--color-border-lighter);
}

.panel-header h2 {
  font-size: var(--font-size-h2);
  font-weight: var(--font-weight-bold);
  color: var(--color-text-primary);
  margin: 0;
}

.subtitle {
  font-weight: var(--font-weight-regular);
  font-size: var(--font-size-body);
  color: var(--color-text-secondary);
  margin-left: var(--spacing-xs);
}

.panel-body {
  flex: 1;
  min-height: 0;
  padding: var(--spacing-base);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.empty-message {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  text-align: center;
  padding: var(--spacing-base);
  color: var(--color-text-secondary);
}

.operation-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--spacing-sm);
  padding: var(--spacing-base) var(--spacing-lg);
  margin-bottom: var(--spacing-lg);
  background: var(--color-bg-white);
  border-radius: var(--radius-base);
  border: 1px solid var(--color-border-lighter);
  flex-shrink: 0;
}

.operation-info {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--spacing-sm);
}

.info-item {
  display: flex;
  align-items: center;
  gap: var(--spacing-xs);
}

.info-label {
  color: var(--color-text-secondary);
}

.info-value {
  color: var(--color-text-primary);
  font-weight: var(--font-weight-medium);
}

.info-value.token {
  font-family: monospace;
}

.info-divider {
  color: var(--color-border);
}

.operation-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--spacing-sm);
}

:deep(.selected-row) {
  background-color: var(--color-primary-light-9);
}

:deep(.el-table) {
  cursor: pointer;
}
</style>
