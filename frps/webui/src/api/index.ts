const API_BASE = '/api/v1'

// --- Types ---

export interface ProxyGroup {
  id: number
  name: string
  client_id: string
  effective_ip: string
  enabled: boolean
  status: string
  status_reason?: string
  created_at: string
  updated_at: string
}

export interface ClientCredential {
  client_id: string
  client_secret: string
}

export interface LocalIP {
  addr: string
  family: string
  standard: string
}

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
  enabled: boolean
}

// --- Request helper ---

interface ApiResponse<T = unknown> {
  data?: T
  error?: string
}

async function request<T>(
  path: string,
  options?: RequestInit
): Promise<ApiResponse<T>> {
  try {
    const response = await fetch(`${API_BASE}${path}`, {
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json'
      },
      ...options
    })

    if (!response.ok) {
      const error = await response.text()
      return { error }
    }

    const data = await response.json()
    return { data }
  } catch (err) {
    return { error: String(err) }
  }
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

  create: (data: { name: string; effective_ip: string; enabled?: boolean }) =>
    request<{ item: ProxyGroup; credential: ClientCredential }>('/proxy-groups', {
      method: 'POST',
      body: JSON.stringify(data)
    }),

  update: (id: number, data: { name?: string; effective_ip?: string; enabled?: boolean }) =>
    request<{ item: ProxyGroup }>(`/proxy-groups/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data)
    }),

  delete: (id: number) => request(`/proxy-groups/${id}`, { method: 'DELETE' }),

  rotateCredentials: (id: number) =>
    request<{ item: ProxyGroup; credential: ClientCredential }>(`/proxy-groups/${id}/credentials`, {
      method: 'POST'
    })
}

export const localIPsApi = {
  list: () => request<{ items: LocalIP[] }>('/local-ips')
}

// --- Tunnels API ---

export const tunnelsApi = {
  list: (groupId?: number) => {
    const query = groupId ? `?group_id=${groupId}` : ''
    return request<{ items: Tunnel[] }>(`/tunnels${query}`)
  },

  create: (data: TunnelPayload) => request<{ item: Tunnel }>('/tunnels', {
    method: 'POST',
    body: JSON.stringify(data)
  }),

  update: (id: number, data: TunnelPayload) => request<{ item: Tunnel }>(`/tunnels/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(data)
  }),

  delete: (id: number) => request(`/tunnels/${id}`, { method: 'DELETE' })
}
