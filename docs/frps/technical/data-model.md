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
- `rate_limit`
- `created_at`
- `updated_at`

当前运行时实际消费：

- `id`
- `name`
- `client_secret_hash`
- `effective_ip`
- `enabled`
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

## 当前未落地但已确认的下一层模型

证书绑定不应再塞回 `certificate_assets`。

下一层建议新增：

- `certificate_asset_usages`

用途是表达“哪个入口正在使用哪张叶子证书”，详见 [../design/certificate-binding.md](../design/certificate-binding.md)。
