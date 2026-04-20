# frps 技术设计

## 1. 设计目标

当前 `frps` 的技术设计只围绕已经落地的能力展开：

- 用最小结构跑通管理面、控制面和正向代理数据面。
- 把管理认证、本地配置、控制协议和公网 listener 的边界收清楚。
- 在不引入额外框架的前提下，给后续迭代保留明确扩展点。

## 2. 当前进程内分层

```text
cmd/frps
  |
internal/app
  |
+----------------------+----------------------+
|                      |                      |
internal/api           internal/control       internal/auth
  |                      |                      |
WebUI/API              frpc protocol         auth.json
  |                      |
internal/storage <------+
  |
SQLite / MySQL
```

当前没有单独的事件总线、连接注册表或插件化数据面。

## 3. 当前模块 ownership

### 3.1 `cmd/frps`

- 解析当前工作目录。
- 固定拼出 `data/config.json` 路径。
- 初始化 logger。
- 处理 `Ctrl+C`。

### 3.2 `internal/config`

- 提供默认值。
- 读取 JSON 配置。
- 校验监听地址、超时、日志格式和数据库配置。
- 解析相对路径。

当前默认值：

- `control_listen_addr = 0.0.0.0:7000`
- `management_listen_addr = 127.0.0.1:7500`
- `read_header_timeout = 5s`
- `shutdown_timeout = 10s`
- `database.type = sqlite`
- `database.path = ./frps.db`
- `webui.dist_dir = ../webui/dist`

MySQL 只接受驱动标准 DSN，不再兼容地址简写。

### 3.3 `internal/auth`

`auth.Manager` 负责：

- 从 `auth.json` 读取 `key_hash`
- 在未初始化时写入 `auth.json`
- 签发管理 challenge
- 校验 `sha256(key_hash + salt)` 证明
- 维护管理 session
- 轮询检测 `auth.json` 是否被删除

当前固定参数：

- challenge TTL：`2m`
- session TTL：`12h`
- 删除监听间隔：`1s`

删除 `auth.json` 后会立即清空：

- `initialized` 状态
- challenge 集合
- session 集合

### 3.4 `internal/app`

`app.App` 当前负责：

- 初始化 `auth.Manager`
- 打开数据库
- 执行 schema bootstrap 和严格校验
- 并发启动管理 API 和控制监听器
- 统一 shutdown

### 3.5 `internal/storage`

当前只提供最小 SQL 封装：

- `storage.SQL`
- `storage.Tx`
- `QueryContext`
- `QueryOneContext`
- `ExecContext`
- `WithTxContext`

数据库驱动注册放在 `internal/storage/drivers`。

### 3.6 `internal/api`

当前拆成 4 个部分：

- `server.go`：HTTP server、路由注册、日志中间件
- `auth.go`：管理认证路由与 Cookie/Bearer 会话
- `management.go`：分组和隧道 CRUD
- `webui.go`：静态资源托管和 SPA fallback

### 3.7 `internal/control`

当前稳定文件边界如下：

- `server.go`：accept loop、shutdown、session 读循环、心跳和基础帧读写
- `auth.go`：`frpc` 登录、challenge 生命周期、group slot
- `config.go`：runtime 加载、`config.push`、`config.ack`
- `connections.go`：控制连接跟踪
- `session.go`：`sessionState`、session 写锁、listener/stream/session 收口
- `listeners.go`：TCP/UDP listener 展开和 serve loop
- `tcp_bridge.go`：TCP `stream.*` 生命周期
- `udp.go`：UDP `udp.*` 生命周期和 idle cleanup
- `sockaddr.go`：地址转换辅助
- `repository.go`：从数据库构造 `GroupRuntime` 和 `ConfigSnapshot`

## 4. 当前数据模型

### 4.1 `proxy_groups`

当前控制面和管理面共用字段：

- `id`
- `name`
- `token_id`
- `token_hash`
- `enabled`
- `rate_limit`
- `client_access_mode`
- `tunnel_access_mode`
- `created_at`
- `updated_at`

当前运行时实际消费：

- `id`
- `name`
- `token_hash`
- `enabled`
- `client_access_mode`
- `updated_at`

### 4.2 `group_client_ip_rules`

字段：

- `id`
- `group_id`
- `action`
- `cidr`
- `comment`
- `created_at`

当前已参与 `frpc` 登录来源 IP 校验。

### 4.3 `group_tunnel_ip_rules`

字段已经存在，但当前数据面没有消费。

### 4.4 `tunnels`

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
- `rate_limit`
- `capture_enabled`
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

## 5. 管理 API 设计

### 5.1 认证接口

认证接口不依赖数据库管理员表，只依赖本地 `auth.json`。

当前行为：

- `GET /api/v1/auth/state`
  - 返回是否初始化
  - 返回当前请求是否已认证
- `POST /api/v1/auth/init`
  - 只接受 `key_hash`
  - 未初始化时写入 `auth.json`
- `POST /api/v1/auth/challenge`
  - 返回 `challenge_id`、`salt`、`expires_at`
- `POST /api/v1/auth/login`
  - 验证 proof
  - 写 Cookie
- `GET /api/v1/auth/session`
  - 返回会话状态
- `POST /api/v1/auth/logout`
  - 删除服务端 session
  - 清理 Cookie

### 5.2 分组接口

当前入参只支持：

- `name`
- `enabled`

创建或重置 token 时：

- 返回完整明文 token
- 数据库只保存 `token_id + token_hash`

### 5.3 隧道接口

当前支持字段：

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

校验规则：

- `protocol` 只能是 `tcp` 或 `udp`
- `remote_type` 只能是 `single` 或 `range`
- 端口范围必须在 `1..65535`
- `single` 模式下起止端口必须相同
- `range` 模式下本地和远端跨度必须一致
- `local_host` 使用 `protocol.ParseHost` 校验

当前不校验端口冲突，也不暴露 `rate_limit` / `capture_enabled` 管理入口。

## 6. 控制面设计

### 6.1 传输与超时

- 传输层由 `pkg/transport` 提供 `4` 字节长度前缀收发。
- 业务帧由 `pkg/protocol` 解析。
- 默认读写超时：`5s`
- 登录 challenge TTL：`30s`
- 心跳间隔：`15s`

会话读超时使用：

```text
max(3 * heartbeat_interval, minimum_read_timeout)
```

### 6.2 登录握手

当前时序：

```text
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
frps -> start listeners
```

登录过程中当前会执行：

1. 读取 `auth.begin`
2. 按 `token_id` 读取 `GroupRuntime`
3. 校验分组是否启用
4. 按 `group_client_ip_rules` 校验来源 IP
5. 签发 challenge
6. 用 `protocol.ChallengeResponse` 计算期望值
7. 常量时间比较响应
8. 抢占单分组单客户端槽位
9. 返回 `server.hello`
10. 下发首次 `config.push`

### 6.3 配置快照

当前 `ConfigSnapshot` 由 `repository.go` 构造：

- `Version`：取分组和隧道最新 `updated_at` 的 Unix microseconds
- `GeneratedAtMs`：快照时间
- `Tunnels`：当前分组所有隧道的协议快照

当前行为边界：

- 配置只在登录阶段加载一次
- `config.ack` 成功后才启动 listener
- 管理 API 改库后不会主动推送给已在线 `frpc`

## 7. 数据面设计

### 7.1 TCP

当前 TCP 数据面流程：

```text
public tcp accept
-> allocate streamId/requestId
-> send stream.open
-> wait stream.opened
-> copy public -> frpc with stream.data
-> copy frpc -> public with stream.data
-> either side sends stream.close
```

当前没有独立工作连接池；所有 `stream.*` 都复用同一条控制连接。

### 7.2 UDP

当前 UDP 数据面流程：

```text
public datagram
-> resolve or create publicUDPSession
-> first packet sends udp.open + udp.data
-> next packets send udp.data
-> frpc local reply returns as udp.data
-> frps writes reply back to public client
```

`publicUDPSession` 当前键为：

```text
tunnelId + remotePort + public client addr
```

当前 idle cleanup 语义：

- `frps` 是唯一生命周期裁决方
- 任何一次成功转发都会立即 `touch`
- 清理判断基于 `lastActive + idleTimeout`
- 默认 idle timeout 为 `30s`
- 默认 sweep 周期为 `1s`
- 清理后发送 `udp.close(reason=idle_timeout)`

### 7.3 范围映射

TCP/UDP 范围映射统一按下面公式执行：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

`frps` 在运行态按逐端口展开 listener，但协议里仍只保留一个 `tunnelId`。

## 8. 当前扩展边界

后续如果继续扩展，必须以当前边界为前提：

- 反向代理应新增独立运行态，而不是塞回现有正向代理结构。
- 连接注册表、速率统计、抓包、限速应建立在当前 TCP/UDP bridge 之上。
- 在线热更新需要补 listener diff、配置推送和 ack 状态管理，不能误写成“仅写库”。
- `group_tunnel_ip_rules`、`rate_limit`、`capture_enabled` 只有进入真实执行链路后，文档才允许改口为“已实现”。
