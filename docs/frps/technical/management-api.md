# frps 管理 API

## 管理会话

业务接口都要求管理会话。

会话传递方式：

- `frps_management_session` Cookie
- `Authorization: Bearer <token>`

## 健康检查

- `GET /healthz`
- `GET /readyz`
- `GET /api/v1/healthz`

## 认证接口

- `GET /api/v1/auth/state`
- `POST /api/v1/auth/init`
- `POST /api/v1/auth/challenge`
- `POST /api/v1/auth/login`
- `GET /api/v1/auth/session`
- `POST /api/v1/auth/logout`

## `proxy_group` 接口

- `GET /api/v1/proxy-groups`
- `POST /api/v1/proxy-groups`
- `PATCH /api/v1/proxy-groups/{id}`
- `DELETE /api/v1/proxy-groups/{id}`
- `POST /api/v1/proxy-groups/{id}/key`
- `GET /api/v1/local-ips`

## 隧道接口

- `GET /api/v1/tunnels`
- `POST /api/v1/tunnels`
- `PATCH /api/v1/tunnels/{id}`
- `DELETE /api/v1/tunnels/{id}`

隧道写接口当前直接承载 tunnel TLS 配置，不再额外拆分单独的 tunnel 证书绑定接口。

Tunnel TLS 仅支持 `protocol=tcp` 且 `remote_type=single` 的单端口 TCP 隧道。

`POST /api/v1/tunnels` 与 `PATCH /api/v1/tunnels/{id}` 当前支持的 TLS 字段：

- `listen_tls_mode`
- `listen_tls_load_system_ca`
- `listen_tls_server_cert_asset_id`
- `listen_tls_client_ca_asset_ids`
- `backend_tls_mode`
- `backend_tls_server_name`
- `backend_tls_load_system_ca`
- `backend_tls_insecure_skip_verify`
- `backend_tls_client_cert_asset_id`
- `backend_tls_ca_asset_ids`

当前返回：

- `GET /api/v1/tunnels` -> `{items: TunnelView[]}`
- `POST /api/v1/tunnels` -> `{item: TunnelView, warnings: string[]}`
- `PATCH /api/v1/tunnels/{id}` -> `{item: TunnelView, warnings: string[]}`

其中 `warnings` 当前主要用于提示：

- tunnel backend TLS 需要向 `frpc` 下发 CA 或客户端证书
- 但该 `proxy_group` 的控制连接策略仍为 `control_transport_security=plain`

如果隧道已经绑定限速策略，则当前不允许直接把它改成 `remote_type=range`；应先解绑对应限速策略。

## 限速策略接口

- `GET /api/v1/rate-policies`
- `POST /api/v1/rate-policies`
- `PATCH /api/v1/rate-policies/{id}`
- `DELETE /api/v1/rate-policies/{id}`
- `GET /api/v1/rate-policies/{id}/bindings`
- `POST /api/v1/rate-policies/{id}/bindings`
- `DELETE /api/v1/rate-policies/{id}/bindings/{tunnel_id}`

`POST /api/v1/rate-policies` 与 `PATCH /api/v1/rate-policies/{id}` 当前使用统一请求体：

- `name`
- `mode = independent | shared`
- `downlink = {value, unit}`
- `uplink = {value, unit}`

约束固定为：

- `unit` 只允许 `K / M / G`
- 省略 `unit` 时默认按 `M`
- 速率内部统一归一为 `bps`
- `value` 必须大于 `0`

当前返回：

- `GET /api/v1/rate-policies` -> `{items: RatePolicyView[]}`
- `POST /api/v1/rate-policies` -> `{item: RatePolicyView}`
- `PATCH /api/v1/rate-policies/{id}` -> `{item: RatePolicyView}`
- `GET /api/v1/rate-policies/{id}/bindings` -> `{items: RatePolicyBindingView[]}`
- `POST /api/v1/rate-policies/{id}/bindings` -> `{item: RatePolicyBindingView}`

当前 binding 约束固定为：

- 只允许绑定单端口 TCP / UDP 隧道
- range 隧道不可绑定
- 一个隧道最多绑定一个限速策略
- 删除隧道时自动删除对应 binding
- 删除 `proxy_group` 时，其下隧道和 binding 一起删除
- 删除仍有 binding 的限速策略会被拒绝

## 证书资产接口

- `GET /api/v1/certificate-assets`
- `POST /api/v1/certificate-assets/upload`
- `POST /api/v1/certificate-assets/paste`
- `POST /api/v1/certificate-assets/generate`
- `PATCH /api/v1/certificate-assets/{id}`
- `GET /api/v1/certificate-assets/{id}/delete-impact`
- `DELETE /api/v1/certificate-assets/{id}`
- `GET /api/v1/certificate-assets/{id}/download-options`
- `GET /api/v1/certificate-assets/{id}/download`

当前 `PATCH` 只允许改：

- `name`
- `remark`

## 入口证书接口

- `GET /api/v1/settings/entry-certificates`
- `PUT /api/v1/settings/entry-certificates/{usage_type}`
- `DELETE /api/v1/settings/entry-certificates/{usage_type}`

`usage_type` 取值：

- `webui_https`：WebUI HTTPS 服务端证书
- `frpc_tls`：frpc 登录监听口 TLS 服务端证书

## 路径前缀

`webui.path_prefix` 不只影响前端页面，也会给管理面额外提供同前缀别名：

- `/healthz`
- `/readyz`
- `/api/v1/*`

根路径 API 继续保留。

## 当前未开放的接口

下面这些资源当前还不存在：

- 连接观察与连接级操作
- 抓包控制

补充说明：

- 全局入口证书绑定走 `settings/entry-certificates`
- tunnel 级证书绑定走 `tunnels` 资源本身
- 可选资产列表统一走 `GET /api/v1/certificate-assets`
