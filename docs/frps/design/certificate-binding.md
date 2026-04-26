# 证书绑定设计：入口证书与 Tunnel TLS

本文档描述 `certificate_assets` 之上的证书绑定层。当前已落地的范围包括：

- 全局入口证书
  - `webui_https`
  - `frpc_tls`
- tunnel 级 TLS
  - `tunnel_listen_server_cert`
  - `tunnel_listen_client_ca`
  - `tunnel_backend_client_cert`
  - `tunnel_backend_ca`

## 1. 目标

- 保持 `certificate_assets` 继续只保存原子证书资产。
- 用一层通用绑定表表达“谁在用哪张证书 / 哪组 CA”。
- 让 `frps` 监听侧和 `frpc` backend 侧都能复用同一套资产体系。
- 明确区分：
  - 入口证书绑定
  - 分组控制链路 TLS 策略
  - tunnel 级 TLS 配置

## 2. 非目标

- 不做旧协议兼容层。
- 不做 SNI 多证书选择。
- 不做 UDP tunnel TLS。
- 不做 tunnel 级独立资源接口；当前直接复用 `tunnels` 资源承载 TLS 配置。

## 3. 数据模型

### 3.1 `certificate_asset_usages`

当前结构：

- `id`
- `target_type`
- `usage_type`
- `target_id`
- `asset_id`
- `enabled`
- `created_at`
- `updated_at`

当前约束：

- `UNIQUE (target_type, target_id, usage_type, asset_id)`
- `target_type=global` 时，`target_id=0`
- `target_type=tunnel` 时，`target_id=tunnels.id`

当前 `target_type`：

- `global`
- `tunnel`

当前 `usage_type`：

- 全局入口
  - `webui_https`
  - `frpc_tls`
- tunnel 监听侧
  - `tunnel_listen_server_cert`
  - `tunnel_listen_client_ca`
- tunnel backend 侧
  - `tunnel_backend_client_cert`
  - `tunnel_backend_ca`

绑定约束：

- 服务端证书 / 客户端证书用途只能绑定 `asset_type=certificate` 且 `key_present=true`
- CA 用途只能绑定 `asset_type=ca`
- `tunnel_listen_client_ca` 与 `tunnel_backend_ca` 允许多条绑定，语义是 CA 池

### 3.2 `tunnels`

除原有隧道字段外，当前新增：

- `listen_tls_mode`
- `listen_tls_load_system_ca`
- `backend_tls_mode`
- `backend_tls_server_name`
- `backend_tls_load_system_ca`
- `backend_tls_insecure_skip_verify`

当前模式值：

- `off`
- `tls`
- `mtls`

职责分层：

- `listen_tls_*` 只描述外网客户端到 `frps` 隧道监听器这一侧
- `backend_tls_*` 只描述 `frpc` 到后端目标这一侧
- 证书和 CA 的选择不写进 `tunnels` 表，而是通过 `certificate_asset_usages` 绑定

## 4. 运行时边界

### 4.1 `frps` 监听侧

`frps` 启动 TCP tunnel listener 时：

1. 读取 tunnel 的 `listen_tls_mode` / `listen_tls_load_system_ca`
2. 本地解析 `tunnel_listen_server_cert`
3. 本地解析 `tunnel_listen_client_ca` CA 池
4. 构造 `tls.Config`

当前策略：

- `off`：不套 TLS
- `tls`：`ClientAuth = tls.NoClientCert`
- `mtls`：`ClientAuth = tls.RequireAndVerifyClientCert`

CA 池规则：

- `listen_tls_load_system_ca` 默认关闭
- 可额外叠加多个 `tunnel_listen_client_ca`
- `mtls` 时必须至少存在一种信任来源：
  - 系统 CA
  - 或自定义 CA 池

关键边界：

- `frps` 监听侧私钥材料不会下发给 `frpc`
- 这部分绑定始终只在 `frps` 本地解析

### 4.2 `frpc` backend 侧

`frps` 生成配置快照时，会把 backend TLS 所需材料投影到 `ConfigSnapshot.Tunnels`：

- `backend_tls_mode`
- `backend_tls_server_name`
- `backend_tls_load_system_ca`
- `backend_tls_insecure_skip_verify`
- `backend_tls_ca_pem`
- `backend_tls_client_cert_pem`
- `backend_tls_client_key_pem`

`frpc` 建立本地 backend 连接时按 tunnel 配置拨号：

- `off`：明文 TCP
- `tls`：TLS，默认启用系统 CA，可追加自定义 CA
- `mtls`：在 TLS 基础上额外带客户端证书和私钥

当前规则：

- `backend_tls_load_system_ca` 在启用 backend TLS 时默认开启
- `backend_tls_insecure_skip_verify=true` 时允许测试模式
- `backend_tls_mode=mtls` 时必须存在客户端证书和私钥
- `backend_tls_insecure_skip_verify=false` 时必须至少有一种信任来源：
  - 系统 CA
  - 或自定义 CA 池

关键边界：

- backend TLS 的 CA / client cert 是由 `frps` 下发给 `frpc`
- 监听侧证书绝不会混入控制面下发

## 5. 控制链路关系

分组控制链路策略仍由：

- `proxy_groups.control_transport_security = plain | tls_required`

决定。

它和 tunnel TLS 的关系是：

- 不强制要求“只要 tunnel TLS 开启，`7000` 控制连接就必须是 TLS”
- 但如果 backend TLS 需要向 `frpc` 下发自定义 CA 或客户端证书，而该分组控制链路仍为 `plain`
  - 保存允许成功
  - 管理 API 返回 `warnings`

当前 warning 语义：

- 提醒管理员：backend TLS 证书或 CA 会通过明文控制连接下发给 `frpc`

## 6. 管理 API

### 6.1 全局入口证书

继续走：

- `GET /api/v1/settings/entry-certificates`
- `PUT /api/v1/settings/entry-certificates/{usage_type}`
- `DELETE /api/v1/settings/entry-certificates/{usage_type}`

当前全局 `usage_type`：

- `webui_https`
- `frpc_tls`

### 6.2 Tunnel TLS

直接走：

- `GET /api/v1/tunnels`
- `POST /api/v1/tunnels`
- `PATCH /api/v1/tunnels/{id}`

当前 tunnel 返回 / 请求字段包括：

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

`POST` / `PATCH` 当前响应：

- `{item, warnings}`

## 7. 当前校验规则

### 7.1 监听侧

- 只允许 `protocol=tcp`
- `listen_tls_mode != off` 时必须绑定 `tunnel_listen_server_cert`
- `listen_tls_mode = mtls` 时必须有客户端 CA 信任来源

### 7.2 Backend 侧

- 只允许 `protocol=tcp`
- `backend_tls_mode = mtls` 时必须绑定 `tunnel_backend_client_cert`
- `backend_tls_mode != off` 且 `backend_tls_insecure_skip_verify=false` 时，必须存在服务端信任来源

### 7.3 资产类型

- 服务端证书 / 客户端证书用途必须是证书资产且带私钥
- CA 用途必须是 CA 资产

## 8. 前端接入方式

当前不需要专门的 tunnel 证书接口。

前端直接组合两类接口即可：

- `GET /api/v1/tunnels`
- `POST/PATCH /api/v1/tunnels`
- `GET /api/v1/certificate-assets`

前端筛选规则：

- 服务端证书 / 客户端证书：`asset_type=certificate` 且 `key_present=true`
- CA 池：`asset_type=ca`

## 9. 当前边界总结

- 已支持入口证书和 tunnel TLS 复用同一套证书资产体系
- 已支持 tunnel 级多 CA 池
- 已支持 `frps` 监听侧 TLS / mTLS
- 已支持 `frpc` backend 侧 TLS / mTLS / insecure skip verify
- 当前仍不支持 UDP tunnel TLS、SNI 多证书、额外的 tunnel 级独立证书资源接口
