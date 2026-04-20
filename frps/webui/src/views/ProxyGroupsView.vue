<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
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

// Group dialog
const groupDialogVisible = ref(false)
const groupDialogMode = ref<'create' | 'edit'>('create')
const groupFormRef = ref()
const groupSubmitting = ref(false)
const groupForm = ref({ name: '', enabled: true })
const groupFormRules = {
  name: [{ required: true, message: '请输入分组名称', trigger: 'blur' }]
}

// New token display
const newTokenVisible = ref(false)
const newTokenValue = ref('')

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

// --- Group CRUD ---

function openCreateGroupDialog() {
  groupDialogMode.value = 'create'
  groupForm.value = { name: '', enabled: true }
  groupDialogVisible.value = true
  nextTick(() => groupFormRef.value?.clearValidate())
}

function openEditGroupDialog(group: ProxyGroup) {
  groupDialogMode.value = 'edit'
  groupForm.value = { name: group.name, enabled: group.enabled }
  groupDialogVisible.value = true
  nextTick(() => groupFormRef.value?.clearValidate())
}

async function submitGroupForm() {
  const valid = await groupFormRef.value?.validate().catch(() => false)
  if (!valid) return

  groupSubmitting.value = true

  try {
    if (groupDialogMode.value === 'create') {
      const result = await proxyGroupsApi.create({
        name: groupForm.value.name,
        enabled: groupForm.value.enabled
      })
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('分组创建成功')
      if (result.data?.token) {
        newTokenValue.value = result.data.token
        newTokenVisible.value = true
      }
    } else {
      const group = selectedGroup.value
      if (!group) return
      const result = await proxyGroupsApi.update(group.id, {
        name: groupForm.value.name,
        enabled: groupForm.value.enabled
      })
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('分组更新成功')
    }

    groupDialogVisible.value = false
    await loadData()
  } finally {
    groupSubmitting.value = false
  }
}

async function handleDeleteGroup(group: ProxyGroup) {
  const tunnelCount = tunnels.value.filter(t => t.group_id === group.id).length
  const message = tunnelCount > 0
    ? `确定删除分组"${group.name}"吗？该分组下有 ${tunnelCount} 条隧道将一并删除，此操作不可恢复。`
    : `确定删除分组"${group.name}"吗？此操作不可恢复。`

  try {
    await ElMessageBox.confirm(message, '删除分组', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
      confirmButtonClass: 'el-button--danger'
    })
  } catch {
    return
  }

  const result = await proxyGroupsApi.delete(group.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  if (selectedGroupId.value === group.id) {
    selectedGroupId.value = null
  }
  ElMessage.success('分组已删除')
  await loadData()
}

async function handleResetToken(group: ProxyGroup) {
  try {
    await ElMessageBox.confirm(
      `确定重置分组"${group.name}"的 Token 吗？重置后旧 Token 将立即失效。`,
      '重置 Token',
      {
        confirmButtonText: '重置',
        cancelButtonText: '取消',
        type: 'warning'
      }
    )
  } catch {
    return
  }

  const result = await proxyGroupsApi.resetToken(group.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('Token 已重置')
  if (result.data?.token) {
    newTokenValue.value = result.data.token
    newTokenVisible.value = true
  }
  await loadData()
}

function copyToken() {
  navigator.clipboard.writeText(newTokenValue.value).then(() => {
    ElMessage.success('已复制到剪贴板')
  }).catch(() => {
    ElMessage.error('复制失败')
  })
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
          <el-button size="small" @click="handleResetToken(selectedGroup)">重置 Token</el-button>
          <el-button size="small" @click="openEditGroupDialog(selectedGroup)">编辑</el-button>
          <el-button size="small" type="danger" @click="handleDeleteGroup(selectedGroup)">删除</el-button>
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
            <el-button type="primary" size="small" @click="openCreateGroupDialog">新建分组</el-button>
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
              <el-table-column label="操作" width="150" align="center" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" size="small" @click.stop="openEditGroupDialog(row)">编辑</el-button>
                  <el-button link type="danger" size="small" @click.stop="handleDeleteGroup(row)">删除</el-button>
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

    <!-- Group create/edit dialog -->
    <el-dialog
      v-model="groupDialogVisible"
      :title="groupDialogMode === 'create' ? '新建分组' : '编辑分组'"
      width="400px"
      :close-on-click-modal="false"
    >
      <el-form
        ref="groupFormRef"
        :model="groupForm"
        :rules="groupFormRules"
        label-width="80px"
      >
        <el-form-item label="名称" prop="name">
          <el-input v-model="groupForm.name" placeholder="请输入分组名称" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="groupForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="groupDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="groupSubmitting" @click="submitGroupForm">
          {{ groupDialogMode === 'create' ? '创建' : '保存' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- New token display dialog -->
    <el-dialog
      v-model="newTokenVisible"
      title="Token 已生成"
      width="500px"
      :close-on-click-modal="false"
    >
      <div class="token-display">
        <p class="token-hint">请保存以下 Token，用于客户端配置：</p>
        <div class="token-box">
          <code class="token-code">{{ newTokenValue }}</code>
          <el-button type="primary" size="small" @click="copyToken">复制</el-button>
        </div>
        <p class="token-warning">此 Token 仅显示一次，关闭后将无法再次查看。</p>
      </div>
      <template #footer>
        <el-button type="primary" @click="newTokenVisible = false">我已保存</el-button>
      </template>
    </el-dialog>
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

/* Token display dialog */
.token-display {
  text-align: center;
}

.token-hint {
  margin-bottom: var(--spacing-md);
  color: var(--color-text-primary);
}

.token-box {
  display: flex;
  align-items: center;
  gap: var(--spacing-sm);
  padding: var(--spacing-base);
  background: var(--color-bg-fill);
  border-radius: var(--radius-base);
  margin-bottom: var(--spacing-md);
}

.token-code {
  flex: 1;
  font-family: monospace;
  font-size: var(--font-size-body);
  word-break: break-all;
  text-align: left;
}

.token-warning {
  color: var(--color-warning);
  font-size: var(--font-size-small);
}
</style>
