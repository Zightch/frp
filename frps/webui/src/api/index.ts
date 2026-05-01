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

export const ratePoliciesApi = {
  list: () => request<{ items: RatePolicy[] }>('/rate-policies'),

  create: (data: RatePolicyPayload) =>
    request<{ item: RatePolicy }>('/rate-policies', {
      method: 'POST',
      body: JSON.stringify(data)
    }),

  update: (id: number, data: Partial<RatePolicyPayload>) =>
    request<{ item: RatePolicy }>(`/rate-policies/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data)
    }),

  delete: (id: number) => request(`/rate-policies/${id}`, { method: 'DELETE' }),

  getTunnels: (id: number) => request<{ items: TunnelBinding[] }>(`/rate-policies/${id}/tunnels`),

  bindTunnels: (id: number, tunnelIds: number[]) =>
    request<{ bound: number }>(`/rate-policies/${id}/tunnels`, {
      method: 'POST',
      body: JSON.stringify({ tunnel_ids: tunnelIds })
    }),

  unbindTunnel: (policyId: number, tunnelId: number) =>
    request(`/rate-policies/${policyId}/tunnels/${tunnelId}`, { method: 'DELETE' })
}

export const bindableTunnelsApi = {
  list: () => request<{ items: TunnelBinding[] }>('/tunnels/bindable')
}
