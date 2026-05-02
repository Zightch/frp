import { API_BASE } from '@/runtime/basePath'

// --- Types ---

export interface ProxyGroup {
  id: number
  name: string
  client_id: string
  effective_ip: string
  enabled: boolean
  control_transport_security: 'plain' | 'tls_required'
  status: string
  status_reason?: string
  created_at: string
  updated_at: string
}

export interface LocalIP {
  addr: string
  family: string
  standard: string
}

export type TunnelTLSMode = 'off' | 'tls' | 'mtls'

export interface Tunnel {
  id: number
  group_id: number
  group_name: string
  name: string
  protocol: 'tcp' | 'udp'
  remote_type: 'single' | 'range'
  remote_start: number
  remote_end: number
  local_host: string
  local_start: number
  local_end: number
  listen_tls_mode: TunnelTLSMode
  listen_tls_load_system_ca: boolean
  listen_tls_server_cert_asset_id?: number
  listen_tls_client_ca_asset_ids?: number[]
  backend_tls_mode: TunnelTLSMode
  backend_tls_server_name?: string
  backend_tls_load_system_ca: boolean
  backend_tls_insecure_skip_verify: boolean
  backend_tls_client_cert_asset_id?: number
  backend_tls_ca_asset_ids?: number[]
  enabled: boolean
  status: string
  status_reason?: string
  created_at: string
  updated_at: string
}

export interface TunnelPayload {
  group_id: number
  name: string
  protocol: 'tcp' | 'udp'
  remote_type: 'single' | 'range'
  remote_start: number
  remote_end: number
  local_host: string
  local_start: number
  local_end: number
  listen_tls_mode: TunnelTLSMode
  listen_tls_load_system_ca?: boolean
  listen_tls_server_cert_asset_id?: number
  listen_tls_client_ca_asset_ids?: number[]
  backend_tls_mode: TunnelTLSMode
  backend_tls_server_name?: string
  backend_tls_load_system_ca?: boolean
  backend_tls_insecure_skip_verify?: boolean
  backend_tls_client_cert_asset_id?: number
  backend_tls_ca_asset_ids?: number[]
  enabled: boolean
}

// --- Request helper ---

interface ApiResponse<T = unknown> {
  data?: T
  error?: string
  errorCode?: string
  details?: unknown
}

async function request<T>(
  path: string,
  options?: RequestInit & { rawBody?: boolean }
): Promise<ApiResponse<T>> {
  try {
    const { rawBody, headers: requestHeaders, ...fetchOptions } = options || {}
    const headers = new Headers(requestHeaders)
    if (!rawBody && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json')
    }

    const response = await fetch(`${API_BASE}${path}`, {
      credentials: 'include',
      headers,
      ...fetchOptions
    })

    if (!response.ok) {
      const contentType = response.headers.get('content-type') || ''
      if (contentType.includes('application/json')) {
        const payload = await response.json().catch(() => null) as {
          error?: string
          error_code?: string
          details?: unknown
        } | null
        if (payload) {
          return {
            error: payload.error || response.statusText || '请求失败',
            errorCode: payload.error_code,
            details: payload.details
          }
        }

        return { error: response.statusText || '请求失败' }
      }

      const error = await response.text()
      return { error: error || response.statusText || '请求失败' }
    }

    if (response.status === 204) {
      return {}
    }

    const contentType = response.headers.get('content-type') || ''
    if (!contentType.includes('application/json')) {
      return {}
    }

    const data = await response.json()
    return { data }
  } catch (err) {
    return { error: String(err) }
  }
}

function parseContentDispositionFileName(contentDisposition: string | null): string | null {
  if (!contentDisposition) {
    return null
  }

  const encodedMatch = contentDisposition.match(/filename\*\s*=\s*UTF-8''([^;]+)/i)
  if (encodedMatch?.[1]) {
    try {
      return decodeURIComponent(encodedMatch[1])
    } catch {
      return encodedMatch[1]
    }
  }

  const plainMatch = contentDisposition.match(/filename\s*=\s*"([^"]+)"|filename\s*=\s*([^;]+)/i)
  if (!plainMatch) {
    return null
  }

  return plainMatch[1] || plainMatch[2]?.trim() || null
}

function normalizeCertificateUsageType(value: string): CertificateUsageType | '' {
  switch (value) {
    case 'webui_https':
      return 'webui_https'
    case 'frpc_tls':
      return 'frpc_tls'
    default:
      return ''
  }
}

function normalizeCertificateUsages(
  items?: Array<Omit<CertificateUsage, 'usage_type'> & { usage_type: string }>
): CertificateUsage[] {
  if (!items?.length) {
    return []
  }

  const normalizedByType = new Map<CertificateUsageType, CertificateUsage>()
  for (const item of items) {
    const usageType = normalizeCertificateUsageType(item.usage_type)
    if (!usageType) {
      continue
    }

    const normalizedItem: CertificateUsage = {
      ...item,
      usage_type: usageType
    }
    const existingItem = normalizedByType.get(usageType)
    if (!existingItem || item.usage_type === usageType) {
      normalizedByType.set(usageType, normalizedItem)
    }
  }

  return Array.from(normalizedByType.values())
}

// --- Auth API ---

export const authApi = {
  state: () => request<{ initialized: boolean; authenticated: boolean; expires_at?: string }>('/auth/state'),

  init: (keyHash: string) => request<{ initialized: boolean }>('/auth/init', {
    method: 'POST',
    body: JSON.stringify({ key_hash: keyHash })
  }),

  challenge: () => request<{ challenge_id: string; salt: string; expires_at: string }>('/auth/challenge', {
    method: 'POST'
  }),

  login: (challengeId: string, proof: string) => request<{ initialized: boolean; authenticated: boolean; expires_at: string }>('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ challenge_id: challengeId, proof })
  }),

  takeover: (pendingLoginTicket: string, observedGeneration: number) => request<{ initialized: boolean; authenticated: boolean; expires_at: string }>('/auth/takeover', {
    method: 'POST',
    body: JSON.stringify({ pending_login_ticket: pendingLoginTicket, observed_generation: observedGeneration })
  }),

  logout: () => request<{ initialized: boolean; authenticated: boolean; logged_out: boolean }>('/auth/logout', {
    method: 'POST'
  }),

  session: () => request<{ initialized: boolean; authenticated: boolean; expires_at: string }>('/auth/session')
}

// --- Proxy Groups API ---

export const proxyGroupsApi = {
  list: () => request<{ items: ProxyGroup[] }>('/proxy-groups'),

  create: (data: {
    name: string
    effective_ip: string
    enabled?: boolean
    control_transport_security?: 'plain' | 'tls_required'
  }) =>
    request<{ item: ProxyGroup; key: string }>('/proxy-groups', {
      method: 'POST',
      body: JSON.stringify(data)
    }),

  update: (id: number, data: {
    name?: string
    effective_ip?: string
    enabled?: boolean
    control_transport_security?: 'plain' | 'tls_required'
  }) =>
    request<{ item: ProxyGroup }>(`/proxy-groups/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data)
    }),

  delete: (id: number) => request(`/proxy-groups/${id}`, { method: 'DELETE' }),

  rotateKey: (id: number) =>
    request<{ item: ProxyGroup; key: string }>(`/proxy-groups/${id}/key`, {
      method: 'POST'
    })
}

export const localIPsApi = {
  list: () => request<{ items: LocalIP[] }>('/local-ips')
}

// --- Tunnels API ---

export interface TunnelCreateResult {
  item: Tunnel
  warnings?: string[]
}

export interface TunnelUpdateResult {
  item: Tunnel
  warnings?: string[]
}

export const tunnelsApi = {
  list: (groupId?: number) => {
    const query = groupId ? `?group_id=${groupId}` : ''
    return request<{ items: Tunnel[] }>(`/tunnels${query}`)
  },

  create: (data: TunnelPayload) => request<TunnelCreateResult>('/tunnels', {
    method: 'POST',
    body: JSON.stringify(data)
  }),

  update: (id: number, data: TunnelPayload) => request<TunnelUpdateResult>(`/tunnels/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(data)
  }),

  delete: (id: number) => request(`/tunnels/${id}`, { method: 'DELETE' })
}

// --- Certificate Assets API ---

export interface CertificateAsset {
  id: number
  name: string
  remark: string
  source: 'upload' | 'generated'
  asset_type: 'certificate' | 'ca'
  format_type: 'pem'
  issuer_asset_id?: number
  issuer_name?: string
  common_name?: string
  subject?: string
  issuer?: string
  serial_number?: string
  not_before?: string
  not_after?: string
  dns_names?: string[]
  ip_addresses?: string[]
  key_present: boolean
  can_issue: boolean
  is_self_signed: boolean
  chain_length: number
  created_at: string
  updated_at: string
}

export interface CertificateAssetDeleteImpact {
  target: CertificateAsset
  affected_items: Array<{
    item: CertificateAsset
    depth: number
  }>
  requires_confirmation: boolean
  warning_message?: string
}

export type CertificateAssetDownloadMode = 'original' | 'single' | 'chain' | 'tree'

export interface CertificateAssetDownloadModeOption {
  mode: CertificateAssetDownloadMode
  default: boolean
}

export interface CertificateAssetDownloadChainItem {
  item: CertificateAsset
  depth: number
}

export interface CertificateAssetDownloadTreeItem {
  item: CertificateAsset
  parent_asset_id?: number
  depth: number
}

export interface CertificateAssetDownloadOptions {
  target: CertificateAsset
  modes: CertificateAssetDownloadModeOption[]
  chain_items?: CertificateAssetDownloadChainItem[]
  tree_items?: CertificateAssetDownloadTreeItem[]
}

export interface CertificateAssetPastePayload {
  name: string
  remark?: string
  crt: string
  key?: string
}

export type CertificateAssetGenerateKeyAlgorithm = 'ecdsa' | 'rsa' | 'ed25519'

export interface CertificateAssetGeneratePayload {
  name: string
  remark?: string
  asset_type: 'certificate' | 'ca'
  issuer_asset_id?: number
  common_name: string
  validity_days: number
  dns_names?: string[]
  ip_addresses?: string[]
  key_algorithm?: CertificateAssetGenerateKeyAlgorithm
  key_bits?: number
}

export type CertificateUsageType = 'webui_https' | 'frpc_tls'

export interface CertificateUsage {
  usage_type: CertificateUsageType
  asset_id?: number
  asset_name?: string
  enabled: boolean
  status: 'enabled' | 'disabled' | 'unbound' | 'error'
  status_reason?: string
  resolved_chain_length: number
  updated_at?: string
  asset?: CertificateAsset
}

export const certificateAssetsApi = {
  list: () => request<{ items: CertificateAsset[] }>('/certificate-assets'),

  update: (id: number, data: { name: string; remark?: string }) =>
    request<{ item: CertificateAsset }>(`/certificate-assets/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data)
    }),

  upload: (formData: FormData) => request<{ item: CertificateAsset }>('/certificate-assets/upload', {
    method: 'POST',
    body: formData,
    rawBody: true
  }),

  paste: (data: CertificateAssetPastePayload) => request<{ item: CertificateAsset }>('/certificate-assets/paste', {
    method: 'POST',
    body: JSON.stringify(data)
  }),

  generate: (data: CertificateAssetGeneratePayload) => request<{ item: CertificateAsset }>('/certificate-assets/generate', {
    method: 'POST',
    body: JSON.stringify(data)
  }),

  getDeleteImpact: (id: number) => request<CertificateAssetDeleteImpact>(`/certificate-assets/${id}/delete-impact`),

  delete: (id: number, cascade: boolean = false) => {
    const query = cascade ? '?cascade=true' : ''
    return request<{ deleted: boolean; deleted_ids: number[] }>(`/certificate-assets/${id}${query}`, {
      method: 'DELETE'
    })
  },

  getDownloadOptions: (id: number) => request<CertificateAssetDownloadOptions>(`/certificate-assets/${id}/download-options`),

  download: async (id: number, params: {
    mode: CertificateAssetDownloadMode
    ancestor_id?: number
    asset_ids?: number[]
  }) => {
    const query = new URLSearchParams({ mode: params.mode })
    if (params.ancestor_id) {
      query.set('ancestor_id', String(params.ancestor_id))
    }
    if (params.asset_ids && params.asset_ids.length > 0) {
      params.asset_ids.forEach(assetId => query.append('asset_ids', String(assetId)))
    }

    try {
      const response = await fetch(`${API_BASE}/certificate-assets/${id}/download?${query.toString()}`, {
        credentials: 'include'
      })

      if (!response.ok) {
        const contentType = response.headers.get('content-type') || ''
        if (contentType.includes('application/json')) {
          const payload = await response.json().catch(() => null) as { error?: string } | null
          return { error: payload?.error || response.statusText || '下载失败' }
        }
        return { error: response.statusText || '下载失败' }
      }

      const blob = await response.blob()
      const fileName = parseContentDispositionFileName(response.headers.get('content-disposition')) || 'download'

      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = fileName
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)

      return { data: true }
    } catch (err) {
      return { error: String(err) }
    }
  }
}

export const certificateUsagesApi = {
  list: async () => {
    const result = await request<{ items: Array<Omit<CertificateUsage, 'usage_type'> & { usage_type: string }> }>('/settings/entry-certificates')
    if (result.data) {
      result.data = {
        items: normalizeCertificateUsages(result.data.items)
      }
    }
    return result as ApiResponse<{ items: CertificateUsage[] }>
  },

  bind: (usageType: CertificateUsageType, assetID: number) =>
    request<{ item: CertificateUsage }>(`/settings/entry-certificates/${usageType}`, {
      method: 'PUT',
      body: JSON.stringify({ asset_id: assetID, enabled: true })
    }),

  unbind: (usageType: CertificateUsageType) =>
    request<{ item: CertificateUsage }>(`/settings/entry-certificates/${usageType}`, {
      method: 'DELETE'
    })
}

// --- Rate Policy API ---

export type RatePolicyMode = 'independent' | 'shared'
export type RatePolicyUnit = 'K' | 'M' | 'G'

interface RatePolicyWire {
  id: number
  name: string
  mode: RatePolicyMode
  downlink_bps: number
  uplink_bps: number
  binding_count: number
  created_at: string
  updated_at: string
}

interface RatePolicyBindingWire {
  id: number
  rate_policy_id: number
  tunnel_id: number
  group_id: number
  group_name: string
  tunnel_name: string
  protocol: 'tcp' | 'udp'
  remote_type: 'single' | 'range'
  remote_start: number
  remote_end: number
  created_at: string
  updated_at: string
}

export interface RatePolicy {
  id: number
  name: string
  mode: RatePolicyMode
  downlink_value: number
  downlink_unit: RatePolicyUnit
  uplink_value: number
  uplink_unit: RatePolicyUnit
  tunnel_count: number
  created_at: string
  updated_at: string
}

export interface RatePolicyPayload {
  name: string
  mode: RatePolicyMode
  downlink_value: number
  downlink_unit: RatePolicyUnit
  uplink_value: number
  uplink_unit: RatePolicyUnit
}

export interface TunnelBinding {
  id: number
  group_id: number
  group_name: string
  name: string
  protocol: 'tcp' | 'udp'
  remote_start: number
  remote_end: number
  rate_policy_id?: number
  rate_policy_name?: string
}

const ratePolicyUnitFactors: Record<RatePolicyUnit, number> = {
  K: 1_000,
  M: 1_000_000,
  G: 1_000_000_000
}

function pickRatePolicyUnit(bps: number): RatePolicyUnit {
  if (bps >= ratePolicyUnitFactors.G && bps % ratePolicyUnitFactors.G === 0) {
    return 'G'
  }
  if (bps >= ratePolicyUnitFactors.M && bps % ratePolicyUnitFactors.M === 0) {
    return 'M'
  }
  return 'K'
}

function normalizeRatePolicyItem(item: RatePolicyWire): RatePolicy {
  const downlinkUnit = pickRatePolicyUnit(item.downlink_bps)
  const uplinkUnit = pickRatePolicyUnit(item.uplink_bps)

  return {
    id: item.id,
    name: item.name,
    mode: item.mode,
    downlink_value: item.downlink_bps / ratePolicyUnitFactors[downlinkUnit],
    downlink_unit: downlinkUnit,
    uplink_value: item.uplink_bps / ratePolicyUnitFactors[uplinkUnit],
    uplink_unit: uplinkUnit,
    tunnel_count: item.binding_count,
    created_at: item.created_at,
    updated_at: item.updated_at
  }
}

function normalizeTunnelBindingItem(item: RatePolicyBindingWire, policyName?: string): TunnelBinding {
  return {
    id: item.tunnel_id,
    group_id: item.group_id,
    group_name: item.group_name,
    name: item.tunnel_name,
    protocol: item.protocol,
    remote_start: item.remote_start,
    remote_end: item.remote_end,
    rate_policy_id: item.rate_policy_id,
    rate_policy_name: policyName
  }
}

function serializeRatePolicyPayload(data: RatePolicyPayload) {
  return {
    name: data.name,
    mode: data.mode,
    downlink: {
      value: data.downlink_value,
      unit: data.downlink_unit
    },
    uplink: {
      value: data.uplink_value,
      unit: data.uplink_unit
    }
  }
}

async function listRatePolicyBindingMap(policies: RatePolicy[]): Promise<ApiResponse<Map<number, { policy_id: number; policy_name: string }>>> {
  if (policies.length === 0) {
    return { data: new Map() }
  }

  const results = await Promise.all(
    policies.map(async (policy) => {
      const response = await request<{ items: RatePolicyBindingWire[] }>(`/rate-policies/${policy.id}/bindings`)
      return { policy, response }
    })
  )

  const bindingMap = new Map<number, { policy_id: number; policy_name: string }>()
  for (const { policy, response } of results) {
    if (response.error) {
      return { error: response.error, errorCode: response.errorCode, details: response.details }
    }

    for (const item of response.data?.items ?? []) {
      bindingMap.set(item.tunnel_id, {
        policy_id: policy.id,
        policy_name: policy.name
      })
    }
  }

  return { data: bindingMap }
}

export const ratePoliciesApi = {
  list: async () => {
    const result = await request<{ items: RatePolicyWire[] }>('/rate-policies')
    if (result.error) {
      return { error: result.error, errorCode: result.errorCode, details: result.details }
    }
    return {
      data: {
        items: (result.data?.items ?? []).map(normalizeRatePolicyItem)
      }
    } satisfies ApiResponse<{ items: RatePolicy[] }>
  },

  create: async (data: RatePolicyPayload) => {
    const result = await request<{ item: RatePolicyWire }>('/rate-policies', {
      method: 'POST',
      body: JSON.stringify(serializeRatePolicyPayload(data))
    })
    if (result.error) {
      return { error: result.error, errorCode: result.errorCode, details: result.details }
    }
    return {
      data: result.data?.item
        ? { item: normalizeRatePolicyItem(result.data.item) }
        : undefined
    } satisfies ApiResponse<{ item: RatePolicy }>
  },

  update: async (id: number, data: RatePolicyPayload) => {
    const result = await request<{ item: RatePolicyWire }>(`/rate-policies/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(serializeRatePolicyPayload(data))
    })
    if (result.error) {
      return { error: result.error, errorCode: result.errorCode, details: result.details }
    }
    return {
      data: result.data?.item
        ? { item: normalizeRatePolicyItem(result.data.item) }
        : undefined
    } satisfies ApiResponse<{ item: RatePolicy }>
  },

  delete: (id: number) => request(`/rate-policies/${id}`, { method: 'DELETE' }),

  getTunnels: async (id: number) => {
    const result = await request<{ items: RatePolicyBindingWire[] }>(`/rate-policies/${id}/bindings`)
    if (result.error) {
      return { error: result.error, errorCode: result.errorCode, details: result.details }
    }
    return {
      data: {
        items: (result.data?.items ?? []).map(item => normalizeTunnelBindingItem(item))
      }
    } satisfies ApiResponse<{ items: TunnelBinding[] }>
  },

  syncBindings: async (policyId: number, tunnelIds: number[]) => {
    const result = await request<{ items: RatePolicyBindingWire[] }>(`/rate-policies/${policyId}/bindings`, {
      method: 'PUT',
      body: JSON.stringify({ tunnel_ids: tunnelIds })
    })
    if (result.error) {
      return { error: result.error, errorCode: result.errorCode, details: result.details }
    }
    return {
      data: {
        items: (result.data?.items ?? []).map(item => normalizeTunnelBindingItem(item))
      }
    } satisfies ApiResponse<{ items: TunnelBinding[] }>
  }
}

export const bindableTunnelsApi = {
  list: async (policies?: RatePolicy[]): Promise<ApiResponse<{ items: TunnelBinding[] }>> => {
    const tunnelsResult = await tunnelsApi.list()
    if (tunnelsResult.error) {
      return { error: tunnelsResult.error, errorCode: tunnelsResult.errorCode, details: tunnelsResult.details }
    }

    let policyItems = policies
    if (!policyItems) {
      const policiesResult = await ratePoliciesApi.list()
      if (policiesResult.error) {
        return { error: policiesResult.error, errorCode: policiesResult.errorCode, details: policiesResult.details }
      }
      policyItems = policiesResult.data?.items ?? []
    }

    const bindingMapResult = await listRatePolicyBindingMap(policyItems)
    if (bindingMapResult.error) {
      return {
        error: bindingMapResult.error,
        errorCode: bindingMapResult.errorCode,
        details: bindingMapResult.details
      }
    }

    const bindingMap = bindingMapResult.data ?? new Map()
    const items = (tunnelsResult.data?.items ?? [])
      .filter(tunnel => tunnel.remote_type === 'single' && (tunnel.protocol === 'tcp' || tunnel.protocol === 'udp'))
      .map((tunnel) => {
        const binding = bindingMap.get(tunnel.id)
        return {
          id: tunnel.id,
          group_id: tunnel.group_id,
          group_name: tunnel.group_name,
          name: tunnel.name,
          protocol: tunnel.protocol,
          remote_start: tunnel.remote_start,
          remote_end: tunnel.remote_end,
          rate_policy_id: binding?.policy_id,
          rate_policy_name: binding?.policy_name
        } satisfies TunnelBinding
      })
      .sort((left, right) => {
        const groupDiff = left.group_name.localeCompare(right.group_name)
        if (groupDiff !== 0) {
          return groupDiff
        }
        return left.name.localeCompare(right.name)
      })

    return { data: { items } }
  }
}
