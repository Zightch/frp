<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules, UploadProps } from 'element-plus'
import {
  authApi,
  certificateAssetsApi,
  certificateUsagesApi,
  type CertificateAsset,
  type CertificateAssetPastePayload,
  type CertificateAssetGeneratePayload,
  type CertificateAssetGenerateKeyAlgorithm,
  type CertificateAssetDownloadMode,
  type CertificateAssetDownloadOptions,
  type CertificateUsage,
  type CertificateUsageType
} from '@/api'
import { useMobile } from '@/composables/useMobile'

defineOptions({
  name: 'CertificateAssetsView'
})

const router = useRouter()
const { isMobile } = useMobile()

type GenerateKeyFormModel = {
  key_algorithm: CertificateAssetGenerateKeyAlgorithm
  key_bits: number
}

type GenerateKeyBitsOption = {
  value: number
  label: string
}

const generateKeyAlgorithmOptions: Array<{ value: CertificateAssetGenerateKeyAlgorithm; label: string }> = [
  { value: 'ecdsa', label: 'ECDSA' },
  { value: 'rsa', label: 'RSA' },
  { value: 'ed25519', label: 'Ed25519' }
]

const generateKeyBitsOptionsByAlgorithm: Record<CertificateAssetGenerateKeyAlgorithm, GenerateKeyBitsOption[]> = {
  ecdsa: [
    { value: 256, label: '256 (P-256)' },
    { value: 384, label: '384 (P-384)' },
    { value: 521, label: '521 (P-521)' }
  ],
  rsa: [
    { value: 2048, label: '2048' },
    { value: 3072, label: '3072' },
    { value: 4096, label: '4096' },
    { value: 6144, label: '6144' },
    { value: 8192, label: '8192' }
  ],
  ed25519: [
    { value: 256, label: '256 (fixed)' }
  ]
}

const minimumRSAGenerateKeyBits = 2048

// Auth state
const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

// Data
const assets = ref<CertificateAsset[]>([])
const certificateUsages = ref<CertificateUsage[]>([])
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
  name: '',
  remark: '',
  crt: '',
  key: ''
})
const importFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  crt: [{ required: true, message: '请输入证书内容', trigger: 'blur' }]
}

// Upload files
const uploadCrtFile = ref<File | null>(null)
const uploadKeyFile = ref<File | null>(null)

// Edit dialog
const editDialogVisible = ref(false)
const editFormRef = ref<FormInstance>()
const editSubmitting = ref(false)
const editingAssetId = ref<number | null>(null)
const editForm = ref({
  name: '',
  remark: ''
})
const editFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }]
}

// Generate dialog
const generateDialogVisible = ref(false)
const generateActiveTab = ref('ca')
const generateCaFormRef = ref<FormInstance>()
const generateCertFormRef = ref<FormInstance>()
const generateSubmitting = ref(false)
const generateCaForm = ref({
  name: '',
  remark: '',
  issuer_asset_id: null as number | null,
  common_name: '',
  validity_days: 3650,
  key_algorithm: 'ecdsa' as CertificateAssetGenerateKeyAlgorithm,
  key_bits: 256
})
const generateCertForm = ref({
  name: '',
  remark: '',
  issuer_asset_id: null as number | null,
  common_name: '',
  validity_days: 365,
  dns_names: '',
  ip_addresses: '',
  key_algorithm: 'ecdsa' as CertificateAssetGenerateKeyAlgorithm,
  key_bits: 256
})
function createValidityDaysValidator(maxDays: number) {
  return (_rule: unknown, value: number) => {
    if (!Number.isInteger(value) || value < 1 || value > maxDays) {
      return Promise.reject(new Error(`有效期必须为 1-${maxDays} 天的整数`))
    }
    return Promise.resolve()
  }
}

function getGenerateKeyBitsOptions(algorithm: CertificateAssetGenerateKeyAlgorithm): GenerateKeyBitsOption[] {
  return generateKeyBitsOptionsByAlgorithm[algorithm] || generateKeyBitsOptionsByAlgorithm.ecdsa
}

function isCustomGenerateKeyBitsAlgorithm(algorithm: CertificateAssetGenerateKeyAlgorithm): boolean {
  return algorithm === 'rsa'
}

function syncGenerateKeyBits(form: GenerateKeyFormModel) {
  if (isCustomGenerateKeyBitsAlgorithm(form.key_algorithm)) {
    if (!Number.isInteger(form.key_bits) || form.key_bits < minimumRSAGenerateKeyBits) {
      form.key_bits = minimumRSAGenerateKeyBits
    }
    return
  }

  const options = getGenerateKeyBitsOptions(form.key_algorithm)
  if (!options.some(option => option.value === form.key_bits)) {
    form.key_bits = options[0].value
  }
}

function createGenerateKeyBitsValidator(getForm: () => GenerateKeyFormModel) {
  return (_rule: unknown, value: number) => {
    if (getForm().key_algorithm === 'rsa') {
      if (!Number.isInteger(value) || value < minimumRSAGenerateKeyBits || value % 8 !== 0) {
        return Promise.reject(new Error(`RSA 密钥长度必须是大于等于 ${minimumRSAGenerateKeyBits} 的 8 的倍数`))
      }
      return Promise.resolve()
    }

    const allowed = getGenerateKeyBitsOptions(getForm().key_algorithm).map(option => option.value)
    if (!allowed.includes(value)) {
      return Promise.reject(new Error('请选择匹配当前算法的密钥长度'))
    }
    return Promise.resolve()
  }
}

const generateCaFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  common_name: [{ required: true, message: '请输入 Common Name', trigger: 'blur' }],
  validity_days: [{ asyncValidator: createValidityDaysValidator(3650), trigger: ['blur', 'change'] }],
  key_algorithm: [{ required: true, message: '请选择密钥算法', trigger: 'change' }],
  key_bits: [{ asyncValidator: createGenerateKeyBitsValidator(() => generateCaForm.value), trigger: ['blur', 'change'] }]
}
const generateCertFormRules: FormRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  issuer_asset_id: [{ required: true, message: '请选择签发 CA', trigger: 'change' }],
  common_name: [{ required: true, message: '请输入 Common Name', trigger: 'blur' }],
  validity_days: [{ asyncValidator: createValidityDaysValidator(3650), trigger: ['blur', 'change'] }],
  key_algorithm: [{ required: true, message: '请选择密钥算法', trigger: 'change' }],
  key_bits: [{ asyncValidator: createGenerateKeyBitsValidator(() => generateCertForm.value), trigger: ['blur', 'change'] }]
}

// Detail drawer
const detailDrawerVisible = ref(false)
const detailAsset = ref<CertificateAsset | null>(null)

// Download dialog
const downloadDialogVisible = ref(false)
const downloadLoading = ref(false)
const downloadOptions = ref<CertificateAssetDownloadOptions | null>(null)
const downloadMode = ref<CertificateAssetDownloadMode>('single')
const downloadAncestorId = ref<number | null>(null)
const downloadSelectedAssetIds = ref<number[]>([])
const downloadTreeProps = {
  children: 'children',
  label: 'name'
}

// Usage binding dialog
const usageDialogVisible = ref(false)
const usageFormRef = ref<FormInstance>()
const usageSubmitting = ref(false)
const usageForm = ref({
  usage_type: 'webui_https' as CertificateUsageType,
  asset_id: null as number | null
})
const usageFormRules: FormRules = {
  asset_id: [{ required: true, message: '请选择证书', trigger: 'change' }]
}

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

const bindableCertificateAssets = computed(() => {
  return assets.value.filter(item => item.asset_type === 'certificate' && item.key_present)
})

const usageCards = computed(() => {
  return [
    getCertificateUsage('webui_https'),
    getCertificateUsage('control_listener_tls')
  ]
})

const modalLayerComponent = computed(() => (isMobile.value ? 'el-drawer' : 'el-dialog'))

const modalLayerProps = computed(() => (
  isMobile.value
    ? { direction: 'btt', size: '88%' }
    : { width: '500px' }
))

const detailDrawerDirection = computed(() => (isMobile.value ? 'btt' : 'rtl'))

const detailDrawerSize = computed(() => (isMobile.value ? '88%' : '500px'))

function getActiveImportForm(): FormInstance | undefined {
  return importActiveTab.value === 'upload'
    ? importUploadFormRef.value
    : importPasteFormRef.value
}

function clearImportValidation() {
  importUploadFormRef.value?.clearValidate()
  importPasteFormRef.value?.clearValidate()
}

function clearEditValidation() {
  editFormRef.value?.clearValidate()
}

function clearUsageValidation() {
  usageFormRef.value?.clearValidate()
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

function handleGenerateCaKeyAlgorithmChange() {
  syncGenerateKeyBits(generateCaForm.value)
  generateCaFormRef.value?.clearValidate(['key_bits'])
}

function handleGenerateCertKeyAlgorithmChange() {
  syncGenerateKeyBits(generateCertForm.value)
  generateCertFormRef.value?.clearValidate(['key_bits'])
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

  const [assetsResult, usagesResult] = await Promise.all([
    certificateAssetsApi.list(),
    certificateUsagesApi.list()
  ])
  loading.value = false

  if (assetsResult.error) {
    error.value = assetsResult.error
    return
  }
  if (usagesResult.error) {
    error.value = usagesResult.error
    return
  }

  assets.value = assetsResult.data?.items || []
  certificateUsages.value = usagesResult.data?.items || []
  if (detailAsset.value) {
    const refreshed = assets.value.find(item => item.id === detailAsset.value?.id) || null
    if (refreshed) {
      detailAsset.value = refreshed
    } else {
      detailAsset.value = null
      detailDrawerVisible.value = false
    }
  }
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

function formatDownloadMode(mode: CertificateAssetDownloadMode): string {
  switch (mode) {
    case 'original': return '原始'
    case 'single': return '当前节点'
    case 'chain': return '证书链'
    case 'tree': return '子树'
    default: return mode
  }
}

function getCertificateUsage(usageType: CertificateUsageType): CertificateUsage {
  return certificateUsages.value.find(item => item.usage_type === usageType) || {
    usage_type: usageType,
    enabled: false,
    status: 'unbound',
    resolved_chain_length: 0
  }
}

function formatUsageType(usageType: CertificateUsageType): string {
  return usageType === 'webui_https' ? 'WebUI HTTPS' : 'frpc 登录 TLS'
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

function buildUsageAssetOptionLabel(asset: CertificateAsset): string {
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

// Import dialog
function openImportDialog() {
  importActiveTab.value = 'upload'
  importForm.value = {
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

function openUsageDialog(usageType: CertificateUsageType) {
  if (bindableCertificateAssets.value.length === 0) {
    ElMessage.error('暂无可绑定的证书，请先导入或生成带私钥的证书')
    return
  }

  const current = getCertificateUsage(usageType)
  usageForm.value = {
    usage_type: usageType,
    asset_id: current.asset_id || bindableCertificateAssets.value[0]?.id || null
  }
  usageDialogVisible.value = true
  nextTick(() => clearUsageValidation())
}

async function submitUsageBinding() {
  const valid = await usageFormRef.value?.validate().catch(() => false)
  if (!valid || !usageForm.value.asset_id) {
    return
  }

  usageSubmitting.value = true
  try {
    const result = await certificateUsagesApi.bind(usageForm.value.usage_type, usageForm.value.asset_id)
    if (result.error) {
      ElMessage.error(result.error)
      return
    }

    upsertCertificateUsage(result.data?.item)
    usageDialogVisible.value = false

    if (usageForm.value.usage_type === 'webui_https' && currentLocationProtocol() !== 'https:') {
      ElMessage.success('WebUI 将切换到 HTTPS')
      scheduleWebUIProtocolRedirect('https:')
      return
    }

    ElMessage.success('绑定成功')
    await loadData()
  } finally {
    usageSubmitting.value = false
  }
}

async function handleUnbindUsage(usageType: CertificateUsageType) {
  try {
    await ElMessageBox.confirm(
      `确定解绑 ${formatUsageType(usageType)} 使用证书吗？`,
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

  usageSubmitting.value = true
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
    usageSubmitting.value = false
  }
}

const handleCrtUploadChange: UploadProps['onChange'] = (file) => {
  if (!file.raw) {
    return
  }
  uploadCrtFile.value = file.raw
  importForm.value.crt = file.raw.name
  importUploadFormRef.value?.clearValidate(['crt'])
}

const handleKeyUploadChange: UploadProps['onChange'] = (file) => {
  if (!file.raw) {
    return
  }
  uploadKeyFile.value = file.raw
  importForm.value.key = file.raw.name
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

// Edit dialog
function openEditDialog(asset: CertificateAsset) {
  editingAssetId.value = asset.id
  editForm.value = {
    name: asset.name,
    remark: asset.remark || ''
  }
  editDialogVisible.value = true
  nextTick(() => clearEditValidation())
}

async function submitEdit() {
  const valid = await editFormRef.value?.validate().catch(() => false)
  if (!valid || !editingAssetId.value) return

  editSubmitting.value = true

  try {
    const result = await certificateAssetsApi.update(editingAssetId.value, {
      name: editForm.value.name,
      remark: editForm.value.remark || undefined
    })
    if (result.error) {
      ElMessage.error(result.error)
      return
    }

    ElMessage.success('资产更新成功')
    editDialogVisible.value = false
    await loadData()
  } finally {
    editSubmitting.value = false
  }
}

// Generate dialog
function openGenerateDialog() {
  generateActiveTab.value = 'ca'
  generateCaForm.value = {
    name: '',
    remark: '',
    issuer_asset_id: null,
    common_name: '',
    validity_days: 3650,
    key_algorithm: 'ecdsa',
    key_bits: 256
  }
  generateCertForm.value = {
    name: '',
    remark: '',
    issuer_asset_id: null,
    common_name: '',
    validity_days: 365,
    dns_names: '',
    ip_addresses: '',
    key_algorithm: 'ecdsa',
    key_bits: 256
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
        issuer_asset_id: generateCaForm.value.issuer_asset_id || undefined,
        common_name: generateCaForm.value.common_name,
        validity_days: generateCaForm.value.validity_days,
        key_algorithm: generateCaForm.value.key_algorithm,
        key_bits: generateCaForm.value.key_bits
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
        ip_addresses: ipAddresses,
        key_algorithm: generateCertForm.value.key_algorithm,
        key_bits: generateCertForm.value.key_bits
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

// Download
async function openDownloadDialog(asset: CertificateAsset) {
  downloadLoading.value = true
  downloadDialogVisible.value = true

  const result = await certificateAssetsApi.getDownloadOptions(asset.id)
  downloadLoading.value = false

  if (result.error) {
    ElMessage.error(result.error)
    downloadDialogVisible.value = false
    return
  }

  downloadOptions.value = result.data || null

  // Reset to default mode
  const defaultMode = downloadOptions.value?.modes.find(m => m.default)?.mode || downloadOptions.value?.modes[0]?.mode
  downloadMode.value = defaultMode || 'single'
  downloadAncestorId.value = null
  downloadSelectedAssetIds.value = []
}

function handleDownloadModeChange() {
  downloadAncestorId.value = null
  downloadSelectedAssetIds.value = []
}

async function executeDownload() {
  if (!downloadOptions.value) return

  const targetId = downloadOptions.value.target.id
  const params: {
    mode: CertificateAssetDownloadMode
    ancestor_id?: number
    asset_ids?: number[]
  } = { mode: downloadMode.value }

  if (downloadMode.value === 'chain' && downloadAncestorId.value) {
    params.ancestor_id = downloadAncestorId.value
  }

  if (downloadMode.value === 'tree' && downloadSelectedAssetIds.value.length > 0) {
    params.asset_ids = downloadSelectedAssetIds.value
  }

  downloadLoading.value = true
  const result = await certificateAssetsApi.download(targetId, params)
  downloadLoading.value = false

  if (result.error) {
    ElMessage.error(result.error)
    return
  }

  ElMessage.success('下载成功')
  downloadDialogVisible.value = false
}

function buildDownloadTreeData(): Array<{ id: number; name: string; children?: Array<{ id: number; name: string }> }> {
  if (!downloadOptions.value?.tree_items) return []

  const itemMap = new Map<number, { id: number; name: string; children: Array<{ id: number; name: string }> }>()
  downloadOptions.value.tree_items.forEach(item => {
    itemMap.set(item.item.id, { id: item.item.id, name: `${item.item.name} (${formatAssetType(item.item.asset_type)})`, children: [] })
  })

  const roots: Array<{ id: number; name: string; children: Array<{ id: number; name: string }> }> = []
  downloadOptions.value.tree_items.forEach(item => {
    const node = itemMap.get(item.item.id)!
    if (item.parent_asset_id) {
      const parent = itemMap.get(item.parent_asset_id)
      parent?.children.push(node)
    } else {
      roots.push(node)
    }
  })

  return roots
}

function handleDownloadTreeCheck(
  _data: unknown,
  checkedInfo: { checkedKeys: Array<number | string> }
) {
  downloadSelectedAssetIds.value = checkedInfo.checkedKeys.map(Number)
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
    message = `删除该资产会同时影响以下证书资产：\n${childList}\n\n删除将同时删除以上资产，此操作不可恢复。\n确定继续删除吗？`
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
      <div class="page-header">
        <el-row justify="space-between" align="middle">
          <h1>证书资产</h1>
          <el-button @click="loadData" :loading="loading">刷新</el-button>
        </el-row>
      </div>

      <el-main class="content-main">
        <el-row :gutter="16" class="usage-row">
          <el-col
            v-for="usage in usageCards"
            :key="usage.usage_type"
            :xs="24"
            :md="12"
          >
            <el-card class="usage-card">
              <template #header>
                <el-row justify="space-between" align="middle">
                  <span class="usage-title">{{ formatUsageType(usage.usage_type) }}</span>
                  <el-tag size="small" :type="usageStatusTagType(usage.status)">
                    {{ formatUsageStatus(usage.status) }}
                  </el-tag>
                </el-row>
              </template>

              <el-descriptions :column="1" border size="small">
                <el-descriptions-item label="当前证书">
                  {{ usage.asset_name || '-' }}
                </el-descriptions-item>
              </el-descriptions>

              <el-space wrap class="usage-actions">
                <el-button type="primary" @click="openUsageDialog(usage.usage_type)">
                  {{ usage.asset_id ? '更换证书' : '绑定证书' }}
                </el-button>
                <el-button
                  :disabled="!usage.asset_id"
                  @click="handleUnbindUsage(usage.usage_type)"
                >
                  解绑
                </el-button>
              </el-space>
            </el-card>
          </el-col>
        </el-row>

        <el-card class="filter-card">
          <el-row :gutter="16" align="middle">
            <el-col :xs="24" :sm="12" :md="6">
              <el-select v-model="filterAssetType" placeholder="资产类型" clearable>
                <el-option label="全部" value="all" />
                <el-option label="证书" value="certificate" />
                <el-option label="CA" value="ca" />
              </el-select>
            </el-col>
            <el-col :xs="24" :sm="12" :md="6">
              <el-select v-model="filterSource" placeholder="来源" clearable>
                <el-option label="全部" value="all" />
                <el-option label="上传" value="upload" />
                <el-option label="生成" value="generated" />
              </el-select>
            </el-col>
            <el-col
              :xs="24"
              :sm="24"
              :md="12"
              :class="['filter-actions', { 'filter-actions-mobile': isMobile }]"
            >
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
            <el-table-column label="备注" min-width="160">
              <template #default="{ row }">
                {{ row.remark || '-' }}
              </template>
            </el-table-column>
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
            <el-table-column label="操作" width="190" align="center" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" size="small" @click="openDetailDrawer(row)">查看</el-button>
                <el-button link type="primary" size="small" @click="openEditDialog(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-main>
    </template>

    <!-- Import dialog -->
    <component
      :is="modalLayerComponent"
      v-model="usageDialogVisible"
      title="绑定入口证书"
      :close-on-click-modal="false"
      v-bind="modalLayerProps"
    >
      <el-form
        ref="usageFormRef"
        :model="usageForm"
        :rules="usageFormRules"
        label-width="88px"
      >
        <el-form-item label="使用点">
          <el-input :model-value="formatUsageType(usageForm.usage_type)" readonly />
        </el-form-item>
        <el-form-item label="证书" prop="asset_id">
          <el-select
            v-model="usageForm.asset_id"
            placeholder="请选择证书"
            class="full-width"
          >
            <el-option
              v-for="asset in bindableCertificateAssets"
              :key="asset.id"
              :label="buildUsageAssetOptionLabel(asset)"
              :value="asset.id"
            />
          </el-select>
        </el-form-item>
      </el-form>

      <el-alert
        v-if="usageForm.usage_type === 'webui_https'"
        type="info"
        :closable="false"
        show-icon
      >
        <template #title>绑定后会切到 HTTPS</template>
        当前浏览器会自动跳转到同地址的 `https://`。
      </el-alert>

      <template #footer>
        <el-button @click="usageDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="usageSubmitting" @click="submitUsageBinding">保存</el-button>
      </template>
    </component>

    <component
      :is="modalLayerComponent"
      v-model="importDialogVisible"
      title="导入证书/CA"
      :close-on-click-modal="false"
      v-bind="modalLayerProps"
    >
      <el-tabs v-model="importActiveTab" @tab-change="handleImportTabChange">
        <el-tab-pane label="上传文件" name="upload">
          <el-form
            ref="importUploadFormRef"
            :model="importForm"
            :rules="importFormRules"
            label-width="80px"
          >
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
                :on-change="handleCrtUploadChange"
              >
                <el-space wrap size="small">
                  <el-button>
                    <el-icon><Upload /></el-icon>
                    选择证书
                  </el-button>
                  <el-text v-if="importForm.crt" size="small" type="info">
                    已选择: {{ uploadCrtFile?.name || '已粘贴内容' }}
                  </el-text>
                </el-space>
              </el-upload>
            </el-form-item>
            <el-form-item label="私钥文件">
              <el-upload
                :auto-upload="false"
                :show-file-list="false"
                accept=".key,.pem"
                :on-change="handleKeyUploadChange"
              >
                <el-space wrap size="small">
                  <el-button>
                    <el-icon><Upload /></el-icon>
                    选择私钥
                  </el-button>
                  <el-text v-if="importForm.key" size="small" type="info">
                    已选择: {{ uploadKeyFile?.name || '已粘贴内容' }}
                  </el-text>
                </el-space>
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
    </component>

    <!-- Edit dialog -->
    <component
      :is="modalLayerComponent"
      v-model="editDialogVisible"
      title="编辑资产"
      :close-on-click-modal="false"
      v-bind="modalLayerProps"
    >
      <el-form
        ref="editFormRef"
        :model="editForm"
        :rules="editFormRules"
        label-width="80px"
      >
        <el-form-item label="名称" prop="name">
          <el-input v-model="editForm.name" placeholder="请输入名称" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="editForm.remark" placeholder="可选备注" />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="editDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="editSubmitting" @click="submitEdit">保存</el-button>
      </template>
    </component>

    <!-- Generate dialog -->
    <component
      :is="modalLayerComponent"
      v-model="generateDialogVisible"
      title="生成证书/CA"
      :close-on-click-modal="false"
      v-bind="modalLayerProps"
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
            <el-form-item label="上游 CA">
              <el-space direction="vertical" fill size="small" class="full-width">
                <el-select
                  v-model="generateCaForm.issuer_asset_id"
                  placeholder="留空则生成自签根 CA"
                  class="full-width"
                  clearable
                >
                  <el-option
                    v-for="ca in canIssueCAs"
                    :key="ca.id"
                    :label="ca.name"
                    :value="ca.id"
                  />
                </el-select>
                <el-text size="small" type="info">
                  选择上游 CA 将生成中间 CA，留空则生成自签根 CA
                </el-text>
              </el-space>
            </el-form-item>
            <el-form-item label="CN" prop="common_name">
              <el-input v-model="generateCaForm.common_name" placeholder="CA 的 CN 字段" />
            </el-form-item>
            <el-form-item label="密钥算法" prop="key_algorithm">
              <el-select
                v-model="generateCaForm.key_algorithm"
                class="full-width"
                @change="handleGenerateCaKeyAlgorithmChange"
              >
                <el-option
                  v-for="option in generateKeyAlgorithmOptions"
                  :key="option.value"
                  :label="option.label"
                  :value="option.value"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="密钥长度" prop="key_bits">
              <el-space direction="vertical" fill size="small" class="full-width">
                <el-input-number
                  v-if="isCustomGenerateKeyBitsAlgorithm(generateCaForm.key_algorithm)"
                  v-model="generateCaForm.key_bits"
                  class="full-width"
                  :min="minimumRSAGenerateKeyBits"
                  :step="8"
                  :step-strictly="true"
                />
                <el-select v-else v-model="generateCaForm.key_bits" class="full-width">
                  <el-option
                    v-for="option in getGenerateKeyBitsOptions(generateCaForm.key_algorithm)"
                    :key="`${generateCaForm.key_algorithm}-${option.value}`"
                    :label="option.label"
                    :value="option.value"
                  />
                </el-select>
                <el-text
                  v-if="isCustomGenerateKeyBitsAlgorithm(generateCaForm.key_algorithm)"
                  size="small"
                  type="info"
                >
                  RSA 密钥长度由管理员输入，必须是大于等于 {{ minimumRSAGenerateKeyBits }} 的 8 的倍数
                </el-text>
              </el-space>
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
            <el-form-item label="CN" prop="common_name">
              <el-input v-model="generateCertForm.common_name" placeholder="证书的 CN 字段" />
            </el-form-item>
            <el-form-item label="密钥算法" prop="key_algorithm">
              <el-select
                v-model="generateCertForm.key_algorithm"
                class="full-width"
                @change="handleGenerateCertKeyAlgorithmChange"
              >
                <el-option
                  v-for="option in generateKeyAlgorithmOptions"
                  :key="option.value"
                  :label="option.label"
                  :value="option.value"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="密钥长度" prop="key_bits">
              <el-space direction="vertical" fill size="small" class="full-width">
                <el-input-number
                  v-if="isCustomGenerateKeyBitsAlgorithm(generateCertForm.key_algorithm)"
                  v-model="generateCertForm.key_bits"
                  class="full-width"
                  :min="minimumRSAGenerateKeyBits"
                  :step="8"
                  :step-strictly="true"
                />
                <el-select v-else v-model="generateCertForm.key_bits" class="full-width">
                  <el-option
                    v-for="option in getGenerateKeyBitsOptions(generateCertForm.key_algorithm)"
                    :key="`${generateCertForm.key_algorithm}-${option.value}`"
                    :label="option.label"
                    :value="option.value"
                  />
                </el-select>
                <el-text
                  v-if="isCustomGenerateKeyBitsAlgorithm(generateCertForm.key_algorithm)"
                  size="small"
                  type="info"
                >
                  RSA 密钥长度由管理员输入，必须是大于等于 {{ minimumRSAGenerateKeyBits }} 的 8 的倍数
                </el-text>
              </el-space>
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
    </component>

    <!-- Detail drawer -->
    <el-drawer
      v-model="detailDrawerVisible"
      title="资产详情"
      :direction="detailDrawerDirection"
      :size="detailDrawerSize"
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
          <el-button @click="openEditDialog(detailAsset)">编辑</el-button>
          <el-button type="primary" @click="openDownloadDialog(detailAsset)">下载</el-button>
          <el-button type="danger" @click="handleDelete(detailAsset)">删除资产</el-button>
        </div>
      </template>
    </el-drawer>

    <!-- Download dialog -->
    <component
      :is="modalLayerComponent"
      v-model="downloadDialogVisible"
      title="下载证书"
      :close-on-click-modal="false"
      v-bind="modalLayerProps"
    >
      <el-form v-loading="downloadLoading" label-width="80px">
        <el-form-item label="下载模式">
          <el-radio-group v-model="downloadMode" @change="handleDownloadModeChange">
            <el-radio
              v-for="mode in downloadOptions?.modes || []"
              :key="mode.mode"
              :value="mode.mode"
            >
              {{ formatDownloadMode(mode.mode) }}
            </el-radio>
          </el-radio-group>
        </el-form-item>

        <el-form-item v-if="downloadMode === 'chain' && downloadOptions?.chain_items" label="目标祖先">
          <el-space direction="vertical" fill size="small" class="full-width">
            <el-select v-model="downloadAncestorId" placeholder="选择要下载到哪个祖先" class="full-width">
              <el-option
                v-for="item in downloadOptions.chain_items"
                :key="item.item.id"
                :label="`${item.item.name} (depth: ${item.depth})`"
                :value="item.item.id"
              />
            </el-select>
            <el-text size="small" type="info">
              选择祖先后，下载从当前资产到该祖先的完整证书链
            </el-text>
          </el-space>
        </el-form-item>

        <el-form-item v-if="downloadMode === 'tree' && downloadOptions?.tree_items" label="选择资产">
          <el-space direction="vertical" fill size="small" class="full-width">
            <el-tree
              :data="buildDownloadTreeData()"
              :props="downloadTreeProps"
              show-checkbox
              node-key="id"
              default-expand-all
              @check="handleDownloadTreeCheck"
            />
            <el-text size="small" type="info">
              勾选要下载的资产，将打包为 ZIP 文件
            </el-text>
          </el-space>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="downloadDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="downloadLoading" @click="executeDownload">下载</el-button>
      </template>
    </component>
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

.usage-row {
  flex-shrink: 0;
}

.usage-card {
  height: 100%;
}

.usage-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.usage-alert {
  margin-top: var(--spacing-md);
}

.usage-actions {
  width: 100%;
  margin-top: var(--spacing-md);
}

.filter-card {
  flex-shrink: 0;
}

.filter-card .el-select {
  width: 100%;
}

.filter-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--spacing-sm);
  justify-content: flex-end;
}

.filter-actions-mobile {
  justify-content: flex-start;
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

.mono-text {
  font-family: monospace;
  word-break: break-all;
}

.drawer-footer {
  margin-top: var(--spacing-xl);
  padding-top: var(--spacing-base);
  border-top: 1px solid var(--el-border-color-lighter);
}
</style>
