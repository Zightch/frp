<script setup lang="ts">
import { ref, computed, onMounted, nextTick, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { ArrowRight } from '@element-plus/icons-vue'
import {
  authApi,
  ratePoliciesApi,
  bindableTunnelsApi,
  type RatePolicy,
  type RatePolicyPayload,
  type RatePolicyMode,
  type RatePolicyUnit,
  type TunnelBinding
} from '@/api'
import { useMobile } from '@/composables/useMobile'

defineOptions({
  name: 'RatePolicyView'
})

const router = useRouter()
const { isMobile } = useMobile()

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data
const policies = ref<RatePolicy[]>([])
const boundTunnels = ref<TunnelBinding[]>([])
const bindableTunnels = ref<TunnelBinding[]>([])
const selectedPolicyId = ref<number | null>(null)
const loading = ref(false)
const error = ref('')

// Filter
const modeFilter = ref<'all' | 'independent' | 'shared'>('all')

// Policy dialog
const policyDialogVisible = ref(false)
const policyDialogMode = ref<'create' | 'edit'>('create')
const policyFormRef = ref<FormInstance>()
const policySubmitting = ref(false)
const policyForm = ref<{
  name: string
  mode: RatePolicyMode
  downlink_value: number | null
  downlink_unit: RatePolicyUnit
  uplink_value: number | null
  uplink_unit: RatePolicyUnit
}>({
  name: '',
  mode: 'independent',
  downlink_value: null,
  downlink_unit: 'M',
  uplink_value: null,
  uplink_unit: 'M'
})

// Bind dialog
const bindDialogVisible = ref(false)
const bindSubmitting = ref(false)
const bindTabActive = ref<string>('')
const bindLeftSelected = ref<number[]>([])
const bindRightSelected = ref<number[]>([])
const bindCart = ref<TunnelBinding[]>([])

// Computed
const selectedPolicy = computed(() =>
  policies.value.find(p => p.id === selectedPolicyId.value)
)

const filteredPolicies = computed(() => {
  if (modeFilter.value === 'all') {
    return policies.value
  }
  return policies.value.filter(p => p.mode === modeFilter.value)
})

const bindableGroups = computed(() => {
  const groupMap = new Map<number, { id: number; name: string; tunnels: TunnelBinding[] }>()
  for (const tunnel of bindableTunnels.value) {
    if (tunnel.rate_policy_id === selectedPolicyId.value) {
      continue
    }
    if (!groupMap.has(tunnel.group_id)) {
      groupMap.set(tunnel.group_id, {
        id: tunnel.group_id,
        name: tunnel.group_name,
        tunnels: []
      })
    }
    groupMap.get(tunnel.group_id)!.tunnels.push(tunnel)
  }
  return Array.from(groupMap.values()).sort((a, b) => a.name.localeCompare(b.name))
})

const currentGroupTunnels = computed(() => {
  const group = bindableGroups.value.find(g => g.name === bindTabActive.value)
  return group?.tunnels ?? []
})

const cartTunnelIds = computed(() => new Set(bindCart.value.map(t => t.id)))

watch(modeFilter, () => {
  if (!filteredPolicies.value.some(policy => policy.id === selectedPolicyId.value)) {
    selectedPolicyId.value = null
    boundTunnels.value = []
  }
})

// Lifecycle
onMounted(async () => {
  const authResult = await authApi.state()
  checking.value = false

  if (!authResult.data) {
    error.value = '无法检查认证状态'
    return
  }

  if (!authResult.data.initialized) {
    router.replace({ name: 'Init' })
    return
  }

  initialized.value = true

  if (!authResult.data.authenticated) {
    router.replace({ name: 'Login' })
    return
  }

  authenticated.value = true
  await loadData()
})

async function loadData() {
  loading.value = true
  error.value = ''

  try {
    const policiesResult = await ratePoliciesApi.list()

    if (policiesResult.error) {
      error.value = policiesResult.error
      return
    }

    policies.value = policiesResult.data?.items ?? []

    const bindableResult = await bindableTunnelsApi.list(policies.value)
    if (bindableResult.error) {
      ElMessage.warning(`可绑定隧道加载失败: ${bindableResult.error}`)
      bindableTunnels.value = []
    } else {
      bindableTunnels.value = bindableResult.data?.items ?? []
    }

    if (!policies.value.some(p => p.id === selectedPolicyId.value)) {
      selectedPolicyId.value = policies.value[0]?.id ?? null
    }

    if (selectedPolicyId.value) {
      await loadBoundTunnels(selectedPolicyId.value)
    }
  } catch {
    error.value = '加载数据失败'
  } finally {
    loading.value = false
  }
}

async function loadBoundTunnels(policyId: number) {
  const result = await ratePoliciesApi.getTunnels(policyId)
  if (result.error) {
    ElMessage.error(result.error)
    boundTunnels.value = []
  } else {
    boundTunnels.value = result.data?.items ?? []
  }
}

// Format helpers
function formatRate(value: number, unit: RatePolicyUnit): string {
  return `${value} ${unit}bps`
}

function formatMode(mode: RatePolicyMode): string {
  return mode === 'independent' ? '独享' : '共享'
}

function modeTagType(mode: RatePolicyMode): 'primary' | 'success' {
  return mode === 'independent' ? 'primary' : 'success'
}

function formatRemotePort(tunnel: TunnelBinding): string {
  return String(tunnel.remote_start)
}

// Policy selection
function handlePolicyRowClick(row: RatePolicy) {
  selectedPolicyId.value = row.id
  loadBoundTunnels(row.id)
}

function getPolicyRowClass({ row }: { row: RatePolicy }): string {
  return row.id === selectedPolicyId.value
    ? 'selectable-row selected-row'
    : 'selectable-row'
}

// Policy CRUD
function openCreatePolicyDialog() {
  policyDialogMode.value = 'create'
  policyForm.value = {
    name: '',
    mode: 'independent',
    downlink_value: null,
    downlink_unit: 'M',
    uplink_value: null,
    uplink_unit: 'M'
  }
  policyDialogVisible.value = true
  nextTick(() => policyFormRef.value?.clearValidate())
}

function openEditPolicyDialog(policy: RatePolicy) {
  policyDialogMode.value = 'edit'
  policyForm.value = {
    name: policy.name,
    mode: policy.mode,
    downlink_value: policy.downlink_value,
    downlink_unit: policy.downlink_unit,
    uplink_value: policy.uplink_value,
    uplink_unit: policy.uplink_unit
  }
  policyDialogVisible.value = true
  nextTick(() => policyFormRef.value?.clearValidate())
}

const policyFormRules: FormRules<typeof policyForm.value> = {
  name: [{ required: true, message: '请输入策略名称', trigger: 'blur' }],
  mode: [{ required: true, message: '请选择模式', trigger: 'change' }],
  downlink_value: [{ required: true, message: '请输入下行速率', trigger: 'blur' }],
  uplink_value: [{ required: true, message: '请输入上行速率', trigger: 'blur' }]
}

async function submitPolicyForm() {
  const valid = await policyFormRef.value?.validate().catch(() => false)
  if (!valid) return

  policySubmitting.value = true

  try {
    const payload: RatePolicyPayload = {
      name: policyForm.value.name.trim(),
      mode: policyForm.value.mode,
      downlink_value: policyForm.value.downlink_value as number,
      downlink_unit: policyForm.value.downlink_unit,
      uplink_value: policyForm.value.uplink_value as number,
      uplink_unit: policyForm.value.uplink_unit
    }

    if (policyDialogMode.value === 'create') {
      const result = await ratePoliciesApi.create(payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      selectedPolicyId.value = result.data?.item.id ?? selectedPolicyId.value
      ElMessage.success('策略创建成功')
    } else {
      if (!selectedPolicyId.value) return
      const result = await ratePoliciesApi.update(selectedPolicyId.value, payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      selectedPolicyId.value = result.data?.item.id ?? selectedPolicyId.value
      ElMessage.success('策略更新成功')
    }

    policyDialogVisible.value = false
    await loadData()
  } finally {
    policySubmitting.value = false
  }
}

async function handleDeletePolicy(policy: RatePolicy) {
  if (policy.tunnel_count > 0) {
    ElMessage.warning(`策略"${policy.name}"仍绑定 ${policy.tunnel_count} 条隧道，请先解绑或迁移后再删除`)
    return
  }

  const message = `确定删除策略"${policy.name}"吗？此操作不可恢复。`

  try {
    await ElMessageBox.confirm(message, '删除策略', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
      confirmButtonClass: 'el-button--danger'
    })
  } catch {
    return
  }

  const result = await ratePoliciesApi.delete(policy.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  if (selectedPolicyId.value === policy.id) {
    selectedPolicyId.value = null
    boundTunnels.value = []
  }
  ElMessage.success('策略已删除')
  await loadData()
}

// Bind dialog
function openBindDialog() {
  if (!selectedPolicyId.value) return

  // Reset state
  bindCart.value = []
  bindLeftSelected.value = []
  bindRightSelected.value = []

  // Set first group as active
  const groups = bindableGroups.value
  bindTabActive.value = groups[0]?.name ?? ''

  bindDialogVisible.value = true
}

function handleAddToCart() {
  if (bindLeftSelected.value.length === 0) return

  const toAdd = currentGroupTunnels.value.filter(t =>
    bindLeftSelected.value.includes(t.id) && !cartTunnelIds.value.has(t.id)
  )

  bindCart.value.push(...toAdd)
  bindLeftSelected.value = []
}

function handleRemoveFromCart(tunnelId: number) {
  bindCart.value = bindCart.value.filter(t => t.id !== tunnelId)
  bindRightSelected.value = bindRightSelected.value.filter(id => id !== tunnelId)
}

function handleRemoveSelectedFromCart() {
  bindCart.value = bindCart.value.filter(t => !bindRightSelected.value.includes(t.id))
  bindRightSelected.value = []
}

function handleSelectAllLeft() {
  bindLeftSelected.value = currentGroupTunnels.value
    .filter(t => !cartTunnelIds.value.has(t.id))
    .map(t => t.id)
}

function handleClearLeftSelection() {
  bindLeftSelected.value = []
}

function handleSelectAllRight() {
  bindRightSelected.value = bindCart.value.map(t => t.id)
}

function handleClearRightSelection() {
  bindRightSelected.value = []
}

function isTunnelInCart(tunnelId: number): boolean {
  return cartTunnelIds.value.has(tunnelId)
}

async function handleConfirmBind() {
  if (bindCart.value.length === 0) {
    ElMessage.warning('请选择要绑定的隧道')
    return
  }

  const alreadyBound = bindCart.value.filter(t => t.rate_policy_id && t.rate_policy_id !== selectedPolicyId.value)
  if (alreadyBound.length > 0) {
    try {
      await ElMessageBox.confirm(
        `选中的 ${alreadyBound.length} 条隧道已绑定其他策略，确认后会先从原策略解绑，再迁移到当前策略。是否继续？`,
        '确认绑定',
        { type: 'warning' }
      )
    } catch {
      return
    }
  }

  bindSubmitting.value = true

  try {
    const result = await ratePoliciesApi.bindTunnels(selectedPolicyId.value!, bindCart.value)

    if (result.error) {
      ElMessage.error(result.error)
      return
    }

    ElMessage.success(`成功绑定 ${bindCart.value.length} 条隧道`)
    bindDialogVisible.value = false
    await loadBoundTunnels(selectedPolicyId.value!)
    await loadData()
  } finally {
    bindSubmitting.value = false
  }
}

async function handleUnbindTunnel(tunnel: TunnelBinding) {
  try {
    await ElMessageBox.confirm(
      `确定解绑隧道"${tunnel.name}"吗？`,
      '解绑隧道',
      { type: 'warning' }
    )
  } catch {
    return
  }

  const result = await ratePoliciesApi.unbindTunnel(selectedPolicyId.value!, tunnel.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('隧道已解绑')
  await loadBoundTunnels(selectedPolicyId.value!)
  await loadData()
}
</script>

<template>
  <el-container
    v-loading="checking"
    element-loading-text="检查认证状态..."
    direction="vertical"
    class="rate-policy-view"
  >
    <el-result v-if="error" icon="error" :title="error">
      <template #extra>
        <el-button type="primary" @click="loadData">重试</el-button>
      </template>
    </el-result>

    <template v-else-if="authenticated">
      <div class="page-header">
        <el-row justify="space-between" align="middle">
          <h1>限速策略</h1>
          <el-button @click="loadData" :loading="loading">刷新</el-button>
        </el-row>
      </div>

      <el-card v-if="selectedPolicy" class="selected-policy-card">
        <el-row justify="space-between" align="middle" :gutter="12">
          <el-col :xs="24" :lg="16">
            <el-space
              wrap
              alignment="center"
              size="small"
              :class="['policy-info-bar', { 'policy-info-bar-mobile': isMobile }]"
            >
              <span class="policy-info-item">
                <span class="policy-info-label">策略</span>
                <span class="policy-info-value">{{ selectedPolicy.name }}</span>
              </span>
              <span class="policy-info-item">
                <span class="policy-info-label">模式</span>
                <span class="policy-info-value">
                  <el-tag :type="modeTagType(selectedPolicy.mode)" size="small">
                    {{ formatMode(selectedPolicy.mode) }}
                  </el-tag>
                </span>
              </span>
              <span class="policy-info-item">
                <span class="policy-info-label">下行</span>
                <span class="policy-info-value">{{ formatRate(selectedPolicy.downlink_value, selectedPolicy.downlink_unit) }}</span>
              </span>
              <span class="policy-info-item">
                <span class="policy-info-label">上行</span>
                <span class="policy-info-value">{{ formatRate(selectedPolicy.uplink_value, selectedPolicy.uplink_unit) }}</span>
              </span>
              <span class="policy-info-item">
                <span class="policy-info-label">绑定隧道</span>
                <span class="policy-info-value">{{ selectedPolicy.tunnel_count }} 条</span>
              </span>
            </el-space>
          </el-col>
          <el-col :xs="24" :lg="8">
            <el-space
              wrap
              alignment="center"
              :class="['policy-actions', { 'policy-actions-mobile': isMobile }]"
            >
              <el-button size="small" @click="openEditPolicyDialog(selectedPolicy)">编辑</el-button>
              <el-button size="small" type="danger" @click="handleDeletePolicy(selectedPolicy)">删除</el-button>
            </el-space>
          </el-col>
        </el-row>
      </el-card>

      <el-main class="content-main">
        <el-row :gutter="16" class="content-row">
          <el-col :xs="24" :md="7" :lg="6" :class="['list-col', { 'list-col-mobile': isMobile }]">
            <el-card class="list-card">
              <template #header>
                <el-row justify="space-between" align="middle">
                  <span class="list-title">策略列表</span>
                  <el-button type="primary" size="small" @click="openCreatePolicyDialog">新建策略</el-button>
                </el-row>
              </template>

              <el-radio-group v-model="modeFilter" size="small" class="filter-group">
                <el-radio-button value="all">全部</el-radio-button>
                <el-radio-button value="shared">共享</el-radio-button>
                <el-radio-button value="independent">独享</el-radio-button>
              </el-radio-group>

              <el-empty v-if="filteredPolicies.length === 0" description="暂无策略，请新建" />
              <el-table
                v-else
                :data="filteredPolicies"
                height="100%"
                stripe
                @row-click="handlePolicyRowClick"
                :row-class-name="getPolicyRowClass"
                highlight-current-row
                v-loading="loading"
                class="policy-table"
              >
                <el-table-column prop="name" label="名称" />
                <el-table-column label="模式" width="70" align="center">
                  <template #default="{ row }">
                    <el-tag :type="modeTagType(row.mode)" size="small">{{ formatMode(row.mode) }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column label="绑定" width="60" align="center">
                  <template #default="{ row }">
                    <span class="tunnel-count">{{ row.tunnel_count }}</span>
                  </template>
                </el-table-column>
              </el-table>
            </el-card>
          </el-col>

          <el-col
            :xs="24"
            :md="17"
            :lg="18"
            :class="['list-col', 'tunnel-list-col', { 'list-col-mobile': isMobile, 'tunnel-list-col-mobile': isMobile }]"
          >
            <el-card class="list-card">
              <template #header>
                <el-row justify="space-between" align="middle">
                  <span class="list-title">
                    绑定隧道
                    <span v-if="selectedPolicy" class="tunnel-list-subtitle">（{{ selectedPolicy.name }}）</span>
                  </span>
                  <el-button v-if="selectedPolicyId" type="primary" size="small" @click="openBindDialog">绑定隧道</el-button>
                </el-row>
              </template>

              <el-empty v-if="!selectedPolicyId" description="请选择策略查看绑定隧道" />
              <el-empty v-else-if="boundTunnels.length === 0" description="该策略暂无绑定隧道" />
              <el-table v-else :data="boundTunnels" height="100%" stripe>
                <el-table-column prop="group_name" label="分组" width="120" />
                <el-table-column prop="name" label="隧道名称" width="120" />
                <el-table-column prop="protocol" label="协议" width="70" align="center">
                  <template #default="{ row }">
                    <el-tag size="small">{{ row.protocol.toUpperCase() }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column label="远端" width="100">
                  <template #default="{ row }">
                    {{ formatRemotePort(row) }}
                  </template>
                </el-table-column>
                <el-table-column label="操作" width="80" align="center">
                  <template #default="{ row }">
                    <el-button link type="danger" size="small" @click="handleUnbindTunnel(row)">解绑</el-button>
                  </template>
                </el-table-column>
              </el-table>
            </el-card>
          </el-col>
        </el-row>
      </el-main>
    </template>

    <!-- Create/Edit Policy Dialog -->
    <el-dialog
      v-model="policyDialogVisible"
      :title="policyDialogMode === 'create' ? '新建策略' : '编辑策略'"
      width="400px"
      :close-on-click-modal="false"
    >
      <el-form
        ref="policyFormRef"
        :model="policyForm"
        :rules="policyFormRules"
        label-width="80px"
        class="policy-form"
      >
        <el-form-item label="名称" prop="name">
          <el-input v-model="policyForm.name" placeholder="请输入策略名称" />
        </el-form-item>
        <el-form-item label="模式" prop="mode">
          <el-select
            v-model="policyForm.mode"
            class="full-width"
            :disabled="policyDialogMode === 'edit'"
          >
            <el-option label="独享" value="independent" />
            <el-option label="共享" value="shared" />
          </el-select>
        </el-form-item>

        <el-divider content-position="left">下行速率</el-divider>
        <el-form-item prop="downlink_value">
          <el-row :gutter="8">
            <el-col :span="16">
              <el-input-number
                v-model="policyForm.downlink_value"
                :min="1"
                :max="policyForm.downlink_unit === 'K' ? 999999 : policyForm.downlink_unit === 'M' ? 999 : 99"
                :controls="false"
                class="full-width"
                placeholder="速率"
              />
            </el-col>
            <el-col :span="8">
              <el-select v-model="policyForm.downlink_unit">
                <el-option label="Kbps" value="K" />
                <el-option label="Mbps" value="M" />
                <el-option label="Gbps" value="G" />
              </el-select>
            </el-col>
          </el-row>
        </el-form-item>

        <el-divider content-position="left">上行速率</el-divider>
        <el-form-item prop="uplink_value">
          <el-row :gutter="8">
            <el-col :span="16">
              <el-input-number
                v-model="policyForm.uplink_value"
                :min="1"
                :max="policyForm.uplink_unit === 'K' ? 999999 : policyForm.uplink_unit === 'M' ? 999 : 99"
                :controls="false"
                class="full-width"
                placeholder="速率"
              />
            </el-col>
            <el-col :span="8">
              <el-select v-model="policyForm.uplink_unit">
                <el-option label="Kbps" value="K" />
                <el-option label="Mbps" value="M" />
                <el-option label="Gbps" value="G" />
              </el-select>
            </el-col>
          </el-row>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="policyDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="policySubmitting" @click="submitPolicyForm">
          {{ policyDialogMode === 'create' ? '创建' : '保存' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Bind Tunnel Dialog (Shopping Cart Mode) -->
    <el-dialog
      v-model="bindDialogVisible"
      title="绑定隧道"
      width="1000px"
      :close-on-click-modal="false"
      class="bind-dialog"
    >
      <div class="bind-container">
        <!-- Left: Group Tabs + Tunnel Pool -->
        <div class="bind-left">
          <el-tabs v-model="bindTabActive" tab-position="left" class="group-tabs">
            <el-tab-pane
              v-for="group in bindableGroups"
              :key="group.id"
              :label="group.name"
              :name="group.name"
            >
              <template #label>
                <span class="group-tab-label">{{ group.name }}</span>
              </template>
            </el-tab-pane>
          </el-tabs>

          <div class="tunnel-pool">
            <el-table
              :data="currentGroupTunnels"
              height="100%"
              @selection-change="(rows: TunnelBinding[]) => bindLeftSelected = rows.map(r => r.id)"
            >
              <el-table-column type="selection" width="40" :selectable="(row: TunnelBinding) => !isTunnelInCart(row.id)" />
              <el-table-column prop="name" label="隧道名称" />
              <el-table-column prop="protocol" label="协议" width="60" align="center">
                <template #default="{ row }">
                  <el-tag size="small">{{ row.protocol.toUpperCase() }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="远端" width="80">
                <template #default="{ row }">
                  {{ formatRemotePort(row) }}
                </template>
              </el-table-column>
              <el-table-column label="当前策略" min-width="100">
                <template #default="{ row }">
                  <span v-if="isTunnelInCart(row.id)" class="in-cart-label">已添加</span>
                  <span v-else-if="row.rate_policy_name">{{ row.rate_policy_name }}</span>
                  <span v-else class="no-policy">-</span>
                </template>
              </el-table-column>
            </el-table>

            <div class="pool-actions">
              <el-button size="small" @click="handleSelectAllLeft">全选</el-button>
              <el-button size="small" @click="handleClearLeftSelection">清空</el-button>
            </div>
          </div>
        </div>

        <!-- Middle: Add Button -->
        <div class="bind-middle">
          <el-button
            type="primary"
            :icon="ArrowRight"
            :disabled="bindLeftSelected.length === 0"
            @click="handleAddToCart"
          >
            添加
          </el-button>
        </div>

        <!-- Right: Cart -->
        <div class="bind-right">
          <div class="cart-header">
            <span class="cart-title">已选隧道 ({{ bindCart.length }})</span>
          </div>

          <el-table
            :data="bindCart"
            height="100%"
            @selection-change="(rows: TunnelBinding[]) => bindRightSelected = rows.map(r => r.id)"
          >
            <el-table-column type="selection" width="40" />
            <el-table-column prop="group_name" label="分组" width="100" />
            <el-table-column prop="name" label="隧道名称" />
            <el-table-column prop="protocol" label="协议" width="60" align="center">
              <template #default="{ row }">
                <el-tag size="small">{{ row.protocol.toUpperCase() }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="远端" width="80">
              <template #default="{ row }">
                {{ formatRemotePort(row) }}
              </template>
            </el-table-column>
            <el-table-column label="操作" width="70" align="center">
              <template #default="{ row }">
                <el-button link type="danger" size="small" @click="handleRemoveFromCart(row.id)">移除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div class="cart-actions">
            <el-button size="small" @click="handleSelectAllRight">全选</el-button>
            <el-button size="small" @click="handleClearRightSelection">清空</el-button>
            <el-button size="small" type="danger" :disabled="bindRightSelected.length === 0" @click="handleRemoveSelectedFromCart">移除</el-button>
          </div>
        </div>
      </div>

      <template #footer>
        <div class="bind-footer">
          <span class="selected-count">已选 {{ bindCart.length }} 条</span>
          <div>
            <el-button @click="bindDialogVisible = false">取消</el-button>
            <el-button type="primary" :loading="bindSubmitting" @click="handleConfirmBind">确认绑定</el-button>
          </div>
        </div>
      </template>
    </el-dialog>
  </el-container>
</template>

<style scoped>
.rate-policy-view {
  height: 100%;
}

.page-header {
  padding: 0;
}

.selected-policy-card {
  margin-top: var(--spacing-base);
}

.selected-policy-card :deep(.el-card__body) {
  padding: var(--spacing-md) var(--spacing-lg);
}

.policy-info-bar {
  width: 100%;
  row-gap: var(--spacing-sm);
}

.policy-info-item {
  display: inline-flex;
  align-items: center;
  gap: var(--spacing-sm);
  min-height: 32px;
  padding-right: var(--spacing-lg);
}

.policy-info-item + .policy-info-item {
  border-left: 1px solid var(--el-border-color-light);
  padding-left: var(--spacing-lg);
}

.policy-info-label {
  color: var(--el-text-color-secondary);
}

.policy-info-value {
  display: inline-flex;
  align-items: center;
}

.policy-actions {
  width: 100%;
  justify-content: flex-end;
}

.policy-actions-mobile {
  justify-content: flex-start;
}

.content-main {
  padding: var(--spacing-base) 0 0 0;
  flex: 1;
  overflow: hidden;
}

.content-row {
  height: 100%;
}

.list-col {
  height: 100%;
  display: flex;
}

.tunnel-list-col {
  margin-top: 0;
}

.list-card {
  width: 100%;
  display: flex;
  flex-direction: column;
}

.list-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.tunnel-list-subtitle {
  font-weight: 400;
  color: var(--el-text-color-secondary);
}

.filter-group {
  margin-bottom: var(--spacing-sm);
}

.policy-table {
  margin-top: var(--spacing-sm);
}

.tunnel-count {
  font-weight: 500;
}

/* Table row styles */
:deep(.selectable-row) {
  cursor: pointer;
}

:deep(.selected-row) {
  background-color: var(--el-color-primary-light-9);
}

/* Policy form styles */
.policy-form :deep(.el-form-item__label) {
  white-space: nowrap;
}

.full-width {
  width: 100%;
}

/* Bind Dialog Styles */
.bind-dialog :deep(.el-dialog__body) {
  padding: var(--spacing-md);
}

.bind-container {
  display: flex;
  gap: var(--spacing-md);
  height: 450px;
}

.bind-left {
  display: flex;
  flex: 0 0 460px;
  gap: var(--spacing-sm);
}

.group-tabs {
  flex: 0 0 140px;
}

.group-tabs :deep(.el-tabs__header) {
  margin-right: 0;
}

.group-tabs :deep(.el-tabs__nav-wrap) {
  height: 100%;
}

.group-tabs :deep(.el-tabs__nav-scroll) {
  height: 100%;
  overflow-y: auto;
}

.group-tabs :deep(.el-tabs__nav) {
  flex-direction: column;
  height: auto;
}

.group-tab-label {
  display: block;
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tunnel-pool {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.pool-actions {
  padding-top: var(--spacing-sm);
  display: flex;
  gap: var(--spacing-sm);
}

.bind-middle {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 80px;
}

.bind-right {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.cart-header {
  padding-bottom: var(--spacing-sm);
}

.cart-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.cart-actions {
  padding-top: var(--spacing-sm);
  display: flex;
  gap: var(--spacing-sm);
}

.in-cart-label {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.no-policy {
  color: var(--el-text-color-placeholder);
}

.bind-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.selected-count {
  color: var(--el-text-color-secondary);
}

/* Mobile Styles */
.policy-info-bar-mobile .policy-info-item {
  padding-right: 0;
}

.policy-info-bar-mobile .policy-info-item + .policy-info-item {
  padding-left: 0;
  border-left: none;
}

.list-col-mobile {
  height: auto;
}

.tunnel-list-col-mobile {
  margin-top: var(--spacing-base);
}
</style>
