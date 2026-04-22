# frps 可测试基建文档

## 1. 适用范围

对于涉及状态机、并发、热更新、轮询恢复、会话切换和故障注入的测试，统一按本文档执行。

- 先做可测性基建，再做具体场景。
- 先把关键竞态点变成“可阻塞、可放行、可注入、可重复”，再写故障测试、竞争态测试和压力测试。
- 后续 `config.push` / `config.ack` / heartbeat / shutdown 等控制连接故障注入、状态观测、不变量断言和场景编排，也继续追加在本文档，不再回填到 [testing-debugging.md](./testing-debugging.md)。

## 2. 通用做法

测试前默认先准备以下能力：

- 关键路径测试钩子：首轮扫描完成、控制端口开放、管理 API 首次可见、`config.push`、`pending config`、`config.ack`、listener freeze/close/bind、runtime scan、session shutdown、本机网络快照刷新等位置要能被测试明确卡住和放行。
- 确定性并发编排：竞争态测试默认使用 barrier/hook 编排交错顺序，不以 `sleep` 或“多跑几次”作为主手段。
- 可控时钟：轮询周期、超时、重试、心跳等等待逻辑优先用 fake clock / manual ticker 驱动。
- 可控注入：listener/bind、网络快照、传输层、session 行为要能脚本化制造占用、释放、断线、重连、重复 ack、晚到 ack、乱序 ack、地址抖动等场景。
- 不变量断言：优先断言状态机结果；对当前端口冲突与热更新链路，至少包括“首轮扫描完成前不可登录”“任一时刻最多一个有效 pending config”“旧 session 晚到消息不能污染新 session”“静态 `冲突` 优先于 runtime `异常`”“空配置恢复必须先补推完整快照再恢复 listener”。

测试时禁止把以下方式当作主验证：

- 依赖 CPU 快慢或调度偶然性触发竞态。
- 依赖长时间 `sleep` 窗口碰运气。
- 只看日志，不校验状态机不变量。

在可测性基建完成前，`-race`、高频扰动、多 `GOMAXPROCS`、长稳 soak 只作为补充捞漏手段，不作为通过依据。

## 3. 测试钩子骨架

测试钩子统一按“业务代码只打点，测试代码才装控制器”的方式设计：

- `frps` / `frpc` 各自提供同形态的 `internal/testhooks` 包，业务路径只允许调用稳定的打点函数，例如 `testhooks.Point(...)`，不允许在业务代码里直接操作 channel、`testing.T` 或测试专用状态。
- 默认构建使用 `//go:build !testhooks` 的空实现；该实现只保留可内联的 no-op 打点函数，不提供注册、等待、放行、枚举命中点等测试控制 API，保证生产构建默认零开销且不可误用。
- 需要确定性编排的测试统一使用 `go test -tags testhooks`；只有在 `//go:build testhooks` 下才编译真实控制器、barrier、命中记录和测试 helper。
- hook 点命名必须稳定，按“模块.路径.阶段”组织，例如 `startup.initial_scan.after_full_scan`、`control.config_push.before_write`、`control.config_ack.after_accept`、`session.listener.before_bind`；测试依赖 hook 名，不直接依赖具体函数名和行号。
- hook payload 只允许传不可变快照字段，例如 `groupID`、`tunnelID`、`sessionID`、`requestID`、`configVersion`、`listenerKey`、`scanRound`；禁止把内部 map、slice 指针或需额外加锁的运行态对象直接暴露给 hook，避免新引入数据竞争。

barrier 语义统一为“双边握手 + 明确放行”：

- 代码命中 hook 时先向控制器登记一次 `hit`，记录 point、序号和 payload。
- 若当前 point 没有注册 barrier，则立即返回，不能改变业务时序。
- 若当前 point 注册了 barrier，则在登记命中后阻塞，直到测试显式 `Release()`；测试侧必须先收到“已到达”信号，再决定是否触发另一条并发路径和何时放行。
- 同一 hook 点必须支持按第 `N` 次命中区分，避免轮询、多 session 或重试场景只能“卡住第一次”。
- 若测试结束时仍有未放行 barrier、未消费命中或出现未声明的关键命中，控制器应直接让测试失败，不能静默吞掉。

第一版作用域按“进程级单活控制器”收口：

- hook 控制器不沿业务函数签名层层传递，避免把测试依赖扩散到生产接口；第一版直接用进程级单活 controller，由测试在 `Setup` 时安装，在 `Cleanup` 时卸载。
- 使用该基建的单测默认不跑 `t.Parallel()`；需要多套并发剧本同时执行时，改走子进程级 e2e/harness，而不是在同一进程里并行安装两套 hook 控制器。
- `frps` 与 `frpc` 的 hook 控制器 API 保持同形态，后续双端协同场景由上层场景编排器统一装配，不要求两端先共享一个包。

当前这层 hook 只解决“可卡住、可观测、可放行”，不承担故障模拟：

- 时间推进、listener/bind 失败、网络快照跳变、传输层断线/乱序等故障，后续分别通过 fake clock、fake listener factory、fake snapshot provider、fake transport/session harness 注入。
- 这些 fake 组件与 hook 共享同一套场景编排顺序，但不把“返回错误”“替换依赖”直接塞进通用 hook API，避免一个入口同时承担阻塞、观测和故障注入三种职责。

当前必须优先落 hook 的边界点如下：

- 首轮扫描开始、首轮扫描完成但尚未发布 `initialRuntimeScanDone`、管理 API 首次可见前、控制端口开放前后。
- 首次登录保留 group slot 后、`config.push` 写出前、客户端应用快照后但 `config.ack` 写出前、服务端接受 `config.ack` 后但 listener 启动前。
- `RefreshGroup()` 拿到活动 session 后、冻结旧 runtime 后、决定下发空配置或完整配置前。
- 未监听轮询决定“直接补 listener”还是“补推完整快照”前。
- listener `bind / attach / freeze / close` 前后、新 session 注册后旧 session 关闭前后、本机网络快照发布前后。

验收口径固定如下：

- 未带 `testhooks` tag 的 `go test` 和生产二进制不应暴露任何可驱动 barrier 的入口。
- 带 `testhooks` tag 的单测必须能在上述关键边界精确阻塞、读取命中 payload、驱动另一侧动作并显式放行。
- 所有后续竞争态测试默认优先复用这套 hook/barrier；若某场景仍需要 `sleep` 才能稳定复现，先补 hook/fake，再写场景。

## 4. 可控调度骨架

时间相关路径统一按“同一套 clock 负责时间值，同一套 scheduler 负责等待与周期驱动”收口，先把测试从 `sleep` 驱动改成手动推进：

- 业务代码不再直接调用 `time.Now`、`time.NewTimer`、`time.NewTicker`、`time.After`、`time.Sleep`；统一经由注入的 `clock / timer / ticker` 接口取时间和等待。
- 背景轮询与周期任务不再各自手写 `go func + for + select + ticker`；统一经由注入的 `scheduler` 启动，便于测试识别“任务已启动、正在等下一拍、这一拍已执行完”。
- 生产默认注入 real clock / real scheduler，只是薄封装标准库，不改变运行行为。
- 测试注入 manual clock / manual scheduler；测试只通过显式 `Advance(...)`、`WaitIdle()`、hook/barrier 放行来推进时序，不再用 `sleep` 给 goroutine“让路”。

第一版骨架建议固定为下列分层：

```go
type Clock interface {
    Now() time.Time
    NewTimer(d time.Duration) Timer
    NewTicker(d time.Duration) Ticker
}

type Timer interface {
    C() <-chan time.Time
    Stop() bool
    Reset(d time.Duration) bool
}

type Ticker interface {
    C() <-chan time.Time
    Stop()
    Reset(d time.Duration)
}

type Scheduler interface {
    Go(name string, fn func(context.Context))
    Every(ctx context.Context, name string, interval time.Duration, fn func(time.Time))
}
```

其中 `Scheduler` 不是为了重新发明 goroutine，而是为了把“后台任务生命周期”和“时间推进后的收敛点”固定下来：

- `Go` / `Every` 都带稳定名字，便于测试日志、hook 和后续场景编排器引用。
- `Every` 默认串行执行，同一个周期任务上一轮未返回时，不允许并发重入下一轮；跨过多个周期时最多积压一次待执行轮次，避免一口气补跑出多个重叠扫描。
- manual scheduler 必须额外提供测试控制面，例如 `Advance(...)`、`WaitIdle()`、`PendingJobs()` 或等价能力；这些 API 只给测试代码使用，不进入业务接口。

manual clock / scheduler 的时间语义固定如下：

- `Advance(d)` 只允许单调前进，不允许回拨；所有到期 timer / ticker / periodic job 按 `(dueAt, registerSeq)` 的稳定顺序变为 ready。
- 时间推进后，测试必须能显式等待“本次推进唤醒的任务都已执行到下一阻塞点或已返回”；否则仍然会退化成“先推进时间，再 `sleep` 一会儿”等待 goroutine 消化。
- 所有写入协议或运行时状态的时间戳都必须来自同一个 `clock.Now()`，包括 `config.generated_at`、`config.ack.applied_at`、heartbeat 时间戳、UDP `lastActive`、网络快照 `captured_at` 等，避免测试里一半是手动时间、一半是真实墙钟。
- 需要超时 `context` 的路径，禁止继续直接调用 `context.WithTimeout`；改为走 clock-aware helper 或 scheduler 包装，让测试可在手动时间下触发超时。

当前与端口冲突检查、热更新和会话恢复直接相关的第一批落点如下：

- `frps/internal/control/runtime_scan.go`：`startRuntimeIssuePolling()` 的轮询周期先切到 manual ticker / scheduler，这是当前“未监听 tunnel 轮询恢复”最直接的时间入口。
- `frps/internal/system/network_snapshot.go`：`poll()` 的本机快照刷新周期切到同一套 scheduler，后续 `effective_ip` 变化、非法地址恢复、地址抖动场景都依赖它可手动推进。
- `frpc/internal/client/client.go`：`Run()` 的重连 backoff 改成 manual timer，保证断线、踢下线、空配置保活后的重连测试不靠真实秒级等待。
- `frpc/internal/client/session.go`：`heartbeatLoop()` 的周期发送改成 manual ticker；`frpc/internal/client/client.go` 的 `config.ack.applied_at` 也改走同一时钟。
- `frps/internal/control/tcp_bridge.go`：等待 `stream.ready` 的打开超时改成 manual timer，保证“旧 session 晚到 / 新 session 接管 / stream open timeout”类场景可精确触发。
- `frps/internal/control/udp.go`：UDP idle sweep、`lastActive` 更新时间统一走同一时钟，后续空闲清理与重入竞争才能稳定复现。
- `frps/internal/control/repository.go`、`frps/internal/control/server.go`、`frps/internal/system/network_collect.go`：凡是会写入 `GeneratedAtMs`、heartbeat pong 时间戳、本机快照采集时间的地方，都要改为从注入时钟取值，避免版本号和状态戳在测试中漂移。

当前先不把所有“带 timeout 的标准库调用”都强塞进 manual clock：

- `frps/pkg/transport/transport.go` 的 `SetReadDeadline` / `SetWriteDeadline` 依赖底层 `net.Conn` 和内核时间；需要确定性测试时，应配套 fake conn / fake transport，而不是只改 `clock`。
- `frps/internal/control/config.go`、`frps/internal/app/app.go` 里的 `context.WithTimeout`、HTTP shutdown timeout 等可以作为第二批接入点；等 fake transport / fake server harness 到位后再一起收口，避免只改一半接口。

测试编排方式固定如下：

1. 先安装 hook / barrier 和 manual clock / scheduler。
2. 启动 `frps` / `frpc` 或目标子模块，确认后台任务已经注册并进入等待点。
3. 用 `Advance(...)` 推进一个明确时间步，例如一个轮询周期、一次 heartbeat 间隔、一次 backoff。
4. 用 `WaitIdle()`、hook 命中确认或显式状态断言收敛本次推进结果。
5. 若还需要交错另一条并发路径，先等 hook 到达，再继续 `Advance(...)` 或 `Release()`；整个过程中不写 `sleep`。

验收口径固定如下：

- 端口冲突检查、未监听 tunnel 轮询恢复、首轮扫描后放行登录、`effective_ip` 变化恢复、会话 heartbeat / backoff 这几类路径，后续测试都应能在 manual 时间下推进，不再依赖真实秒级等待。
- 如果某个场景仍然必须依赖真实时间，先判断是缺少 clock seam、缺少 fake transport、还是缺少 fake listener；补前置基建，不允许直接把 `sleep` 塞回测试。
- 生产构建默认不暴露任何“手动推进时间”的入口；manual clock / scheduler 只作为测试注入实现存在。

## 5. listener 故障注入骨架

listener 相关测试统一按“正式启动路径和 runtime probe 路径共用同一套 bind seam”设计，避免一边 fake、一边还残留真实 `net.Listen`：

- `frps/internal/control/listeners.go` 的 `startTunnelListeners()` 和 `closeStartedTunnelListeners()`，以及 `frps/internal/control/runtime_scan.go` 的 `probeTunnelRuntimeIssue()`，后续都必须改走同一个注入的 listener factory / binder。
- 第一版注入点至少同时覆盖：
  - TCP `Listen`
  - UDP `ResolveUDPAddr + ListenUDP`
  - `Close`
  - “端口当前是否已被本进程或外部占用”的判断结果
- 这样正式 listener 启动、轮询探测、freeze 后 close、shutdown 清理、恢复时重绑，才会共享同一套故障脚本和同一套资源视图，不会出现“探测结果和实际启动结果来自两套世界”的假阳性。

第一版边界建议固定为“两层实现”：

- real impl：只是对 `net.Listen`、`net.ResolveUDPAddr`、`net.ListenUDP`、真实 listener `Close()` 的薄封装，不额外改变当前生产行为。
- fake impl：维护一份测试内的“监听资源账本”，按脚本决定每次 bind / close / probe 的返回结果，并能精确记录资源何时真正释放。
- 业务代码只依赖稳定接口，不感知 fake 细节；测试代码只通过脚本和观测接口控制 fake，不直接改内部 map。

第一版 listener seam 至少要能表达以下信息：

```go
type ListenKey struct {
    Protocol string
    IP       string
    Port     uint16
}

type BindKind string

const (
    BindKindRuntimeProbe BindKind = "runtime_probe"
    BindKindRuntimeStart BindKind = "runtime_start"
)
```

- `ListenKey` 作为统一资源键，正式启动和 runtime probe 都按同一维度占位。
- `BindKind` 必须区分“探测性临时绑定”和“正式 listener 启动”，因为两者虽然共用同一资源空间，但测试里往往需要对两类路径下发不同故障脚本。
- 对 range tunnel，不按整个区间一次性返回成功/失败；而是保留逐端口 bind 事件，才能稳定覆盖“第 N 个端口失败”“部分端口成功后回滚 close”。

第一版脚本能力固定至少支持以下故障原语：

- 首次 bind 失败。
- 第 `N` 次 bind 失败。
- 指定 `groupID / tunnelID / configVersion / protocol / port` 失败。
- probe 成功但正式 start 失败。
- start 成功后，下一次 probe 看到“仍被占用”。
- 外部占用先存在，随后释放。
- 释放后再次被外部占用。
- `Close()` 调用已发起，但资源延迟到测试显式放行后才真正释放。
- 同一 tunnel 的部分端口成功、部分端口失败。
- TCP 成功但 UDP 失败，或 UDP 成功但 TCP 失败。
- UDP `ResolveUDPAddr` 失败与 `ListenUDP` 失败要能分开制造，不能都压成同一种错误。

这些故障都必须是“脚本化稳定复现”，不是依赖真实系统端口抢占：

- fake binder 提供按调用序列匹配的 rule/step 机制，测试可声明“第 1 次 probe 返回占用，第 2 次 probe 返回成功，第 1 次正式 bind 在端口 7001 失败”。
- 对“占用后释放”“释放后复占”“close 晚到”这类场景，资源状态变化必须由测试显式推进，例如通过 `ReleaseClose(handleID)`、`SetOccupied(key, true/false)` 或等价脚本步骤完成，而不是靠真实 OS 何时回收 socket。
- 这样快机器、慢机器、不同 `GOMAXPROCS` 下看到的仍是同一时序，不会出现“本机来不及撞上窗口，CI 又偶现”的情况。

与现有 hook / barrier / manual scheduler 的配合方式固定如下：

- hook/barrier 继续负责“卡在 bind 前后、attach 前后、freeze 后、close 前后”这些并发边界。
- fake listener 负责“这一拍 bind / probe / close 到底成功、失败、还是延迟释放”。
- manual scheduler 负责驱动轮询恢复、心跳、重试等时间推进；但 listener 资源状态本身不依赖真实时间自动变化，必须由测试显式脚本或 fake close 完成。
- 若某个 listener 场景要同时覆盖“轮询推进 + 资源释放 + 热更新交错”，推荐顺序固定为：
  1. 先用 hook 卡住目标边界。
  2. 用 fake listener 改写占用/释放状态。
  3. 用 manual `Advance(...)` 推进轮询或重试。
  4. 用 `WaitIdle()` 和状态断言收敛，再决定是否放行下一道 barrier。

第一批需要打开 listener seam 的代码位置如下：

- `frps/internal/control/listeners.go`
  - `startTunnelListeners()`
  - `closeStartedTunnelListeners()`
  - `ensureTunnelListeners()`
- `frps/internal/control/runtime_scan.go`
  - `probeTunnelRuntimeIssue()`
  - `scanGroupRuntimeIssues()`
  - `recoverScannedActiveSessionTunnels()`
- `frps/internal/control/session.go`
  - `freezeTunnelRuntime()`
  - `resetTunnelRuntime()`
  - `shutdownSession()`

其中职责边界固定如下：

- `startTunnelListeners()` 和 `probeTunnelRuntimeIssue()` 只负责声明“我要按什么 key 绑定、这是 probe 还是 start”，不各自私下直接调用 `net.Listen`。
- `closeStartedTunnelListeners()` 统一经由注入 listener handle 关闭，不能一部分走 fake、一部分还直接 `.Close()` 真 listener。
- `serveTunnelListener()` / `serveUDPTunnelListener()` 当前重点仍在 I/O；第一版 listener 故障注入不要求 fake 完整 accept/read 数据面，只要能稳定覆盖 bind/close 生命周期即可。

必须直接可观测的测试态信息固定如下，后续测试不得只靠日志判断：

- 当前哪些 `ListenKey` 处于已占用、待释放、已释放状态。
- 每个占用的 owner 是 runtime probe、runtime start、还是外部占用。
- 每个 fake listener handle 的 `created / closeCalled / closeCompleted` 时序。
- 指定 tunnel 当前已 attach 的 listener 数量、缺失的端口集合、最近一次失败原因。
- 每次 bind/probe 命中的 rule、调用序号、返回错误和关联的 `groupID / tunnelID / configVersion`。

第一版验收口径固定如下：

- 能稳定制造“首次失败、第 `N` 次失败、部分 tunnel 失败、占用后释放、释放后复占、close 晚到、TCP/UDP 差异化失败”这些场景。
- 同一份脚本下，`probeTunnelRuntimeIssue()` 与 `startTunnelListeners()` 观察到的是同一资源世界，不会一边判可用、一边又稳定绑定失败。
- freeze / shutdown / reload 触发的 close 必须可被观测到“调用已发生”和“资源已真正释放”两个阶段，便于后续覆盖 close 晚到竞态。
- 若某个 listener 竞争态测试仍然必须启动真实外部占用进程或靠 `sleep` 等待端口释放，说明 fake listener 能力还不够，先补基建，不进入场景实现。

## 6. 网络快照故障注入骨架

网络快照相关测试统一按“采集、发布、消费走同一套 fake snapshot seam”设计，避免 `Start()/poll()` 一套数据源、`resolveGroupEffectiveIP()` 又从另一套状态猜当前本机地址：

- `frps/internal/system/network_snapshot.go` 后续需要把 `collector.Collect()`、轮询驱动、`storeSnapshot()` 发布边界统一收口到可注入 seam；测试既能决定“这一轮采集返回什么”，也能决定“采集结果何时对外可见”。
- `frps/internal/control/listeners.go` 的 `resolveGroupEffectiveIP()`、`frps/internal/control/runtime_scan.go` 的 `scanGroupRuntimeIssues()` / `recoverScannedActiveSessionTunnels()`、`frps/internal/control/refresh.go` 的 `runtimeRefreshSnapshot()` / `RefreshGroup()` 都必须继续只消费同一个 `SnapshotReader.Current()` 视图，不能为了测试再额外偷读别的 fake 状态。
- 第一版目标不是模拟整个 OS 网卡栈，而是稳定控制“当前 `frps` 认为哪些 IP 属于本机、这一认知何时切换、切换前后有没有错误或抖动”。

第一版边界建议固定为“两层 provider + 一层发布控制”：

- real provider：继续复用当前 platform collector，只是把 `Collect()` 这一层显式抽出来，生产行为不变。
- fake provider：按测试脚本返回固定快照、跳变快照、错误快照或非法输入结果，不依赖真实网卡、VPN、虚拟网卡或系统命令。
- publish barrier：采集完成后、`storeSnapshot()` 前后允许测试显式卡住，稳定制造“采集结果已算出但尚未发布”“发布刚完成、消费方准备读取”的交错窗口。

第一版 fake snapshot 脚本能力至少固定支持以下原语：

- 固定快照：始终返回同一组本机地址，覆盖稳定基线。
- 快照跳变：第 `N` 轮从地址集 A 切到地址集 B，覆盖地址新增、地址移除、地址替换。
- 地址族切换：IPv4-only、IPv6-only、双栈、有 wildcard 但无具体地址等组合可脚本切换。
- 非法 `effective_ip`：保留配置值非法、为空、格式异常、带 zone/scope、带空白等输入，验证消费侧按“无效配置”而不是“非本机地址”处理。
- 非本机地址：`effective_ip` 本身合法，但 fake 快照不包含该地址，验证首次登录拒绝、运行中下发空配置和后续恢复路径。
- 高频抖动：按脚本在“包含该地址 / 不包含该地址”之间连续翻转，验证不会靠 CPU 快慢偶现。
- 采集错误：指定轮次 `Collect()` 返回 error，验证旧快照保持、错误不会直接把当前可用地址清空。
- 发布延迟：本轮 `Collect()` 已返回新快照，但在测试显式放行前 `Current()` 仍只能看到旧快照。
- 前后轮不一致：连续两轮返回互相矛盾的地址集，验证轮询恢复、热更新和登录路径不会在同一业务动作里混用两份视图。

快照模型后续至少要显式表达这些信息，避免 fake 只能回答一个 `HasIP`：

```go
type Snapshot struct {
    Platform     string
    CapturedAt   time.Time
    Interfaces   []NetworkInterface
    AvailableIPs []string
}
```

- 测试脚本必须能直接构造完整 `Snapshot`，而不是只传一个 `[]string`，这样地址顺序、采集时间戳、接口来源都能纳入断言。
- `CapturedAt` 后续也要接入同一套 manual clock，避免快照内容是 fake 的、时间戳却还在读真实墙钟。
- 对非法 `effective_ip` 场景，fake provider 不负责替用户“纠正配置”；非法值仍由 `NormalizeListenIP()` / `resolveGroupEffectiveIP()` 自己返回错误，fake provider 只负责提供“当前本机地址集”。

与现有 hook / barrier / manual scheduler 的配合方式固定如下：

- hook / barrier 负责把执行流卡在“初始快照采集完成后、发布前”“轮询采集完成后、发布前后”“`resolveGroupEffectiveIP()` 读取 `Current()` 前后”这些并发边界。
- manual scheduler 负责推进 `NetworkSnapshotService.poll()`、未监听 tunnel 轮询、heartbeat、重连 backoff 等时间相关任务；网络快照变化本身不靠真实时间自然发生，而是由 fake provider 在第 `N` 次采集返回指定脚本结果。
- fake snapshot provider 负责决定“这一轮看到什么地址集、返回错误还是成功”；它不负责替代 barrier，也不负责直接改 listener/runtime issue。
- 若某个场景要同时覆盖“快照切换 + 热更新 + 轮询恢复”，推荐顺序固定为：
  1. 先用 hook 卡住目标边界，例如 `storeSnapshot()` 前或 `resolveGroupEffectiveIP()` 前。
  2. 配置 fake provider 的下一轮脚本结果。
  3. 用 manual `Advance(...)` 推进一次快照轮询或目标业务时钟。
  4. 用 `Release()` 控制发布或读取时机。
  5. 用状态断言确认 runtime issue、pending config、listener 集合和 session 状态收敛，再进入下一步。

第一批需要打开 snapshot seam 的代码位置如下：

- `frps/internal/system/network_snapshot.go`
  - `Start()`
  - `poll()`
  - `storeSnapshot()`
- `frps/internal/system/network_collect.go`
  - platform collector 的 `Collect()`
  - `NormalizeListenIP()` / `IsSpecialListenIP()` 所在解析路径
- `frps/internal/control/listeners.go`
  - `resolveGroupEffectiveIP()`
- `frps/internal/control/runtime_scan.go`
  - `scanGroupRuntimeIssues()`
  - `recoverScannedActiveSessionTunnels()`
- `frps/internal/control/refresh.go`
  - `RefreshGroup()`
  - `runtimeRefreshSnapshot()`

必须直接可观测的测试态信息固定如下，后续测试不得只从日志反推：

- 当前已发布的 `Snapshot` 全量内容，以及它的版本/序号、`CapturedAt` 和来源轮次。
- fake provider 下一轮计划返回什么、已经消费到第几轮、最近一次 `Collect()` 返回的是成功还是错误。
- `resolveGroupEffectiveIP()` 最近一次读取时看到的快照版本，以及它把错误判成了“非法地址”还是“非本机地址”。
- 指定 group/session 当前是否处于“完整配置可下发”“空配置保活”“等待恢复补推”“只允许健康 tunnel 直接补 listener”中的哪一种状态。
- `runtime issue` 当前是否由 `effective_ip` 不可用导致，还是由 listener bind/runtime probe 导致；两类原因不能在观测层混成一条字符串。

第一版验收口径固定如下：

- 能稳定制造“固定快照、跳变、地址族切换、非法 `effective_ip`、非本机地址、高频抖动、采集错误、发布延迟、前后轮不一致”这些场景。
- 首次登录拒绝、运行中下发空配置、轮询恢复补启动、热更新恢复补推完整快照这几条链路，都必须能在同一套 fake snapshot 脚本下复现，不依赖真实网卡改动。
- 若测试仍需临时改本机网卡、启停 VPN、等待系统网络服务刷新或靠 `sleep` 撞快照窗口，说明 snapshot seam 还不够，先补基建，不进入场景实现。

## 7. 控制连接故障注入骨架

控制连接相关测试统一按“登录握手、`config.push/config.ack`、heartbeat、runtime close、session shutdown 共用同一套脚本化 transport/session harness”设计，避免一部分场景还在用真实 `net.Conn` + `net.Pipe` 撞时序，另一部分场景才 fake 消息顺序：

- `frps/internal/control/server.go` 的 `readFrameWithTimeout()` / `writeFrame()`、`frps/internal/control/auth.go` 的登录握手、`frps/internal/control/config.go` 的 `pushReloadConfig()` / `handleConfigAck()`，以及 `frpc/internal/client/client.go` 的 `readMessage()` / `writeMessage()`、`frpc/internal/client/login.go` 的 `login()`、`frpc/internal/client/session.go` 的 `readLoop()` / `heartbeatLoop()`，后续都必须继续通过同一条“frame 级”传输 seam 交互。
- 第一版目标不是把整个 TCP/IP 栈 fake 掉，而是稳定控制“哪一帧什么时候写出、什么时候被对端读到、是否被丢弃/延迟/重复/重排、连接何时半关闭或全关闭、旧连接残帧是否仍尝试到达”。
- 只有把故障注入放在 frame transport / session harness 这一层，才能同时覆盖首登 `config.push`、热更新 `config.push`、`config.ack`、heartbeat、`stream.close` / `udp.close`、session replacement 和晚到错误回包；若只在 `handleConfigAck()` 或 `applyConfigPush()` 上层做 stub，会绕过真实 requestId、streamId、session 交接和写锁语义。

第一版边界建议固定为“两层 transport + 一层脚本化连接编排”：

- real transport：继续薄封装当前 `transport.ReadFrame()` / `transport.WriteFrame()` 与真实 `net.Conn`，生产行为不变。
- fake frame transport：按测试脚本控制每次 `ReadFrame` / `WriteFrame` 的可见顺序、返回错误、阻塞点、连接状态和连接切换。
- session harness：在 fake transport 之上再提供“连接 1 / 连接 2 / 替换 session / 旧 session 晚到消息 / 半关闭 / 重连”的场景编排能力，避免每个测试各自拼多个 `net.Pipe` 和 goroutine。

第一版故障原语至少固定支持以下能力：

- `config.push` 写出前断线。
- `config.push` 已写出但客户端尚未读到时断线。
- 客户端已读到 `config.push`，但 `config.ack` 写出前断线。
- `config.ack` 已写出但服务端尚未读到时断线。
- `config.ack` 超时不来、重复到达、乱序到达、晚到到达。
- 旧 `config.ack` 在新 `config.push` 或新 session 建立后晚到。
- 服务端错误回包晚到，且晚到时旧 session 已关闭或新 session 已接管。
- heartbeat 丢失、晚到、重复、与 `config.push` / `config.ack` 交错。
- 服务端 `stream.close` / `udp.close` / shutdown 帧与 reload、断线、半关闭并发。
- 单向半关闭：
  - client write closed / server read EOF，但 server->client 方向仍可写。
  - server write closed / client read EOF，但 client->server 方向仍可写。
- 全关闭后仍尝试投递旧连接残留帧，验证它们不会污染新 session。
- 重连时复用同一 group，但旧连接上尚有未消费帧或未应用错误，验证新旧 session 严格隔离。

第一版 fake transport 不应只按“连接是否可用”返回粗粒度错误，而要显式建模 frame 级事件：

```go
type FrameDirection string

const (
    DirClientToServer FrameDirection = "c2s"
    DirServerToClient FrameDirection = "s2c"
)

type ConnID string

type FrameEvent struct {
    ConnID      ConnID
    Direction   FrameDirection
    FrameType   protocol.Type
    RequestID   uint32
    StreamID    uint32
    SessionID   uint64
    ConfigVer   uint64
    Action      string // deliver / drop / delay / duplicate / close / half_close / error
}
```

- 测试脚本必须能按 `ConnID + Direction + FrameType + RequestID + SessionID` 匹配规则；仅按“下一帧”匹配不够，因为重连、多 session 和晚到消息会让同一类型帧同时存在。
- `SessionID` 不能只靠协议头推断；在 harness 层要显式绑定“这条连接当前代表哪个逻辑 session”，否则无法稳定表达“旧 session 的晚到 ack”。
- `Action=delay` 不能依赖真实时间；延迟帧必须停在 harness 队列里，由测试显式 `ReleaseDelayed(...)` 或脚本下一步放行。
- `Action=duplicate` 必须复制同一帧字节和同一元数据，而不是重新编码一份“看起来一样”的帧；否则无法保证重复包和原包完全等价。

与现有 hook / barrier / manual scheduler 的配合方式固定如下：

- hook / barrier 负责把执行流卡在“`config.push` 编码后未写出”“写出后未标记 pending”“`config.ack` 解析后未 accept”“旧 session 注销前”“新 session 注册后”“shutdown 已发起但连接未关闭”这些代码边界。
- fake transport / harness 负责决定“这一帧现在到底能不能过、会不会丢、会不会重复、会不会晚到、连接当前是全关还是半关”。
- manual scheduler 负责推进 heartbeat 周期、重连 backoff、ack 等待、轮询恢复等时间路径；控制连接里的延迟消息本身不靠真实时间到达，而是由 fake transport 显式放行。
- 若某个场景要同时覆盖“热更新触发 `config.push` + 旧 ack 晚到 + 客户端重连”，推荐顺序固定为：
  1. 先用 hook 卡住 `pushReloadConfig()` 写出前或 `handleConfigAck()` 接收前。
  2. 用 fake transport 为旧连接、新连接分别装入脚本事件。
  3. 用 manual `Advance(...)` 推进 heartbeat/backoff/轮询等时间事件。
  4. 按顺序 `Release()` 目标 barrier 或 delayed frame。
  5. 用状态观测断言 `pending config`、active session、listener 集合和最后生效快照已收敛，再进入下一步。

第一批需要打开 transport/session seam 的代码位置如下：

- `frps/internal/control/server.go`
  - `readFrameWithTimeout()`
  - `writeFrame()`
  - `runSession()`
  - `handleHeartbeatPing()`
- `frps/internal/control/auth.go`
  - `authenticate()`
  - `reserveGroupSlot()`
  - `releaseGroupSlot()`
- `frps/internal/control/config.go`
  - `pushConfig()`
  - `pushReloadConfig()`
  - `handleConfigAck()`
- `frps/internal/control/session.go`
  - `writeFrameWithSession()`
  - `writeRuntimeFrameWithSession()`
  - `shutdownSession()`
- `frps/internal/control/refresh.go`
  - `registerActiveSession()`
  - `unregisterActiveSession()`
  - `RefreshGroup()`
- `frpc/internal/client/client.go`
  - `Run()`
  - `runOnce()`
  - `readMessage()`
  - `writeMessage()`
  - `applyConfigPush()`
- `frpc/internal/client/login.go`
  - `login()`
  - `readLoginFrame()`
- `frpc/internal/client/session.go`
  - `readLoop()`
  - `heartbeatLoop()`

其中职责边界固定如下：

- `server.go` / `client.go` 这一层只声明“我要读/写一帧、当前连接属于哪个 logical session、读取超时/写入错误如何上抛”，不私下直接调用另一套真实 `transport.ReadFrame/WriteFrame`。
- 登录握手、热更新推配置、heartbeat、session shutdown 必须继续共用同一 transport seam，不能登录路径一套 fake、运行态路径另一套 fake。
- reconnect 不通过“修改同一 `net.Conn` 内部状态”模拟，而是由 harness 显式创建新 `ConnID` 并走一次真实的 `runOnce()/login()/registerActiveSession()` 入口；否则测不到旧 session 注销、新 session 接管和 group slot 交接边界。

为了稳定覆盖“旧 session 晚到消息不能污染新 session”，第一版 harness 必须额外具备以下能力：

- 明确区分连接实例 ID、逻辑 session ID、group ID，三者都能直接观测。
- 能在旧连接已被 `Close()` 或 `shutdownSession()` 后，继续尝试投递一帧旧 `config.ack`、旧 heartbeat、旧 error，验证服务端/客户端会丢弃或因连接关闭而失败，而不是被新 session 吞掉。
- 能脚本化表达“旧 session 的 `config.push` 请求 ID 与新 session 当前 pending request ID 恰好相同/不同”的组合，验证校验逻辑依赖的是当前 session 上下文，不是全局 requestId。
- 能在 `unregisterActiveSession(old)` 与 `registerActiveSession(new)` 交接窗口内精确卡住，验证 group slot、active session map 和 pending config 不会出现双活或空洞。

必须直接可观测的测试态信息固定如下，后续测试不得只从日志反推：

- 每个 `ConnID` 当前的方向状态：
  - open
  - read closed
  - write closed
  - fully closed
- 每个 `ConnID` 已写出、已投递、已丢弃、仍延迟队列中的 frame 明细，以及它们关联的 `FrameType / RequestID / StreamID / SessionID / ConfigVersion`。
- 服务端当前 active session、已占用 group slot、待注销旧 session、当前 `pendingConfigRequestID` / `pendingSnapshot.Version`。
- 客户端当前最后已应用快照版本、最后已 ack 版本、当前 snapshot 内容、重连中的连接尝试序号。
- 旧 session 的晚到帧最终命中的是“被连接层拒绝”“被协议校验拒绝”“被 session 上下文忽略”中的哪一种结果，不能只得到一条模糊日志。
- heartbeat 最近一次发送/接收/响应对应的连接和 session 身份，避免把旧连接上的 pong 误记到新连接。

第一版验收口径固定如下：

- 能稳定制造“`config.push` 前断线、`config.push` 后未 ack 断线、重复 ack、乱序 ack、旧 ack 晚到、错误回包晚到、heartbeat 晚到、半关闭、重连接管、旧 session 残留帧”这些场景。
- 同一套 fake transport/harness 下，首登、热更新、空配置保活、恢复补推完整快照、session replacement 这几条链路都能复用；不允许每类场景各造一套专用 pipe helper。
- 任一控制连接竞争态场景若仍必须依赖真实网络抖动、真实 TCP 半关闭时机或 `sleep` 才能稳定复现，说明 transport/session seam 还不够，先补基建，不进入具体场景实现。
