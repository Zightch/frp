# `frps/internal/control` v2 详细设计稿

更新时间：2026-04-27

本文是当前这一轮 `control v2` 重构的执行设计稿，只服务当前重构，不承担旧实现兼容义务。

如果本文与现有 `docs/project-overview.md`、`docs/frps-frpc-current-architecture.md` 或 `docs/frps/*` 中基于旧实现的描述冲突，以本文作为后续实现基线；等 `control v2` 落地并替换旧实现后，再把稳定结果同步回正式文档。

## 1. 目标

本轮重构只解决一件事：

- 用一套显式状态、单写者 ownership 和统一 reconcile 的 `control v2`，整体替换当前 `frps/internal/control` 的共享锁拼装模型。

当前重构不是“小修小补”，而是重写下面这条主链路：

```text
frpc 连接接入
-> transport 协商
-> frpc 登录
-> 配置同步
-> listener 启停
-> TCP/UDP 数据面桥接
-> 管理面刷新 / 本机网络变化 / listener 恢复
```

## 2. 当前问题

现有 `control/` 的主要问题不是“功能不够”，而是状态和资源边界分散：

- `Server` 同时持有接入、连接、challenge、runtime scan、active session 索引、TLS runtime。
- `sessionState` 同时持有配置状态、listener、TCP stream、UDP session、runtime generation 和写锁。
- 管理面刷新、后台 `runtime scan` 和连接读循环会从不同 goroutine 推进同一个 session。
- listener 启停、冲突裁决和恢复逻辑分散在 `listeners.go`、`refresh.go`、`runtime_scan.go`、`runtime_coordinator.go`。
- 当前真实状态依赖 `pending/current`、`frozen`、`listeners.started`、`generation`、多个 map 和 `recoveryMode` 的组合，不够显式。

结果是：

- 资源所有权难一眼看清。
- 恢复路径分叉太多。
- 需要大量场景测试才能兜住并发边界。
- 后续扩展控制协议、session takeover、局部恢复时，心智负担会继续上升。

## 3. 新架构总图

`control v2` 固定拆成 5 个核心部件：

```text
Server
  |
  | 接入 / 编解码 / 帧收发
  v
Supervisor
  |
  | 为每个 group 路由事件
  v
SessionAgent (one group -> one agent)
  |
  | 产生 action
  +--> BindManager
  +--> RuntimeRepo
  +--> ControlConnWriter

RuntimeRepo
  |
  +--> DB / 证书绑定 / 运行时快照投影

BindManager
  |
  +--> TCP listener / UDP listener / 端口 claim / 冲突裁决
```

### 3.1 `Server`

职责只保留：

- 控制端口监听
- 每条 `frpc` 控制连接的 transport 协商
- 登录期 `auth.begin/challenge/finish`
- 帧编解码
- 把连接事件和消息事件投递给 `Supervisor`

明确不再负责：

- 直接修改 active session 运行态
- listener 启停决策
- 恢复决策
- 运行态扫描补洞

### 3.2 `Supervisor`

`Supervisor` 是 `control v2` 的全局协调器，职责固定为：

- 维护 `groupID -> SessionAgent`
- 维护 `sessionID -> SessionAgent`
- 接收下面几类上游事件：
  - 新登录成功
  - 已登录连接收到控制帧
  - 管理面配置刷新
  - 本机网络快照变化
  - listener / runtime 异常回调
  - 进程 shutdown
- 保证同一 group 同时只有一个权威 `SessionAgent`
- 决定“新登录替换旧会话”还是“旧会话保持”

`Supervisor` 不做细粒度协议业务和 listener I/O。

### 3.3 `SessionAgent`

`SessionAgent` 是单个 group 的唯一状态写入者：

- 一个 group 最多只有一个在线 `SessionAgent`
- 该 group 的配置状态、runtime 状态、stream、udp session 全由同一个 goroutine 串行推进
- 所有外部输入都先变成 event 进入 agent inbox
- agent 内部通过 `Reduce(state, event) -> actions` 更新状态
- 所有 side effect 通过 action 执行，执行结果再以 event 形式回流 agent

`SessionAgent` 是整个新架构的权威核心。

### 3.4 `BindManager`

`BindManager` 是 listener/端口的唯一拥有者：

- 持有所有当前已经 claim 的监听地址
- 统一创建/关闭 TCP listener 和 UDP listener
- 统一做端口冲突裁决
- 给 session 回报 bind success / bind failed / listener closed

这会直接替代当前：

- `listeners.go` 内部的运行时冲突推断
- `runtime scan` 通过快照反推“谁还没监听起来”
- `activeRuntimeGroups` 用于冲突探测的跨 session 扫描

### 3.5 `RuntimeRepo`

`RuntimeRepo` 只负责把持久化配置投影成不可变运行时快照：

- `LoadDesiredRuntimeByClientID`
- `LoadDesiredRuntimeByGroupID`
- `ListDesiredRuntimes`

输出模型只服务 `control v2`：

- `DesiredGroupRuntime`
- `DesiredTunnelRuntime`
- `DesiredRuntimeSnapshot`

不再让旧 `protocol.TunnelEntry` 同时承载数据库语义、服务端运行时语义和客户端下发语义。

## 4. Ownership 规则

这是 `control v2` 的最高约束。

### 4.1 状态所有权

- `Server` 只拥有未登录连接和帧 I/O 生命周期。
- `Supervisor` 只拥有 agent 注册表和全局会话替换策略。
- `SessionAgent` 只拥有某个 group 的运行时状态。
- `BindManager` 只拥有 listener/claim 生命周期。
- `RuntimeRepo` 不拥有可变状态，只提供快照读取。

### 4.2 修改权

下面这些对象只能被单一 owner 修改：

- group 级配置状态：只允许 `SessionAgent`
- stream / udp session map：只允许 `SessionAgent`
- listener handle / bind claim：只允许 `BindManager`
- active agent registry：只允许 `Supervisor`

### 4.3 side effect 规则

状态转移和 side effect 必须分离：

- reducer 内不做 I/O
- reducer 不直接关连接、不直接起 listener、不直接发帧
- reducer 只产生 action
- action 执行后回流结果 event

这条规则用于避免旧模型里“逻辑状态推进”和“资源操作”缠在一起。

## 5. 运行态模型

`SessionAgent` 的权威状态如下：

```go
type SessionPhase uint8

const (
    SessionPhaseHandshaking SessionPhase = iota + 1
    SessionPhaseSyncingConfig
    SessionPhaseOnline
    SessionPhaseDraining
    SessionPhaseClosed
)

type RuntimePhase uint8

const (
    RuntimePhaseEmpty RuntimePhase = iota + 1
    RuntimePhaseBinding
    RuntimePhaseActive
    RuntimePhaseBlocked
    RuntimePhaseRecovering
)

type BlockReason uint8

const (
    BlockReasonNone BlockReason = iota
    BlockReasonEffectiveIPInvalid
    BlockReasonEffectiveIPNotLocal
    BlockReasonPortConflict
    BlockReasonListenerStartFailed
    BlockReasonSessionReplaced
    BlockReasonShutdown
)
```

完整状态对象：

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

    ConnState ControlConnState
    Epoch     uint64

    Bindings    map[BindingKey]BindingState
    Streams     map[uint32]TCPStreamState
    UDPSessions map[uint32]UDPSessionState
}
```

### 5.1 配置状态

配置同步不再拆成 `current/pending/acked/recoveryMode` 多段散布字段，而是明确成：

- `Desired`
  - 最新希望达到的完整配置
- `Pending`
  - 已经推给 `frpc`、等待 `config.ack` 的配置
- `Applied`
  - 已经被 `frpc` 确认、且当前服务端数据面应以其为准的配置

规则固定为：

- 一个 session 同时最多只有一个 `Pending`
- `Applied` 只能由 `ConfigAckAccepted` 事件推进
- `Desired` 可以先变化多次，reconcile 决定是否要生成新的 `Pending`

### 5.2 epoch

保留旧实现里 `generation/configVersion` 的优点，但提升成显式 epoch：

- 每次 `Applied` 变化都会推进 `Epoch`
- 所有 listener、stream、udp session 都带上创建时 epoch
- 来自旧 epoch 的运行时回包或 listener 结果，统一丢弃或转换成 no-op

这样仍能守住“旧配置资源不继续写当前控制连接”的约束，但不再散在多个锁之间。

## 6. 事件模型

`SessionAgent` 只吃事件，不直接被外部函数改状态。

```go
type Event interface {
    sessionEvent()
}
```

### 6.1 上游事件

- `SessionAttached`
  - 新登录成功，agent 绑定连接
- `DesiredRuntimeUpdated`
  - 管理面改配置后，Supervisor 把最新快照送入 agent
- `NetworkSnapshotChanged`
  - 本机 IP 快照变化
- `SessionTakeoverRequested`
  - 新登录请求接管旧 session
- `ShutdownRequested`

### 6.2 协议事件

- `ConfigAckReceived`
- `HeartbeatPingReceived`
- `StreamOpenedReceived`
- `StreamDataReceived`
- `StreamClosedReceived`
- `UDPDataReceived`
- `UDPCloseReceived`
- `ControlConnClosed`
- `ProtocolErrorDetected`

### 6.3 运行时结果事件

- `BindingsPrepared`
- `BindingStarted`
- `BindingStartFailed`
- `BindingClosed`
- `TCPAccepted`
- `TCPReadFailed`
- `UDPDatagramReceived`
- `UDPIdleTimeoutReached`

### 6.4 内部控制事件

- `ReconcileRequested`
- `PendingConfigExpired`
- `DrainCompleted`

## 7. 动作模型

reducer 输出 action，执行器只负责执行，不做业务判断。

```go
type Action interface {
    sessionAction()
}
```

### 7.1 连接与协议动作

- `ActionSendServerHello`
- `ActionPushConfig`
- `ActionSendConfigError`
- `ActionSendHeartbeatPong`
- `ActionSendStreamOpen`
- `ActionSendStreamClose`
- `ActionSendUDPStart`
- `ActionSendUDPData`
- `ActionSendUDPClose`
- `ActionCloseControlConn`

### 7.2 runtime 动作

- `ActionPrepareBindings`
- `ActionStartBindings`
- `ActionStopBindings`
- `ActionDrainStreams`
- `ActionDrainUDPSessions`
- `ActionResetRuntime`

### 7.3 调度动作

- `ActionRequestReconcile`
- `ActionPublishObservation`
- `ActionLogTransition`

## 8. reducer 规则

reducer 只做三件事：

1. 校验当前状态是否允许该 event。
2. 生成新状态。
3. 返回动作列表。

所有 reducer 必须满足：

- 幂等：重复 event 不造成额外资源泄漏
- 单向：无 side effect
- 可测试：纯输入输出即可断言

### 8.1 首次上线

```text
SessionAttached
-> state.Phase = SessionPhaseSyncingConfig
-> ActionSendServerHello
-> ActionPushConfig(initial desired runtime)
```

### 8.2 配置确认

```text
ConfigAckReceived
-> Pending -> Applied
-> Epoch++
-> if Applied empty: RuntimePhaseEmpty
-> else: RuntimePhaseBinding
-> ActionPrepareBindings
```

### 8.3 listener 启动成功

```text
BindingStarted(all required ports satisfied)
-> RuntimePhaseActive
```

### 8.4 阻塞

```text
BindingStartFailed(conflict / invalid effective_ip / not local)
-> RuntimePhaseBlocked
-> BlockReason = ...
```

### 8.5 恢复

```text
DesiredRuntimeUpdated / NetworkSnapshotChanged / BindingClosed
-> ActionRequestReconcile
```

### 8.6 takeover

```text
SessionTakeoverRequested
-> old session: SessionPhaseDraining + BlockReasonSessionReplaced
-> drain runtime
-> close conn
-> SessionPhaseClosed
```

## 9. reconcile 规则

`Reconcile` 是 `control v2` 的核心，不再让管理面刷新、网络变化、scan 恢复分别走各自逻辑。

统一入口：

```go
func Reconcile(state SessionState) []Action
```

固定处理顺序：

1. 如果 session 已关闭，返回空。
2. 如果控制连接不可用，转 `Draining/Closed`。
3. 计算 `Desired` 与 `Applied` 是否一致。
4. 如果不一致且当前没有 `Pending`，生成新的 `ActionPushConfig`。
5. 如果 `Applied` 为空配置，确保 runtime 已经完全收缩。
6. 如果 `Applied` 非空但 runtime 未满足，生成 `ActionPrepareBindings/ActionStartBindings`。
7. 如果 runtime 因网络/IP/冲突阻塞，但阻塞条件已消失，重新进入 `Binding/Recovering`。

### 9.1 reconcile 统一接收的触发源

- 登录完成
- `config.ack`
- 管理面刷新
- 本机网络快照变化
- listener 异常关闭
- drain 完成

也就是说，旧模型里的：

- `RefreshGroup`
- `runtime scan`
- `ensureTunnelListeners`
- `rebindGroupRuntime`
- `push empty/full config`

在新模型里不再是散落命令，而是统一通过 `Reconcile` 计算动作。

## 10. Session Takeover 规则

旧模型当前是“一个 group 只能有 1 个在线 `frpc`，新连接登录直接被拒绝”。

`control v2` 改成：

- 新连接登录成功后默认接管旧 session
- 旧 session 收到 `SessionTakeoverRequested`
- 旧 session 进入 `Draining`
- 旧 session 主动关闭 stream / udp session / bindings
- drain 完成后关闭控制连接
- 新 session 成为该 group 的权威 agent

这样可以直接删掉旧实现里很多围绕 group slot 和竞争窗口的复杂补丁。

## 11. `BindManager`

`BindManager` 对外暴露的接口只保留：

```go
type BindManager interface {
    Prepare(ctx context.Context, req PrepareBindingsRequest) (PrepareBindingsResult, error)
    Start(ctx context.Context, req StartBindingsRequest) error
    Stop(ctx context.Context, req StopBindingsRequest) error
}
```

### 11.1 `Prepare`

职责：

- 校验 `effective_ip`
- 展开 tunnel 需要的全部远端端口
- 做端口冲突裁决
- 形成 binding plan

输出必须是确定性结果：

- 全部可启动
- 哪些端口冲突
- 哪些端口因 `effective_ip` 无效或非本机而阻塞

### 11.2 `Start`

职责：

- 以 binding plan 启动 TCP/UDP listener
- 把 accept/read 回调翻译成 agent event

### 11.3 `Stop`

职责：

- 按 epoch 或 session 范围关闭 listener
- 释放 claim

### 11.4 冲突裁决原则

端口冲突必须发生在 claim/start 前，而不是事后扫描：

- 同一个 `protocol + effective_ip + port` 只能有一个 owner
- range tunnel 只是多端口 claim 的批量形式
- `0.0.0.0` / `::` 与具体 IP 的冲突规则仍按现有 `ports` 逻辑裁决，但裁决入口统一移到 `BindManager`

## 12. TCP/UDP runtime

新模型里 TCP/UDP 仍分开，不做“万能 tunnel runtime”。

### 12.1 TCP

```text
listener accept
-> TCPAccepted event
-> reducer 生成 ActionSendStreamOpen
-> 收到 stream.opened(ok)
-> 标记 stream active
-> 后续 data 双向转发
-> 任一路径关闭 -> stream drain/close
```

### 12.2 UDP

```text
listener datagram
-> UDPDatagramReceived event
-> 若 session 不存在则创建
-> ActionSendUDPStart + ActionSendUDPData
-> frpc 回包 -> 写回公网客户端
-> idle timeout -> ActionSendUDPClose
```

### 12.3 runtime epoch 护栏

- TCP stream 和 UDP session 都带 `epoch`
- 如果控制面已经切到新 epoch，旧 runtime 结果事件进入 reducer 时直接判为 stale
- stale event 只能做资源释放，不能再回写控制连接

## 13. 协议与接入层

### 13.1 `Server` 内部读循环

新 `server.go` 只保留：

- accept
- `transport.client_hello`
- `auth.begin/challenge/finish`
- 读帧
- 把帧翻译成 typed event 发给 `Supervisor`

不再保留：

- 直接处理 `config.ack`
- 直接处理 `stream.*`
- 直接处理 `udp.*`
- 直接做 listener 启停

### 13.2 协议变更原则

本轮允许改 `frps <-> frpc` 协议，但只在下面两种场景改：

- 有助于让 `SessionAgent` 的状态更明确
- 有助于减少旧协议里“靠隐含语义补边界”的部分

否则，优先保持现有 wire protocol 不动，把收益集中在运行态模型重写上。

## 14. 观测模型

旧 `ObserveState` 的思路保留，但改成直接读取显式状态，而不是从散落字段拼接。

新的 session 观测重点：

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

这样测试可以直接断言：

- “当前是 blocked，因为 effective_ip_not_local”
- “当前是 syncing_config，pending version=xxx”
- “当前是 recovering，epoch=4”

## 15. 文件布局建议

建议直接新建 `frps/internal/controlv2/`：

```text
internal/controlv2/
  server.go
  supervisor.go
  repo.go
  observe.go

  session/
    agent.go
    state.go
    events.go
    actions.go
    reducer.go
    reconcile.go
    execute.go

  bind/
    manager.go
    claims.go
    tcp.go
    udp.go

  protocol/
    handshake.go
    dispatch.go
```

替换完成前：

- `app.App` 暂时改为可切换装配 `controlv2`
- 老 `control/` 不再继续加功能

替换完成后：

- 删除旧 `control/` 整个实现
- 删除旧的 runtime coordinator/scan/registry/refresh 拼装路径

## 16. 测试策略

### 16.1 reducer 单测

覆盖：

- 首次 `SessionAttached`
- `DesiredRuntimeUpdated`
- `ConfigAckReceived`
- block / recover
- takeover
- shutdown

### 16.2 reconcile 单测

覆盖：

- desired != applied
- applied empty
- runtime blocked but constraints recovered
- binding partial failure
- stale epoch event

### 16.3 actor 集成测试

覆盖：

- 登录 -> 首次配置 -> listener active
- 管理面热重载
- `effective_ip` 消失 -> empty runtime
- `effective_ip` 恢复 -> full runtime recover
- 新 session takeover 旧 session

### 16.4 端到端回归

至少保留：

- TCP single/range
- UDP single/range
- 管理面刷新
- 本机网络变化
- listener conflict

## 17. 实施顺序

### 第 1 步

- 建 `controlv2/session` 的状态、事件、动作、reducer、reconcile 骨架

### 第 2 步

- 建 `Supervisor` 和 agent registry

### 第 3 步

- 抽 `RuntimeRepo`

### 第 4 步

- 抽 `BindManager`

### 第 5 步

- 接入 `Server` 的登录和帧分发

### 第 6 步

- 接回 TCP bridge

### 第 7 步

- 接回 UDP bridge

### 第 8 步

- 接回管理面刷新和网络变化

### 第 9 步

- 切换 `app.App`

### 第 10 步

- 删除旧 `control/`

## 18. 明确不做的事

- 不引第三方状态机库
- 不引通用事件总线框架
- 不保留旧 `runtime scan` 作为主控制路径
- 不让多个 goroutine 继续共享修改一个 session 状态对象
- 不做“TCP/UDP 完全统一抽象”

## 19. 当前实现入口

基于本文，下一步代码落地从下面这几个最小文件开始：

- `frps/internal/controlv2/session/state.go`
- `frps/internal/controlv2/session/events.go`
- `frps/internal/controlv2/session/actions.go`
- `frps/internal/controlv2/session/reducer.go`
- `frps/internal/controlv2/session/reconcile.go`

这一步先把状态和转移模型写稳，再接 `Supervisor` 和 `BindManager`。
