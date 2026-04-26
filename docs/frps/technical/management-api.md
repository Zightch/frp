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

## 分组接口

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
- 但该分组的控制连接策略仍为 `control_transport_security=plain`

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
- 抓包与限速控制

补充说明：

- 全局入口证书绑定走 `settings/entry-certificates`
- tunnel 级证书绑定走 `tunnels` 资源本身
- 可选资产列表统一走 `GET /api/v1/certificate-assets`
