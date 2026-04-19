# 初步框架重构蓝图

更新时间：2026-04-19

本文是当前这一轮“初步框架重构”的冻结蓝图。它只定义本轮必须落地的结构收束方案、迁移顺序和不变边界，不承载长期能力愿景。

如果本文与现有设计文档中的长期设想冲突，以本文作为本轮执行基线；等对应代码完成等价迁移后，再把稳定结果同步回 `docs/frps/*.md`、`docs/frpc/*.md` 和 `docs/project-overview.md`。

## 1. 本轮目标

- 在不改变当前对外行为的前提下，给 `frps` 和 `frpc` 建立一套清晰、极简、可继续演进的骨架。
- 把当前“单文件/少文件承载多类职责”的实现，收束成明确的装配层、会话层、桥接层和运行态边界。
- 让后续 TCP/UDP 能力扩展、回归测试和问题定位不再依赖单个大文件理解全链路。

本轮是“等价迁移”，不是功能轮。

## 2. 不变边界

本轮重构期间，以下边界必须保持不变：

- 不改 `frps <-> frpc` wire protocol。
- 不改 `frpc` challenge 登录流程和 token 语义。
- 不改管理密钥模型：`auth.json` 只初始化一次，删除后自动重置。
- 不改 TCP/UDP single/range 映射公式：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

- 不改 UDP 生命周期裁决权：`frps` 是唯一会话裁决方。
- 不改 UDP idle cleanup 语义：一旦有成功收发立即刷新最后活跃时间，然后从最后一次活动结束开始计时，空闲约 `30s` 后清理。
- 不引入插件系统、事件总线、DI 容器、通用中间件链或“统一覆盖 `frps` / `frpc` 的大抽象层”。
- 不为“未来可能会用到”而提前建立多实现接口。

## 3. 设计原则

### 3.1 只做当前复杂度需要的抽象

- 优先拆职责，不优先拆包层级。
- 能用同一 package 下的小文件表达清楚的，不升级为多 package 协作。
- 能用显式调用关系表达清楚的，不引入注册表、反射或回调总线。

### 3.2 先收装配边界，再收业务边界

- `cmd/*` 和 `internal/app` 只做启动与装配。
- 运行时协议、会话、桥接、listener 生命周期从装配层剥离。
- 数据流入口和资源所有权必须可从文件结构直接看出来。

### 3.3 共享规则以“已稳定且重复”为前提

- 当前只共享真正稳定的传输与协议基础：`pkg/protocol`、`pkg/transport`。
- 端口换算、target 解析、session key 规则等，只有在 `frps` / `frpc` 都出现明确重复且稳定后，才考虑抽成极小纯函数。
- 本轮不建立横跨服务端和客户端的“统一 runtime 框架”。

## 4. 目标总图

```text
frps
  cmd/frps
  internal/config
  internal/logging
  internal/app              # 进程装配和生命周期
  internal/auth             # 管理面 auth.json / challenge / session
  internal/api              # HTTP server / routes / handlers
  internal/control          # frpc 控制连接、配置、TCP/UDP 桥接
  internal/storage          # SQL 包装与事务
  pkg/protocol
  pkg/transport

frpc
  cmd/frpc
  internal/config
  internal/client           # 连接、登录、会话、TCP/UDP 桥接
  依赖 frps module 暴露的 pkg/protocol 与 pkg/transport
```

本轮不新增新的平台级共享模块。结构收束优先通过文件内聚和调用方向清晰来达成。

## 5. `frps` 目标蓝图

### 5.1 包级边界

### `cmd/frps`

- 固定读取 `data/config.json`
- 初始化日志
- 交给 `internal/app` 启动

`cmd/frps` 不持有任何业务状态。

### `internal/app`

职责固定为进程装配层：

- 初始化管理认证管理器
- 打开数据库和校验 schema
- 装配 `api.Server`、`control.Server`
- 统一启动、等待、关闭

`internal/app` 不承载协议处理、listener 展开或具体管理 API 业务逻辑。

### `internal/auth`

继续保持独立：

- `auth.json` 初始化、读取、删除监听和自动复位
- challenge / session 管理
- 仅服务管理面

本轮不把 `frpc` 登录逻辑混入这里。

### `internal/api`

职责固定为管理面 HTTP 边界：

- `http.Server` 装配
- 路由注册
- 认证校验和 HTTP 编解码
- WebUI 静态资源托管
- 管理 CRUD handler

`internal/api` 可以保留轻量 service，但该 service 只服务 HTTP handler，不反向控制 `frpc` 控制面运行时。

### `internal/control`

这是本轮重构重点。目标不是改成多 package，而是在同一个 `control` package 下形成清晰文件分工。

建议目标文件结构：

```text
internal/control/
  server.go          # accept loop、连接生命周期、顶层 dispatch 装配
  auth.go            # frpc 登录握手、challenge、group slot
  session.go         # sessionState、写锁、request/stream id、close 协议
  config.go          # group runtime 加载、config.push / config.ack
  listeners.go       # TCP/UDP listener 展开、启动、回收
  tcp_bridge.go      # stream.open / stream.data / stream.close
  udp_bridge.go      # udp.open / udp.data / udp.close / idle cleanup
  repository.go      # 从 storage 组装 GroupRuntime / ConfigSnapshot
```

这里的关键不是文件名本身，而是 ownership：

- `server.go` 只保留“连接入口”和“消息分发入口”，不继续堆积 TCP/UDP 细节。
- `auth.go` 独占 `auth.begin`、`auth.finish`、challenge 生命周期、group slot 占用与释放。
- `config.go` 独占配置快照加载、首次 `config.push`、`config.ack` 收口。
- `listeners.go` 只负责把 tunnel 配置展开为公网 listener，并管理启动和关闭。
- `tcp_bridge.go` 只负责 TCP stream 生命周期。
- `udp_bridge.go` 只负责 UDP session 生命周期与空闲清理。

### `internal/storage`

继续保持极简：

- 包装 `*sql.DB` / `*sql.Tx`
- 提供统一 query / exec / tx 辅助

本轮不把业务 repository 统一迁入 `storage`，避免把“SQL 基础设施”和“控制面运行态视图”混在一起。

### 5.2 `frps/internal/control` 内部边界

`control.Server` 在本轮结束后应满足下面的阅读顺序：

1. `server.go` 能看清楚连接是如何接入、认证成功后如何进入 session。
2. `session.go` 能看清楚一个已登录 session 持有哪些运行态资源。
3. `config.go` 能看清楚配置如何下发和确认。
4. `listeners.go` / `tcp_bridge.go` / `udp_bridge.go` 能各自独立解释具体协议消息和资源回收。

明确禁止：

- 在 `server.go` 中继续直接堆积 `stream.*` 和 `udp.*` 的具体处理细节。
- 通过一个“万能 runtime manager”统一抽象 TCP stream 和 UDP session。
- 为 listener、stream、udp session 分别建立无实际替换场景的 interface。

### 5.3 `frps` 数据流固定形态

### 登录和配置

```text
accept conn
-> auth.begin / auth.finish
-> load GroupRuntime
-> reserve group slot
-> server.hello
-> config.push
-> wait config.ack
-> start tunnel listeners
-> session read loop
```

### TCP

```text
public tcp accept
-> allocate stream runtime
-> send stream.open(actual remotePort)
-> wait stream.opened
-> bridge public <-> frpc frames
-> stream.close on EOF / error / remote close
```

### UDP

```text
public udp datagram
-> bind/find session by tunnelId + remotePort + clientAddr
-> first packet sends udp.open + udp.data
-> later packets send udp.data
-> any successful transfer touches lastActive
-> frps idle timeout closes session and sends udp.close
```

## 6. `frpc` 目标蓝图

`frpc` 当前体量明显小于 `frps`，因此本轮仍保持单一 `internal/client` package，不做额外层级拆包。

建议目标文件结构：

```text
internal/client/
  client.go         # Run、runOnce、重连、session 编排
  login.go          # auth.begin / auth.finish / server.hello / 首次 config.push
  session.go        # readLoop、heartbeatLoop、frame dispatch、snapshot 原子替换
  targets.go        # tunnel 查找、range 映射、local target 解析
  tcp_bridge.go     # stream.open / data / close、本地 TCP copy
  udp_bridge.go     # udp.open / data / close、本地 UDP session copy
  runtime_info.go   # hostname / os / arch
```

核心约束：

- `client.go` 只保留连接生命周期和重连控制。
- `login.go` 只保留登录握手，不混入长会话读循环。
- `session.go` 只保留 session 级状态和 frame 分发。
- `targets.go` 负责把“配置快照 + remotePort”转换为本地目标，不让 TCP/UDP 分别维护一套散落的端口换算逻辑。
- `tcp_bridge.go` 和 `udp_bridge.go` 继续分开，不做“统一 tunnel bridge”。

### 6.1 `frpc` 数据流固定形态

### 控制连接

```text
dial server
-> login handshake
-> receive initial config.push and ack
-> start read loop + heartbeat loop
-> on disconnect clean local streams / udp sessions
-> backoff reconnect
```

### TCP

```text
receive stream.open
-> resolve local target from snapshot + remotePort
-> dial local tcp
-> reply stream.opened
-> bridge local <-> frps frames
-> close on local EOF / write error / remote close
```

### UDP

```text
receive udp.open
-> resolve local target from snapshot + remotePort
-> create local UDPConn by sessionId
-> receive udp.data and write to local service
-> copy local datagram back as udp.data
-> close only on udp.close / local fatal error / session end
```

## 7. 必须落地的抽象与必须保持直接实现的部分

### 必须落地

- 显式 session state 文件与资源 ownership。
- 显式 frame dispatch 入口。
- 显式 TCP / UDP bridge 责任边界。
- 显式 target 解析入口。
- 显式 listener 生命周期入口。

### 必须保持直接实现

- `net.Conn` / `net.UDPConn` 直接使用，不包一层“通用连接接口”。
- TCP stream 和 UDP session 分别维护，不合并成统一“channel”模型。
- 当前单个 `Repository` 继续服务 `frps` 控制面，不为管理面和控制面强行做统一仓储层。
- `frpc` 不新增 `app` 装配层，当前体量还不值得。

## 8. 迁移顺序

### 步骤 1：冻结蓝图

- 完成本文件
- 将本文作为当前轮执行基线

### 步骤 2：收束 `frps` 骨架

按下面顺序等价迁移：

1. 先把 `sessionState`、写辅助、request/stream id 等 session 基础设施从 `server.go` 收出去。
2. 再把 `config.push / config.ack` 和配置快照加载逻辑单独收口。
3. 再把 TCP listener / stream 桥接收口到独立文件。
4. 再把 UDP listener / session / idle cleanup 收口到独立文件。
5. 让 `server.go` 最终只剩 accept、登录入口、session read loop 和顶层 dispatch。

### 步骤 3：收束 `frpc` 骨架

按下面顺序等价迁移：

1. 先拆 `login.go`，把登录握手从长会话逻辑中拿出去。
2. 再拆 `session.go`，把读循环、心跳、dispatch 与 snapshot 操作收口。
3. 再拆 `targets.go`，统一 TCP/UDP 的目标地址计算入口。
4. 最后整理 `tcp_bridge.go` 和 `udp_bridge.go`，只保留桥接逻辑。

### 步骤 4：提炼最小共享规则与回归入口

- 只抽取已经稳定且重复的纯规则函数。
- 统一 `go test` 和 Python e2e 的回归入口说明。
- 不重写现有测试策略，只做入口收束。

### 步骤 5：总回归和文档收束

- 回归 TCP/UDP single/range
- 回归 UDP idle cleanup
- 将最终稳定边界同步回正式文档

## 9. 每一步的验收标准

### `frps` 骨架验收

- `frps/internal/control/server.go` 的职责明显变窄。
- `go test ./...` 在 `frps/` 模块通过。
- 现有管理面、控制面、TCP/UDP 行为无回退。

### `frpc` 骨架验收

- `frpc/internal/client/client.go` 的职责明显变窄。
- `go test ./...` 在 `frpc/` 模块通过。
- token 登录、配置应用、TCP/UDP 转发行为无回退。

### 本轮总验收

- Python e2e 继续通过：
  - `test/e2e_tcp_single.py --scenario happy_path`
  - `test/e2e_tcp_range.py`
  - `test/e2e_udp_single.py --scenario happy_path`
  - `test/e2e_udp_single.py --scenario idle_cleanup`
  - `test/e2e_udp_range.py`

## 10. 当前唯一下一步

蓝图冻结后，第一条实现动作固定为：

- 先收束 `frps/internal/control` 的 session 基础设施和顶层 dispatch，让 `server.go` 从“既负责登录又负责运行态细节”收窄为“连接入口 + 会话入口 + 分发入口”。
