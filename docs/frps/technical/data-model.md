# frps 数据模型

## 两层配置

当前数据明确分成两层：

- 持久化配置
  - 数据库中的 `proxy_groups`、`tunnels`、`rate_policies`、`rate_policy_bindings`、`certificate_assets`、`certificate_asset_relations`、`certificate_asset_usages`
- 运行时配置
  - `internal/control/repo` 从 SQL row 投影出来的 `GroupRuntime` / `ConfigSnapshot`
  - `internal/control/domain/runtime` 定义运行时模型和 desired/applied snapshot 转换
  - `internal/certassets` 准备出来的系统 CA 和资产运行时

## `proxy_groups`

当前核心字段：

- `id`
- `name`
- `client_id`
- `client_secret_hash`
- `effective_ip`
- `enabled`
- `control_transport_security`
- `created_at`
- `updated_at`

当前运行时实际消费：

- `id`
- `name`
- `client_secret_hash`
- `effective_ip`
- `enabled`
- `control_transport_security`
- `updated_at`

当前正式 schema 中已经不再保留 `rate_limit` 字段；已确认的后续限速业务设计见 [../design/rate-policy.md](../design/rate-policy.md)。

## `tunnels`

当前字段：

- `id`
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
- `backend_tls_mode`
- `backend_tls_server_name`
- `backend_tls_load_system_ca`
- `backend_tls_insecure_skip_verify`
- `enabled`
- `created_at`
- `updated_at`

当前运行时实际消费：

- `id`
- `protocol`
- `remote_type`
- `remote_start`
- `remote_end`
- `local_host`
- `local_start`
- `local_end`
- `listen_tls_mode`
- `listen_tls_load_system_ca`
- `backend_tls_mode`
- `backend_tls_server_name`
- `backend_tls_load_system_ca`
- `backend_tls_insecure_skip_verify`
- `enabled`
- `updated_at`

补充语义：

- `listen_tls_mode = off | tls | mtls`
- `backend_tls_mode = off | tls | mtls`
- tunnel TLS 字段只对 `protocol=tcp` 且 `remote_type=single` 的隧道生效
- 如果隧道已经绑定限速策略，则当前不允许直接改成 `remote_type=range`；需要先解绑
- `listen_tls_*` 只作用于外网客户端到 `frps` 隧道监听器
- `backend_tls_*` 只作用于 `frpc` 到内网后端目标
- `listen_tls_server_cert_asset_id`、`listen_tls_client_ca_asset_ids`、`backend_tls_client_cert_asset_id` 和 `backend_tls_ca_asset_ids` 是管理 API 字段；实际绑定存放在 `certificate_asset_usages`，不是 `tunnels` 表列

## `rate_policies`

当前字段：

- `id`
- `name`
- `mode`
- `downlink_bps`
- `uplink_bps`
- `created_at`
- `updated_at`

当前语义：

- `name` 全局唯一
- `mode = independent | shared`
- `downlink_bps`、`uplink_bps` 是内部真实来源
- 管理 API 输入时用 `K/M/G`，落库后统一归一为 `bps`
- 当前不保存原始展示单位，不保存 burst

## `rate_policy_bindings`

当前字段：

- `id`
- `rate_policy_id`
- `tunnel_id`
- `created_at`
- `updated_at`

当前约束：

- `UNIQUE (tunnel_id)`
- `INDEX (rate_policy_id)`
- 一个限速策略可以绑定多条隧道
- 一个隧道最多只能绑定一个限速策略
- 当前只允许绑定单端口 TCP / UDP 隧道
- range 隧道不进入这张绑定表
- 删除隧道时，对应 binding 必须一起删除
- 删除 `proxy_group` 时，其下隧道和对应 binding 一起删除
- 删除仍有 binding 的限速策略时，管理 API 默认拒绝

## `certificate_assets`

当前字段：

- `id`
- `name`
- `remark`
- `source`
- `asset_type`
- `format_type`
- `crt`
- `crt_hash`
- `key`
- `created_at`
- `updated_at`

当前语义：

- `source = upload | generated`
- `asset_type = certificate | ca`
- `format_type` 当前固定为 `pem`
- `crt_hash` 用于快速候选去重
- `source=generated` 时，`crt` 只保存当前节点一张证书
- `source=upload` 时，`crt` 允许保留原始多证书 PEM 结构

## `certificate_asset_relations`

当前字段：

- `id`
- `child_asset_id`
- `parent_asset_id`
- `relation_type`
- `created_at`
- `updated_at`

当前语义：

- `relation_type` 当前固定为 `issued_by`
- `child_asset_id` 唯一，保证只有一个直接上游
- `parent_asset_id` 只允许引用生成 CA
- 上传资产不进入这张关系表

当前模型只支持树状单父关系，不支持：

- 多父
- 交叉签名
- AIA 自动补链入库

## `certificate_asset_usages`

当前用途已经扩展为“通用证书绑定层”，既承接全局入口证书，也承接 tunnel 级 TLS 绑定。

当前字段：

- `id`
- `target_type`
- `usage_type`
- `target_id`
- `asset_id`
- `enabled`
- `created_at`
- `updated_at`

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

当前约束：

- `UNIQUE (target_type, target_id, usage_type, asset_id)`
- `target_type=global` 时，`target_id` 固定为 `0`
- `target_type=tunnel` 时，`target_id = tunnels.id`
- 服务端证书 / 客户端证书用途只允许绑定 `asset_type=certificate` 且带私钥的资产
- CA 用途只允许绑定 `asset_type=ca` 的资产
- CA 用途允许多条绑定，语义是 CA 池

当前运行时边界：

- `frps` 监听侧 TLS 材料不下发给 `frpc`，而是按 tunnel id 在 `frps` 本地解析
- `frpc` backend TLS 所需 CA / client cert 会从这张绑定表解析后投影进 `ConfigSnapshot.Tunnels`
- `frpc` 登录连接的“某个 `proxy_group` 是否要求 TLS”仍不属于这张表，而是进入 `proxy_groups.control_transport_security = plain | tls_required`

详见 [../design/certificate-binding.md](../design/certificate-binding.md)。
