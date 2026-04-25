# 证书绑定设计：WebUI HTTPS 与 7000 TLS

本文档描述“证书资产”之上的下一层能力：把已有证书绑定到 `frps` 的实际使用点。当前只覆盖两个入口：

- 管理面 / WebUI
- `7000` `frpc` 登录口

当前状态：未实现，属于已收口方案。

## 1. 目标

- 不改变 `certificate_assets` 的“原子资产”定位。
- 新增一层“使用关系”，表达“哪个入口正在使用哪张证书”。
- `frps` 运行时在内存完成证书链组装、`tls.Certificate` 构造和热切换。
- WebUI 绑定证书后，管理面直接按 HTTPS 暴露。
- `7000` 绑定证书后，先完成安全模式协商，再决定是否进入 TLS 握手，之后才进入现有登录流程。

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
  - `control_listener_tls`
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

## 6. 7000 TLS

### 6.1 使用点

- `usage_type = control_listener_tls`

### 6.2 当前问题

当前 `7000` 登录口还是裸 TCP，第一帧就是现有业务协议里的 `auth.begin`。如果直接把监听器改成纯 TLS：

- 旧流程的第一包会变成 TLS ClientHello 与 `auth.begin` 冲突。
- `frps` 无法在握手前知道对端是否准备走 TLS。

所以必须先加一层“预协商”。

### 6.3 目标流程

首版建议所有新版本 `frpc` 都先走同一套预协商，再进入后续链路：

```text
frpc -> transport.client_hello
frps -> transport.server_hello
if selected_security_mode == tls:
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

### 6.4 协商结果

`transport.server_hello` 至少返回：

- `selected_security_mode`
  - `plain`
  - `tls_required`
- `server_capability_bits`

当 `selected_security_mode=tls_required` 时：

- 双方在同一 TCP 连接上立刻开始 TLS 握手。
- TLS 成功后，才允许发送现有 `auth.begin`。

当 `selected_security_mode=plain` 时：

- 直接沿现有业务协议继续登录。

### 6.5 兼容策略

当前仓库默认按“同仓同步升级”处理，不维护长期兼容层。

因此首版建议：

- 新版 `frpc` 总是先发 `transport.client_hello`。
- 新版 `frps` 总是先等预协商，而不是继续接受“首包直接 `auth.begin`”的旧模式。
- 如果 `7000` 已绑定 TLS 证书而客户端不支持预协商，应直接拒绝连接。

### 6.6 热更新边界

- `7000` 证书绑定变更只影响新建控制连接。
- 当前已在线的 plain/TLS session 不强制踢下线；后续是否增加“强制重连”管理动作，单独设计。

### 6.7 frpc 侧配套要求

`7000` 做 TLS 不能只做“加密”，还必须做服务端身份校验。否则只能防窃听，不能防中间人。

因此配套需要：

- `server_name`
- 信任来源
  - 系统 CA
  - 或显式自定义 CA / pinned CA

这部分配置归 `frpc` 侧实现，但 `frps` 设计必须把它作为前置条件明确写清。

## 7. 建议管理接口

当前建议独立新增 usage 资源，不复用 `certificate-assets` 路径：

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

## 8. 与当前证书资产层的关系

- 证书资产层继续负责“保存原子证书、私钥、树状关系、上传原结构和下载打包”。
- 证书使用层只负责“哪个入口正在用哪张叶子证书”。
- 两层不要混成一张表，也不要把 `usage_type`、监听地址或 TLS 选项塞回 `certificate_assets`。

## 9. 落地顺序建议

1. 先加 `certificate_asset_usages` 表和后端管理接口。
2. 先做 `webui_https`，因为只影响 `frps` 自身监听器。
3. 再做 `7000` 预协商与 TLS。
4. 最后再考虑是否扩展到 tunnel / 反向代理 / SNI 多证书。
