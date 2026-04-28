# frps 控制面技术设计

## 拆分目标边界

当前控制面后续只保留一个稳定入口：`frps/internal/control`。这个根包面向
`internal/app` 和 `internal/api`，职责应收敛到 facade：`Options`、`Server`、
`NewServer`、生命周期方法、运行态刷新、观测查询，以及控制面 TLS 配置入口。

`frps/internal/controlv2` 只能作为迁移中间态存在，不再承载新功能。新增控制面能力
必须回到 `control` 这条主线，通过子包逐层拆分，而不是继续形成双轨实现。

目标包边界如下：

- `control/protocol/*`：frpc 控制连接的 frame IO、错误回复、transport/auth handshake、dispatch、config sync、heartbeat、stream 和 UDP frame 语义。
- `control/session/*`：纯会话状态机、agent、supervisor、action executor seam。
- `control/runtime/*`：具体运行态状态、listener 启停、TCP/UDP 数据面、runtime scan、recovery、conflict、issues、observe projection。
- `control/domain/*`：`GroupRuntime`、`ConfigSnapshot`、desired/applied snapshot 转换、tunnel helper 和版本语义。
- `control/repo/*`：SQL 查询、row decode、持久化配置到 domain runtime model 的投影。
- `control/wiring/*`：把 repo、protocol、session、runtime、observer、scanner 拼成最终业务对象。

根包后续不应直接实现 frame handler、listener serve loop、runtime scan 算法或
recovery 策略。发现循环依赖时，优先下沉 domain model 或提取更小 capability，
不要用 `any` 或 placeholder type 扩大边界。

## RuntimeOperator 冻结规则

`control/runtime.RuntimeOperator` 当前仍是迁移期聚合接口，用来承接既有 scan、
recovery、listener 和数据面调用。该接口不再扩张；新增 runtime 依赖必须先落到小
能力接口，再逐步改造调用点。

当前第一批 compile-only seam 包括：

- `RuntimeReadiness`
- `RuntimeIssueWriter`
- `RuntimeIPResolver`
- `ListenerStarter`
- `RuntimeSessionRegistry`
- `RuntimeRecoveryCoordinator`
- `RuntimeScanCoordinator`
- `RuntimeScannerDeps`
- `RuntimeServeDeps`

这些 seam 先只建立编译边界，不改变现有业务路径。后续迁移时，scan 只依赖
`RuntimeScannerDeps`，TCP/UDP 数据面只依赖 `RuntimeServeDeps`，listener start/probe
拆到独立 runtime listener 能力，最终删除 `RuntimeOperator`。

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

当前 `ConfigSnapshot` 由仓储层投影生成：

- `Version`
- `GeneratedAtMs`
- `Tunnels`

使用边界：

- 首次登录与在线热重载复用同一快照路径
- `config.ack` 成功后才启动或重启 listener
- 管理面写库成功且命中运行态字段时，如果分组在线，会触发整组补推

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

- 运行态问题记录在 `runtime issue store`
- 在线 session、group slot、listener 绑定状态集中在 `runtime registry`
- 轮询恢复和 `RefreshGroup()` 共用同一套恢复语义

## 当前 TLS 边界

当前 `frpc` 登录传输已支持分组级 TLS 策略。

- `transport.client_hello` 必须前置携带 `client_id`
- `frps` 先按分组的 `control_transport_security` 决定返回明文还是 TLS
- 分组要求 TLS 时，TLS 成功后才进入现有 `auth.begin` 链路
- 服务端证书来源于 `certificate_asset_usages.frpc_tls`

相关设计见 [../design/certificate-binding.md](../design/certificate-binding.md)。
