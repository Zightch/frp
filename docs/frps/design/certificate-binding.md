# 证书绑定设计：WebUI HTTPS 与 frpc TLS

本文档描述“证书资产”之上的下一层能力：把已有证书绑定到 `frps` 的实际使用点。当前只覆盖两个入口：

- 管理面 / WebUI
- `frpc` 登录口（默认监听端口 `7000`）

当前状态：已实现首版，本文档继续作为该能力的设计与边界说明。

## 1. 目标

- 不改变 `certificate_assets` 的“原子资产”定位。
- 新增一层“使用关系”，表达“哪个入口正在使用哪张证书”。
- `frps` 运行时在内存完成证书链组装、`tls.Certificate` 构造和热切换。
- WebUI 绑定证书后，管理面直接按 HTTPS 暴露。
- `frpc` 登录口支持分组级安全策略：有的分组走明文，有的分组强制 TLS。
- `frpc` 登录口的预协商阶段必须先带 `client_id`，`frps` 才能按分组决定后续是否 TLS。

## 2. 非目标

- 当前不做 SNI 多证书选择。
- 当前不做 mTLS 客户端证书认证。
- 当前不把证书直接绑定到 tunnel、端口或反向代理入口。
- 当前不把信任分发问题偷塞回 `certificate_assets` 表。

## 3. 数据模型

新增表：`certificate_asset_usages`

建议字段：

- `id`
- `usage_type`
- `target_id`
- `asset_id`
- `enabled`
- `options_json`
- `created_at`
- `updated_at`

字段语义：

- `usage_type` 当前固定为：
  - `webui_https`
  - `frpc_tls`
- `target_id` 当前允许为空；这两个使用点目前都是全局单例。
- `asset_id` 指向被绑定的叶子证书资产。
- `enabled` 表示该绑定是否生效。
- `options_json` 只作预留，不承载首版核心语义。

约束：

- `UNIQUE (usage_type, target_id)`，一个使用点同时只能有一条生效绑定。
- `asset_id` 外键指向 `certificate_assets.id`。
- 只允许绑定 `asset_type=certificate` 的资产。
- 绑定资产必须有私钥。
- 绑定资产必须能沿 `certificate_asset_relations` 向上组出完整链。

补充规则：

- `certificate_assets` 继续只保存证书原文、私钥和最小元数据，不保存“谁在使用我”。
- 删除资产时，`delete-impact` 必须把 usage 一起纳入计算；默认阻止删除正在被使用的资产。

这张表只负责“入口用哪张证书”，不负责“哪个分组要求 TLS”。

`frpc` 登录连接的分组级安全策略建议独立放在 `proxy_groups`，例如新增字段：

- `control_transport_security`

首版取值建议固定为：

- `plain`
- `tls_required`

这样职责分层保持清楚：

- `certificate_asset_usages.frpc_tls`
- 回答 `frpc` 登录监听器（默认端口 `7000`）在需要 TLS 时用哪张服务端证书
- `proxy_groups.control_transport_security`
- 回答某个分组连接 `frpc` 登录监听器时是走明文还是强制 TLS

## 4. 运行时加载

运行时不直接保存 ZIP、临时文件或外部工具产物，只做内存对象：

1. 根据 `asset_id` 读取叶子证书资产。
2. 沿 `certificate_asset_relations` 向上收集父链。
3. 在内存拼出 `leaf + intermediates`。
4. 用叶子 `crt + key` 构造 `tls.Certificate`。
5. 生成对应使用点的 `tls.Config`。

要求：

- 不落盘。
- 不调用 `openssl` 等外部工具。
- 证书切换失败时，不能覆盖当前已生效的旧配置。

## 5. WebUI HTTPS

### 5.1 使用点

- `usage_type = webui_https`

### 5.2 生效语义

- 绑定成功后，`management_listen_addr` 对应监听器直接切为 TLS。
- 这不是“HTTP 收到请求后升级”，而是监听器本身从 HTTP 变成 HTTPS。
- 同一个地址切换后，浏览器访问协议应改为 `https://`。

### 5.3 前端行为

- WebUI 提交绑定成功后，接口返回新的 HTTPS 基址。
- 前端立即跳转到新的 `https://host:port/...`。
- 监听器热切换瞬间允许当前页面请求失败；前端只需按“绑定成功 -> 立即重连 HTTPS”处理。

### 5.4 热更新边界

- 切换绑定时需要重建管理监听器。
- 当前管理 session 的逻辑有效期不必重置，但底层 TCP 连接会被重建。
- 解绑后，管理面再切回纯 HTTP。

## 6. frpc TLS

### 6.1 使用点

- `usage_type = frpc_tls`

这里的 `frpc_tls` 只表示 `frpc` 登录监听器（默认端口 `7000`）的全局服务端证书，不表示所有分组都必须走 TLS。

分组是否强制 TLS 由 `proxy_groups.control_transport_security` 决定。

### 6.2 当前问题

当前 `frpc` 登录口（默认端口 `7000`）还是裸 TCP，第一帧就是现有业务协议里的 `auth.begin`。如果直接把监听器改成纯 TLS：

- 旧流程的第一包会变成 TLS ClientHello 与 `auth.begin` 冲突。
- `frps` 无法在握手前知道对端属于哪个分组，也就无法知道该分组是否要求 TLS。

所以必须先加一层“预协商”。

### 6.3 目标流程

首版建议所有新版本 `frpc` 都先走同一套预协商，再进入后续链路：

```text
frpc -> transport.client_hello(client_id, supported_security_modes)
frps -> transport.server_hello(selected_security_mode)
if selected_security_mode == tls_required:
    TLS handshake
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
```

约定：

- 预协商报文不复用现有 `auth.begin` / `server.hello` 业务含义。
- 当前已有 `server.hello` 名称已被业务握手占用，因此预协商阶段应使用单独命名，例如：
  - `transport.client_hello`
  - `transport.server_hello`
- `client_id` 必须前置到 `transport.client_hello`。
- 现有 `auth.begin` 里的 `client_id` 首版可以继续保留，但服务端必须校验它与 `transport.client_hello.client_id` 一致。

### 6.4 协商结果

`transport.client_hello` 至少应包含：

- `client_id`
- `supported_security_modes`
  - `plain`
  - `tls`

`transport.server_hello` 至少返回：

- `selected_security_mode`
  - `plain`
  - `tls_required`
- `server_capability_bits`

服务端决策规则固定为：

1. 先按 `client_id` 查出目标分组。
2. 再读取该分组的 `control_transport_security`。
3. 根据分组策略和客户端声明的能力，返回 `plain` 或 `tls_required`。

当 `selected_security_mode=tls_required` 时：

- 双方在同一 TCP 连接上立刻开始 TLS 握手。
- TLS 成功后，才允许发送现有 `auth.begin`。

当 `selected_security_mode=plain` 时：

- 直接沿现有业务协议继续登录。

如果出现下面任一情况，应在预协商阶段直接拒绝：

- `client_id` 不存在
- 分组被禁用
- 分组要求 `tls_required`，但 `frpc` 登录监听器没有已配置的服务端证书
- 分组要求 `tls_required`，但客户端不支持 TLS

### 6.5 兼容策略

当前仓库默认按“同仓同步升级”处理，不维护长期兼容层。

因此首版建议：

- 新版 `frpc` 总是先发 `transport.client_hello`。
- 新版 `frps` 总是先等预协商，而不是继续接受“首包直接 `auth.begin`”的旧模式。
- 如果客户端不支持预协商，应直接拒绝连接。

### 6.6 热更新边界

- `frpc` 登录监听器证书绑定变更只影响新建控制连接。
- 当前已在线的 plain/TLS session 不强制踢下线；后续是否增加“强制重连”管理动作，单独设计。
- 分组的 `control_transport_security` 变更同样只影响新建控制连接；首版不强制中断现有 session。

### 6.7 frpc 侧配套要求

`frpc` TLS 不能只做“加密”，还必须做服务端身份校验。否则只能防窃听，不能防中间人。

因此配套需要：

- `server_name`
- 信任来源
  - 系统 CA
  - 或显式自定义 CA / pinned CA

这部分配置归 `frpc` 侧实现，但 `frps` 设计必须把它作为前置条件明确写清。

## 7. 建议管理接口

当前建议拆成两类管理入口。

### 7.1 证书使用接口

独立新增 usage 资源，不复用 `certificate-assets` 路径：

- `GET /api/v1/certificate-usages`
- `PUT /api/v1/certificate-usages/{usage_type}`
- `DELETE /api/v1/certificate-usages/{usage_type}`

`PUT` 请求体建议至少包含：

- `asset_id`
- `enabled`

返回模型建议至少包含：

- `usage_type`
- `asset_id`
- `asset_name`
- `enabled`
- `status`
- `status_reason`
- `resolved_chain_length`
- `updated_at`

### 7.2 分组安全策略接口

分组级 TLS 策略不走 `certificate-usages`，而是进入现有分组管理模型：

- `POST /api/v1/proxy-groups`
- `PATCH /api/v1/proxy-groups/{id}`

建议新增字段：

- `control_transport_security`

首版行为约束：

- 分组切到 `tls_required` 时，后端必须先校验 `frpc_tls` 已配置有效服务端证书。
- 如果已有分组正在使用 `tls_required`，则不允许解绑 `frpc_tls`。

## 8. 与当前证书资产层的关系

- 证书资产层继续负责“保存原子证书、私钥、树状关系、上传原结构和下载打包”。
- 证书使用层只负责“哪个入口正在用哪张叶子证书”。
- 分组配置层只负责“该分组连接 `frpc` 登录监听器时是否强制 TLS”。
- 不要把 `usage_type`、分组 TLS 策略、监听地址或 TLS 选项塞回 `certificate_assets`。

## 9. 落地顺序建议

1. 先加 `certificate_asset_usages` 表，并给 `proxy_groups` 补 `control_transport_security`。
2. 先做 `webui_https`，因为只影响 `frps` 自身监听器。
3. 再做 `frpc` 登录预协商，固定 `client_hello(client_id) -> server_hello(是否 TLS)`。
4. 再做 `frpc TLS` 握手与后续登录链路衔接。
5. 最后再考虑是否扩展到 tunnel / 反向代理 / SNI 多证书。
