# frps 数据模型

## 两层配置

当前数据明确分成两层：

- 持久化配置
  - 数据库中的 `proxy_groups`、`tunnels`、`certificate_assets`、`certificate_asset_relations`
- 运行时配置
  - `internal/control/repository.go` 投影出来的 `GroupRuntime` / `ConfigSnapshot`
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
- `rate_limit`
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

`rate_limit` 目前仍保留在 schema，但运行时不消费。

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
- `enabled`
- `updated_at`

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

当前用途是表达“哪个入口正在使用哪张叶子证书”。

其中：

- `webui_https`
  - 管理面 HTTPS 服务端证书
- `frpc_tls`
  - `frpc` 登录监听口在需要 TLS 时使用的全局服务端证书

当前约束：

- `usage_type + target_id` 唯一
- 当前 `target_id` 固定为全局单例 `0`
- 只允许绑定 `asset_type=certificate` 且带私钥的资产
- 运行时解析链时：
  - 生成资产按关系树向上拼 `leaf + intermediates`
  - 上传资产直接复用原始 PEM 结构

`frpc` 登录连接的“某个分组是否要求 TLS”不属于这张表，而是进入 `proxy_groups.control_transport_security = plain | tls_required`。

详见 [../design/certificate-binding.md](../design/certificate-binding.md)。
