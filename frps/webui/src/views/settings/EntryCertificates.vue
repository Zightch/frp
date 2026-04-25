<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  authApi,
  certificateAssetsApi,
  certificateUsagesApi,
  type CertificateAsset,
  type CertificateUsage,
  type CertificateUsageType
} from '@/api'
import { useMobile } from '@/composables/useMobile'

defineOptions({
  name: 'EntryCertificatesView'
})

const router = useRouter()
const { isMobile } = useMobile()

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data
const certificateUsages = ref<CertificateUsage[]>([])
const assets = ref<CertificateAsset[]>([])
const loading = ref(false)
const error = ref('')

// Binding dialog
const bindingDialogVisible = ref(false)
const bindingFormRef = ref<FormInstance>()
const bindingSubmitting = ref(false)
const bindingForm = ref({
  usage_type: 'webui_https' as CertificateUsageType,
  asset_id: null as number | null
})
const bindingFormRules: FormRules = {
  asset_id: [{ required: true, message: '请选择证书', trigger: 'change' }]
}

// Computed
const webuiHttpsUsage = computed(() => getCertificateUsage('webui_https'))
const frpcTLSUsage = computed(() => getCertificateUsage('frpc_tls'))

const bindableCertificateAssets = computed(() => {
  return assets.value.filter(item => item.asset_type === 'certificate' && item.key_present)
})

const modalLayerComponent = computed(() => (isMobile.value ? 'el-drawer' : 'el-dialog'))

const modalLayerProps = computed(() => (
  isMobile.value
    ? { direction: 'btt', size: '70%' }
    : { width: '450px' }
))

function getCertificateUsage(usageType: CertificateUsageType): CertificateUsage {
  return certificateUsages.value.find(item => item.usage_type === usageType) || {
    usage_type: usageType,
    enabled: false,
    status: 'unbound',
    resolved_chain_length: 0
  }
}

function formatUsageType(usageType: CertificateUsageType): string {
  return usageType === 'webui_https' ? 'WebUI HTTPS' : 'frpc TLS'
}

function formatUsageStatus(status: CertificateUsage['status']): string {
  switch (status) {
    case 'enabled':
      return '已启用'
    case 'disabled':
      return '已禁用'
    case 'error':
      return '异常'
    case 'unbound':
    default:
      return '未绑定'
  }
}

function usageStatusTagType(status: CertificateUsage['status']): 'success' | 'info' | 'danger' {
  switch (status) {
    case 'enabled':
      return 'success'
    case 'error':
      return 'danger'
    default:
      return 'info'
  }
}

function buildAssetOptionLabel(asset: CertificateAsset): string {
  const commonName = asset.common_name?.trim()
  if (commonName) {
    return `${asset.name} (${commonName})`
  }
  return asset.name
}

function currentLocationProtocol(): 'http:' | 'https:' | '' {
  if (typeof window === 'undefined') {
    return ''
  }
  return window.location.protocol === 'https:' ? 'https:' : 'http:'
}

function scheduleWebUIProtocolRedirect(protocol: 'http:' | 'https:') {
  if (typeof window === 'undefined') {
    return
  }

  const targetURL = new URL(window.location.href)
  targetURL.protocol = protocol
  window.setTimeout(() => {
    window.location.replace(targetURL.toString())
  }, 400)
}

function upsertCertificateUsage(item?: CertificateUsage) {
  if (!item) {
    return
  }
  const nextItems = certificateUsages.value.filter(existing => existing.usage_type !== item.usage_type)
  nextItems.push(item)
  certificateUsages.value = nextItems
}

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

// Data loading
async function loadData() {
  loading.value = true
  error.value = ''

  const [usagesResult, assetsResult] = await Promise.all([
    certificateUsagesApi.list(),
    certificateAssetsApi.list()
  ])
  loading.value = false

  if (usagesResult.error) {
    error.value = usagesResult.error
    return
  }
  if (assetsResult.error) {
    error.value = assetsResult.error
    return
  }

  certificateUsages.value = usagesResult.data?.items || []
  assets.value = assetsResult.data?.items || []
}

// Binding dialog
function openBindingDialog(usageType: CertificateUsageType) {
  if (bindableCertificateAssets.value.length === 0) {
    ElMessage.error('暂无可绑定的证书，请先导入或生成带私钥的证书')
    return
  }

  const current = getCertificateUsage(usageType)
  bindingForm.value = {
    usage_type: usageType,
    asset_id: current.asset_id || bindableCertificateAssets.value[0]?.id || null
  }
  bindingDialogVisible.value = true
  nextTick(() => bindingFormRef.value?.clearValidate())
}

async function submitBinding() {
  const valid = await bindingFormRef.value?.validate().catch(() => false)
  if (!valid || !bindingForm.value.asset_id) {
    return
  }

  bindingSubmitting.value = true
  try {
    const result = await certificateUsagesApi.bind(bindingForm.value.usage_type, bindingForm.value.asset_id)
    if (result.error) {
      ElMessage.error(result.error)
      return
    }

    upsertCertificateUsage(result.data?.item)
    bindingDialogVisible.value = false

    if (bindingForm.value.usage_type === 'webui_https' && currentLocationProtocol() !== 'https:') {
      ElMessage.success('WebUI 将切换到 HTTPS')
      scheduleWebUIProtocolRedirect('https:')
      return
    }

    ElMessage.success('绑定成功')
    await loadData()
  } finally {
    bindingSubmitting.value = false
  }
}

async function handleUnbind(usageType: CertificateUsageType) {
  const usageName = formatUsageType(usageType)
  try {
    await ElMessageBox.confirm(
      `确定解绑 ${usageName} 使用证书吗？`,
      '解绑证书',
      {
        confirmButtonText: '解绑',
        cancelButtonText: '取消',
        type: 'warning'
      }
    )
  } catch {
    return
  }

  bindingSubmitting.value = true
  try {
    const result = await certificateUsagesApi.unbind(usageType)
    if (result.error) {
      ElMessage.error(result.error)
      return
    }

    upsertCertificateUsage(result.data?.item)

    if (usageType === 'webui_https' && currentLocationProtocol() === 'https:') {
      ElMessage.success('WebUI 将切回 HTTP')
      scheduleWebUIProtocolRedirect('http:')
      return
    }

    ElMessage.success('解绑成功')
    await loadData()
  } finally {
    bindingSubmitting.value = false
  }
}
</script>

<template>
  <el-container
    v-loading="checking"
    element-loading-text="检查认证状态..."
    direction="vertical"
    class="entry-certificates-view"
  >
    <el-result v-if="error" icon="error" :title="error">
      <template #extra>
        <el-button type="primary" @click="loadData">重试</el-button>
      </template>
    </el-result>

    <template v-else-if="authenticated">
      <div class="usage-list">
        <!-- WebUI HTTPS -->
        <el-card class="usage-card">
          <el-row :gutter="16" align="middle">
            <el-col :span="isMobile ? 24 : 4">
              <span class="usage-title">WebUI HTTPS</span>
            </el-col>
            <el-col :span="isMobile ? 12 : 4">
              <el-tag size="small" :type="usageStatusTagType(webuiHttpsUsage.status)">
                {{ formatUsageStatus(webuiHttpsUsage.status) }}
              </el-tag>
            </el-col>
            <el-col :span="isMobile ? 12 : 10">
              <span class="usage-current">
                当前证书: {{ webuiHttpsUsage.asset_name || '-' }}
              </span>
            </el-col>
            <el-col :span="isMobile ? 24 : 6" :class="['usage-actions', { 'usage-actions-mobile': isMobile }]">
              <template v-if="webuiHttpsUsage.asset_id">
                <el-button size="small" @click="openBindingDialog('webui_https')">更换</el-button>
                <el-button size="small" @click="handleUnbind('webui_https')">解绑</el-button>
              </template>
              <el-button v-else size="small" type="primary" @click="openBindingDialog('webui_https')">
                绑定
              </el-button>
            </el-col>
          </el-row>
        </el-card>

        <!-- frpc TLS -->
        <el-card class="usage-card">
          <el-row :gutter="16" align="middle">
            <el-col :span="isMobile ? 24 : 4">
              <span class="usage-title">frpc TLS</span>
            </el-col>
            <el-col :span="isMobile ? 12 : 4">
              <el-tag size="small" :type="usageStatusTagType(frpcTLSUsage.status)">
                {{ formatUsageStatus(frpcTLSUsage.status) }}
              </el-tag>
            </el-col>
            <el-col :span="isMobile ? 12 : 10">
              <span class="usage-current">
                当前证书: {{ frpcTLSUsage.asset_name || '-' }}
              </span>
            </el-col>
            <el-col :span="isMobile ? 24 : 6" :class="['usage-actions', { 'usage-actions-mobile': isMobile }]">
              <template v-if="frpcTLSUsage.asset_id">
                <el-button size="small" @click="openBindingDialog('frpc_tls')">更换</el-button>
                <el-button size="small" @click="handleUnbind('frpc_tls')">解绑</el-button>
              </template>
              <el-button v-else size="small" type="primary" @click="openBindingDialog('frpc_tls')">
                绑定
              </el-button>
            </el-col>
          </el-row>
        </el-card>
      </div>

      <!-- Binding dialog -->
      <component
        :is="modalLayerComponent"
        v-model="bindingDialogVisible"
        title="绑定入口证书"
        :close-on-click-modal="false"
        v-bind="modalLayerProps"
      >
        <el-form
          ref="bindingFormRef"
          :model="bindingForm"
          :rules="bindingFormRules"
          label-width="80px"
        >
          <el-form-item label="使用点">
            <el-input :model-value="formatUsageType(bindingForm.usage_type)" readonly />
          </el-form-item>
          <el-form-item label="证书" prop="asset_id">
            <el-select
              v-model="bindingForm.asset_id"
              placeholder="请选择证书"
              class="full-width"
            >
              <el-option
                v-for="asset in bindableCertificateAssets"
                :key="asset.id"
                :label="buildAssetOptionLabel(asset)"
                :value="asset.id"
              />
            </el-select>
          </el-form-item>
        </el-form>

        <el-alert
          v-if="bindingForm.usage_type === 'webui_https'"
          type="info"
          :closable="false"
          show-icon
          class="binding-alert"
        >
          <template #title>绑定后会切换到 HTTPS</template>
          当前浏览器会自动跳转到同地址的 HTTPS。
        </el-alert>

        <template #footer>
          <el-button @click="bindingDialogVisible = false">取消</el-button>
          <el-button type="primary" :loading="bindingSubmitting" @click="submitBinding">
            保存
          </el-button>
        </template>
      </component>
    </template>
  </el-container>
</template>

<style scoped>
.entry-certificates-view {
  height: 100%;
}

.usage-list {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-md);
}

.usage-card {
  flex-shrink: 0;
}

.usage-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.usage-current {
  color: var(--el-text-color-regular);
}

.usage-actions {
  display: flex;
  gap: var(--spacing-xs);
  justify-content: flex-end;
}

.usage-actions-mobile {
  justify-content: flex-start;
  margin-top: var(--spacing-sm);
}

.full-width {
  width: 100%;
}

.binding-alert {
  margin-top: var(--spacing-md);
}
</style>
