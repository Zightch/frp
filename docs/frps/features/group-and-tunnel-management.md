# 分组与隧道管理

## 分组管理

当前管理面直接开放的分组字段只有：

- `name`
- `effective_ip`
- `enabled`
- `control_transport_security`

创建分组时：

- 自动生成完整登录 `key`
- 返回完整明文 `key` 一次
- 数据库只保存 `client_id` 与 `client_secret_hash`

当前业务规则：

- 分组名全局唯一
- `effective_ip` 只能是本机当前可用 IPv4、IPv6，或 `0.0.0.0`、`::`
- `effective_ip` 是分组级公网绑定地址
- `control_transport_security = plain | tls_required`
- 分组切到 `tls_required` 前，必须先绑定 `frpc_tls`
- 一个分组固定只允许一个在线 `frpc`
- 禁用分组后，新登录必须被拒绝

当前分组返回还会附带运行态派生字段：

- `status`
- `status_reason`

语义固定为：

- `禁用`
- `启用`
- `异常`

## 隧道管理

当前只支持：

- `protocol = tcp | udp`
- `remote_type = single | range`

当前字段：

- `group_id`
- `name`
- `protocol`
- `remote_type`
- `remote_start`
- `remote_end`
- `local_host`
- `local_start`
- `local_end`
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
- `enabled`

当前校验：

- 端口必须在 `1..65535`
- `single` 模式要求本地和远端都为单端口
- `range` 模式要求本地和远端跨度一致
- `local_host` 必须是合法 IP 或 hostname
- 隧道 TLS 当前只支持 `protocol=tcp`
- `listen_tls_mode = off | tls | mtls`
- `backend_tls_mode = off | tls | mtls`
- `listen_tls_mode != off` 时必须绑定 `listen_tls_server_cert_asset_id`
- `listen_tls_mode = mtls` 时，必须满足：
  - `listen_tls_load_system_ca = true`
  - 或 `listen_tls_client_ca_asset_ids` 非空
- `backend_tls_mode = mtls` 时必须绑定 `backend_tls_client_cert_asset_id`
- `backend_tls_mode != off` 且 `backend_tls_insecure_skip_verify = false` 时，必须满足：
  - `backend_tls_load_system_ca = true`
  - 或 `backend_tls_ca_asset_ids` 非空

当前 tunnel TLS 语义：

- `listen_tls_*` 表示外网客户端连接 `frps` 隧道监听器这一侧
- `backend_tls_*` 表示 `frpc` 连接内网后端目标这一侧
- `listen_tls_client_ca_asset_ids` 和 `backend_tls_ca_asset_ids` 都允许多选，语义是 CA 池
- `listen_tls_load_system_ca` 默认关闭
- `backend_tls_load_system_ca` 在启用 backend TLS 时默认开启
- 如果 backend TLS 需要向 `frpc` 下发自定义 CA 或客户端证书，但分组 `control_transport_security=plain`，保存仍允许成功，但会返回 warning

## 跨平台统一端口冲突规则

管理面当前已经固定采用统一静态冲突矩阵：

- 只比较同协议监听空间
- 只在远端端口区间相交时继续比较
- `tcp` 与 `udp` 完全隔离
- 即使某些平台偶然允许第二次绑定，只要命中矩阵冲突，管理面仍拒绝写库

当前冲突结果直接影响：

- `POST /api/v1/tunnels`
- `PATCH /api/v1/tunnels/{id}`
- `PATCH /api/v1/proxy-groups/{id}` 中会改变监听空间的字段

## 隧道状态

当前管理 API 会返回隧道运行态派生状态：

- `禁用`
- `冲突`
- `异常`
- `启用`

优先级固定为：

1. 分组或隧道禁用
2. 命中静态端口冲突
3. 启动期扫描、轮询恢复、listener 启动或 `effective_ip` 本地重绑发现运行态失败
4. 其余情况

`status_reason` 用于承接：

- 冲突对端说明
- 外部端口占用
- listener `bind` 失败
- `effective_ip` 当前不可绑定

## 在线热重载

分组在线时，下列变更会触发整组热重载：

- 分组 `enabled`
- 分组 `effective_ip`
- 隧道新增 / 删除
- 隧道监听空间和本地目标相关字段
- 隧道监听侧 TLS 相关字段
- 隧道 backend TLS 相关字段
- 隧道 `enabled`

下列变更当前不触发热重载：

- 分组名称修改
- 分组 `control_transport_security`
- 隧道名称修改

`control_transport_security` 的变更只影响新建控制连接：

- `plain`：hello 协商后继续明文登录
- `tls_required`：hello 协商后必须先完成 TLS 握手，再进入登录会话

在线热重载当前固定为：

1. 先写库成功
2. 冻结该分组旧 listener 与活动 TCP/UDP 运行态
3. 下发新的整组完整快照
4. 等待 `frpc config.ack(status=ok)`
5. `frps` 按新快照重建 listener
