<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules, UploadProps } from 'element-plus'
import {
  authApi,
  certificateAssetsApi,
  type CertificateAsset,
  type CertificateAssetPastePayload,
  type CertificateAssetGeneratePayload
} from '@/api'

defineOptions({
  name: 'CertificateAssetsView'
})

const router = useRouter()

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data
const assets = ref<CertificateAsset[]>([])
const loading = ref(false)
const error = ref('')

// Filters
const filterAssetType = ref<'all' | 'certificate' | 'ca'>('all')
const filterSource = ref<'all' | 'upload' | 'generated'>('all')

// Import dialog
const importDialogVisible = ref(false)
const importActiveTab = ref('upload')
const importUploadFormRef = ref<FormInstance>()
const importPasteFormRef = ref<FormInstance>()
const importSubmitting = ref(false)
const importForm = ref({
  asset_type: 'certificate' as 'certificate' | 'ca',
  name: '',
  remark: '',
  crt: '',
  key: ''
})
const importFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  asset_type: [{ required: true, message: '请选择资产类型', trigger: 'change' }],
  crt: [{ required: true, message: '请输入证书内容', trigger: 'blur' }]
}

// Upload files
const uploadCrtFile = ref<File | null>(null)
const uploadKeyFile = ref<File | null>(null)

// Generate dialog
const generateDialogVisible = ref(false)
const generateActiveTab = ref('ca')
const generateCaFormRef = ref<FormInstance>()
const generateCertFormRef = ref<FormInstance>()
const generateSubmitting = ref(false)
const generateCaForm = ref({
  name: '',
  remark: '',
  common_name: '',
  validity_days: 3650
})
const generateCertForm = ref({
  name: '',
  remark: '',
  issuer_asset_id: null as number | null,
  common_name: '',
  validity_days: 365,
  dns_names: '',
  ip_addresses: ''
})
function createValidityDaysValidator(maxDays: number) {
  return (_rule: unknown, value: number) => {
    if (!Number.isInteger(value) || value < 1 || value > maxDays) {
      return Promise.reject(new Error(`有效期必须为 1-${maxDays} 天的整数`))
    }
    return Promise.resolve()
  }
}

const generateCaFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  common_name: [{ required: true, message: '请输入 Common Name', trigger: 'blur' }],
  validity_days: [{ asyncValidator: createValidityDaysValidator(3650), trigger: ['blur', 'change'] }]
}
const generateCertFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  issuer_asset_id: [{ required: true, message: '请选择签发 CA', trigger: 'change' }],
  common_name: [{ required: true, message: '请输入 Common Name', trigger: 'blur' }],
  validity_days: [{ asyncValidator: createValidityDaysValidator(3650), trigger: ['blur', 'change'] }]
}

// Detail drawer
const detailDrawerVisible = ref(false)
const detailAsset = ref<CertificateAsset | null>(null)

// Computed
const filteredAssets = computed(() => {
  return assets.value.filter(item => {
    if (filterAssetType.value !== 'all' && item.asset_type !== filterAssetType.value) {
      return false
    }
    if (filterSource.value !== 'all' && item.source !== filterSource.value) {
      return false
    }
    return true
  })
})

const canIssueCAs = computed(() => {
  return assets.value.filter(item => item.asset_type === 'ca' && item.can_issue)
})

function getActiveImportForm(): FormInstance | undefined {
  return importActiveTab.value === 'upload'
    ? importUploadFormRef.value
    : importPasteFormRef.value
}

function clearImportValidation() {
  importUploadFormRef.value?.clearValidate()
  importPasteFormRef.value?.clearValidate()
}

function getActiveGenerateForm(): FormInstance | undefined {
  return generateActiveTab.value === 'ca'
    ? generateCaFormRef.value
    : generateCertFormRef.value
}

function clearGenerateValidation() {
  generateCaFormRef.value?.clearValidate()
  generateCertFormRef.value?.clearValidate()
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

  const result = await certificateAssetsApi.list()
  loading.value = false

  if (result.error) {
    error.value = result.error
    return
  }

  assets.value = result.data?.items || []
}

// Format helpers
function formatAssetType(type: string): string {
  return type === 'ca' ? 'CA' : '证书'
}

function formatSource(source: string): string {
  return source === 'upload' ? '上传' : '生成'
}

function formatDate(dateStr?: string): string {
  if (!dateStr) return '-'
  return dateStr.split('T')[0]
}

function getStatus(asset: CertificateAsset): 'valid' | 'expiring' | 'expired' | 'not_yet_valid' | 'unknown' {
  if (!asset.not_before || !asset.not_after) return 'unknown'
  const now = new Date()
  const notBefore = new Date(asset.not_before)
  const notAfter = new Date(asset.not_after)

  if (now < notBefore) return 'not_yet_valid'
  if (now > notAfter) return 'expired'
  const weekLater = new Date(now.getTime() + 7 * 24 * 60 * 60 * 1000)
  if (notAfter < weekLater) return 'expiring'
  return 'valid'
}

function getStatusType(asset: CertificateAsset): 'success' | 'warning' | 'danger' | 'info' {
  const status = getStatus(asset)
  switch (status) {
    case 'valid': return 'success'
    case 'expiring': return 'warning'
    case 'expired': return 'danger'
    case 'not_yet_valid': return 'info'
    default: return 'info'
  }
}

function getStatusText(asset: CertificateAsset): string {
  const status = getStatus(asset)
  switch (status) {
    case 'valid': return '有效'
    case 'expiring': return '即将过期'
    case 'expired': return '已过期'
    case 'not_yet_valid': return '未生效'
    default: return '未知'
  }
}

// Import dialog
function openImportDialog() {
  importActiveTab.value = 'upload'
  importForm.value = {
    asset_type: 'certificate',
    name: '',
    remark: '',
    crt: '',
    key: ''
  }
  uploadCrtFile.value = null
  uploadKeyFile.value = null
  importDialogVisible.value = true
  nextTick(() => clearImportValidation())
}

function handleImportTabChange() {
  uploadCrtFile.value = null
  uploadKeyFile.value = null
  importForm.value.crt = ''
  importForm.value.key = ''
  nextTick(() => clearImportValidation())
}

const handleCrtUpload: UploadProps['beforeUpload'] = (file) => {
  uploadCrtFile.value = file
  importForm.value.crt = file.name
  return false
}

const handleKeyUpload: UploadProps['beforeUpload'] = (file) => {
  uploadKeyFile.value = file
  importForm.value.key = file.name
  return false
}

async function submitImport() {
  const valid = await getActiveImportForm()?.validate().catch(() => false)
  if (!valid) return

  importSubmitting.value = true

  try {
    if (importActiveTab.value === 'upload') {
      if (!uploadCrtFile.value) {
        ElMessage.error('请选择证书文件')
        return
      }

      const formData = new FormData()
      formData.append('name', importForm.value.name)
      formData.append('remark', importForm.value.remark)
      formData.append('asset_type', importForm.value.asset_type)
      formData.append('crt', uploadCrtFile.value, uploadCrtFile.value.name)
      if (uploadKeyFile.value) {
        formData.append('key', uploadKeyFile.value, uploadKeyFile.value.name)
      }

      const result = await certificateAssetsApi.upload(formData)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('导入成功')
    } else {
      const payload: CertificateAssetPastePayload = {
        name: importForm.value.name,
        remark: importForm.value.remark,
        asset_type: importForm.value.asset_type,
        crt: importForm.value.crt,
        key: importForm.value.key || undefined
      }

      const result = await certificateAssetsApi.paste(payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('导入成功')
    }

    importDialogVisible.value = false
    await loadData()
  } finally {
    importSubmitting.value = false
  }
}

// Generate dialog
function openGenerateDialog() {
  generateActiveTab.value = 'ca'
  generateCaForm.value = {
    name: '',
    remark: '',
    common_name: '',
    validity_days: 3650
  }
  generateCertForm.value = {
    name: '',
    remark: '',
    issuer_asset_id: null,
    common_name: '',
    validity_days: 365,
    dns_names: '',
    ip_addresses: ''
  }
  generateDialogVisible.value = true
  nextTick(() => clearGenerateValidation())
}

function handleGenerateTabChange() {
  nextTick(() => clearGenerateValidation())
}

async function submitGenerate() {
  const valid = await getActiveGenerateForm()?.validate().catch(() => false)
  if (!valid) return

  generateSubmitting.value = true

  try {
    if (generateActiveTab.value === 'ca') {
      const payload: CertificateAssetGeneratePayload = {
        name: generateCaForm.value.name,
        remark: generateCaForm.value.remark,
        asset_type: 'ca',
        common_name: generateCaForm.value.common_name,
        validity_days: generateCaForm.value.validity_days
      }

      const result = await certificateAssetsApi.generate(payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('CA 生成成功')
    } else {
      if (!generateCertForm.value.issuer_asset_id) {
        ElMessage.error('请选择签发 CA')
        return
      }

      const dnsNames = generateCertForm.value.dns_names
        ? generateCertForm.value.dns_names.split(',').map(s => s.trim()).filter(Boolean)
        : undefined
      const ipAddresses = generateCertForm.value.ip_addresses
        ? generateCertForm.value.ip_addresses.split(',').map(s => s.trim()).filter(Boolean)
        : undefined

      const payload: CertificateAssetGeneratePayload = {
        name: generateCertForm.value.name,
        remark: generateCertForm.value.remark,
        asset_type: 'certificate',
        issuer_asset_id: generateCertForm.value.issuer_asset_id,
        common_name: generateCertForm.value.common_name,
        validity_days: generateCertForm.value.validity_days,
        dns_names: dnsNames,
        ip_addresses: ipAddresses
      }

      const result = await certificateAssetsApi.generate(payload)
      if (result.error) {
        ElMessage.error(result.error)
        return
      }
      ElMessage.success('证书生成成功')
    }

    generateDialogVisible.value = false
    await loadData()
  } finally {
    generateSubmitting.value = false
  }
}

// Detail drawer
function openDetailDrawer(asset: CertificateAsset) {
  detailAsset.value = asset
  detailDrawerVisible.value = true
}

function getIssuerDisplayName(asset: CertificateAsset): string {
  if (asset.is_self_signed) return '(自签)'
  if (asset.issuer_name) return asset.issuer_name
  if (asset.issuer) return asset.issuer
  return '(无)'
}

// Delete
async function handleDelete(asset: CertificateAsset) {
  const impactResult = await certificateAssetsApi.getDeleteImpact(asset.id)
  if (impactResult.error) {
    ElMessage.error(impactResult.error)
    return
  }

  const impact = impactResult.data!
  let message = `确定删除"${asset.name}"吗？此操作不可恢复。`
  let cascade = false

  if (impact.requires_confirmation && impact.affected_items.length > 0) {
    const childList = impact.affected_items
      .map(item => `- ${item.item.name} (${formatAssetType(item.item.asset_type)})`)
      .join('\n')
    message = `该资产被以下证书引用：\n${childList}\n\n删除将同时删除以上子证书，此操作不可恢复。\n确定继续删除吗？`
    cascade = true
  }

  try {
    await ElMessageBox.confirm(message, '删除确认', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
      confirmButtonClass: 'el-button--danger'
    })
  } catch {
    return
  }

  const result = await certificateAssetsApi.delete(asset.id, cascade)
  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('已删除')
  if (detailDrawerVisible.value && detailAsset.value?.id === asset.id) {
    detailDrawerVisible.value = false
  }
  await loadData()
}
</script>

<template>
  <el-container
    v-loading="checking"
    element-loading-text="检查认证状态..."
    direction="vertical"
    class="certificate-assets-view"
  >
    <el-result v-if="error" icon="error" :title="error">
      <template #extra>
        <el-button type="primary" @click="loadData">重试</el-button>
      </template>
    </el-result>

    <template v-else-if="authenticated">
      <el-header height="auto" class="page-header">
        <el-row justify="space-between" align="middle">
          <h1>证书资产</h1>
          <el-button @click="loadData" :loading="loading">刷新</el-button>
        </el-row>
      </el-header>

      <el-main class="content-main">
        <el-card class="filter-card">
          <el-row :gutter="16" align="middle">
            <el-col :span="6">
              <el-select v-model="filterAssetType" placeholder="资产类型" clearable>
                <el-option label="全部" value="all" />
                <el-option label="证书" value="certificate" />
                <el-option label="CA" value="ca" />
              </el-select>
            </el-col>
            <el-col :span="6">
              <el-select v-model="filterSource" placeholder="来源" clearable>
                <el-option label="全部" value="all" />
                <el-option label="上传" value="upload" />
                <el-option label="生成" value="generated" />
              </el-select>
            </el-col>
            <el-col :span="12" class="filter-actions">
              <el-button type="primary" @click="openImportDialog">导入</el-button>
              <el-button type="primary" @click="openGenerateDialog">生成</el-button>
            </el-col>
          </el-row>
        </el-card>

        <el-card class="table-card">
          <el-empty v-if="filteredAssets.length === 0 && !loading" description="暂无证书资产">
            <template #image>
              <el-icon :size="64"><Key /></el-icon>
            </template>
          </el-empty>
          <el-table v-else :data="filteredAssets" stripe v-loading="loading">
            <el-table-column prop="name" label="名称" min-width="150" />
            <el-table-column label="类型" width="80" align="center">
              <template #default="{ row }">
                <el-tag size="small" :type="row.asset_type === 'ca' ? 'warning' : ''">
                  {{ formatAssetType(row.asset_type) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="来源" width="80" align="center">
              <template #default="{ row }">
                <el-tag size="small" type="info">{{ formatSource(row.source) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="颁发者" min-width="150">
              <template #default="{ row }">
                {{ getIssuerDisplayName(row) }}
              </template>
            </el-table-column>
            <el-table-column label="有效期" width="120">
              <template #default="{ row }">
                {{ formatDate(row.not_after) }}
              </template>
            </el-table-column>
            <el-table-column label="状态" width="100" align="center">
              <template #default="{ row }">
                <el-tag size="small" :type="getStatusType(row)">
                  {{ getStatusText(row) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="140" align="center" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" size="small" @click="openDetailDrawer(row)">查看</el-button>
                <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-main>
    </template>

    <!-- Import dialog -->
    <el-dialog
      v-model="importDialogVisible"
      title="导入证书/CA"
      width="500px"
      :close-on-click-modal="false"
    >
      <el-tabs v-model="importActiveTab" @tab-change="handleImportTabChange">
        <el-tab-pane label="上传文件" name="upload">
          <el-form
            ref="importUploadFormRef"
            :model="importForm"
            :rules="importFormRules"
            label-width="80px"
          >
            <el-form-item label="资产类型" prop="asset_type">
              <el-select v-model="importForm.asset_type" class="full-width">
                <el-option label="证书" value="certificate" />
                <el-option label="CA" value="ca" />
              </el-select>
            </el-form-item>
            <el-form-item label="名称" prop="name">
              <el-input v-model="importForm.name" placeholder="请输入名称" />
            </el-form-item>
            <el-form-item label="备注">
              <el-input v-model="importForm.remark" placeholder="可选备注" />
            </el-form-item>
            <el-form-item label="证书文件" prop="crt">
              <el-upload
                :auto-upload="false"
                :show-file-list="false"
                accept=".crt,.pem,.cer"
                :before-upload="handleCrtUpload"
              >
                <el-button>
                  <el-icon><Upload /></el-icon>
                  选择证书
                </el-button>
                <template #tip v-if="importForm.crt">
                  <span class="upload-tip">已选择: {{ uploadCrtFile?.name || '已粘贴内容' }}</span>
                </template>
              </el-upload>
            </el-form-item>
            <el-form-item label="私钥文件">
              <el-upload
                :auto-upload="false"
                :show-file-list="false"
                accept=".key,.pem"
                :before-upload="handleKeyUpload"
              >
                <el-button>
                  <el-icon><Upload /></el-icon>
                  选择私钥
                </el-button>
                <template #tip v-if="importForm.key">
                  <span class="upload-tip">已选择: {{ uploadKeyFile?.name || '已粘贴内容' }}</span>
                </template>
              </el-upload>
            </el-form-item>
          </el-form>
        </el-tab-pane>
        <el-tab-pane label="粘贴 PEM" name="paste">
          <el-form
            ref="importPasteFormRef"
            :model="importForm"
            :rules="importFormRules"
            label-width="80px"
          >
            <el-form-item label="资产类型" prop="asset_type">
              <el-select v-model="importForm.asset_type" class="full-width">
                <el-option label="证书" value="certificate" />
                <el-option label="CA" value="ca" />
              </el-select>
            </el-form-item>
            <el-form-item label="名称" prop="name">
              <el-input v-model="importForm.name" placeholder="请输入名称" />
            </el-form-item>
            <el-form-item label="备注">
              <el-input v-model="importForm.remark" placeholder="可选备注" />
            </el-form-item>
            <el-form-item label="证书内容" prop="crt">
              <el-input
                v-model="importForm.crt"
                type="textarea"
                :rows="6"
                placeholder="粘贴 PEM 格式证书内容"
              />
            </el-form-item>
            <el-form-item label="私钥内容">
              <el-input
                v-model="importForm.key"
                type="textarea"
                :rows="4"
                placeholder="粘贴 PEM 格式私钥内容（可选）"
              />
            </el-form-item>
          </el-form>
        </el-tab-pane>
      </el-tabs>
      <template #footer>
        <el-button @click="importDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="importSubmitting" @click="submitImport">导入</el-button>
      </template>
    </el-dialog>

    <!-- Generate dialog -->
    <el-dialog
      v-model="generateDialogVisible"
      title="生成证书/CA"
      width="500px"
      :close-on-click-modal="false"
    >
      <el-tabs v-model="generateActiveTab" @tab-change="handleGenerateTabChange">
        <el-tab-pane label="生成 CA" name="ca">
          <el-form
            ref="generateCaFormRef"
            :model="generateCaForm"
            :rules="generateCaFormRules"
            label-width="100px"
          >
            <el-form-item label="名称" prop="name">
              <el-input v-model="generateCaForm.name" placeholder="请输入名称" />
            </el-form-item>
            <el-form-item label="备注">
              <el-input v-model="generateCaForm.remark" placeholder="可选备注" />
            </el-form-item>
            <el-form-item label="Common Name" prop="common_name">
              <el-input v-model="generateCaForm.common_name" placeholder="CA 的 CN 字段" />
            </el-form-item>
            <el-form-item label="有效期(天)" prop="validity_days">
              <el-input-number v-model="generateCaForm.validity_days" :min="1" :max="3650" />
            </el-form-item>
          </el-form>
        </el-tab-pane>
        <el-tab-pane label="生成证书" name="certificate">
          <el-form
            ref="generateCertFormRef"
            :model="generateCertForm"
            :rules="generateCertFormRules"
            label-width="100px"
          >
            <el-form-item label="名称" prop="name">
              <el-input v-model="generateCertForm.name" placeholder="请输入名称" />
            </el-form-item>
            <el-form-item label="备注">
              <el-input v-model="generateCertForm.remark" placeholder="可选备注" />
            </el-form-item>
            <el-form-item label="签发 CA" prop="issuer_asset_id">
              <el-select v-model="generateCertForm.issuer_asset_id" placeholder="选择 CA" class="full-width">
                <el-option
                  v-for="ca in canIssueCAs"
                  :key="ca.id"
                  :label="ca.name"
                  :value="ca.id"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="Common Name" prop="common_name">
              <el-input v-model="generateCertForm.common_name" placeholder="证书的 CN 字段" />
            </el-form-item>
            <el-form-item label="SAN 域名">
              <el-input v-model="generateCertForm.dns_names" placeholder="多个域名用逗号分隔" />
            </el-form-item>
            <el-form-item label="SAN IP">
              <el-input v-model="generateCertForm.ip_addresses" placeholder="多个 IP 用逗号分隔" />
            </el-form-item>
            <el-form-item label="有效期(天)" prop="validity_days">
              <el-input-number v-model="generateCertForm.validity_days" :min="1" :max="3650" />
            </el-form-item>
          </el-form>
        </el-tab-pane>
      </el-tabs>
      <template #footer>
        <el-button @click="generateDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="generateSubmitting" @click="submitGenerate">生成</el-button>
      </template>
    </el-dialog>

    <!-- Detail drawer -->
    <el-drawer
      v-model="detailDrawerVisible"
      title="资产详情"
      size="500px"
    >
      <template v-if="detailAsset">
        <el-descriptions :column="1" border>
          <el-descriptions-item label="名称">{{ detailAsset.name }}</el-descriptions-item>
          <el-descriptions-item label="类型">
            <el-tag size="small" :type="detailAsset.asset_type === 'ca' ? 'warning' : ''">
              {{ formatAssetType(detailAsset.asset_type) }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="来源">
            <el-tag size="small" type="info">{{ formatSource(detailAsset.source) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="备注">{{ detailAsset.remark || '-' }}</el-descriptions-item>
          <el-descriptions-item label="上游 CA">{{ detailAsset.issuer_name || '(无)' }}</el-descriptions-item>
          <el-descriptions-item label="创建时间">{{ detailAsset.created_at }}</el-descriptions-item>
          <el-descriptions-item label="更新时间">{{ detailAsset.updated_at }}</el-descriptions-item>
        </el-descriptions>

        <el-divider content-position="left">证书信息</el-divider>

        <el-descriptions :column="1" border>
          <el-descriptions-item label="Common Name">{{ detailAsset.common_name || '-' }}</el-descriptions-item>
          <el-descriptions-item label="Subject">
            <div class="mono-text">{{ detailAsset.subject || '-' }}</div>
          </el-descriptions-item>
          <el-descriptions-item label="Issuer">
            <div class="mono-text">{{ detailAsset.issuer || '-' }}</div>
          </el-descriptions-item>
          <el-descriptions-item label="序列号">
            <div class="mono-text">{{ detailAsset.serial_number || '-' }}</div>
          </el-descriptions-item>
          <el-descriptions-item label="有效期起始">{{ detailAsset.not_before || '-' }}</el-descriptions-item>
          <el-descriptions-item label="有效期结束">{{ detailAsset.not_after || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag size="small" :type="getStatusType(detailAsset)">
              {{ getStatusText(detailAsset) }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="SAN 域名">
            {{ detailAsset.dns_names?.length ? detailAsset.dns_names.join(', ') : '-' }}
          </el-descriptions-item>
          <el-descriptions-item label="SAN IP">
            {{ detailAsset.ip_addresses?.length ? detailAsset.ip_addresses.join(', ') : '-' }}
          </el-descriptions-item>
          <el-descriptions-item label="是否自签">
            {{ detailAsset.is_self_signed ? '是' : '否' }}
          </el-descriptions-item>
          <el-descriptions-item label="链长度">{{ detailAsset.chain_length }}</el-descriptions-item>
        </el-descriptions>

        <el-divider content-position="left">私钥信息</el-divider>

        <el-descriptions :column="1" border>
          <el-descriptions-item label="私钥状态">
            {{ detailAsset.key_present ? '已存储' : '未存储' }}
          </el-descriptions-item>
          <el-descriptions-item label="可签发">
            {{ detailAsset.can_issue ? '是' : '否' }}
          </el-descriptions-item>
        </el-descriptions>

        <div class="drawer-footer">
          <el-button type="danger" @click="handleDelete(detailAsset)">删除资产</el-button>
        </div>
      </template>
    </el-drawer>
  </el-container>
</template>

<style scoped>
.certificate-assets-view {
  height: 100%;
}

.page-header {
  padding: 0;
}

.content-main {
  padding: var(--el-main-padding) 0 0 0;
  flex: 1;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  gap: var(--el-card-padding);
}

.filter-card {
  flex-shrink: 0;
}

.filter-card .el-select {
  width: 100%;
}

.filter-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}

.table-card {
  flex: 1;
  overflow: hidden;
}

.table-card :deep(.el-card__body) {
  height: 100%;
  display: flex;
  flex-direction: column;
}

.table-card :deep(.el-table) {
  flex: 1;
}

.full-width {
  width: 100%;
}

.upload-tip {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.form-hint {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.mono-text {
  font-family: monospace;
  word-break: break-all;
}

.drawer-footer {
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--el-border-color-lighter);
}
</style>
