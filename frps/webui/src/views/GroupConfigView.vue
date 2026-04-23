<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  authApi,
  proxyGroupsApi as groupConfigApi,
  tunnelsApi,
  localIPsApi,
  type ProxyGroup,
  type Tunnel,
  type TunnelPayload,
  type LocalIP,
  type ClientCredential
} from '@/api'

defineOptions({
  name: 'GroupConfigView'
})

type TunnelFormModel = {
  name: string
  protocol: 'tcp' | 'udp'
  remote_type: 'single' | 'range'
  remote_start: number | null
  remote_end: number | null
  local_host: string
  local_start: number | null
  local_end: number | null
  enabled: boolean
}

const PORT_MIN = 1
const PORT_MAX = 65535

const router = useRouter()

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data
const groups = ref<ProxyGroup[]>([])
const tunnels = ref<Tunnel[]>([])
const localIPs = ref<LocalIP[]>([])
const localIPsLoading = ref(false)
const selectedGroupId = ref<number | null>(null)
const loading = ref(false)
const error = ref('')

// Group dialog
const groupDialogVisible = ref(false)
const groupDialogMode = ref<'create' | 'edit'>('create')
const groupFormRef = ref()
const groupSubmitting = ref(false)
const editingGroupId = ref<number | null>(null)
const editingGroupStatus = ref<string>('')
const editingGroupStatusReason = ref<string>('')
const editingGroupEffectiveIP = ref<string>('')
const groupForm = ref({ name: '', effective_ip: '', enabled: true })
const groupFormRules = {
  name: [{ required: true, message: '请输入分组名称', trigger: 'blur' }],
  effective_ip: [{ required: true, message: '请选择生效 IP', trigger: 'change' }]
}

// New credential display
const newCredentialVisible = ref(false)
const newCredential = ref<ClientCredential | null>(null)

// Tunnel drawer
const tunnelDrawerVisible = ref(false)
const tunnelDrawerMode = ref<'create' | 'edit'>('create')
const tunnelFormRef = ref<FormInstance>()
const tunnelSubmitting = ref(false)
const tunnelForm = ref<TunnelFormModel>(createTunnelForm())
const tunnelFormRules: FormRules<TunnelFormModel> = {
  name: [{ asyncValidator: createRequiredTextValidator('隧道名称'), trigger: 'blur' }],
  protocol: [{ required: true, message: '请选择协议', trigger: 'change' }],
  remote_type: [{ required: true, message: '请选择远端类型', trigger: 'change' }],
  remote_start: [{ asyncValidator: createPortStartValidator('remote_start'), trigger: ['blur', 'change'] }],
  remote_end: [{ asyncValidator: createPortEndValidator('remote_end'), trigger: ['blur', 'change'] }],
  local_host: [{ asyncValidator: createRequiredTextValidator('本地地址'), trigger: 'blur' }],
  local_start: [{ asyncValidator: createPortStartValidator('local_start'), trigger: ['blur', 'change'] }],
  local_end: [{ asyncValidator: createPortEndValidator('local_end'), trigger: ['blur', 'change'] }],
  enabled: [{ asyncValidator: createEnabledValidator(), trigger: 'change' }]
}

// Editing tunnel ID (for edit mode)
const editingTunnelId = ref<number | null>(null)
const editingTunnelGroupId = ref<number | null>(null)

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
  localIPsLoading.value = true
  error.value = ''

  try {
    const [groupsResult, tunnelsResult, ipsResult] = await Promise.all([
      groupConfigApi.list(),
      tunnelsApi.list(),
      localIPsApi.list()
    ])

    if (groupsResult.error) {
      error.value = groupsResult.error
      return
    }

    if (tunnelsResult.error) {
      error.value = tunnelsResult.error
      return
    }

    if (ipsResult.error) {
      error.value = ipsResult.error
      return
    }

    groups.value = groupsResult.data?.items || []
    tunnels.value = tunnelsResult.data?.items || []
    localIPs.value = ipsResult.data?.items || []

    if (!groups.value.some(group => group.id === selectedGroupId.value)) {
      selectedGroupId.value = groups.value[0]?.id ?? null
    }
  } catch {
    error.value = '加载数据失败'
  } finally {
    loading.value = false
    localIPsLoading.value = false
  }
}

function effectiveIPOptions(): { value: string; label: string; disabled: boolean }[] {
  const availableSet = new Set(localIPs.value.map(ip => ip.addr))
  const currentIP = editingGroupEffectiveIP.value
  const options: { value: string; label: string; disabled: boolean }[] = []

  // If editing and current effective_ip is not in available list, show it as invalid option
  if (groupDialogMode.value === 'edit' && currentIP && !availableSet.has(currentIP)) {
    options.push({ value: currentIP, label: `${currentIP}（已失效）`, disabled: false })
  }

  for (const ip of localIPs.value) {
    const familyTag = ip.family === 'ipv6' ? 'IPv6' : 'IPv4'
    options.push({ value: ip.addr, label: `${ip.addr} (${familyTag})`, disabled: false })
  }

  return options
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

function createTunnelForm(): TunnelFormModel {
  return {
    name: '',
    protocol: 'tcp',
    remote_type: 'single',
    remote_start: null,
    remote_end: null,
    local_host: '127.0.0.1',
    local_start: null,
    local_end: null,
    enabled: true
  }
}

function createRequiredTextValidator(label: string) {
  return (_rule: unknown, value: string) => {
    if (!value?.trim()) {
      return Promise.reject(new Error(`请输入${label}`))
    }
    return Promise.resolve()
  }
}

function createEnabledValidator() {
  return (_rule: unknown, value: boolean) => {
    if (typeof value !== 'boolean') {
      return Promise.reject(new Error('请选择启用状态'))
    }
    return Promise.resolve()
  }
}

function getPortError(value: number | null, label: string): Error | null {
  if (value == null) {
    return new Error(`请输入${label}`)
  }

  if (!Number.isInteger(value) || value < PORT_MIN || value > PORT_MAX) {
    return new Error(`${label}必须为 ${PORT_MIN}-${PORT_MAX} 的整数`)
  }

  return null
}

function getRangeOrderError(start: number | null, end: number | null, labelPrefix: string): Error | null {
  if (start == null || end == null) {
    return null
  }

  if (start >= end) {
    return new Error(`${labelPrefix}开始端口必须小于结束端口`)
  }

  return null
}

function getRangeCountError(): Error | null {
  if (tunnelForm.value.remote_type !== 'range') {
    return null
  }

  const { remote_start, remote_end, local_start, local_end } = tunnelForm.value
  if (remote_start == null || remote_end == null || local_start == null || local_end == null) {
    return null
  }

  const remoteRangeCount = remote_end - remote_start
  const localRangeCount = local_end - local_start
  if (remoteRangeCount !== localRangeCount) {
    return new Error('远端与本地端口范围数量必须一致')
  }

  return null
}

function createPortStartValidator(field: 'remote_start' | 'local_start') {
  return (_rule: unknown, value: number | null) => {
    const labelPrefix = field === 'remote_start' ? '远端' : '本地'
    const label = `${labelPrefix}端口`
    const orderError = field === 'remote_start'
      ? getRangeOrderError(value, tunnelForm.value.remote_end, labelPrefix)
      : getRangeOrderError(value, tunnelForm.value.local_end, labelPrefix)

    return Promise.resolve()
      .then(() => {
        const portError = getPortError(value, label)
        if (portError) {
          throw portError
        }
      })
      .then(() => {
        if (tunnelForm.value.remote_type !== 'range') {
          return
        }
        if (orderError) {
          throw orderError
        }
        const countError = getRangeCountError()
        if (countError) {
          throw countError
        }
      })
  }
}

function createPortEndValidator(field: 'remote_end' | 'local_end') {
  return (_rule: unknown, value: number | null) => {
    if (tunnelForm.value.remote_type !== 'range') {
      return Promise.resolve()
    }

    const labelPrefix = field === 'remote_end' ? '远端' : '本地'
    const label = `${labelPrefix}结束端口`
    const start = field === 'remote_end' ? tunnelForm.value.remote_start : tunnelForm.value.local_start

    return Promise.resolve()
      .then(() => {
        const portError = getPortError(value, label)
        if (portError) {
          throw portError
        }
      })
      .then(() => {
        const orderError = getRangeOrderError(start, value, labelPrefix)
        if (orderError) {
          throw orderError
        }
        const countError = getRangeCountError()
        if (countError) {
          throw countError
        }
      })
  }
}

function validateTunnelPortFields() {
  const fields: Array<keyof TunnelFormModel> = ['remote_start', 'local_start']

  if (tunnelForm.value.remote_type === 'range') {
    fields.push('remote_end', 'local_end')
  } else {
    tunnelFormRef.value?.clearValidate(['remote_end', 'local_end'])
  }

  nextTick(() => {
    tunnelFormRef.value?.validateField(fields).catch(() => undefined)
  })
}

// Group selection
function handleGroupRowClick(row: ProxyGroup) {
  selectedGroupId.value = row.id
}

function getGroupRowClass({ row }: { row: ProxyGroup }): string {
  return row.id === selectedGroupId.value
    ? 'selectable-row selected-row'
    : 'selectable-row'
}

// --- Group CRUD ---

function openCreateGroupDialog() {
  groupDialogMode.value = 'create'
  editingGroupId.value = null
  editingGroupStatus.value = ''
  editingGroupStatusReason.value = ''
  editingGroupEffectiveIP.value = ''
  const defaultIP = localIPs.value.find(ip => ip.addr === '0.0.0.0')?.addr || localIPs.value[0]?.addr || ''
  groupForm.value = { name: '', effective_ip: defaultIP, enabled: true }
  groupDialogVisible.value = true
  nextTick(() => groupFormRef.value?.clearValidate())
}

function openEditGroupDialog(group: ProxyGroup) {
  groupDialogMode.value = 'edit'
  editingGroupId.value = group.id
  editingGroupStatus.value = group.status
  editingGroupStatusReason.value = group.status_reason || ''
  editingGroupEffectiveIP.value = group.effective_ip
  groupForm.value = { name: group.name, effective_ip: group.effective_ip, enabled: group.enabled }
  groupDialogVisible.value = true
  nextTick(() => groupFormRef.value?.clearValidate())
}

async function submitGroupForm() {
  const valid = await groupFormRef.value?.validate().catch(() => false)
  if (!valid) return

  groupSubmitting.value = true

  try {
    if (groupDialogMode.value === 'create') {
      const result = await groupConfigApi.create({
        name: groupForm.value.name,
        effective_ip: groupForm.value.effective_ip,
        enabled: groupForm.value.enabled
      })
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('分组创建成功')
      if (result.data?.credential) {
        newCredential.value = result.data.credential
        newCredentialVisible.value = true
      }
    } else {
      if (!editingGroupId.value) return
      const result = await groupConfigApi.update(editingGroupId.value, {
        name: groupForm.value.name,
        effective_ip: groupForm.value.effective_ip,
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

  const result = await groupConfigApi.delete(group.id)
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

// --- Tunnel CRUD ---

function openCreateTunnelDrawer() {
  if (!selectedGroupId.value) return
  tunnelDrawerMode.value = 'create'
  editingTunnelId.value = null
  editingTunnelGroupId.value = selectedGroupId.value
  tunnelForm.value = createTunnelForm()
  tunnelDrawerVisible.value = true
  nextTick(() => tunnelFormRef.value?.clearValidate())
}

function openEditTunnelDrawer(tunnel: Tunnel) {
  tunnelDrawerMode.value = 'edit'
  editingTunnelId.value = tunnel.id
  editingTunnelGroupId.value = tunnel.group_id
  tunnelForm.value = {
    name: tunnel.name,
    protocol: tunnel.protocol,
    remote_type: tunnel.remote_type,
    remote_start: tunnel.remote_start,
    remote_end: tunnel.remote_end,
    local_host: tunnel.local_host,
    local_start: tunnel.local_start,
    local_end: tunnel.local_end,
    enabled: tunnel.enabled
  }
  tunnelDrawerVisible.value = true
  nextTick(() => tunnelFormRef.value?.clearValidate())
}

function buildTunnelPayload(groupId: number): TunnelPayload {
  const isRange = tunnelForm.value.remote_type === 'range'
  const remoteStart = tunnelForm.value.remote_start as number
  const localStart = tunnelForm.value.local_start as number

  return {
    group_id: groupId,
    name: tunnelForm.value.name.trim(),
    protocol: tunnelForm.value.protocol,
    remote_type: tunnelForm.value.remote_type,
    remote_start: remoteStart,
    remote_end: isRange ? (tunnelForm.value.remote_end as number) : remoteStart,
    local_host: tunnelForm.value.local_host.trim(),
    local_start: localStart,
    local_end: isRange ? (tunnelForm.value.local_end as number) : localStart,
    enabled: tunnelForm.value.enabled
  }
}

async function submitTunnelForm() {
  const valid = await tunnelFormRef.value?.validate().catch(() => false)
  if (!valid) return

  tunnelSubmitting.value = true

  try {
    if (tunnelDrawerMode.value === 'create') {
      if (!selectedGroupId.value) return
      const result = await tunnelsApi.create(buildTunnelPayload(selectedGroupId.value))
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('隧道创建成功')
    } else {
      if (!editingTunnelId.value || !editingTunnelGroupId.value) return
      const result = await tunnelsApi.update(
        editingTunnelId.value,
        buildTunnelPayload(editingTunnelGroupId.value)
      )
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('隧道更新成功')
    }

    tunnelDrawerVisible.value = false
    await loadData()
  } finally {
    tunnelSubmitting.value = false
  }
}

async function handleDeleteTunnel(tunnel: Tunnel) {
  try {
    await ElMessageBox.confirm(
      `确定删除隧道"${tunnel.name}"吗？此操作不可恢复。`,
      '删除隧道',
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        type: 'warning',
        confirmButtonClass: 'el-button--danger'
      }
    )
  } catch {
    return
  }

  const result = await tunnelsApi.delete(tunnel.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('隧道已删除')
  await loadData()
}

async function handleRotateCredentials(group: ProxyGroup) {
  try {
    await ElMessageBox.confirm(
      `确定轮转分组"${group.name}"的客户端密钥吗？轮转后旧 client_secret 将立即失效。`,
      '轮转客户端密钥',
      {
        confirmButtonText: '轮转',
        cancelButtonText: '取消',
        type: 'warning'
      }
    )
  } catch {
    return
  }

  const result = await groupConfigApi.rotateCredentials(group.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('客户端密钥已轮转')
  if (result.data?.credential) {
    newCredential.value = result.data.credential
    newCredentialVisible.value = true
  }
  await loadData()
}

function copyCredentialField(label: string, value: string | undefined) {
  if (!value) {
    ElMessage.error(`${label} 为空`)
    return
  }
  navigator.clipboard.writeText(value).then(() => {
    ElMessage.success(`${label} 已复制`)
  }).catch(() => {
    ElMessage.error(`${label} 复制失败`)
  })
}
</script>

<template>
  <div v-if="checking" style="display: flex; align-items: center; justify-content: center; height: 100%">
    <span>检查认证状态...</span>
  </div>

  <div v-else-if="error" style="display: flex; flex-direction: column; align-items: center; justify-content: center; height: 100; gap: 12px">
    <span style="color: var(--el-color-danger)">{{ error }}</span>
    <el-button size="small" @click="loadData">重试</el-button>
  </div>

  <template v-else-if="authenticated">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px">
      <h1 style="font-size: 20px; font-weight: 600; margin: 0">分组配置</h1>
      <el-button @click="loadData" :loading="loading">刷新</el-button>
    </div>

    <el-card v-if="selectedGroup" style="margin-bottom: 20px" :body-style="{ padding: '12px 20px' }">
      <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px">
        <el-descriptions :column="4">
          <el-descriptions-item label="已选中">{{ selectedGroup.name }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tooltip
              v-if="selectedGroup.status === '异常' && selectedGroup.status_reason"
              :content="selectedGroup.status_reason"
              placement="top"
            >
              <el-tag type="danger" size="small">{{ selectedGroup.status }}</el-tag>
            </el-tooltip>
            <el-tag
              v-else
              :type="selectedGroup.status === '启用' ? 'success' : selectedGroup.status === '禁用' ? 'info' : 'danger'"
              size="small"
            >
              {{ selectedGroup.status }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="Client ID">
            <code style="font-family: monospace">{{ selectedGroup.client_id }}</code>
          </el-descriptions-item>
          <el-descriptions-item label="生效 IP">{{ selectedGroup.effective_ip }}</el-descriptions-item>
        </el-descriptions>
        <div style="display: flex; gap: 8px">
          <el-button size="small" @click="handleRotateCredentials(selectedGroup)">轮转密钥</el-button>
          <el-button size="small" @click="openEditGroupDialog(selectedGroup)">编辑</el-button>
          <el-button size="small" type="danger" @click="handleDeleteGroup(selectedGroup)">删除</el-button>
        </div>
      </div>
    </el-card>

    <div style="display: grid; grid-template-columns: minmax(320px, 1fr) minmax(560px, 2fr); gap: 20px; height: calc(100vh - 200px)">
      <el-card style="display: flex; flex-direction: column">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center">
            <span style="font-weight: 600">分组列表</span>
            <el-button type="primary" size="small" @click="openCreateGroupDialog">新建分组</el-button>
          </div>
        </template>
        <el-empty v-if="groups.length === 0" description="暂无分组，请新建" />
        <el-table
          v-else
          :data="groups"
          height="100%"
          @row-click="handleGroupRowClick"
          :row-class-name="getGroupRowClass"
          highlight-current-row
          v-loading="loading"
        >
          <el-table-column prop="name" label="名称" />
          <el-table-column prop="client_id" label="Client ID" width="200" />
          <el-table-column label="状态" width="100" align="center">
            <template #default="{ row }">
              <el-tooltip
                v-if="row.status === '异常' && row.status_reason"
                :content="row.status_reason"
                placement="top"
              >
                <el-tag type="danger" size="small">{{ row.status }}</el-tag>
              </el-tooltip>
              <el-tag
                v-else
                :type="row.status === '启用' ? 'success' : row.status === '禁用' ? 'info' : 'danger'"
                size="small"
              >
                {{ row.status }}
              </el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-card>

      <el-card style="display: flex; flex-direction: column">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center">
            <span style="font-weight: 600">
              隧道列表
              <span v-if="selectedGroup" style="font-weight: 400; color: var(--el-text-color-secondary)">（{{ selectedGroup.name }}）</span>
            </span>
            <el-button v-if="selectedGroupId" type="primary" size="small" @click="openCreateTunnelDrawer">新建隧道</el-button>
          </div>
        </template>
        <el-empty v-if="!selectedGroupId" description="请选择分组查看隧道" />
        <el-empty v-else-if="filteredTunnels.length === 0" description="该分组暂无隧道" />
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
              <el-tooltip
                v-if="(row.status === '冲突' || row.status === '异常') && row.status_reason"
                :content="row.status_reason"
                placement="top"
              >
                <el-tag type="danger" size="small">{{ row.status }}</el-tag>
              </el-tooltip>
              <el-tag
                v-else
                :type="row.status === '启用' ? 'success' : row.status === '禁用' ? 'info' : 'danger'"
                size="small"
              >
                {{ row.status }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="120" align="center">
            <template #default="{ row }">
              <el-button link type="primary" size="small" @click="openEditTunnelDrawer(row)">编辑</el-button>
              <el-button link type="danger" size="small" @click="handleDeleteTunnel(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-card>
    </div>
  </template>

  <!-- Group create/edit dialog -->
    <el-dialog
      v-model="groupDialogVisible"
      :title="groupDialogMode === 'create' ? '新建分组' : '编辑分组'"
      width="400px"
      :close-on-click-modal="false"
    >
      <el-alert
        v-if="groupDialogMode === 'edit' && editingGroupStatus === '异常' && editingGroupStatusReason"
        type="error"
        :closable="false"
        show-icon
        style="margin-bottom: 12px"
      >
        <template #title>当前状态：异常</template>
        {{ editingGroupStatusReason }}
      </el-alert>
      <el-form
        ref="groupFormRef"
        :model="groupForm"
        :rules="groupFormRules"
        label-width="80px"
      >
        <el-form-item label="名称" prop="name">
          <el-input v-model="groupForm.name" placeholder="请输入分组名称" />
        </el-form-item>
        <el-form-item label="生效 IP" prop="effective_ip">
          <el-select
            v-model="groupForm.effective_ip"
            placeholder="请选择生效 IP"
            style="width: 100%"
            :loading="localIPsLoading"
          >
            <el-option
              v-for="opt in effectiveIPOptions()"
              :key="opt.value"
              :label="opt.label"
              :value="opt.value"
              :disabled="opt.disabled"
            />
          </el-select>
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

    <!-- New credential display dialog -->
    <el-dialog
      v-model="newCredentialVisible"
      title="客户端凭据已生成"
      width="500px"
      :close-on-click-modal="false"
    >
      <div style="text-align: center">
        <p style="margin-bottom: 12px; color: var(--el-text-color-primary)">请保存以下 Client ID 和 Client Secret，用于客户端配置：</p>
        <div style="display: grid; gap: 12px; padding: 16px; background: var(--el-fill-color); border-radius: 8px; margin-bottom: 12px">
          <div style="display: flex; align-items: center; gap: 8px">
            <code style="flex: 1; font-family: monospace; word-break: break-all; text-align: left">{{ newCredential?.client_id }}</code>
            <el-button type="primary" size="small" @click="copyCredentialField('Client ID', newCredential?.client_id)">复制</el-button>
          </div>
          <div style="display: flex; align-items: center; gap: 8px">
            <code style="flex: 1; font-family: monospace; word-break: break-all; text-align: left">{{ newCredential?.client_secret }}</code>
            <el-button type="primary" size="small" @click="copyCredentialField('Client Secret', newCredential?.client_secret)">复制</el-button>
          </div>
        </div>
        <p style="color: var(--el-color-warning); font-size: 12px">此 Client Secret 仅显示一次，关闭后将无法再次查看。</p>
      </div>
      <template #footer>
        <el-button type="primary" @click="newCredentialVisible = false">我已保存</el-button>
      </template>
    </el-dialog>

    <!-- Tunnel create/edit drawer -->
    <el-drawer
      v-model="tunnelDrawerVisible"
      :title="tunnelDrawerMode === 'create' ? '新建隧道' : '编辑隧道'"
      size="400px"
      :close-on-click-modal="false"
    >
      <el-form
        ref="tunnelFormRef"
        :model="tunnelForm"
        :rules="tunnelFormRules"
        label-width="100px"
      >
        <el-form-item label="名称" prop="name">
          <el-input v-model="tunnelForm.name" placeholder="请输入隧道名称" />
        </el-form-item>
        <el-form-item label="协议" prop="protocol">
          <el-select v-model="tunnelForm.protocol" style="width: 100%">
            <el-option label="TCP" value="tcp" />
            <el-option label="UDP" value="udp" />
          </el-select>
        </el-form-item>
        <el-form-item label="远端类型" prop="remote_type">
          <el-select
            v-model="tunnelForm.remote_type"
            style="width: 100%"
            @change="validateTunnelPortFields"
          >
            <el-option label="单端口" value="single" />
            <el-option label="端口范围" value="range" />
          </el-select>
        </el-form-item>
        <el-form-item label="远端端口" prop="remote_start">
          <el-input-number
            v-model="tunnelForm.remote_start"
            :min="1"
            :max="65535"
            :controls="false"
            style="width: 100%"
            placeholder="端口号"
            @change="validateTunnelPortFields"
          />
        </el-form-item>
        <el-form-item v-if="tunnelForm.remote_type === 'range'" label="远端结束" prop="remote_end">
          <el-input-number
            v-model="tunnelForm.remote_end"
            :min="1"
            :max="65535"
            :controls="false"
            style="width: 100%"
            placeholder="结束端口"
            @change="validateTunnelPortFields"
          />
        </el-form-item>
        <el-form-item label="本地地址" prop="local_host">
          <el-input v-model="tunnelForm.local_host" placeholder="IP 或域名" />
        </el-form-item>
        <el-form-item label="本地端口" prop="local_start">
          <el-input-number
            v-model="tunnelForm.local_start"
            :min="1"
            :max="65535"
            :controls="false"
            style="width: 100%"
            placeholder="端口号"
            @change="validateTunnelPortFields"
          />
        </el-form-item>
        <el-form-item v-if="tunnelForm.remote_type === 'range'" label="本地结束" prop="local_end">
          <el-input-number
            v-model="tunnelForm.local_end"
            :min="1"
            :max="65535"
            :controls="false"
            style="width: 100%"
            placeholder="结束端口"
            @change="validateTunnelPortFields"
          />
        </el-form-item>
        <el-form-item label="启用" prop="enabled">
          <el-switch v-model="tunnelForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="tunnelDrawerVisible = false">取消</el-button>
        <el-button type="primary" :loading="tunnelSubmitting" @click="submitTunnelForm">
          {{ tunnelDrawerMode === 'create' ? '创建' : '保存' }}
        </el-button>
      </template>
    </el-drawer>
  </template>
