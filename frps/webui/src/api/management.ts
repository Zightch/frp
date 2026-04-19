import { http } from "@/api/http";

type ProxyGroupShape = {
  id: number;
  name: string;
  token_id: string;
  enabled: boolean;
  client_access_mode: string;
  tunnel_access_mode: string;
  created_at: string;
  updated_at: string;
};

type TunnelShape = {
  id: number;
  group_id: number;
  group_name: string;
  name: string;
  protocol: string;
  remote_type: string;
  remote_start: number;
  remote_end: number;
  local_host: string;
  local_start: number;
  local_end: number;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type ProxyGroup = {
  id: number;
  name: string;
  tokenId: string;
  enabled: boolean;
  clientAccessMode: string;
  tunnelAccessMode: string;
  createdAt: string;
  updatedAt: string;
};

export type ProxyGroupPayload = {
  name: string;
  enabled: boolean;
};

export type ProxyGroupTokenResponse = {
  item: ProxyGroup;
  token: string;
};

export type TunnelProtocol = "tcp" | "udp";
export type TunnelRemoteType = "single" | "range";

export type Tunnel = {
  id: number;
  groupId: number;
  groupName: string;
  name: string;
  protocol: TunnelProtocol;
  remoteType: TunnelRemoteType;
  remoteStart: number;
  remoteEnd: number;
  localHost: string;
  localStart: number;
  localEnd: number;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
};

export type TunnelPayload = {
  groupId: number;
  name: string;
  protocol: TunnelProtocol;
  remoteType: TunnelRemoteType;
  remoteStart: number;
  remoteEnd: number;
  localHost: string;
  localStart: number;
  localEnd: number;
  enabled: boolean;
};

function normalizeProxyGroup(item: ProxyGroupShape): ProxyGroup {
  return {
    id: item.id,
    name: item.name,
    tokenId: item.token_id,
    enabled: item.enabled,
    clientAccessMode: item.client_access_mode,
    tunnelAccessMode: item.tunnel_access_mode,
    createdAt: item.created_at,
    updatedAt: item.updated_at,
  };
}

function normalizeTunnel(item: TunnelShape): Tunnel {
  return {
    id: item.id,
    groupId: item.group_id,
    groupName: item.group_name,
    name: item.name,
    protocol: item.protocol as TunnelProtocol,
    remoteType: item.remote_type as TunnelRemoteType,
    remoteStart: item.remote_start,
    remoteEnd: item.remote_end,
    localHost: item.local_host,
    localStart: item.local_start,
    localEnd: item.local_end,
    enabled: item.enabled,
    createdAt: item.created_at,
    updatedAt: item.updated_at,
  };
}

export async function fetchProxyGroups(): Promise<ProxyGroup[]> {
  const response = await http.get<{ items: ProxyGroupShape[] }>("/proxy-groups");
  return response.data.items.map(normalizeProxyGroup);
}

export async function createProxyGroup(payload: ProxyGroupPayload): Promise<ProxyGroupTokenResponse> {
  const response = await http.post<{ item: ProxyGroupShape; token: string }>("/proxy-groups", {
    name: payload.name,
    enabled: payload.enabled,
  });
  return {
    item: normalizeProxyGroup(response.data.item),
    token: response.data.token,
  };
}

export async function updateProxyGroup(id: number, payload: ProxyGroupPayload): Promise<ProxyGroup> {
  const response = await http.patch<{ item: ProxyGroupShape }>(`/proxy-groups/${id}`, {
    name: payload.name,
    enabled: payload.enabled,
  });
  return normalizeProxyGroup(response.data.item);
}

export async function deleteProxyGroup(id: number): Promise<void> {
  await http.delete(`/proxy-groups/${id}`);
}

export async function resetProxyGroupToken(id: number): Promise<ProxyGroupTokenResponse> {
  const response = await http.post<{ item: ProxyGroupShape; token: string }>(`/proxy-groups/${id}/token`);
  return {
    item: normalizeProxyGroup(response.data.item),
    token: response.data.token,
  };
}

export async function fetchTunnels(): Promise<Tunnel[]> {
  const response = await http.get<{ items: TunnelShape[] }>("/tunnels");
  return response.data.items.map(normalizeTunnel);
}

export async function createTunnel(payload: TunnelPayload): Promise<Tunnel> {
  const response = await http.post<{ item: TunnelShape }>("/tunnels", {
    group_id: payload.groupId,
    name: payload.name,
    protocol: payload.protocol,
    remote_type: payload.remoteType,
    remote_start: payload.remoteStart,
    remote_end: payload.remoteEnd,
    local_host: payload.localHost,
    local_start: payload.localStart,
    local_end: payload.localEnd,
    enabled: payload.enabled,
  });
  return normalizeTunnel(response.data.item);
}

export async function updateTunnel(id: number, payload: TunnelPayload): Promise<Tunnel> {
  const response = await http.patch<{ item: TunnelShape }>(`/tunnels/${id}`, {
    group_id: payload.groupId,
    name: payload.name,
    protocol: payload.protocol,
    remote_type: payload.remoteType,
    remote_start: payload.remoteStart,
    remote_end: payload.remoteEnd,
    local_host: payload.localHost,
    local_start: payload.localStart,
    local_end: payload.localEnd,
    enabled: payload.enabled,
  });
  return normalizeTunnel(response.data.item);
}

export async function deleteTunnel(id: number): Promise<void> {
  await http.delete(`/tunnels/${id}`);
}
