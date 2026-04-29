# frps 控制面技术设计

## 当前包边界

当前控制面只保留一个稳定入口：`frps/internal/control`。这个根包面向
`internal/app` 和 `internal/api`，职责已经收敛到 facade：`Options`、`Server`、
`NewServer`、生命周期方法、运行态刷新、观测查询，以及控制面 TLS 配置入口都通过
type alias / wrapper 暴露。

`frps/internal/controlv2` 已经删除。新增控制面能力必须回到 `control` 这条主线，
通过子包逐层拆分，而不是继续形成双轨实现。

当前包边界如下：

- `control/protocol/*`：frpc 控制连接的 frame IO、错误回复、transport/auth handshake、dispatch、config sync、heartbeat、stream 和 UDP frame 语义。
- `control/session/*`：纯会话状态机、agent、supervisor 和 action executor 接口。
- `control/runtime/*`：具体运行态状态、listener 启停、TCP/UDP 数据面、runtime scan、recovery、conflict、issues 和观测投影。
- `control/domain/*`：`GroupRuntime`、`ConfigSnapshot`、desired/applied snapshot 转换、tunnel helper 和版本语义。
- `control/repo/*`：SQL 查询、row decode、持久化配置到 domain runtime model 的投影。
- `control/wiring/*`：业务拼装层，组合 repo、protocol、session、runtime、observer、scanner，并承载白盒场景测试。
- 根 `control`：只保留 app/api 稳定入口和兼容 alias，不承载业务拼装实现。

根包后续不应直接实现 frame handler、listener serve loop、runtime scan 算法或
recovery 策略。现在根包只有 `facade.go`，实际协议、session、runtime 和拼装逻辑由子包承载。
发现循环依赖时，优先下沉 domain model 或提取更小 capability，不用 `any` 或 placeholder type 扩大边界。

## 状态所有权

当前控制面只有一条会话事实链路：

- `control/runtime/state.ConcreteSessionState` 持有单个 session 的控制状态、desired/applied/pending 配置、恢复模式、listener、TCP stream 和 UDP session。
- `control/session` 只表达纯状态机、事件和 agent 执行，不保存另一份运行态副本。
- `control/session/supervisor` 负责 session agent、group slot、session 注册和事件分发，不再维护独立的 `stateBySession` 投影缓存。
- `control/wiring/sessionState` 只是拼装层薄包装，用于把 `ConcreteSessionState` 接到连接、写锁、frame IO 和 runtime executor。
- 读侧投影统一从状态所有者生成：`ProjectedSessionState` 给 session/supervisor 使用，`RuntimeObservedConfig` 给 runtime snapshot/index 使用。

因此，新增状态字段时应先判断它属于控制状态、配置状态还是具体运行态资源；除非只是适配外部接口，否则不应在 `wiring`、`supervisor` 或 observe 层再建平行字段。

## Runtime Capability 边界

`control/runtime.RuntimeOperator` 聚合接口已经删除。scan、recovery、listener start
和数据面现在通过小能力接口接入 `control/wiring` 拼装层；新增 runtime 依赖必须继续落到小 capability，
不能重新引入跨扫描、恢复、listener、serve、repo、scheduler 的大接口。

当前主要 capability 包括：

- `RuntimeReadiness`
- `RuntimeIssueWriter`
- `RuntimeIPResolver`
- `ListenerStarter`
- `RuntimeListenerProbe`
- `RuntimeActiveSessionFinder`
- `RuntimeActiveGroupProvider`
- `RuntimeSnapshotProvider`
- `RuntimeSessionStateProvider`
- `RuntimeSessionEventDispatcher`
- `RuntimeAuditedRecoveryRequester`
- `RuntimeDesiredUpdater`
- `RuntimeExecutorProvider`
- `RuntimeScannedSessionRecoveryDeps`
- `RuntimeAuditedSessionRecoveryDeps`
- `RuntimeScanCoordinator`
- `RuntimeRepositoryProvider`
- `RuntimeLoggerProvider`
- `RuntimeSchedulerProvider`
- `RuntimeClockProvider`
- `RuntimeServeContextFactory`
- `RuntimeDataPlaneServicer`
- `RuntimeStartPlannerDeps`
- `RuntimeStartApplyDeps`
- `RuntimeScannerDeps`
- `RuntimeStartDeps`
- `RuntimeServeDeps`

这些 capability 已经成为实际业务边界：scan 只依赖 `RuntimeScannerDeps`，listener start
通过 `RuntimeStartDeps` 计划和应用，TCP/UDP 数据面只依赖 `RuntimeServeDeps`，
audited recovery 只依赖 session registry / executor provider 能力。

## Protocol Frame IO

控制连接的帧读写细节已经下沉到 `control/protocol/frameio`：

- 构造服务端 `transport.FrameContext`
- 按 timeout 读取并解析 `protocol.Frame`
- marshal 并写出 `protocol.Frame`
- 通过 `frameio.Writer` 给上层提供最小写帧接口

`control/wiring` 保留 `readFrame` / `writeFrame` 等适配方法来连接握手、auth、
session loop 和 runtime IO。session 写锁仍在 wiring 的 session 适配层持有，锁内
只委托 `frameio.Writer` 写帧；runtime IO 仍先检查 config version 和 runtime 锁，
再委托同一个 writer。

## Protocol Errors

协议错误回复和连接关闭原因映射下沉到 `control/protocol/errors`：

- `ReplyProtocolError`
- `ReplyError`
- `WriteError`
- `ConnectionDetails`
- `IsExpectedConnectionClose`
- `ConnectionReason`

该包只依赖最小 `FrameWriter` 接口，不依赖 `Server` 或 `sessionState`。错误
回复可以复用 control frame 写锁，也能在后续 handshake/auth/dispatch 子包中直接使用。

## 传输层

- `pkg/transport` 负责长度前缀收发
- `pkg/protocol` 负责业务帧解析
- 默认读写超时 `5s`
- challenge TTL `30s`
- heartbeat 间隔 `15s`

会话读超时当前按下面规则计算：

```text
max(3 * heartbeat_interval, minimum_read_timeout)
```

## 当前登录握手

当前 `frpc` 登录口（默认端口 `7000`）已经固定为“两段握手”：

```text
frpc -> transport.client_hello
frps -> transport.server_hello
if selected_security_mode == tls:
    TLS handshake
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
frps -> start listeners
```

服务端登录阶段当前会做：

1. 读取 `transport.client_hello`
2. 按 `client_id` 读取 `GroupRuntime`
3. 按分组 `control_transport_security` 决定 `plain` 还是 `tls`
4. 如需 TLS，则在同一 TCP 连接上升级到 TLS
5. 读取 `auth.begin`，并校验其中 `client_id` 与 hello 一致
6. 签发 challenge
7. 常量时间校验 challenge 响应
8. 抢占单分组单客户端槽位
9. 返回 `server.hello`
10. 下发首次 `config.push`

## 首轮扫描门闩

当前控制面在真正打开 `frpc` 登录监听口（默认端口 `7000`）前，已经先执行过一次 `EnsureInitialRuntimeScan()`。

这会保证：

- 首次外显的隧道状态已经过 runtime scan
- 首次 `frpc` 登录不会早于首轮扫描
- 管理面与控制面共用同一轮启动门闩

## `ConfigSnapshot`

当前 `ConfigSnapshot` 定义在 `control/domain/runtime`，由仓储层投影生成：

- `Version`
- `GeneratedAtMs`
- `Tunnels`

使用边界：

- 首次登录与在线热重载复用同一快照路径
- `config.ack` 成功后才启动或重启 listener
- 管理面写库成功且命中运行态字段时，如果分组在线，会触发整组补推

`repo` 只负责把 SQL row decode 为 domain runtime model；根 `control` 通过
facade 暴露 app/api 需要的稳定 API，`wiring` 通过 alias 连接 repo、bind 和 runtime。
持久化模型、wire snapshot 和 runtime state 的转换不应回流到 app/api 层。

## 在线热重载

当前固定采用：

- 整组冻结
- 整组完整快照下发
- `ack` 后整组 listener 重建

关键规则：

- 同一分组同一时刻只允许一个未确认的在线配置更新
- 如果上一版 `config.push` 仍未确认，又来了下一版变更，直接断开当前控制连接
- `config.ack` 超时或失败，也直接断开连接，交给 `frpc` 重连

## 运行态恢复

当前未监听轮询与刷新路径已经收口为统一恢复编排：

- 运行态问题记录在 `control/runtime.IssueStore`
- 在线 session 和 group slot 由 `control/session/supervisor` 管理
- listener、TCP stream、UDP session 和恢复模式由 `ConcreteSessionState` 管理
- 轮询恢复和 `RefreshGroup()` 共用同一套恢复语义

## 当前 TLS 边界

当前 `frpc` 登录传输已支持分组级 TLS 策略。

- `transport.client_hello` 必须前置携带 `client_id`
- `frps` 先按分组的 `control_transport_security` 决定返回明文还是 TLS
- 分组要求 TLS 时，TLS 成功后才进入现有 `auth.begin` 链路
- 服务端证书来源于 `certificate_asset_usages.frpc_tls`

相关设计见 [../design/certificate-binding.md](../design/certificate-binding.md)。
