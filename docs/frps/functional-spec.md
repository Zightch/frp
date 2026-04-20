# frps 功能文档

## 1. 当前功能总览

当前 `frps` 只定义已经进入真实代码和测试链路的功能：

- 管理密钥初始化、challenge 登录和会话管理
- 分组最小 CRUD 与 token 重置
- 隧道最小 CRUD
- `frpc` token challenge/response 登录
- 客户端来源 IP 规则校验
- TCP/UDP 单端口与连续范围转发
- `frps` 侧 UDP idle cleanup

## 2. 管理认证

### 2.1 初始化

当前规则：

- 服务启动时检查本地 `auth.json`
- 文件不存在时，管理面处于未初始化态
- 未初始化态下不能访问需要管理会话的业务接口
- `POST /api/v1/auth/init` 只接受 `key_hash`
- `key_hash` 必须是 `64` 位小写 hex

### 2.2 登录

当前登录流程：

1. `POST /api/v1/auth/challenge`
2. 前端本地计算 `proof = sha256(key_hash + salt)`
3. `POST /api/v1/auth/login`
4. 服务端写入管理 session

当前行为：

- challenge 只能使用一次
- challenge 过期后立即失效
- session 默认有效 `12h`
- 会话可通过 Cookie 或 Bearer Token 使用

### 2.3 删除 `auth.json`

当前固定语义：

- 删除 `auth.json` 等价于重置管理认证
- 服务端自动回到未初始化态
- 已签发 challenge 立即失效
- 已签发 session 立即失效
- 不支持在线轮换管理密钥

## 3. 分组管理

### 3.1 当前可管理字段

当前 API 和 WebUI 允许直接管理的字段只有：

- `name`
- `enabled`

创建分组时：

- 自动生成完整 token
- 返回完整明文 token 一次
- 数据库只保存 `token_id` 和 `token_hash`

### 3.2 当前业务规则

- 分组名全局唯一
- `token = token_id + token_secret`
- `token_id` 固定为 `32` 位小写 hex
- `token_secret` 固定为 `64` 位小写 hex
- 一个分组固定只允许 `1` 个在线 `frpc`
- 禁用分组后，新登录必须被拒绝

### 3.3 当前未开放管理的分组字段

下面这些字段当前不会通过管理 API/WebUI 编辑：

- `client_access_mode`
- `tunnel_access_mode`
- `rate_limit`

其中：

- `client_access_mode` 和 `group_client_ip_rules` 已经会被控制面读取
- `tunnel_access_mode` 和 `group_tunnel_ip_rules` 当前未进入数据面执行
- `rate_limit` 当前不生效

## 4. 隧道管理

### 4.1 当前支持

当前隧道只支持：

- `protocol = tcp`
- `protocol = udp`
- `remote_type = single`
- `remote_type = range`

### 4.2 创建与修改规则

当前校验：

- `group_id > 0`
- `name` 不能为空
- 端口必须位于 `1..65535`
- `single` 隧道要求远端和本地都为单端口
- `range` 隧道要求远端和本地跨度一致
- `local_host` 必须是合法 IP 或 hostname

当前不做：

- 端口冲突检测
- 隧道入口 ACL 配置
- 限速配置
- 抓包配置

### 4.3 当前返回字段

当前隧道查询会返回：

- `id`
- `group_id`
- `group_name`
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

`rate_limit` 和 `capture_enabled` 当前不会通过管理 API 返回或编辑。

## 5. frpc 登录与配置下发

### 5.1 当前登录规则

`frpc` 登录时，`frps` 当前会检查：

- `token_id` 是否存在
- 分组是否启用
- 客户端来源 IP 是否允许
- challenge 是否有效且未重放
- challenge 响应是否匹配
- 当前分组单客户端槽位是否已被占用

### 5.2 当前配置下发规则

当前只在登录阶段下发完整快照。

当前快照包含：

- `configVersion`
- `generatedAtMs`
- `tunnels`

当前不下发：

- ACL
- 限速
- 抓包
- 任何反向代理配置

## 6. TCP 转发

当前 TCP 行为：

- 只有启用状态的 tunnel 会监听公网端口
- listener 在对应 `config.ack` 后才启动
- 公网连接进入后，`frps` 分配 `streamId`
- 通过 `stream.open` 通知 `frpc`
- `frpc` 回 `stream.opened`
- 双方通过 `stream.data` 传输原始字节
- 任一侧结束后通过 `stream.close` 收口

当前已验证的失败路径：

- 错误 token
- 禁用分组
- 禁用隧道
- 本地目标不可达

## 7. UDP 转发

当前 UDP 行为：

- 第一次看到某个公网 UDP 会话时，`frps` 发送 `udp.open`
- 首包和后续包都通过 `udp.data` 转发
- `sessionId` 由 `frps` 分配
- `frps` 按 `tunnelId + remotePort + public client addr` 识别 session
- `frpc` 不做本地 idle timer
- `frps` 空闲约 `30s` 后删除 session 并发送 `udp.close`

当前关键语义：

- 不是固定 `30s` 轮询窗口
- 只要有成功转发，立即刷新活跃时间
- 空闲时间从最后一次成功转发结束时开始重新计算

## 8. 当前未实现清单

下面这些功能当前不能视为 `frps` 已支持：

- 反向代理管理
- 证书管理
- 在线热更新推送
- WebSocket 实时态
- 连接列表和强制断开
- 限速执行
- 抓包执行
- 隧道入口 ACL 执行
- 分组/隧道规则管理页

这些内容如果后续开始实现，必须先同步更新本文档。
