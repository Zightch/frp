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

- 证书绑定 usage 管理
- tunnel 级 CA 池管理
- 连接观察与连接级操作
- 抓包与限速控制
