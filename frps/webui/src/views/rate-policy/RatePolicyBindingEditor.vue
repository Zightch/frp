<script setup lang="ts">
import { ArrowRight } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { computed, ref, watch } from 'vue'
import type { TunnelBinding } from '@/api'

defineOptions({
  name: 'RatePolicyBindingEditor'
})

type PolicyRef = {
  policy_id: number
  policy_name: string
}

type BindableGroup = {
  id: number
  name: string
  tunnels: TunnelBinding[]
}

const props = defineProps<{
  modelValue: boolean
  submitting: boolean
  isMobile: boolean
  policyId: number
  policyName: string
  boundTunnels: TunnelBinding[]
  bindableTunnels: TunnelBinding[]
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: boolean): void
  (event: 'submit', tunnelIds: number[]): void
}>()

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit('update:modelValue', value)
})

const bindTabActive = ref('')
const bindLeftSelected = ref<number[]>([])
const bindRightSelected = ref<number[]>([])
const bindCart = ref<TunnelBinding[]>([])
const originalBoundIds = ref(new Set<number>())
const originalOtherPolicyMap = ref(new Map<number, PolicyRef>())

const cartTunnelIds = computed(() => new Set(bindCart.value.map(tunnel => tunnel.id)))

const bindableGroups = computed<BindableGroup[]>(() => {
  const groupMap = new Map<number, BindableGroup>()
  for (const tunnel of props.bindableTunnels) {
    if (cartTunnelIds.value.has(tunnel.id)) {
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

  return Array.from(groupMap.values()).sort((left, right) => left.name.localeCompare(right.name))
})

const currentGroupTunnels = computed(() => {
  const group = bindableGroups.value.find(item => item.name === bindTabActive.value)
  return group?.tunnels ?? []
})

const shellProps = computed(() => props.isMobile
  ? {
      direction: 'btt',
      size: '92%',
      class: 'bind-drawer'
    }
  : {
      width: '1000px',
      class: 'bind-dialog'
    }
)

watch(
  () => [props.modelValue, props.policyId] as const,
  ([open]) => {
    if (!open) {
      return
    }
    initializeDialog()
  },
  { immediate: true }
)

watch(bindableGroups, (groups) => {
  if (groups.length === 0) {
    bindTabActive.value = ''
    return
  }
  if (!groups.some(group => group.name === bindTabActive.value)) {
    bindTabActive.value = groups[0].name
  }
})

function initializeDialog() {
  bindLeftSelected.value = []
  bindRightSelected.value = []
  bindCart.value = [...props.boundTunnels]
  originalBoundIds.value = new Set(props.boundTunnels.map(tunnel => tunnel.id))
  originalOtherPolicyMap.value = new Map()

  for (const tunnel of props.bindableTunnels) {
    if (tunnel.rate_policy_id && tunnel.rate_policy_id !== props.policyId && tunnel.rate_policy_name) {
      originalOtherPolicyMap.value.set(tunnel.id, {
        policy_id: tunnel.rate_policy_id,
        policy_name: tunnel.rate_policy_name
      })
    }
  }

  bindTabActive.value = bindableGroups.value[0]?.name ?? ''
}

function isOriginallyBound(tunnelId: number): boolean {
  return originalBoundIds.value.has(tunnelId)
}

function getOriginalOtherPolicy(tunnelId: number): PolicyRef | undefined {
  return originalOtherPolicyMap.value.get(tunnelId)
}

function canSelectTunnel(tunnel: TunnelBinding): boolean {
  return !tunnel.rate_policy_id || tunnel.rate_policy_id === props.policyId
}

function getTunnelPolicyStatus(tunnel: TunnelBinding): string {
  if (isOriginallyBound(tunnel.id) && !cartTunnelIds.value.has(tunnel.id)) {
    return '提交后解绑'
  }

  const otherPolicy = getOriginalOtherPolicy(tunnel.id)
  if (otherPolicy) {
    return otherPolicy.policy_name
  }

  return '-'
}

function getCartTunnelSource(tunnel: TunnelBinding): string {
  if (isOriginallyBound(tunnel.id)) {
    return '原本属于当前策略'
  }

  const otherPolicy = getOriginalOtherPolicy(tunnel.id)
  if (otherPolicy) {
    return `从 ${otherPolicy.policy_name} 迁移`
  }

  return '新绑定'
}

function handleAddToCart() {
  if (bindLeftSelected.value.length === 0) {
    return
  }

  const toAdd = currentGroupTunnels.value.filter(tunnel =>
    bindLeftSelected.value.includes(tunnel.id) && !cartTunnelIds.value.has(tunnel.id)
  )

  bindCart.value.push(...toAdd)
  bindLeftSelected.value = []
}

function handleMigrateTunnel(tunnel: TunnelBinding) {
  if (cartTunnelIds.value.has(tunnel.id)) {
    return
  }
  bindCart.value.push(tunnel)
}

function handleRemoveFromCart(tunnelId: number) {
  bindCart.value = bindCart.value.filter(tunnel => tunnel.id !== tunnelId)
  bindRightSelected.value = bindRightSelected.value.filter(id => id !== tunnelId)
}

function handleRemoveSelectedFromCart() {
  bindCart.value = bindCart.value.filter(tunnel => !bindRightSelected.value.includes(tunnel.id))
  bindRightSelected.value = []
}

function handleSelectAllLeft() {
  bindLeftSelected.value = currentGroupTunnels.value
    .filter(tunnel => canSelectTunnel(tunnel) && !cartTunnelIds.value.has(tunnel.id))
    .map(tunnel => tunnel.id)
}

function handleSelectAllRight() {
  bindRightSelected.value = bindCart.value.map(tunnel => tunnel.id)
}

function handleSubmit() {
  const cartIds = new Set(bindCart.value.map(tunnel => tunnel.id))
  const hasChanges =
    bindCart.value.length !== originalBoundIds.value.size ||
    bindCart.value.some(tunnel => !originalBoundIds.value.has(tunnel.id)) ||
    [...originalBoundIds.value].some(id => !cartIds.has(id))

  if (!hasChanges) {
    ElMessage.info('绑定未发生变化')
    visible.value = false
    return
  }

  const migratedCount = bindCart.value.filter(tunnel =>
    !isOriginallyBound(tunnel.id) && getOriginalOtherPolicy(tunnel.id)
  ).length
  const removedCount = [...originalBoundIds.value].filter(id => !cartIds.has(id)).length

  const confirmSubmit = async () => {
    if (migratedCount === 0 && removedCount === 0) {
      emit('submit', bindCart.value.map(tunnel => tunnel.id))
      return
    }

    const messages: string[] = []
    if (migratedCount > 0) {
      messages.push(`迁移 ${migratedCount} 条隧道`)
    }
    if (removedCount > 0) {
      messages.push(`解绑 ${removedCount} 条隧道`)
    }

    try {
      await ElMessageBox.confirm(
        `本次操作将${messages.join('，')}，是否继续？`,
        '确认绑定',
        { type: 'warning' }
      )
    } catch {
      return
    }

    emit('submit', bindCart.value.map(tunnel => tunnel.id))
  }

  void confirmSubmit()
}
</script>

<template>
  <component
    :is="isMobile ? 'el-drawer' : 'el-dialog'"
    v-model="visible"
    title="编辑绑定"
    :close-on-click-modal="false"
    v-bind="shellProps"
  >
    <div :class="['bind-container', { 'bind-container-mobile': isMobile }]">
      <div :class="['bind-left', { 'bind-left-mobile': isMobile }]">
        <el-tabs
          v-model="bindTabActive"
          :tab-position="isMobile ? 'top' : 'left'"
          class="group-tabs"
        >
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
            @selection-change="(rows: TunnelBinding[]) => bindLeftSelected = rows.map(row => row.id)"
          >
            <el-table-column
              type="selection"
              width="40"
              :selectable="(row: TunnelBinding) => canSelectTunnel(row)"
            />
            <el-table-column prop="name" label="隧道名称" />
            <el-table-column prop="protocol" label="协议" width="60" align="center">
              <template #default="{ row }">
                <el-tag size="small">{{ row.protocol.toUpperCase() }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="远端" width="80">
              <template #default="{ row }">
                {{ row.remote_start }}
              </template>
            </el-table-column>
            <el-table-column label="当前策略" min-width="100">
              <template #default="{ row }">
                <span :class="{ 'will-unbind': getTunnelPolicyStatus(row) === '提交后解绑' }">
                  {{ getTunnelPolicyStatus(row) }}
                </span>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="70" align="center">
              <template #default="{ row }">
                <el-button
                  v-if="getOriginalOtherPolicy(row.id)"
                  link
                  type="primary"
                  size="small"
                  @click="handleMigrateTunnel(row)"
                >
                  迁移
                </el-button>
              </template>
            </el-table-column>
          </el-table>

          <el-space wrap size="small" class="action-row">
            <el-button size="small" @click="handleSelectAllLeft">全选</el-button>
            <el-button size="small" @click="bindLeftSelected = []">清空</el-button>
          </el-space>
        </div>
      </div>

      <div :class="['bind-middle', { 'bind-middle-mobile': isMobile }]">
        <el-button
          type="primary"
          :icon="ArrowRight"
          :disabled="bindLeftSelected.length === 0"
          @click="handleAddToCart"
        >
          添加
        </el-button>
      </div>

      <div class="bind-right">
        <div class="cart-header">
          <span class="cart-title">当前策略目标集合 ({{ bindCart.length }})</span>
          <span class="cart-subtitle">({{ policyName }})</span>
        </div>

        <el-table
          :data="bindCart"
          height="100%"
          @selection-change="(rows: TunnelBinding[]) => bindRightSelected = rows.map(row => row.id)"
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
              {{ row.remote_start }}
            </template>
          </el-table-column>
          <el-table-column label="来源" min-width="120">
            <template #default="{ row }">
              <span :class="['source-label', { 'source-original': isOriginallyBound(row.id) }]">
                {{ getCartTunnelSource(row) }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="70" align="center">
            <template #default="{ row }">
              <el-button link type="danger" size="small" @click="handleRemoveFromCart(row.id)">移除</el-button>
            </template>
          </el-table-column>
        </el-table>

        <el-space wrap size="small" class="action-row">
          <el-button size="small" @click="handleSelectAllRight">全选</el-button>
          <el-button size="small" @click="bindRightSelected = []">清空</el-button>
          <el-button
            size="small"
            type="danger"
            :disabled="bindRightSelected.length === 0"
            @click="handleRemoveSelectedFromCart"
          >
            移除
          </el-button>
        </el-space>
      </div>
    </div>

    <div class="bind-footer">
      <span class="selected-count">最终目标 {{ bindCart.length }} 条</span>
      <el-space wrap size="small">
        <el-button @click="visible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="handleSubmit">确认提交</el-button>
      </el-space>
    </div>
  </component>
</template>

<style scoped>
.bind-dialog :deep(.el-dialog__body),
.bind-drawer :deep(.el-drawer__body) {
  padding: var(--spacing-md);
}

.bind-container {
  display: flex;
  gap: var(--spacing-md);
  height: 450px;
}

.bind-container-mobile {
  flex-direction: column;
  height: auto;
  min-height: 60vh;
}

.bind-left {
  display: flex;
  flex: 0 0 460px;
  gap: var(--spacing-sm);
  min-width: 0;
}

.bind-left-mobile {
  flex: 0 0 auto;
  flex-direction: column;
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

.bind-left-mobile .group-tabs {
  flex: 0 0 auto;
}

.bind-left-mobile .group-tabs :deep(.el-tabs__nav) {
  flex-direction: row;
}

.group-tab-label {
  display: block;
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tunnel-pool,
.bind-right {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.bind-middle {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 80px;
}

.bind-middle-mobile {
  flex: 0 0 auto;
}

.cart-header {
  padding-bottom: var(--spacing-sm);
}

.cart-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.cart-subtitle {
  margin-left: var(--spacing-xs);
  color: var(--el-text-color-secondary);
}

.action-row {
  padding-top: var(--spacing-sm);
}

.will-unbind {
  color: var(--el-color-warning);
  font-size: 12px;
}

.source-label {
  font-size: 12px;
}

.source-original {
  color: var(--el-text-color-secondary);
}

.bind-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: var(--spacing-sm);
  padding-top: var(--spacing-md);
}

.selected-count {
  color: var(--el-text-color-secondary);
}
</style>
