const API_BASE = '/api/v1'

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

// Auth API
export const authApi = {
  checkInit: () => request<{ initialized: boolean }>('/auth/init'),
  init: (secret: string) => request('/auth/init', {
    method: 'POST',
    body: JSON.stringify({ secret })
  }),
  getChallenge: () => request<{ challenge: string }>('/auth/challenge'),
  login: (challenge: string, response: string) => request('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ challenge, response })
  }),
  logout: () => request('/auth/logout', { method: 'POST' }),
  checkSession: () => request<{ authenticated: boolean }>('/auth/session')
}

// Proxy Groups API
export const proxyGroupsApi = {
  list: () => request<{ groups: Array<{ id: string; name: string; token_id: string }> }>('/proxy-groups'),
  create: (name: string) => request<{ id: string; token_id: string; token_secret: string }>('/proxy-groups', {
    method: 'POST',
    body: JSON.stringify({ name })
  }),
  update: (id: string, name: string) => request(`/proxy-groups/${id}`, {
    method: 'PUT',
    body: JSON.stringify({ name })
  }),
  delete: (id: string) => request(`/proxy-groups/${id}`, { method: 'DELETE' }),
  resetToken: (id: string) => request<{ token_id: string; token_secret: string }>(`/proxy-groups/${id}/token`, {
    method: 'POST'
  })
}

// Tunnels API
export const tunnelsApi = {
  list: (groupId?: string) => {
    const query = groupId ? `?group_id=${groupId}` : ''
    return request<{ tunnels: Array<{
      id: string
      name: string
      group_id: string
      type: string
      local_addr: string
      remote_addr: string
    }> }>(`/tunnels${query}`)
  },
  create: (data: {
    name: string
    group_id: string
    type: string
    local_addr: string
    remote_port?: number
    subdomain?: string
    custom_domain?: string
  }) => request('/tunnels', {
    method: 'POST',
    body: JSON.stringify(data)
  }),
  update: (id: string, data: Partial<{
    name: string
    type: string
    local_addr: string
    remote_port: number
    subdomain: string
    custom_domain: string
  }>) => request(`/tunnels/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data)
  }),
  delete: (id: string) => request(`/tunnels/${id}`, { method: 'DELETE' })
}
