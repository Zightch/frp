<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  authApi,
  bindableTunnelsApi,
  ratePoliciesApi,
  type RatePolicy,
  type RatePolicyPayload,
  type RatePolicyMode,
  type RatePolicyUnit,
  type TunnelBinding
} from '@/api'
import { useMobile } from '@/composables/useMobile'
import RatePolicyBindingEditor from './rate-policy/RatePolicyBindingEditor.vue'
import RatePolicyFormDialog from './rate-policy/RatePolicyFormDialog.vue'

defineOptions({
  name: 'RatePolicyView'
})

const router = useRouter()
const { isMobile } = useMobile()

const checking = ref(true)
const authenticated = ref(false)

const policies = ref<RatePolicy[]>([])
const boundTunnels = ref<TunnelBinding[]>([])
const bindableTunnels = ref<TunnelBinding[]>([])
const selectedPolicyId = ref<number | null>(null)
const loading = ref(false)
const bindingsLoading = ref(false)
const error = ref('')

const modeFilter = ref<'all' | 'independent' | 'shared'>('all')

const policyDialogVisible = ref(false)
const policyDialogMode = ref<'create' | 'edit'>('create')
const policySubmitting = ref(false)

const bindDialogVisible = ref(false)
const bindSubmitting = ref(false)

const selectedPolicy = computed(() =>
  policies.value.find(policy => policy.id === selectedPolicyId.value)
)

const filteredPolicies = computed(() => {
  if (modeFilter.value === 'all') {
    return policies.value
  }
  return policies.value.filter(policy => policy.mode === modeFilter.value)
})

let loadDataGeneration = 0
let boundTunnelsGeneration = 0

watch(modeFilter, () => {
  if (filteredPolicies.value.some(policy => policy.id === selectedPolicyId.value)) {
    return
  }
  selectedPolicyId.value = null
  boundTunnels.value = []
  bindingsLoading.value = false
})

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

  if (!authResult.data.authenticated) {
    router.replace({ name: 'Login' })
    return
  }

  authenticated.value = true
  await loadData()
})

async function loadData() {
  const generation = ++loadDataGeneration
  loading.value = true
  error.value = ''

  try {
    const policiesResult = await ratePoliciesApi.list()
    if (generation !== loadDataGeneration) {
      return
    }
    if (policiesResult.error) {
      error.value = policiesResult.error
      return
    }

    policies.value = policiesResult.data?.items ?? []

    const bindableResult = await bindableTunnelsApi.list(policies.value)
    if (generation !== loadDataGeneration) {
      return
    }
    if (bindableResult.error) {
      ElMessage.warning(`可绑定隧道加载失败: ${bindableResult.error}`)
      bindableTunnels.value = []
    } else {
      bindableTunnels.value = bindableResult.data?.items ?? []
    }

    if (!policies.value.some(policy => policy.id === selectedPolicyId.value)) {
      selectedPolicyId.value = policies.value[0]?.id ?? null
    }

    if (!selectedPolicyId.value) {
      boundTunnels.value = []
      bindingsLoading.value = false
      return
    }

    await loadBoundTunnels(selectedPolicyId.value)
  } catch {
    if (generation === loadDataGeneration) {
      error.value = '加载数据失败'
    }
  } finally {
    if (generation === loadDataGeneration) {
      loading.value = false
    }
  }
}

async function loadBoundTunnels(policyId: number) {
  const generation = ++boundTunnelsGeneration
  bindingsLoading.value = true
  boundTunnels.value = []

  const result = await ratePoliciesApi.getTunnels(policyId)
  if (generation !== boundTunnelsGeneration || selectedPolicyId.value !== policyId) {
    if (generation === boundTunnelsGeneration) {
      bindingsLoading.value = false
    }
    return
  }

  bindingsLoading.value = false
  if (result.error) {
    ElMessage.error(result.error)
    boundTunnels.value = []
    return
  }

  boundTunnels.value = result.data?.items ?? []
}

function formatRate(value: number, unit: RatePolicyUnit): string {
  return `${value} ${unit}bps`
}

function formatMode(mode: RatePolicyMode): string {
  return mode === 'independent' ? '独享' : '共享'
}

function modeTagType(mode: RatePolicyMode): 'primary' | 'success' {
  return mode === 'independent' ? 'primary' : 'success'
}

function handlePolicyRowClick(row: RatePolicy) {
  if (selectedPolicyId.value === row.id) {
    return
  }

  selectedPolicyId.value = row.id
  void loadBoundTunnels(row.id)
}

function getPolicyRowClass({ row }: { row: RatePolicy }): string {
  return row.id === selectedPolicyId.value
    ? 'selectable-row selected-row'
    : 'selectable-row'
}

function openCreatePolicyDialog() {
  policyDialogMode.value = 'create'
  policyDialogVisible.value = true
}

function openEditPolicyDialog() {
  if (!selectedPolicy.value) {
    return
  }
  policyDialogMode.value = 'edit'
  policyDialogVisible.value = true
}

async function submitPolicyForm(payload: RatePolicyPayload) {
  policySubmitting.value = true

  try {
    if (policyDialogMode.value === 'create') {
      const result = await ratePoliciesApi.create(payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      selectedPolicyId.value = result.data?.item.id ?? selectedPolicyId.value
      ElMessage.success('策略创建成功')
    } else {
      if (!selectedPolicyId.value) {
        return
      }
      const result = await ratePoliciesApi.update(selectedPolicyId.value, payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
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

  try {
    await ElMessageBox.confirm(
      `确定删除策略"${policy.name}"吗？此操作不可恢复。`,
      '删除策略',
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

  const result = await ratePoliciesApi.delete(policy.id)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  if (selectedPolicyId.value === policy.id) {
    selectedPolicyId.value = null
    boundTunnels.value = []
    bindingsLoading.value = false
  }

  ElMessage.success('策略已删除')
  await loadData()
}

async function handleSubmitBindings(tunnelIds: number[]) {
  if (!selectedPolicyId.value) {
    return
  }

  bindSubmitting.value = true
  try {
    const result = await ratePoliciesApi.syncBindings(selectedPolicyId.value, tunnelIds)
    if (result.error) {
      ElMessage.error(result.error)
      return
    }

    ElMessage.success('绑定更新成功')
    bindDialogVisible.value = false
    await loadData()
  } finally {
    bindSubmitting.value = false
  }
}

async function handleUnbindTunnel(tunnel: TunnelBinding) {
  if (!selectedPolicyId.value) {
    return
  }

  try {
    await ElMessageBox.confirm(
      `确定解绑隧道"${tunnel.name}"吗？`,
      '解绑隧道',
      { type: 'warning' }
    )
  } catch {
    return
  }

  const tunnelIds = boundTunnels.value
    .filter(item => item.id !== tunnel.id)
    .map(item => item.id)

  const result = await ratePoliciesApi.syncBindings(selectedPolicyId.value, tunnelIds)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('隧道已解绑')
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
          <el-button :loading="loading" @click="loadData">刷新</el-button>
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
              <el-button size="small" @click="openEditPolicyDialog">编辑</el-button>
              <el-button size="small" type="danger" @click="handleDeletePolicy(selectedPolicy)">删除</el-button>
            </el-space>
          </el-col>
        </el-row>
      </el-card>

      <el-main class="content-main">
        <el-row :gutter="16" class="content-row">
          <el-col :xs="24" :md="7" :lg="6" class="list-col">
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
                highlight-current-row
                :row-class-name="getPolicyRowClass"
                v-loading="loading"
                class="policy-table"
                @row-click="handlePolicyRowClick"
              >
                <el-table-column prop="name" label="名称" />
                <el-table-column label="模式" width="70" align="center">
                  <template #default="{ row }">
                    <el-tag :type="modeTagType(row.mode)" size="small">
                      {{ formatMode(row.mode) }}
                    </el-tag>
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

          <el-col :xs="24" :md="17" :lg="18" :class="['list-col', { 'list-col-mobile': isMobile }]">
            <el-card class="list-card">
              <template #header>
                <el-row justify="space-between" align="middle">
                  <span class="list-title">
                    绑定隧道
                    <span v-if="selectedPolicy" class="tunnel-list-subtitle">（{{ selectedPolicy.name }}）</span>
                  </span>
                  <el-button
                    v-if="selectedPolicyId"
                    type="primary"
                    size="small"
                    :disabled="bindingsLoading"
                    @click="bindDialogVisible = true"
                  >
                    编辑绑定
                  </el-button>
                </el-row>
              </template>

              <el-empty v-if="!selectedPolicyId" description="请选择策略查看绑定隧道" />
              <el-empty v-else-if="!bindingsLoading && boundTunnels.length === 0" description="该策略暂无绑定隧道" />
              <el-table v-else :data="boundTunnels" height="100%" stripe v-loading="bindingsLoading">
                <el-table-column prop="name" label="隧道名称" min-width="160" />
                <el-table-column prop="protocol" label="类型" width="80" align="center">
                  <template #default="{ row }">
                    <el-tag size="small">{{ row.protocol.toUpperCase() }}</el-tag>
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

    <RatePolicyFormDialog
      v-model="policyDialogVisible"
      :mode="policyDialogMode"
      :policy="selectedPolicy"
      :submitting="policySubmitting"
      @submit="submitPolicyForm"
    />

    <RatePolicyBindingEditor
      v-if="selectedPolicyId && selectedPolicy"
      v-model="bindDialogVisible"
      :submitting="bindSubmitting"
      :is-mobile="isMobile"
      :policy-id="selectedPolicyId"
      :bound-tunnels="boundTunnels"
      :bindable-tunnels="bindableTunnels"
      @submit="handleSubmitBindings"
    />
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

.policy-info-label,
.tunnel-list-subtitle {
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

.content-row,
.list-col {
  height: 100%;
}

.list-col {
  display: flex;
}

.list-col-mobile {
  height: auto;
  margin-top: var(--spacing-base);
}

.list-card {
  width: 100%;
  display: flex;
  flex-direction: column;
}

.list-card :deep(.el-card__body) {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.list-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.filter-group {
  margin-bottom: var(--spacing-sm);
}

.policy-table {
  flex: 1;
  min-height: 0;
}

.tunnel-count {
  font-weight: 500;
}

:deep(.selectable-row) {
  cursor: pointer;
}

:deep(.selected-row) {
  background-color: var(--el-color-primary-light-9);
}

.policy-info-bar-mobile .policy-info-item {
  padding-right: 0;
}

.policy-info-bar-mobile .policy-info-item + .policy-info-item {
  padding-left: 0;
  border-left: none;
}
</style>
