# 分组与隧道管理

## 分组管理

当前管理面直接开放的分组字段只有：

- `name`
- `effective_ip`
- `enabled`

创建分组时：

- 自动生成完整登录 `key`
- 返回完整明文 `key` 一次
- 数据库只保存 `client_id` 与 `client_secret_hash`

当前业务规则：

- 分组名全局唯一
- `effective_ip` 只能是本机当前可用 IPv4、IPv6，或 `0.0.0.0`、`::`
- `effective_ip` 是分组级公网绑定地址
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
- `enabled`

当前校验：

- 端口必须在 `1..65535`
- `single` 模式要求本地和远端都为单端口
- `range` 模式要求本地和远端跨度一致
- `local_host` 必须是合法 IP 或 hostname

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
- 隧道 `enabled`

下列变更当前不触发热重载：

- 分组名称修改
- 隧道名称修改

在线热重载当前固定为：

1. 先写库成功
2. 冻结该分组旧 listener 与活动 TCP/UDP 运行态
3. 下发新的整组完整快照
4. 等待 `frpc config.ack(status=ok)`
5. `frps` 按新快照重建 listener
