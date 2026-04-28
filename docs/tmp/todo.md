# frps control 拆分 Todo

## 总目标

- 目标保持单一稳定入口：`frps/internal/control/`。
- `frps/internal/controlv2/` 只允许作为迁移中间态存在，最终必须整体删除。
- 后续不再给旧 `control/` 和 `controlv2/` 双轨补功能；所有改动都服务“去分叉”和“组件摊平后重新拼装”。
- 拆分方向不是简单按现有文件切小，而是先识别最小职责组件，再按领域分类形成多级子包，最后由最上层业务 facade 逐层拼装。

最终形态应该是：

```text
app/api
  |
internal/control            # 对外 facade，只暴露 Server / Options / Refresh / Observe / TLS runtime
  |
  +-- protocol/*            # frpc 控制连接线协议
  +-- session/*             # 会话状态机与 supervisor
  +-- runtime/*             # listener、数据面、扫描、恢复、issue
  +-- domain/*              # 运行时配置模型与转换
  +-- repo/*                # SQL 到 runtime projection
```

## 当前 control 最小组件摊开

### 入口生命周期

- `Options`
  - 控制监听地址
  - SQL store / runtime repository
  - network snapshot reader
  - read/write/challenge/heartbeat/runtime-scan timeout
  - clock / scheduler
  - listener factory
  - frame IO
- `Server`
  - 控制监听器生命周期
  - 活动连接集合
  - shutdown channel / closeOnce / wait groups
  - 首轮 runtime scan 门闩
  - runtime scan cancel 和 in-flight 状态
  - supervisor 引用
  - runtime issue store
  - control TLS certificate runtime
- `ListenAndServe`
  - 确保首轮扫描完成
  - 打开控制监听口
  - 启动 runtime issue polling
  - accept frpc 控制连接
- `Shutdown`
  - 关闭监听器
  - cancel runtime scan
  - stop supervisor
  - 关闭活动连接
  - 等待连接 goroutine 和 scan goroutine 退出
- `EnsureInitialRuntimeScan`
  - 控制面和管理面共享启动门闩
  - 必须在 login gate 打开前完成一次全量 runtime scan

### 控制连接线协议

- frame IO
  - `readFrame`
  - `readFrameWithTimeout`
  - `readFrameWithSessionTimeout`
  - `writeFrame`
  - `writeFrameWithContext`
  - `writeFrameWithSession`
  - `writeRuntimeFrameWithSession`
  - `writeRuntimeFramesWithSession`
  - `frameContext`
- protocol error reply
  - `replyProtocolError`
  - `replyProtocolErrorWithSession`
  - `replyError`
  - `replyErrorWithSession`
  - `writeError`
  - `writeErrorWithSession`
- connection logging
  - expected close 判断
  - EOF / closed / timeout / protocol error reason 映射
  - accept/login/session ended 日志分类
- transport handshake
  - `transport.client_hello`
  - 按 client id 查询 group
  - 选择 plain / tls
  - `transport.server_hello`
  - TLS upgrade
- auth handshake
  - `auth.begin`
  - challenge issue / ttl / replay protection
  - `auth.finish`
  - challenge response 常量时间校验
  - group enabled 校验
  - session id 分配
  - attach supervisor
- control handlers
  - `config.ack`
  - `heartbeat.ping`
  - `stream.opened`
  - `stream.data`
  - `stream.close`
  - `udp.data`
  - `udp.close`
  - unknown frame type error

### 会话状态机

- 当前 `internal/control/session/` 已经相对干净。
- 最小组件：
  - `SessionState`
  - `SessionPhase`
  - `RuntimePhase`
  - `BlockReason`
  - `BindingPhase`
  - `ControlConnState`
  - desired / pending / applied runtime snapshot
  - binding state
  - TCP stream state
  - UDP session state
  - events
  - actions
  - reducer
  - reconcile
  - agent event loop
  - executor seam
- 继续拆时要保持原则：
  - reducer 只做状态决策，不直接做 IO。
  - action 表达“需要发生什么”，不表达“怎么发生”。
  - executor 是状态机到外部世界的唯一副作用桥。

### session 运行态容器

- 当前 `sessionState` 仍在根包 `runtime.go`，职责过多。
- 最小组件：
  - `ConcreteSessionState`
  - control state mutex
  - runtime state mutex
  - write mutex
  - runtime IO mutex
  - done channel
  - request id allocator
  - stream id allocator
  - current group / snapshot
  - pending group / recovery mode
  - listener attachment
  - runtime freeze / reset / allow start
  - active tunnel id projection
  - public stream map
  - UDP session map
  - UDP key index
  - observe config state
  - observe runtime state

### 配置同步

- 最小组件：
  - desired runtime snapshot conversion
  - runtime config snapshot conversion
  - `prepareConfigPush`
  - `pushReloadConfig`
  - `handleConfigAck`
  - `acceptConfigAck`
  - initial startup ack 校验
  - effective IP startup rejection
  - pending config refresh
  - online `RefreshGroup`
  - empty config snapshot
  - same pushed/runtime snapshot 判断
- 当前问题：
  - `config.push` 写帧逻辑与 session state mutation 混在一起。
  - `config.ack` 既做协议校验，又做状态变更，还做 initial startup runtime 校验。
  - `RefreshGroup` 同时查询仓储、更新 desired、触发 supervisor、影响 runtime recovery。

### listener 和 bind

- 已有 `internal/control/bind/`，但还有职责散在根包和 `runtime/` 包。
- 最小组件：
  - listen key
  - bind kind
  - listener bind context
  - listener factory
  - scripted listener factory
  - UDP listener abstraction
  - binding claim owner
  - binding conflict
  - binding manager
  - expand desired tunnels to binding keys
  - tunnel listener operation context
  - TCP listener start
  - UDP resolve + listener start
  - tunnel listener TLS config loading
  - probe listener start
  - close started listeners
- 当前问题：
  - `bind.Manager` 基本没有接入主路径，实际启动仍主要依赖 OS bind/probe 和 runtime conflict scan。
  - tunnel listener TLS 读取依赖 storage 和 entrycerts，放在 `bind` 里会让底层 bind 包承担证书业务。
  - listener test hook 包在 `Server.listenTCP/listenUDP/resolveUDPAddr`，导致 runtime 包不能独立测试真实 listener start 编排。

### TCP 数据面

- 最小组件：
  - accept public TCP connection
  - allocate stream id / request id
  - build `stream.open`
  - add public stream
  - wait `stream.opened`
  - public -> frpc copy loop
  - frpc -> public `stream.data`
  - stream close frame
  - close public stream
  - write full payload
  - client addr projection
  - stream observe state
- 当前问题：
  - 数据面 TCP 逻辑在 `wire.go`，但依赖 `Server` 的 clock/options/frame writer/sessionState。
  - session FSM 已有 stream action/event，但主路径仍直接操作 `sessionState`，状态机和真实数据面没有完全闭环。

### UDP 数据面

- 最小组件：
  - UDP listener read loop
  - public UDP session key
  - allocate UDP pseudo stream id
  - build `udp.open`
  - forward `udp.data`
  - frpc -> public UDP write
  - UDP idle cleanup scheduler
  - close public UDP session
  - clone UDP addr
  - UDP observe state
- 当前问题：
  - idle cleanup 由 `Server` 直接调度。
  - UDP session map 和 key index 在 session runtime state 内，但行为方法仍在根包 `wire.go`。
  - 和 TCP 一样，FSM 中的 UDP action/event 还没有成为唯一数据面入口。

### runtime 审计、扫描、恢复

- 已有 `internal/control/runtime/`，但 `RuntimeOperator` 太宽。
- 最小组件：
  - runtime issue store
  - static configured conflict detection
  - active runtime conflict detection
  - runtime snapshot index
  - non-listening tunnel selection
  - tunnel status projection
  - listener probe
  - scan round concurrency guard
  - polling scheduler
  - scan all groups
  - preserve healthy issue until recovery
  - active session config recovery
  - audited listener recovery
  - runtime issue clear/record/apply semantics
- 当前问题：
  - `RuntimeOperator` 同时包含状态、仓储、supervisor、listener、serve loop、scan、recovery、logging、scheduler。
  - interface 里有 `any` 和 placeholder `Supervisor`，说明包边界仍有循环依赖压力。
  - runtime 包既包含纯计算，又调用实际副作用，测试边界不够清楚。

### supervisor / session registry

- 最小组件：
  - group -> agent
  - session -> agent
  - group -> runtime executor
  - session -> runtime executor
  - session state cache
  - group slot
  - cancel registry
  - attach session
  - takeover old session
  - dispatch event by session id
  - update desired runtime
  - network change broadcast
  - runtime snapshot
  - active session lookup
  - shutdown all agents
- 当前问题：
  - supervisor 在根包，直接依赖 `runtimeExecutor` 和 `sessionState`。
  - `control/runtime` 又定义 placeholder `Supervisor`，这是需要拆掉的临时适配味道。
  - active session 对外返回的是 runtime 包模型，但内部仍是根包私有结构。

### action executor

- 最小组件：
  - send server hello
  - push config
  - send config error
  - heartbeat pong
  - prepare bindings
  - start bindings
  - stop bindings
  - drain streams
  - drain UDP sessions
  - reset runtime
  - close control connection
  - binding failure mapping
  - binding outcome mapping
  - block reason mapping
- 当前问题：
  - executor 直接依赖 `Server` 全量能力。
  - executor 里同时包含协议编码、frame 写入、effective IP 解析、listener start、runtime drain。
  - 这是后续“业务拼装层”的核心拆分点。

### 仓储投影

- 已有 `internal/control/repo/`。
- 最小组件：
  - `Repository`
  - `GroupRuntime`
  - `ConfigSnapshot`
  - load by client id
  - load by group id
  - list group runtimes
  - group row decode
  - tunnel row decode
  - tunnel TLS usage map
  - snapshot version / generated_at 计算
  - DB enum decode
- 第二阶段进展：
  - `GroupRuntime` 和 `ConfigSnapshot` 已落到 `internal/control/domain/runtime`。
  - `repo` 通过 type alias 返回 domain model，职责收敛为 SQL projection。
  - 旧 `control` 和 `control/runtime` API 暂保留 alias/delegate，避免上层一次性迁移。

### 观测输出

- 最小组件：
  - server observed state
  - initial scan / control listener / login gate 状态
  - group slots
  - session observed state
  - attached listeners
  - missing listeners
  - runtime connections
  - tunnel status
  - runtime issue kind
  - static conflict projection
- 当前问题：
  - `ObserveState` 直接从 server 内部抓 supervisor、repo、issue store。
  - 观测 projection 应下沉为独立 query/projection 层，Server 只提供快照数据源。

## 目标多级子包结构

### 第一层：对外 facade

```text
frps/internal/control
  server.go
  options.go
  aliases.go
```

职责：

- 对 `internal/app` 和 `internal/api` 暴露稳定类型。
- 保持 `NewServer(options, logger, version)`。
- 保持 `ListenAndServe`、`Shutdown`、`EnsureInitialRuntimeScan`。
- 保持 `RefreshGroup`、`ObserveState`。
- 保持 `ConfigureControlTLS`、`ClearControlTLS`。
- 不直接实现 frame handler、listener serve loop、runtime scan 算法。

### 第二层：server 生命周期

```text
frps/internal/control/server
  lifecycle/
  accept/
  gate/
```

职责：

- control listener 打开/关闭。
- accept loop。
- active connection registry。
- shutdown orchestration。
- initial runtime scan gate。
- runtime scan polling 的启动时机。

注意：

- 这里可以保留 `net.Listener` 和 goroutine 管理。
- 不应该知道 `config.ack`、`stream.data`、`udp.data` 的内部协议规则。

### 第二层：protocol

```text
frps/internal/control/protocol
  frameio/
  errors/
  handshake/
  auth/
  dispatch/
  heartbeat/
  configsync/
  streams/
  udp/
```

职责：

- 所有 frpc 控制连接 frame 校验。
- frame body marshal/unmarshal。
- protocol error 回复。
- transport hello 和 TLS upgrade。
- auth challenge 生命周期。
- session frame dispatch。

设计要求：

- `frameio` 只关心 `transport.FrameIO`、timeout、frame context。
- `handshake` 只依赖 repo、TLS certificate provider、frame reader/writer。
- `auth` 只依赖 repo、clock、challenge store、session factory/supervisor seam。
- `dispatch` 只把 frame 转成 session event 或调用数据面 handler。

### 第二层：domain

```text
frps/internal/control/domain
  runtime/
  snapshot/
  tunnel/
```

职责：

- `GroupRuntime`
- `ConfigSnapshot`
- desired/applied snapshot 转换
- runtime snapshot 比较
- enabled tunnel 过滤
- protocol name/value 转换
- config version 语义
- empty config 语义

设计要求：

- domain 不能依赖 storage、net listener、scheduler、server。
- domain 可以依赖 `pkg/protocol`，因为 wire tunnel entry 目前是运行时配置载体。

### 第二层：repo

```text
frps/internal/control/repo
  runtime/
  auth/
  sql/
  decode/
```

职责：

- SQL 查询。
- row decode。
- persistent config -> domain runtime projection。
- auth 登录所需 group credential projection。
- tunnel certificate usage projection。

设计要求：

- repo 返回 domain model。
- repo 不做 runtime listener 决策。
- repo 不记录 runtime issue。

### 第二层：session

```text
frps/internal/control/session
  state/
  event/
  action/
  reducer/
  reconcile/
  agent/
  supervisor/
  executor/
```

职责：

- state machine 保持纯逻辑。
- supervisor 管 session/agent/group slot。
- executor 只定义副作用接口和 action dispatch。

设计要求：

- reducer 不依赖 protocol marshal。
- reducer 不依赖 net.Conn。
- supervisor 不依赖 server 根对象。
- executor 不应该只有一个巨型 `Server` 依赖，应拆成小 capability。

### 第二层：runtime

```text
frps/internal/control/runtime
  state/
  bind/
  listener/
  listener/tls/
  serve/tcp/
  serve/udp/
  scan/
  recovery/
  conflict/
  issues/
  observe/
```

职责：

- session concrete runtime state。
- listener 启停。
- TCP/UDP 数据面服务。
- runtime issue 记录。
- runtime scan。
- runtime recovery。
- runtime observed projection。

设计要求：

- `runtime/state` 管数据结构和并发锁。
- `runtime/listener` 管监听器创建、probe、close。
- `runtime/serve/tcp` 只管 TCP 数据面。
- `runtime/serve/udp` 只管 UDP 数据面和 idle。
- `runtime/scan` 不直接知道 server，只依赖小接口。
- `runtime/recovery` 不直接持有 supervisor 实现，只依赖 session registry seam。

### 第三层：wiring / assembly

```text
frps/internal/control/wiring
  container.go
  capabilities.go
  executor.go
```

职责：

- 把 repo、protocol、session、runtime、scanner、observer 拼起来。
- 统一注入 clock/scheduler/logger/frameIO/listenerFactory/network/store。
- 给 facade `control.Server` 提供最终组合对象。

设计要求：

- wiring 可以依赖所有子包。
- 子包之间不能反向依赖 wiring。
- 当发现循环依赖时，优先提取 interface 到更底层或单独 seam 包。

## 关键接口拆分

当前 `control/runtime.RuntimeOperator` 太宽，应拆成以下小接口。

### RuntimeReadiness

```go
type RuntimeReadiness interface {
    IsShuttingDown() bool
}
```

### RuntimeIssueWriter

```go
type RuntimeIssueWriter interface {
    RecordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string)
    ClearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{})
    ApplyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{})
}
```

### RuntimeIPResolver

```go
type RuntimeIPResolver interface {
    ResolveGroupEffectiveIP(group GroupRuntime) (string, error)
}
```

### ListenerStarter

```go
type ListenerStarter interface {
    StartTunnelListeners(ctx TunnelListenerOperationContext) (TunnelListenerBatch, error)
    ProbeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string
}
```

### RuntimeSessionRegistry

```go
type RuntimeSessionRegistry interface {
    ActiveSession(groupID int64) (*ActiveSession, bool)
    ActiveRuntimeGroups(exclude any) []RuntimeGroupSnapshot
    RuntimeSnapshotIndex() RuntimeSnapshotIndex
    SessionState(sessionID uint64) (session.SessionState, bool)
    DispatchBySessionID(sessionID uint64, event session.Event) bool
    ApplySessionEvent(sessionID uint64, event session.Event)
}
```

### RuntimeScannerDeps

```go
type RuntimeScannerDeps interface {
    RuntimeReadiness
    RuntimeIssueWriter
    RuntimeIPResolver
    ListenerStarter
    RuntimeSessionRegistry
    Repo() Repository
    Logger() *slog.Logger
}
```

### RuntimeServeDeps

```go
type RuntimeServeDeps interface {
    Clock() clock.Clock
    Scheduler() clock.Scheduler
    FrameWriter() protocol.FrameWriter
}
```

拆分目标：

- scan 不再要求 listener serve loop 能力。
- listener start 不再要求 repo 能力。
- TCP/UDP data plane 不再要求 runtime scan 能力。
- executor 按 action 类型依赖最小 capability。

## 推荐迁移顺序

### 第 1 阶段：建立文档和边界保护

- 更新 `docs/tmp/todo.md` 作为拆分总路线。
- 在 `docs/frps/technical/control-plane.md` 补充最新目标边界。
- 标记不再继续扩大 `RuntimeOperator`。
- 新增包时优先写 compile-only seam，避免一次迁移过大。

验收：

- 文档能说明每个组件归属。
- 新增代码不引入 controlv2 新功能。

### 第 2 阶段：下沉 domain model（已完成）

- 新建 `internal/control/domain/runtime`。
- 移动或别名：
  - `GroupRuntime`
  - `ConfigSnapshot`
  - snapshot conversion
  - enabled tunnel helpers
  - same snapshot helpers
  - protocol name/value helpers
- `repo` 改为返回 domain model。
- 根 `control` 保留 aliases，避免外部一次性全改。

完成内容：

- `internal/control/domain/runtime` 持有 `GroupRuntime`、`ConfigSnapshot`。
- snapshot conversion、enabled tunnel helper、same snapshot helper、protocol name/value helper 已下沉到 domain runtime。
- `repo.Repository` 仍保持原签名形态，但返回类型实际是 domain runtime alias。
- `control/runtime` 仅保留兼容 wrapper，内部委托 domain runtime。
- `runtime/scan` 不再为了 domain 类型导入 `repo`。
- `internal/app` 仍可通过 `control.GroupRuntime` 和 `control.ConfigSnapshot` 编译。

验收：

- `go test ./internal/control/...`
- `go test ./internal/app ./internal/api/...`
- `internal/app` tests 仍可通过 `control.GroupRuntime` alias 编译。

### 第 3 阶段：拆 frame IO 和 protocol error（已完成）

- 新建 `internal/control/protocol/frameio`。
- 下沉：
  - frame context
  - read frame with timeout
  - write frame with timeout
  - session-aware writer lock seam
- 新建 `internal/control/protocol/errors`。
- 下沉：
  - protocol error reply
  - error body marshal
  - connection reason mapping

完成内容：

- `internal/control/protocol/frameio` 持有 server frame context 构造、read frame with timeout、write frame with timeout。
- `frameio.Writer` 成为根包和协议错误包之间的写帧 seam，frame marshal/write 细节不再留在 `server.go`。
- `internal/control/protocol/errors` 持有 protocol error reply、error body marshal、连接关闭原因映射。
- session-aware error reply 通过 `sessionFrameWriter` 继续复用 `writeFrameWithSession`，保留 control frame 写锁。
- runtime IO 写入仍先进入 `lockRuntimeIOWrite`，再委托 `frameio.Writer`，保持 runtime IO 锁顺序。

验收：

- `server.go` 不再直接包含 frame marshal/write 细节。
- session-aware writer 仍能保证 control frame 和 runtime IO 的写锁顺序。

### 第 4 阶段：拆 transport handshake 和 control TLS runtime（已完成）

- 新建 `internal/control/protocol/handshake`。
- 下沉：
  - `negotiateTransport`
  - `selectTransportSecurityMode`
  - `upgradeControlConnToTLS`
- 新建 control TLS certificate provider。
- `ConfigureControlTLS` / `ClearControlTLS` 保持在 facade，内部委托给 TLS runtime store。

完成内容：

- `internal/control/protocol/handshake` 持有 transport client/server hello 编排、client id 加载 group、plain/tls 模式选择和 TLS upgrade。
- `handshake.Repository`、`handshake.FrameReader`、`protocol/errors.FrameWriter`、`handshake.TLSCertificateProvider` 成为 transport negotiation 的最小 seam。
- `handshake.ControlTLSStore` 持有 control listener TLS 证书运行态，root `Server` 不再直接维护 TLS 证书锁和证书字段。
- 根包 `tls.go` 压缩为 facade/adapter：`ConfigureControlTLS`、`ClearControlTLS` 仍保持对 app/api 的稳定入口，`negotiateTransport`、`selectTransportSecurityMode`、`upgradeControlConnToTLS` 只委托子包。
- 新增 TLS required / unsupported / unavailable 选择逻辑单测，保持原有错误码优先级：证书不可用优先返回 `transport_tls_unavailable`，证书可用但客户端不支持 TLS 返回 `transport_tls_unsupported`。

验收：

- TLS required / unsupported / unavailable 测试通过。
- `tls.go` 从根包移除或只剩 facade adapter。
- `go test ./internal/control/...`
- `go test ./internal/app ./internal/api/...`

### 第 5 阶段：拆 auth challenge service（已完成）

- 新建 `internal/control/protocol/auth`。
- 下沉：
  - `authChallenge`
  - issue challenge
  - consume challenge
  - purge expired challenge
  - auth begin/finish frame 校验
- auth service 输出“已认证 group + session attach request”，不要直接构造完整 server runtime。

完成内容：

- `internal/control/protocol/auth` 持有 auth.begin/auth.finish 帧校验、auth.challenge 写出、challenge issue/consume/purge 和 group enabled 二次校验。
- `auth.ChallengeService` 接管 challenge 运行态，root `Server` 不再直接维护 `challengeMu`、`challenges`、`nextChallengeID`。
- `auth.Authenticate` 通过 `Repository`、`FrameReader`、`FrameWriter`、`ChallengeService` 这些最小 seam 完成协议认证，返回已认证 `GroupRuntime` 和 finish request id。
- 根包 `auth.go` 只保留登录后的 session/runtime/supervisor 拼装；`issueChallenge` 和 `consumeChallenge` 暂留兼容 adapter，内部委托 `auth.ChallengeService`。
- 新增 auth 子包单测，覆盖 challenge replay、expired、mismatch 后标记 used、auth.begin client mismatch、auth.finish stream id 校验，以及不启动完整 Server 的认证成功路径。

验收：

- auth 单测可以不启动完整 Server。
- challenge replay / expired / mismatch 行为保持不变。
- `go test ./internal/control/...`
- `go test ./internal/app ./internal/api/...`

### 第 6 阶段：拆 concrete session runtime state（已完成）

- 新建 `internal/control/runtime/state`。
- 移动：
  - `ConcreteSessionState`
  - stream model
  - UDP session model
  - listener state
  - runtime freeze/reset/allow
  - active tunnel ids
  - observe runtime state
- 根包 `sessionState` 临时变成 thin wrapper 或 type alias。

完成内容：

- 新建 `internal/control/runtime/state`，集中承载 concrete session 运行态数据结构和并发锁。
- `ConcreteSessionState`、listener state、stream model、UDP session model、runtime freeze/reset/allow、active tunnel id projection、runtime observe projection 已下沉。
- `control/runtime` 保留 alias/delegate 兼容层，避免一次性改动上层 app/api 和 runtime scanner 边界。
- 根包 `sessionState` 改为嵌入 `controlruntime.ConcreteSessionState` 的 thin wrapper，`runtime.go` 中 runtime state mutation 只做委托。
- `wire.go` 中 UDP session map/key/index/idle cleanup 行为改为委托 `runtime/state`，根包不再持有对应 map 操作实现。
- 新增 `runtime/state` 单测，覆盖 listener attach、TCP/UDP connection tracking、observe projection 和 freeze drain。

验收：

- 根包 `runtime.go` 行数显著下降。
- `sessionState` 不再是所有 runtime 行为的聚合地。
- `go test ./internal/control/runtime/state`
- `go test ./internal/control/...`
- `go test ./internal/app ./internal/api/...`

### 第 7 阶段：拆 config sync（已完成）

- 新建 `internal/control/protocol/configsync`。
- 下沉：
  - config push body build
  - config ack frame 校验
  - ack accept error mapping
  - initial startup runtime validation
- 把 `prepareConfigPush` / `acceptConfigAck` 留在 session runtime state 或专门 `session/config`。
- `RefreshGroup` 拆成：
  - load latest group
  - compute runtime refresh snapshot
  - update desired runtime
  - notify supervisor

验收：

- `handleConfigAck` 不再同时承担 frame 校验、状态变更、runtime 校验、agent enqueue。
- refresh 相关场景测试通过。

完成内容：

- 新建 `internal/control/protocol/configsync`，集中承载 `config.push` body/frame 构造、`config.ack` frame 校验、ack accept 错误映射和 initial startup runtime 校验编排。
- `handleConfigAck` 压缩为：读取 pending ack state、委托协议校验、提交 session state、委托启动期 runtime 校验、通知 agent。
- `pushReloadConfig` 和 executor 的 `ActionPushConfig` 复用 configsync 的 push 构造逻辑；`pushReloadConfig` 保持“先 marshal、再 pending mutation”的原行为顺序。
- `RefreshGroup` 拆为活动会话查找、最新 group 加载、runtime refresh snapshot 计算、active session desired 更新、supervisor 通知五段私有编排函数。
- 新增 configsync 单测覆盖 push 构造、ack 请求/stream/version/status 校验、accept 错误映射和启动期 runtime issue 记录。

### 第 8 阶段：拆 listener start/probe/close（已完成）

- 新建 `internal/control/runtime/listener`。
- 移动：
  - `TunnelListenerOperationContext`
  - `TunnelListenerBatch`
  - TCP/UDP listener start
  - probe listener start
  - close started listeners
  - listener test hooks
- 新建 `internal/control/runtime/listener/tls`。
- 移动 tunnel listener TLS config loading。

验收：

- listener start 可以用 fake listener factory 单测，不依赖完整 Server。
- probe 和 start 共用同一 listener starter。

完成内容：

- 新建 `internal/control/runtime/listener`，承载 listener operation context、listener batch、TCP/UDP listener runtime model、start/probe/close 和监听器 testhook。
- 新建 `internal/control/runtime/listener/tls`，承载 tunnel listener TLS 配置读取，`bind` 包不再承担证书业务。
- `control/runtime` 保留兼容 alias/delegate，现有 scanner、plan、server RuntimeOperator 边界不需要一次性迁移。
- 根包 `Server.startTunnelListeners` 压缩为 assembly adapter，只注入 listener factory 和 TLS config loader。
- 新增 listener 包单测，覆盖 TCP/UDP start、range 中途失败自动清理、probe 复用同一 starter。

### 第 9 阶段：拆 TCP 数据面（已完成）

- 新建 `internal/control/runtime/serve/tcp`。
- 移动：
  - `serveTunnelListener`
  - `handlePublicConnection`
  - stream open operation
  - stream opened/data/close handlers
  - public -> frpc copy loop
  - stream close sender
  - `writeConnFull`
  - sock addr helpers中 TCP 相关部分

验收：

- TCP 数据面只依赖 session runtime state、frame writer、clock、logger。
- `wire.go` 中 TCP 相关逻辑清空或只剩 adapter。

完成内容：

- 新建 `internal/control/runtime/serve/tcp`，承载 TCP listener accept loop、public TCP connection open、stream.opened/data/close handler、public -> frpc copy loop、stream.close 发送、`WriteConnFull` 和 TCP sock addr projection。
- `wire.go` 中 TCP 主逻辑压缩为 adapter，继续保留同包测试需要的兼容 wrapper；UDP 逻辑暂留待第10阶段。
- 新 TCP 包通过最小 seam 依赖 session runtime state、runtime/control frame writer、clock、logger，不直接依赖完整 `Server`。
- 新增 TCP 数据面单测，覆盖 stream.open 构造与连接元数据、stream.opened ready signal、stream.data 写 public conn、public read EOF 发送 stream.close。

### 第 10 阶段：拆 UDP 数据面（已完成）

- 新建 `internal/control/runtime/serve/udp`。
- 移动：
  - `serveUDPTunnelListener`
  - `handlePublicUDPDatagram`
  - UDP datagram forward operation
  - UDP session bind/lookup/close
  - idle cleanup
  - UDP close sender
  - UDP addr clone/key helpers

验收：

- UDP idle cleanup 可以用 manual scheduler 单测。
- UDP path 不再需要完整 Server。

完成内容：

- 新建 `internal/control/runtime/serve/udp`，承载 UDP listener read loop、public UDP datagram forward、frpc -> public UDP data、udp.close handler、udp.close 发送和 idle cleanup。
- UDP public session 创建、client addr projection、UDP addr clone 和 datagram frame 构造下沉到 UDP 数据面包。
- `wire.go` / `runtime.go` 中 UDP 主路径压缩为 adapter，继续保留同包测试兼容 wrapper。
- 新增 UDP 数据面单测，覆盖 UDP open/data frame 构造与 session 复用、frpc UDP data 写回 public listener、missing session close、manual scheduler 驱动 idle cleanup。

### 第 11 阶段：拆 supervisor

- 新建 `internal/control/session/supervisor`。
- 移动：
  - group/session agent registry
  - runtime executor registry
  - group slots
  - cancel registry
  - snapshot builder
  - active session lookup
- 消除 `control/runtime` 中 placeholder `Supervisor`。

验收：

- runtime 包不再通过 `any` 获取 supervisor。
- takeover、shutdown、active session 测试通过。

### 第 12 阶段：拆 executor capabilities

- 新建 `internal/control/session/executor`。
- 将当前 `serverActionExecutor` 拆成多个 handler：
  - hello sender
  - config pusher
  - heartbeat pong sender
  - binding preparer
  - binding starter
  - runtime drainer
  - control closer
- 每个 handler 只依赖自己的 capability。

验收：

- action executor 不再直接依赖完整 `Server`。
- binding failure/outcome mapping 有独立测试。

### 第 13 阶段：拆 runtime scan/recovery 接口

- 把 `RuntimeOperator` 拆成小接口。
- `scan` 只依赖 scanner deps。
- `recovery` 只依赖 session registry + desired updater。
- `conflict` 保持纯函数。
- `issues` 保持状态 store。

验收：

- `runtime/interface.go` 不再是 300 行级别巨型接口文件。
- 不再出现 placeholder type 和 `any` 作为主要边界。

### 第 14 阶段：拆 observe projection

- 新建 `internal/control/runtime/observe` 或 `internal/control/observe` 子包。
- 下沉：
  - server observed state builder
  - tunnel status projection
  - runtime issue kind
  - sorting
- Server 只提供：
  - lifecycle snapshot
  - supervisor snapshot
  - group runtime list
  - issue snapshot

验收：

- `ObserveState` 变成 facade 方法。
- projection 可以用构造数据单测。

### 第 15 阶段：收口根包 facade

- 根包只保留：
  - `Options`
  - `Server`
  - `NewServer`
  - 对 app/api 需要的接口方法
  - 兼容 alias
- 删除或压缩：
  - 根包 `runtime.go`
  - 根包 `wire.go`
  - 根包 `executor.go`
  - 根包 `supervisor.go`
  - 根包 `auth.go`
  - 根包 `tls.go`
- 删除 `internal/controlv2/`。

验收：

- `go test ./...`
- 根包没有大型业务实现文件。
- docs 更新到新结构。

## 风险点和约束

- 不要一次性大搬迁所有文件；测试体量大，容易把行为回归藏在移动噪声里。
- 每一阶段都要保持 `control.NewServer` 对 app/api 的外部形态稳定。
- 不要在拆包时同时改协议语义。
- 不要在拆包时同时改 runtime recovery 策略。
- 不要让 `runtime` 重新反向依赖根 `control`。
- 不要继续扩大 `RuntimeOperator`。
- 遇到循环依赖时，先拆模型或 capability，不要用 `any` 绕过去。
- 保持 test hook 名称稳定；现有场景测试依赖 hook 名，不依赖函数名。
- 保持 manual clock/scheduler seam，不要退回 `time.Now()` / `time.After()` 的硬编码路径。
- listener fake 和 frame fake 要继续走同一生产 seam，避免测试特化路径。

## 推荐阶段性命令

```powershell
go test ./internal/control/...
go test ./internal/app ./internal/api/...
go test ./...
```

如果只改文档：

```powershell
git status --short
git diff -- docs/tmp/todo.md
git add docs/tmp/todo.md
git commit -m "docs: outline frps control decomposition plan"
```
