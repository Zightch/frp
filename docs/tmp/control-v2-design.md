# `frps/internal/control` 最终统一结构设计稿

更新时间：2026-04-27

本文仍沿用历史文件名 `control-v2-design.md`，但目标已经明确调整为最终稳定结构：

- 最终只保留一个控制面包：`frps/internal/control/`
- `frps/internal/controlv2/` 仅允许作为迁移中间态存在，最终必须删除
- 不再接受“旧 `control` 继续做外壳，新 `controlv2` 继续做状态机”这种长期分叉

如果本文与现有 `docs/project-overview.md`、`docs/frps-frpc-current-architecture.md` 或 `docs/frps/*` 中基于旧实现的描述冲突，以本文作为后续实现基线；等统一结构落地后，再把稳定结果同步回正式文档。

## 1. 目标

本轮最终目标只解决一件事：

- 用一套显式状态、单写者 ownership、统一 reconcile 和单目录实现，整体替换当前 `frps/internal/control` 与 `frps/internal/controlv2` 的双轨模型。

重写范围覆盖完整主链路：

```text
frpc 连接接入
-> transport 协商
-> frpc 登录
-> 配置同步
-> listener 启停
-> TCP/UDP 数据面桥接
-> 管理面刷新 / 本机网络变化 / 运行态恢复 / 运行态观测
```

最终稳定态必须满足：

- 顶层只有一个 `control.Server`
- 同一个 group 只有一个权威 `SessionAgent`
- 同一个运行态资源只有一个 owner
- 控制面生命周期与数据面 fast path 分离
- 不再依赖旧 `sessionState`、`runtimeRegistry`、`runtimeView`、`runtimeIssueStore`、`runtime scan` 主恢复路径

## 2. 当前问题

当前问题已经不是“旧 `control` 有缺陷、新 `controlv2` 还没补完”，而是这两个目录的职责拆分方式本身不适合作为最终结构：

- `internal/control` 里仍然持有真实 listener、TCP/UDP bridge、TLS runtime、repo、runtime scan、观察投影。
- `internal/controlv2` 里只持有 session 状态机骨架和部分 supervisor/bind claim 逻辑。
- 新旧两侧之间还靠桥接层同步状态，导致真实状态既存在于旧 `sessionState`，也存在于新 `session.SessionState`。
- 旧目录继续承担 I/O 和资源生命周期，新目录继续承担部分决策，这会把 ownership 永久切裂。

如果继续保持分叉，后续所有功能都会重复落在两处：

- 一处维护显式状态
- 一处维护真实资源
- 中间再加一层投影或桥

这不是过渡复杂度，而是结构性复杂度。

## 3. 最终架构总图

最终稳定态固定拆成 6 个核心部件：

```text
App / API
   |
   v
control.Server
   |
   +--> Authenticator
   +--> Supervisor
   +--> Observer
   +--> TLSRuntime

Supervisor
   |
   +--> SessionAgent (one group -> one agent)
           |
           +--> RuntimeExecutor
                   |
                   +--> ControlConnWriter
                   +--> BindManager
                   +--> RuntimeRepo
                   +--> Timers / Retry Scheduler

BindManager
   |
   +--> TCP listener / UDP listener / claim / conflict / listener callback
```

这里最重要的边界是：

- `SessionAgent` 只拥有逻辑状态
- `RuntimeExecutor` 只拥有真实资源句柄
- `BindManager` 只拥有监听地址 claim 和 listener 生命周期

## 4. 最终目录结构

最终目录应当直接收成：

```text
internal/control/
  server.go
  supervisor.go
  observe.go
  auth.go
  wire.go
  tls.go
  runtime.go
  executor.go

  session/
    agent.go
    state.go
    events.go
    actions.go
    reducer.go
    reconcile.go

  bind/
    manager.go
    claims.go
    tcp.go
    udp.go
    tls.go

  repo/
    model.go
    auth.go
    runtime.go
    sql.go
```

最终必须删除：

- `internal/controlv2/`
- 旧 `internal/control` 中只服务双轨桥接的文件

不建议保留新的 `internal/controlv2/` 再做一次“包名迁移”。稳定态包名直接回归 `internal/control`。

## 5. 顶层对象职责

### 5.1 `control.Server`

`Server` 只保留顶层生命周期和对外接口：

- `Warmup(ctx)` 或同等语义的启动预热
- `ListenAndServe(ctx)`
- `Shutdown(ctx)`
- `RefreshGroup(groupID)`
- `ObserveState()`
- `TunnelRuntimeIssues()`
- `ConfigureControlTLS(binding)`
- `ClearControlTLS()`

`Server` 允许持有：

- 控制端口 listener
- 未登录连接集合
- TLS 入口证书
- `Authenticator`
- `Supervisor`
- `Observer`

`Server` 不再允许直接持有：

- 某个 group 的配置状态
- 某个 group 的 stream / udp session map
- listener handle map
- runtime issue store
- runtime scan 决策状态

### 5.2 `Authenticator`

`Authenticator` 负责：

- `transport.client_hello`
- transport security 模式选择
- 必要时升级 control 连接到 TLS
- `auth.begin/challenge/finish`
- 认证期 repo 读取

输出必须收束为一个 bootstrap 结果：

```go
type AuthenticatedSessionBootstrap struct {
    GroupID     int64
    SessionID   uint64
    Desired     session.DesiredRuntimeSnapshot
    Conn        net.Conn
    ConnID      string
    RequestID   uint32
}
```

这样登录层不会再把旧 `GroupRuntime`、旧 `sessionState` 或 runtime 资源直接泄漏给后续链路。

### 5.3 `Supervisor`

`Supervisor` 是全局协调器，职责固定为：

- 维护 `groupID -> SessionAgent`
- 维护 `sessionID -> SessionAgent`
- 维护 `sessionID -> RuntimeExecutor`
- 决定 takeover 策略
- 处理 group refresh、网络变化、shutdown、listener 回调分发

`Supervisor` 不负责：

- 协议帧业务判断
- listener 启停细节
- TCP/UDP payload 转发

### 5.4 `SessionAgent`

`SessionAgent` 是单个 group 的唯一状态写入者：

- 同一个 group 只允许一个在线 agent
- 所有状态转移都通过 event 串行推进
- reducer 只改状态，不做 I/O
- reconcile 只根据状态计算动作

它只拥有逻辑状态，不拥有资源句柄。

### 5.5 `RuntimeExecutor`

`RuntimeExecutor` 是每个在线 session 的真实资源 owner，负责：

- 向 control connection 写 `server.hello/config.push/heartbeat/stream/udp` 帧
- 持有当前 epoch 的 TCP/UDP 运行时资源句柄
- 调用 `BindManager` 启停 listener
- 维护 stream open 后的 data pump
- 维护 UDP session 的 peer/runtime handle 映射
- 管理 idle timeout / retry timer

`RuntimeExecutor` 不做业务决策，只执行 action，并把结果回流成 event。

### 5.6 `BindManager`

`BindManager` 是监听地址和 listener 的唯一 owner，负责：

- `effective_ip` 校验
- 端口 claim / 冲突裁决
- TCP listener 启动与 accept 回调
- UDP listener 启动与 read 回调
- listener 关闭、异常、释放 claim

它直接替代旧实现里分散在：

- `listeners.go`
- `refresh.go`
- `runtime_scan.go`
- `runtime conflict` 推断路径

## 6. Ownership 规则

这是最终结构的最高约束。

### 6.1 状态所有权

- `Server` 只拥有进程级生命周期和未登录连接。
- `Supervisor` 只拥有 agent/executor 注册表和全局替换策略。
- `SessionAgent` 只拥有 group 级逻辑状态。
- `RuntimeExecutor` 只拥有当前在线 session 的真实资源句柄。
- `BindManager` 只拥有 listener/claim 生命周期。
- `Repo` 不拥有可变运行态，只提供快照读取。

### 6.2 修改权

只能由单一 owner 修改的对象：

- group 配置状态：只允许 `SessionAgent`
- stream / udp session 元数据：只允许 `SessionAgent`
- stream / udp session 真实句柄：只允许 `RuntimeExecutor`
- listener handle / bind claim：只允许 `BindManager`
- active agent/executor registry：只允许 `Supervisor`

### 6.3 side effect 规则

- reducer 内不做 I/O
- reducer 不直接起 listener、不直接关连接、不直接发帧
- reducer 只产生 action
- action 执行结果必须再回流成 event

## 7. repo 模型必须拆开

当前旧 `GroupRuntime` 同时承载：

- 认证信息
- transport security 策略
- desired runtime
- 观测语义

最终必须拆成至少两类读取模型：

```go
type AuthGroupRecord struct {
    GroupID                  int64
    Enabled                  bool
    ClientSecretHash         [32]byte
    ControlTransportSecurity string
}

type DesiredRuntimeRecord struct {
    GroupID   int64
    GroupName string
    Snapshot  session.DesiredRuntimeSnapshot
}
```

必要时再加：

- tunnel listen TLS 运行时读取模型
- 观测投影读取模型

稳定态不能再让一个 repo struct 同时适配登录、配置同步、扫描和 WebUI 观测。

## 8. Session 状态模型

权威逻辑状态仍按 actor 模型组织，但要做两点收口：

1. `SessionState` 只保存生命周期和元数据，不保存 payload 或真实 socket
2. `StreamDataReceived` / `UDPDataReceived` 这种 payload 级 event 不再作为稳定态核心模型

建议保留的核心状态：

```go
type SessionState struct {
    GroupID   int64
    SessionID uint64

    Phase        SessionPhase
    RuntimePhase RuntimePhase
    BlockReason  BlockReason

    Desired *DesiredRuntimeSnapshot
    Pending *PendingConfigPush
    Applied *AppliedRuntimeSnapshot

    Conn   ControlConnState
    Epoch  uint64

    Bindings    map[BindingKey]BindingState
    Streams     map[uint32]TCPStreamMeta
    UDPSessions map[uint32]UDPSessionMeta
}
```

其中：

- `Streams` 只保存 `streamID/tunnelID/remotePort/clientAddr/epoch/established`
- `UDPSessions` 只保存 `sessionID/tunnelID/remotePort/clientAddr/epoch/opened/idleTimeout`
- 真正的 `net.Conn`、`UDPListener`、timer handle、pump goroutine 都只在 `RuntimeExecutor`

### 8.1 `Desired / Pending / Applied`

规则保持单向明确：

- `Desired`：最新希望达到的配置
- `Pending`：已下发、等待 `config.ack` 的配置
- `Applied`：已确认、当前服务端 runtime 应以其为准的配置

固定约束：

- 同时最多一个 `Pending`
- `Applied` 只由 `ConfigAckReceived` 推进
- 每次 `Applied` 变化都推进 `Epoch`

### 8.2 `Epoch`

`Epoch` 是跨逻辑状态与真实资源的护栏：

- listener
- TCP stream
- UDP session
- timer / retry task

全部都带创建时 `epoch`。

来自旧 epoch 的结果只允许：

- 安静丢弃
- 资源释放

不允许：

- 回写 control connection
- 覆盖当前状态

## 9. 事件与动作模型

### 9.1 保留的核心事件

- `SessionAttached`
- `DesiredRuntimeUpdated`
- `NetworkSnapshotChanged`
- `SessionTakeoverRequested`
- `ShutdownRequested`
- `ConfigAckReceived`
- `HeartbeatPingReceived`
- `StreamOpenedReceived`
- `StreamClosedReceived`
- `UDPCloseReceived`
- `ControlConnClosed`
- `ProtocolErrorDetected`
- `BindingsPrepared`
- `BindingStarted`
- `BindingStartFailed`
- `BindingClosed`
- `TCPAccepted`
- `UDPPeerDiscovered`
- `UDPIdleTimeoutReached`
- `ReconcileRequested`
- `DrainCompleted`

### 9.2 需要从稳定模型中降级的 payload 事件

下面这些不应再作为稳定态 reducer 主输入：

- `StreamDataReceived`
- `UDPDataReceived`
- 每一个公网侧 TCP/UDP payload 事件

原因不是它们不能工作，而是：

- payload 事件量太大
- 它们不改变高层生命周期状态
- 把所有 payload 都串进 agent 会让状态机承担不必要的吞吐路径

稳定态里，actor 负责 lifecycle，payload 走 fast path。

### 9.3 保留的核心动作

- `ActionSendServerHello`
- `ActionPushConfig`
- `ActionSendConfigError`
- `ActionSendHeartbeatPong`
- `ActionSendStreamOpen`
- `ActionSendStreamClose`
- `ActionSendUDPOpen`
- `ActionSendUDPClose`
- `ActionPrepareBindings`
- `ActionStartBindings`
- `ActionStopBindings`
- `ActionDrainStreams`
- `ActionDrainUDPSessions`
- `ActionResetRuntime`
- `ActionScheduleRetry`
- `ActionCloseControlConn`
- `ActionRequestReconcile`
- `ActionLogTransition`

payload 级写动作也应降级成 executor fast path，不作为 reducer 的高频输出。

## 10. 数据面 fast path

这是最终结构和当前 `controlv2` 骨架最大的差异。

### 10.1 TCP

控制流：

```text
listener accept
-> TCPAccepted event
-> agent 分配 streamID/requestID
-> ActionSendStreamOpen
-> StreamOpenedReceived(ok)
-> executor 启动双向 data pump
```

一旦 `stream.opened(ok)` 完成：

- `publicConn -> frpc stream.data`
- `frpc stream.data -> publicConn`

都由 `RuntimeExecutor` 直接走 fast path，不逐 payload 过 reducer。

actor 只处理：

- open
- close
- stale epoch
- drain

### 10.2 UDP

控制流：

```text
listener datagram from new peer
-> UDPPeerDiscovered event
-> agent 分配 udp session id
-> ActionSendUDPOpen
-> executor 建立 peer handle
```

之后：

- 同一 peer 的 datagram 直接 fast path 写到 control connection
- `frpc -> udp.data` 直接按 session handle 回写公网

actor 只处理：

- 首次 peer/session 建立
- close
- idle timeout
- stale epoch

## 11. 恢复模型

最终结构里不再保留“全局 runtime scan 作为主恢复路径”。

恢复统一由下面几类事件触发：

- `DesiredRuntimeUpdated`
- `NetworkSnapshotChanged`
- `BindingClosed`
- `BindingStartFailed`
- `ControlConnClosed`
- retry timer 到期

也就是说，恢复逻辑改成：

- session 级状态判断
- reconcile 生成 retry / rebind / empty/full push
- executor 执行动作

### 11.1 startup gate 仍然保留，但语义改成 audit

虽然不再保留旧 `runtime scan` 主路径，但启动门闩仍需要保留，用于：

- 发布首次 runtime status
- 检查静态配置冲突
- 检查本机 `effective_ip` 可见性

因此旧 `EnsureInitialRuntimeScan()` 的最终语义应改成：

- `Warmup(ctx)` 或 `EnsureInitialAudit(ctx)`

它只负责启动前观测预热，不负责会话恢复和 listener 修补。

## 12. 观测模型

观测应直接从显式状态拼装，而不是从旧运行态字段反推。

### 12.1 观测输入源

- `Supervisor`：agent/executor 注册表
- `SessionAgent`：逻辑状态
- `RuntimeExecutor`：资源元数据快照
- `BindManager`：listener/claim 快照
- `Repo`：必要时补静态 desired runtime

### 12.2 观测输出重点

- `SessionPhase`
- `RuntimePhase`
- `BlockReason`
- `DesiredVersion`
- `PendingVersion`
- `AppliedVersion`
- `Epoch`
- `BindingCount`
- `ActiveStreams`
- `ActiveUDPSessions`

`TunnelRuntimeIssues()` 也不再由独立 store 维护，而是从：

- agent block state
- binding failure reason
- bind manager claim/conflict 信息

派生得到。

## 13. 旧实现去向

最终落地时，旧文件应该按下面方式吸收或删除：

- `server.go` / `auth.go` / `transport_security.go` / `tls_runtime.go`
  - 保留在顶层 `control/`，但缩成接入层和顶层 API
- `session.go` / `config.go` / `control_v2_runtime.go`
  - 拆入 `session/` 与 `executor.go`
- `listeners.go`
  - 拆入 `bind/` 与 `runtime.go`
- `tcp_bridge.go` / `udp.go` / `session_io.go`
  - 迁入 `executor.go` 与 `runtime.go`
- `runtime_registry.go` / `runtime_view.go` / `runtime_issue_store.go`
  - 删除，能力由 `Supervisor + Agent + Executor + Observer` 直接替代
- `runtime_scan.go`
  - 删除主恢复职责，只保留必要的 startup audit 能力，且改名
- `refresh.go`
  - 删除单独编排逻辑，统一改成 `Supervisor -> DesiredRuntimeUpdated -> Reconcile`

## 14. 测试结构

最终测试也应跟着结构重排：

### 14.1 `session/` 纯单测

覆盖：

- attach
- desired update
- config ack
- block / recover
- takeover
- shutdown
- stale epoch

### 14.2 `bind/` 单测

覆盖：

- claim / release
- `0.0.0.0` 与具体 IP 冲突
- TCP/UDP range 展开
- TLS listener 绑定

### 14.3 `executor` 集成测试

覆盖：

- hello/config/ack
- TCP stream open/close/data pump
- UDP session open/idle/data fast path
- listener start/stop/drain

### 14.4 顶层场景测试

覆盖：

- 登录 -> 首次配置 -> listener active
- group refresh
- network change
- takeover
- startup gate / audit
- control TLS

稳定性测试入口最终应从 `./internal/control` 继续跑，但包内结构将不再区分旧 `control` 与 `controlv2`。

## 15. 推荐实施顺序

### 第 1 步

- 先固定最终文档和目标目录，停止给旧 `control/` 与 `controlv2/` 双轨继续补功能

### 第 2 步

- 在 `internal/control/session/` 固定最终 `state/events/actions/reducer/reconcile`
- 同时删掉 payload 级 event/action 的稳定态依赖

### 第 3 步

- 新建 `executor.go` / `runtime.go`
- 把真实 control conn writer、stream handle、udp handle、timer、epoch resource map 收口进去

### 第 4 步

- 新建 `bind/` 真实 listener manager
- 接管 claim、冲突裁决、TCP/UDP listener 和 tunnel listen TLS

### 第 5 步

- 拆 `repo/` 为认证读取与 desired runtime 读取

### 第 6 步

- 把 `Supervisor` 和顶层 `Server` 切到新结构

### 第 7 步

- 删除 `internal/controlv2/`
- 删除旧 `runtimeRegistry/runtimeView/runtimeIssueStore/runtime scan` 主路径

## 16. 明确不做的事

- 不保留 `internal/control` 与 `internal/controlv2` 长期并存
- 不让旧 `sessionState` 和新 `SessionState` 再双写
- 不把每个 TCP/UDP payload 都送进 reducer
- 不保留全局 `runtime scan` 作为主恢复控制流
- 不引第三方状态机或事件总线框架

## 17. 最终判定标准

只有满足下面条件，才算真正完成“去分叉”：

- 仓库里只剩一个控制面实现目录：`frps/internal/control/`
- `app`、`api`、测试和稳定性入口都不再 import 或依赖 `internal/controlv2`
- `ObserveState()` 不再依赖旧 runtime view 混合投影
- `TunnelRuntimeIssues()` 不再依赖独立 store
- TCP/UDP bridge 不再依赖旧 `sessionState`
- `runtime scan` 不再驱动在线恢复

在达到这些条件之前，都只能算迁移中间态，不能算稳定结构。
