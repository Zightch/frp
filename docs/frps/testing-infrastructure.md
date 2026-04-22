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

- 当前状态：已可用（最小实现）；已落地共享 `FrameIO` seam、scripted frame transport / conn pair、基础 delay/drop/duplicate/error 规则，以及服务端/客户端读写路径接线。更复杂的跨进程控制接口、半关闭细节和旧连接残帧剧本仍待增强。

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

## 8. 状态观测骨架

状态观测相关测试后续统一按“业务状态快照 + 注入层状态快照 + 启停门闩状态”三层收口，避免测试继续通过日志、私有字段临时 helper、或 fake 侧反推业务结论：

- 当前状态：已可用（最小实现）；`frps` server / app / network snapshot、`frpc` client、fake listener、fake transport 都已提供结构化 `ObserveState()` 出口，场景层可聚合成统一快照。跨端总快照自动聚合和更细粒度消费者视角仍待增强。

- 业务状态快照负责回答“`frps` / `frpc` 当前认为系统处于什么状态”，例如当前 listener 集合、active session、pending config、已生效 snapshot、空配置保活状态。
- 注入层状态快照负责回答“fake listener / fake snapshot / fake transport 当前制造了什么外部世界和消息世界”，例如端口占用脚本、快照轮次、延迟 frame 队列。
- 启停门闩状态负责回答“哪些外部可见入口已经被放行”，例如首轮扫描是否完成、控制端口是否已开放、管理 API 是否已首次可见。
- 三层观测都必须返回不可变副本；测试只能读结构化快照，不能拿 live map、slice、`net.Conn`、session 指针回去自行推断。

第一版统一观测入口建议固定为“按进程导出的测试态 snapshot”，而不是散落多个临时 helper：

```go
type ObservedState struct {
    App       AppObservedState
    Server    ServerObservedState
    Client    ClientObservedState
    Snapshot  *SnapshotObservedState
    Listeners *ListenerObservedState
    Transport *TransportObservedState
}
```

- 单端单测可以只实现 `ServerObservedState` 或 `ClientObservedState` 子集。
- 双端或双进程 harness 再把服务端、客户端、listener fake、snapshot fake、transport fake 的观测结果聚合成一份总快照。
- `Observe()` / `Snapshot()` 这类入口只做“锁内复制 + 结构化导出”，不承担等待收敛、推进时间或释放 barrier；等待仍由 hook / manual scheduler / harness 控制。

当前已有零散可观测口子，但还不够统一，后续要收口为同一套测试态快照：

- 服务端已有 `TunnelRuntimeIssues()`、`activeSession()`、`initialRuntimeScanDone`、`groupSlots`、`session.currentGroupAndSnapshot()`、`session.configAckState()` 等局部状态。
- 客户端已有 `snapshotValue()`、`lastAckedConfigVersion`、`activeStreams`、`activeUDPSessions` 等局部状态。
- 网络快照已有 `NetworkSnapshotService.Current()`。
- 这些入口目前仍偏“零散 helper”或“测试直接摸内部字段”；状态观测骨架的目标是把它们收口成统一、稳定、可组合的测试态视图。

服务端第一批必须直接可观测的状态固定如下：

- 启动门闩与外部可见性：
  - `InitialRuntimeScanDone`：对应 `frps/internal/control/server.go` 的首轮扫描完成标志。
  - `ControlListenerOpen`：控制端口是否已经真正进入监听。
  - `LoginGateOpen`：首个 `frpc` 登录是否已被允许进入认证流程；后续即使实现继续靠“监听未开放”达成，也要有结构化布尔位可断言。
  - `ManagementAPIVisible`：管理 API 是否已首次可见；这层状态要在 `frps/internal/app/app.go` 收口，不能只靠“端口能不能拨通”间接判断。
- session / group slot：
  - 每个 group 当前占用的 `groupSlot -> sessionID`。
  - 每个 active session 的 `groupID / sessionID / connID`、当前 `Group.EffectiveIP`、当前 `Snapshot.Version`、`LastAckedConfigVersion`。
  - 每个 active session 当前是否存在 `pendingConfigRequestID`、对应 `pendingSnapshot.Version`、`pendingSnapshot.TunnelCount`、`pendingGroup.EffectiveIP`。
  - 每个 active session 当前是否 `runtimeFrozen`、是否 `listenersStarted`、当前 `runtimeGeneration`。
- listener 运行态：
  - 当前已 attach 的 listener 集合，至少包含 `groupID / sessionID / tunnelID / protocol / bindIP / port / configVersion / listenerKind(tcp|udp)`。
  - 每个 tunnel 当前缺失了哪些监听端口；不能只暴露“是否有 listener”这种粗粒度结论，否则无法精确覆盖“部分端口恢复”。
  - 空配置保活态必须显式可见，不能靠“当前 `Snapshot.Tunnels == 0` 且 session 还活着”由测试自行猜；因为“天然没有 tunnel”与“因 `effective_ip` 失效被 shrink 成空配置”不是同一业务语义。
- runtime issue 与状态优先级：
  - 原始 runtime issue 必须结构化暴露，至少包括 `tunnelID`、`reason`，以及可选的 `kind`，例如 `effective_ip_invalid`、`effective_ip_not_local`、`runtime_bind_conflict`、`runtime_bind_error`。
  - 静态 `冲突` 结果必须与 runtime issue 分开暴露，至少能直接知道某个 tunnel 当前是否命中静态冲突，以及冲突对端是谁。
  - 最终派生状态也必须直接暴露，至少包含 `finalStatus / finalReason`，便于直接断言“静态 `冲突` 优先于 runtime `异常`”，而不是测试重新抄一遍管理 API 派生逻辑。
- snapshot 与恢复语义：
  - 每个 active session 当前已生效 snapshot、pending snapshot、最后 ack 版本。
  - 当前是否处于“完整配置可下发”“空配置保活”“等待恢复补推完整快照”“仅补未监听健康 tunnel listener”中的哪一种恢复模式。
  - 当前已发布的本机网络 snapshot 版本/序号、`CapturedAt`、`AvailableIPs`，以及最近一次 `resolveGroupEffectiveIP()` 读取看到的是哪一版。

客户端第一批必须直接可观测的状态固定如下：

- 当前逻辑会话身份：`ConnID / SessionID / GroupID`，以及该连接是否已被新 session 替换。
- 当前已应用 snapshot：至少包含 `ConfigVersion`、`GeneratedAtMs`、`TunnelCount` 和必要时的 tunnel 摘要。
- `lastAckedConfigVersion`：当前最后成功写出并生效的 ack 版本。
- 当前活跃 `stream` / `udp session` 数量，以及 reload 后关闭了多少旧对象。
- 重连尝试序号必须显式可见，不能只从日志里的 `retry_in` 反推；后续竞争态场景需要直接断言“这是第几次重连、旧连接是否已经失效、新连接是否已接管”。
- 客户端当前恢复模式也要结构化可见，至少能区分“正常运行”“收到空配置保活”“等待下一次完整快照”“正在用新连接接管旧 session”。

为了避免观测层本身又制造新的竞态，第一版观测接口必须遵守以下规则：

- 所有观测结果都必须在锁内复制，返回只读快照，不返回 live 指针。
- 不允许把字符串日志当主状态源；字符串只能作为补充 reason，人类可读，但测试断言优先消费结构化字段。
- 对“同一结论的不同来源”必须拆开暴露，例如：
  - 静态 `冲突`
  - runtime `异常`
  - 空配置保活
  - 待恢复完整快照
  - 仅缺 listener 的局部恢复
- 对“同一对象的不同生命周期”必须直接暴露稳定 ID，而不是靠地址比较，例如 `groupID / tunnelID / sessionID / connID / configVersion / requestID`。
- 观测层不得要求测试重复执行业务计算，例如再次跑一遍 `detectTunnelConflicts()`、再次扫描 listener map 推断“谁是 active session”；这些应在观测快照里直接给出。

状态观测与 hook / fake / harness / manual scheduler 的边界固定如下：

- hook / barrier 负责把执行流卡在“观测前后”的边界，例如首轮扫描完成未发布、`config.push` 已写出未标记 pending、`config.ack` 已解析未 accept、管理 API 已创建未放行。
- fake snapshot / listener / transport 负责暴露“外部世界当前被脚本制造成什么样”，例如端口被谁占用、下一轮快照是什么、哪些 frame 仍在 delay 队列。
- manual scheduler 负责推进“什么时候发生下一轮扫描 / heartbeat / backoff / poll”，但不直接替代业务状态观测。
- 场景编排器负责在“放行某个 barrier 之后”拉取一份统一快照做断言，避免测试一边读服务端状态、一边读 fake 状态时跨过了两个不同时间点。

第一批代码落点固定如下：

- `frps/internal/app/app.go`
  - 收口 `InitialRuntimeScanDone -> 控制端口开放 -> 管理 API 首次可见` 这条启动门闩状态。
- `frps/internal/control/server.go`
  - 收口 `initialRuntimeScanDone`、control listener 是否已开放、`sessions`、`groupSlots`、`tunnelRuntimeIssues`。
- `frps/internal/control/session.go`
  - 收口 `Snapshot`、`LastAckedConfigVersion`、`pendingConfigRequestID`、`pendingSnapshot`、listener/runtime 冻结态。
- `frps/internal/control/listeners.go`
  - 收口当前 attach 的 listener 集合、按 tunnel 缺失的端口集合、局部恢复结果。
- `frps/internal/control/runtime_scan.go`
  - 收口当前扫描目标、扫描结论、恢复模式切换结果。
- `frps/internal/control/refresh.go`
  - 收口空配置保活、恢复补推完整快照、仅补 listener 三种模式切换。
- `frps/internal/system/network_snapshot.go`
  - 收口当前已发布 snapshot 的版本/轮次、最近一次成功/失败采集。
- `frpc/internal/client/client.go`
  - 收口重连尝试序号、当前连接身份、最近一次 `config.push` 应用结果。
- `frpc/internal/client/session.go`
  - 收口当前 snapshot、最后 ack 版本、stream / udp session 数量和 reload 摘要。

第一版验收口径固定如下：

- 任一测试在一次统一观测调用里，都能直接断言“首轮扫描是否完成”“控制端口是否已开放”“管理 API 是否已首次可见”，不再靠拨端口或看日志旁证。
- 任一测试都能直接断言“每个 active session 当前最多一个有效 pending config”，且能看到它对应的 request/version/group/snapshot。
- 任一测试都能直接断言“某个 tunnel 当前是静态 `冲突`、runtime `异常`、还是已恢复”，并能同时拿到原始 runtime issue 与最终派生状态。
- 任一测试都能直接断言“当前是空配置保活，还是天然无 tunnel，还是等待恢复补推完整快照”，不再靠 `len(snapshot.Tunnels)` 猜业务模式。
- 任一测试都能直接断言“旧 session 的晚到 ack / heartbeat / error 最终落到哪个连接和 session 身份上”，不再只得到一条模糊日志。
- 如果某个场景仍然必须靠日志关键字、端口探活、或临时读取私有字段来判断是否成功，说明状态观测骨架还不够，先补观测口子，不进入不变量断言库和具体场景实现。

## 9. 不变量断言库

不变量断言相关测试后续统一按“直接消费结构化状态快照，不从日志和端口旁证反推业务结论”收口，避免测试断言本身又退回非确定性：

- 当前状态：已可用（最小实现）；已提供启动门闩、single pending config、静态 `冲突` 优先级、空配置恢复顺序、旧 session 隔离等 helper，并已有最小单测覆盖。更丰富的 diff 输出与规则矩阵仍待扩展。

- 断言 helper 必须直接读取上一节状态观测骨架导出的 `ObservedState` 或等价不可变快照，不能自己重新扫描 listener、重跑冲突判定、重算 session 归属。
- 日志只能作为失败时的补充线索，不能作为主断言输入；日志存在丢行、异步刷新、语义漂移问题，无法稳定支撑竞争态测试。
- 端口探活、HTTP 首次可见性探测、真实连接拨测也只能作为补充旁证，不能代替状态断言；这些方法会把测试结果重新绑回 OS 调度、网络栈和机器速度。
- 断言库的职责是“把已经观测到的结构化状态变成统一规则校验与稳定失败输出”，不是替代 hook、manual scheduler、fake listener 或 fake transport。

第一版断言 helper 建议固定为分层设计，而不是把所有规则塞进一个巨型 `AssertEverything()`：

- 启动门闩断言：
  - 校验 `InitialRuntimeScanDone`、`ControlListenerOpen`、`LoginGateOpen`、`ManagementAPIVisible` 的先后关系。
  - 用于首轮扫描、控制端口开放、管理 API 首次可见这类启动序列场景。
- session / pending config 断言：
  - 校验 active session 唯一性、group slot 归属、`pendingConfigRequestID`、`pendingSnapshot.Version`、`LastAckedConfigVersion` 的一致关系。
  - 用于首登、热更新、断线重连、session replacement、旧 ack 晚到场景。
- listener / runtime issue / 最终状态断言：
  - 校验 listener 集合、缺失端口集合、runtime issue、静态 `冲突`、最终派生状态之间是否匹配。
  - 用于端口冲突、轮询恢复、局部补 listener、静态 `冲突` 优先级场景。
- 空配置保活与恢复顺序断言：
  - 校验 `effective_ip` 失效时是否进入空配置保活，恢复时是否先补推完整快照，再等待 `config.ack`，最后恢复 listener。
  - 用于运行时 `effective_ip` 非法、非本机、网络快照恢复、热更新恢复场景。
- 旧 session 晚到消息隔离断言：
  - 校验旧连接的晚到 `config.ack`、heartbeat、error、shutdown 帧不会污染新 session 的 pending config、ack 版本、活跃 listener 和恢复模式。
  - 用于重连接管、旧连接残帧、重复 ack、乱序 ack 场景。

第一版最少必须沉淀的核心不变量固定如下：

- 首轮扫描未完成前，不允许任何客户端登录路径进入“已通过登录门闩”的状态；如果当前实现通过“控制端口尚未监听”达成，也必须在快照里体现为 `LoginGateOpen=false`。
- 管理 API 首次可见前，不允许暴露半成品状态；至少要保证首轮扫描结论、初始 tunnel 派生状态、管理 API 首次可见门闩已经一起收敛。
- 任一时刻，同一 active session 最多只有一个有效 `pending config`；同一 group 也不能同时存在两个“当前有效”的 `pendingConfigRequestID`。
- 旧 session 的晚到消息不能污染新 session；旧 `ConnID / SessionID` 上收到的 `config.ack`、heartbeat、error 只能被连接层拒绝、协议层拒绝或被显式忽略，不能改变当前 active session 状态。
- 运行时 `effective_ip` 失效后进入空配置保活时，不允许直接恢复 listener；恢复顺序必须是“网络快照恢复有效 -> 补推完整快照 -> 等待 `config.ack` -> listener 恢复”。
- 静态 `冲突` 优先于 runtime `异常`；如果某个 tunnel 同时命中两类原因，最终派生状态必须稳定显示 `冲突`，runtime issue 只作为原始观测存在。
- 后续轮询只扫描当前没有监听的 tunnel 时，断言结果必须允许“之前冲突/异常的 tunnel 在条件恢复后被重新评估并补启动”，而不是永远停在旧异常。

断言 helper 的输入输出形式建议固定如下：

```go
type InvariantCheckInput struct {
    Before *ObservedState
    After  *ObservedState

    ExpectedGroupID   string
    ExpectedTunnelIDs []string
    ExpectedSessionID uint64
}

type InvariantViolation struct {
    Rule     string
    Summary  string
    Expected string
    Actual   string
    Fields   []string
}
```

- 单点状态规则可以只消费 `After`；涉及顺序和因果关系的规则，例如“先补推完整快照再恢复 listener”，必须同时消费 `Before/After` 或一段显式步骤快照。
- helper 返回值要么为空表示通过，要么返回结构化 `InvariantViolation` 列表；测试框架再统一决定是立即 `Fatal`、累计多个失败还是转为 diff 输出。
- 断言 helper 不直接依赖 `testing.T`，避免后续单测、双进程 harness、脚本化 e2e 场景各自再抄一套断言逻辑。

失败输出格式必须优先可定位，而不是只打一段泛化报错；建议统一包含以下信息：

- 规则名，例如 `startup.login_gate_before_initial_scan`、`session.single_pending_config`、`recovery.full_snapshot_before_listener_resume`。
- 失败摘要，直接说明违反了什么顺序或唯一性约束。
- 关键身份字段：`groupID / tunnelID / connID / sessionID / requestID / configVersion`。
- 关键状态差异：例如 `expected LoginGateOpen=false, actual=true`，或 `expected pending request count<=1, actual=2`。
- 必要时附上观测快照子树 diff，但只打印相关字段，不整份 dump 所有状态，避免失败输出被噪声淹没。

断言库与场景编排器的边界固定如下：

- 场景编排器负责“何时采样”和“采样哪些步骤”，例如在 barrier 放行前后、手动时间推进后、延迟 frame 放行后抓快照。
- 不变量断言库负责“拿到这些快照后如何校验规则”，不负责启动进程、不负责推进时间、不负责释放 barrier。
- 如果某条规则必须通过“先抓 A 点、再抓 B 点、再抓 C 点”才能表达，应由场景编排器提供步骤化快照，再由断言库消费；不要把编排逻辑偷偷塞回断言 helper。
- 若某个场景无法给出稳定快照切面，只能靠日志串推导是否成功，说明仍缺 hook、fake 或观测口，不应靠更复杂的断言 helper 硬补。

第一版验收口径固定如下：

- 任一后续测试都能复用同一组 helper 直接断言“首轮扫描门闩”“管理 API 首次可见”“pending config 唯一性”“旧 session 晚到消息隔离”“空配置恢复顺序”“静态 `冲突` 优先级”这些规则。
- 任一后续测试失败时，都能输出稳定的规则名、身份字段和状态差异，不再出现“只知道某条日志没打出来”这类不可定位失败。
- 任一后续测试若仍必须自己重新扫 listener、重算冲突、拼接 session 上下文后才能断言，说明断言库或状态观测骨架还不够，先补基建，不进入场景实现。

## 10. 场景编排基座

场景编排相关测试后续统一按“统一脚本模型 + 显式 barrier 放行 + 显式快照采样 + 显式失败输出”收口，避免每个测试各自手搓 goroutine、channel、`sleep` 和临时 helper：

- 当前状态：已可用（最小实现）；`frps/internal/testsupport` 已提供 step runner、trace、显式 barrier/clock/observe/assert 编排骨架和最小单测。跨进程 actor、联合 listener/snapshot/transport 注入动作仍待继续补齐。

- 场景编排器的职责不是替代 hook、manual scheduler、fake listener、fake snapshot、fake transport，而是把这些底层能力组织成一套稳定的剧本执行框架。
- 每个场景都必须能明确表达：
  - 谁在动作
  - 卡在哪个边界
  - 何时放行
  - 何时采样状态
  - 何时做不变量断言
  - 失败时输出哪一步、哪个 actor、哪个状态 diff
- 编排器必须优先服务当前主线场景：
  - `frps` 启动后首轮全面扫描、控制端口开放、管理 API 首次可见的启动门闩。
  - 首次登录时 `effective_ip` 非法直接拒绝。
  - 运行中 `effective_ip` 失效后下发空配置保活，恢复后补推完整快照并等待 `config.ack`。
  - 后续轮询只扫描当前未监听 tunnel，之前因冲突/异常未启动的 tunnel 在条件恢复后重新评估并补启动。
  - 热更新与 session replacement 并发发生时，旧 session 晚到消息不能污染新 session。

第一版编排模型建议固定为“场景 -> 阶段 -> 步骤 -> 采样点”四层，而不是让测试直接调一堆散落 helper：

```go
type Scenario struct {
    Name   string
    Actors ActorSet
    Steps  []ScenarioStep
}

type ScenarioStep struct {
    Name   string
    Actor  string
    Action StepAction

    WaitFor   []BarrierRef
    Release   []BarrierRef
    Advance   *AdvanceRef
    Observe   []ObserveRef
    Assert    []AssertRef
}
```

- `Actors` 至少包含：
  - `frps` 进程或单进程 server harness
  - `frpc#1`、`frpc#2` 或逻辑 session `session#1`、`session#2`
  - `listener-world`，负责外部占用与 close 晚到
  - `snapshot-world`，负责本机 IP 快照变化
  - `transport-world`，负责帧延迟、乱序、断线、半关闭
  - `clock` / `scheduler`，负责时间推进
- `ScenarioStep` 必须是显式有限动作，例如：
  - 启动 `frps`
  - 启动 `frpc`
  - 等待某个 hook/barrier 命中
  - 放行 barrier
  - 推进一个轮询周期
  - 下发一轮假快照
  - 修改一条 listener 占用脚本
  - 投递或丢弃一帧 `config.ack`
  - 采样统一 `ObservedState`
  - 执行一组不变量断言
- 单个步骤只允许承担一个主动作；例如“推进时间并采样并放行两处 barrier”这种组合动作必须拆成多个步骤，否则失败时无法定位。

场景编排器必须直接支持以下剧本原语，后续测试不得再各自重造一套：

- `StartActor(name)`：启动 `frps`、`frpc`、外部占用 actor 或子进程 harness。
- `WaitBarrier(point, hitIndex, filters...)`：等待指定 hook 命中，并可按 `groupID / tunnelID / sessionID / configVersion` 过滤。
- `ReleaseBarrier(point, hitIndex)`：显式放行被卡住的执行流。
- `AdvanceClock(d)` / `RunScheduled(name)`：推进 manual clock / scheduler。
- `InjectListener(rule)`：修改 fake listener 世界，例如“端口先占用后释放”“close 晚到”“第 N 次 bind 失败”。
- `InjectSnapshot(round)`：修改 fake snapshot 世界，例如“下一轮快照去掉该 IP”“下一轮恢复该 IP”。
- `InjectFrame(action)`：修改 fake transport 世界，例如“延迟旧 ack”“重复 heartbeat”“在写出前断线”。
- `Observe(label)`：抓取统一 `ObservedState` 快照，并给后续断言或失败 diff 使用。
- `Assert(ruleSet)`：执行断言库中的稳定规则。
- `ExpectEvent(...)`：断言某类 hook/frame/bind/probe 在当前阶段必须出现或不得出现。

单进程单测、子进程 harness、双进程脚本的职责边界必须先固定，再进入具体场景：

- 单进程单测：
  - 优先覆盖纯状态机和确定性边界。
  - 适合首轮扫描门闩、轮询恢复、单 session 热更新、空配置恢复顺序、旧 ack 晚到但仍在同一进程内可控的场景。
- 子进程或双进程 harness：
  - 用于验证跨进程真实生命周期、真实网络监听、真实管理 API 首次可见、双客户端/双连接接管。
  - 仍然必须复用同一套脚本步骤模型和同一套观测/断言接口，不能进入 e2e 就退回“shell 脚本 + 日志 grep”。
- 即使是双进程场景，也应优先通过 IPC 或测试控制接口暴露 barrier、采样和放行，而不是靠真实时间窗口对撞。

第一版场景脚本至少要能稳定表达以下组合：

- `frps` 首轮扫描完成前卡住，验证控制端口未开放、登录门闩未放行、管理 API 未首次可见。
- 首轮扫描完成后先采样，再放行控制端口开放，再放行管理 API 首次可见，验证三者严格有序。
- `frpc#1` 首次登录使用非法 `effective_ip`，验证服务端拒绝登录且不会占住 group slot。
- 运行中热更新把 `effective_ip` 改成非法值，验证空配置保活；随后网络快照恢复有效，再验证“完整快照 -> `config.ack` -> listener 恢复”。
- 外部占用导致 tunnel 首轮冲突未监听；后续轮询前释放占用，再推进一拍，验证 tunnel 被重新评估并补启动。
- `session#1` 待 ack 时触发 `session#2` 接管，随后投递旧 `config.ack`、旧 heartbeat、旧错误帧，验证都不会污染新 session。
- 连续两次配置变更在 `pending config` 尚未完成前到达，验证旧 pending 被正确替换或拒绝，不出现双 pending。

为了让失败可定位，编排器必须自带步骤化执行记录，而不是只在失败时 dump 一份最终状态：

- 每一步都要有稳定 `stepID`、`actor`、开始时间、结束时间、前置 barrier、放行动作、采样标签。
- 每个 `Observe(label)` 都要可回溯到对应步骤和同一时刻的 hook/fake 世界状态。
- 如果断言失败，输出必须至少包含：
  - 场景名
  - 步骤名
  - 当前 actor
  - 最近一次 barrier 命中
  - 最近一次时间推进
  - 相关 `ObservedState` 子树 diff
- 如果编排器自身超时，也必须说明是卡在哪个 barrier、哪个 actor 没收敛，而不是只报“test timeout”。

场景编排器与现有基建的边界固定如下：

- hook / barrier 提供“卡住/放行”能力，但不决定场景顺序。
- manual clock / scheduler 提供时间推进，但不决定何时采样和何时断言。
- fake listener / snapshot / transport 提供外部世界脚本，但不负责编排多 actor 交错。
- 状态观测骨架提供结构化快照，不负责管理步骤生命周期。
- 不变量断言库负责校验规则，不负责决定“在哪一步校验哪一条规则”。

第一版代码落点固定如下：

- `frps/internal/testsupport/` 或等价测试基础设施目录：
  - `scenario_runner.go`
  - `scenario_script.go`
  - `scenario_trace.go`
  - `scenario_assert.go`
- 与现有基建衔接：
  - `frps/internal/testhooks`
  - `frps/internal/system` 的 fake snapshot seam
  - `frps/internal/control` 的 fake listener / fake transport seam
  - `frpc/internal/client` 的 session harness 接口

第一版验收口径固定如下：

- 任一后续场景都必须能用显式步骤脚本表达，不再手写 `go func + sleep + select`。
- 任一失败都必须定位到“哪一步、哪个 actor、哪个 barrier、哪个状态 diff”，而不是只知道最终断言没过。
- 任一双进程或竞争态场景如果仍必须依赖偶然调度窗口，说明编排器或底层 seam 仍不够，先补基建，不进入测试矩阵。

## 11. 故障注入覆盖表

故障注入覆盖后续统一按“先列能力，再列可覆盖场景，再列缺口”收口，避免写测试时才发现关键路径根本不可制造：

- 覆盖表是场景实现前的准入清单，不是事后总结。
- 每一种故障注入能力都必须同时回答：
  - 现在能稳定制造什么
  - 依赖哪些 seam
  - 哪些主线场景会用到
  - 当前是“已定稿待落地”还是“仍缺注入点”
- 后续新增具体场景前，必须先确认它依赖的故障原语已经出现在本表里；如果没有，先补表和补基建。

第一版覆盖表建议至少固定以下字段：

- `Capability`：注入能力名。
- `Faults`：可稳定制造的故障原语。
- `Main Scenarios`：直接覆盖的场景类型。
- `Required Seams`：依赖的 hook / fake / harness / scheduler。
- `Current Status`：`已可用（最小实现）`、`已定稿待实现`、`部分缺口`、`未开始`。
- `Gap`：当前还不能稳定表达的边界。

第一版覆盖表按当前主线收口如下：

| Capability | Faults | Main Scenarios | Required Seams | Current Status | Gap |
| --- | --- | --- | --- | --- | --- |
| 启动门闩编排 | 首轮扫描未完成前阻止控制端口开放、阻止管理 API 首次可见、阻止登录进入 | 启动序列、管理 API 首次可见、首登前门闩 | hook、场景编排器、状态观测 | 已可用（最小实现） | 已有单进程 hook/controller；仍需统一跨进程 barrier 控制面与更多登录/配置阶段命中点 |
| 手动时间推进 | 轮询、heartbeat、backoff、超时、网络快照刷新 | 未监听 tunnel 轮询恢复、热更新恢复、断线重连 | manual clock、manual scheduler、场景编排器 | 已可用（最小实现） | `runtime scan` 与 `network snapshot` 已接入；`frpc` backoff/heartbeat 与更多 `time.*` 路径仍待切 seam |
| listener 绑定世界 | bind 失败、probe 失败、close 晚到、外部占用、部分端口失败、释放后复占 | 首轮冲突、后续恢复、局部补 listener、close/rebind 竞态 | fake listener、hook、状态观测 | 已可用（最小实现） | `start/probe/close` 已统一走 seam；close 延迟释放、部分 range 脚本与真实 accept/read 数据面仍未覆盖 |
| 网络快照世界 | 非法 IP、非本机 IP、地址族变化、快照抖动、采集错误、发布延迟 | 首登非法 `effective_ip`、运行时空配置保活、恢复补推完整快照 | fake snapshot、hook、manual scheduler、状态观测 | 已可用（最小实现） | fake collector / reader 与手动轮询已落地；发布版本号、消费侧统一观测和更复杂抖动剧本仍待补齐 |
| 控制连接世界 | `config.push` 前后断线、`config.ack` 晚到/重复/乱序、heartbeat 丢失/晚到、半关闭、旧连接残帧 | 热更新、session replacement、旧 session 晚到消息隔离 | fake transport、session harness、hook、manual scheduler | 已可用（最小实现） | 已有共享 `FrameIO` seam、scripted conn pair 与基础规则注入；跨进程控制接口、半关闭/残帧更复杂剧本仍待增强 |
| 统一状态观测 | active session、pending config、listener 集合、runtime issue、恢复模式、管理 API 门闩 | 所有主线场景 | 观测骨架、场景编排器 | 已可用（最小实现） | `frps` / `frpc` / listener fake / transport fake 已提供结构化观测；跨端总快照自动聚合仍待补齐 |
| 不变量断言 | 启动门闩、单 pending、静态 `冲突` 优先级、空配置恢复顺序、旧 session 隔离 | 所有进入矩阵的正式场景 | 断言库、观测骨架 | 已可用（最小实现） | 已有核心 helper 和最小单测；更丰富的 diff 输出、更多恢复/竞争态规则仍待补齐 |
| 场景编排 | 多 actor 交错、显式 barrier、显式采样、显式失败定位 | 双 session、双进程、外部占用、连续配置变更 | 场景编排器、上述全部 seam | 已可用（最小实现） | 已有 step runner 与 trace 骨架；跨进程 actor、联合注入动作和脚本原语仍待扩展 |

在上述能力之外，当前还必须额外标出“已知不可测或暂不可稳定制造”的空白区，避免误判已经覆盖：

- 仅靠真实 OS 行为才会出现的“瞬时端口释放窗口”不能算已覆盖；这类情况只有在 fake listener 能显式控制 `closeCalled` 与 `closeCompleted` 后才算可测。
- 仅靠真实网络时序才会出现的 TCP 半关闭、帧乱序、旧连接残帧晚到，不能算已覆盖；必须由 fake transport / harness 显式控制。
- 仅靠真实网卡/VPN 刷新时序才能出现的 `effective_ip` 短暂消失或地址族跳变，不能算已覆盖；必须由 fake snapshot seam 控制。
- 仅靠多次重跑或提高并发度“偶尔撞到”的双 pending、旧 ack 污染、双 session 交接窗口，不能算已覆盖；必须存在 barrier/hook 和统一编排脚本。

覆盖表后续使用规则固定如下：

- 任一计划进入测试矩阵的场景，先在覆盖表里找到它依赖的所有故障原语；找不齐，就不进入实现。
- 如果某个场景依赖的故障原语横跨 listener、snapshot、transport 三个世界，必须先确认场景编排器能把三者串起来，而不是默认“分别可测就等于联合可测”。
- 覆盖表中的 `Current Status` 只有在 seam 真正进入代码且能被最小验证场景驱动后，才能从“已定稿待实现”改成“已可用”；文档定稿本身不等于能力已落地。

第一版验收口径固定如下：

- 覆盖表必须覆盖当前端口冲突与热更新交互主线涉及的所有外部世界：启动门闩、listener、snapshot、transport、时间推进、状态观测、断言、编排。
- 任一后续场景在开工前都能明确知道自己依赖哪些能力、哪些能力已可用、哪些仍缺口。
- 如果后续某个关键场景在实现时才第一次暴露“缺注入点”，说明覆盖表不完整，需要先补表和补基建，再继续场景实现。

## 12. 稳定性补充层基线

稳定性补充后续统一按“确定性场景先通过，再上补充层回归”收口，`-race`、多 `GOMAXPROCS`、高频扰动、长稳 soak、双平台和资源压力都只能是第二层，而不是替代前置可测性基建：

- 确定性单场景失败时，禁止直接上 `-race` 或 soak 碰运气找原因；先修可测性。
- 补充层的职责是回答“在更真实、更高压、更长时间的运行条件下，前面已经收口的规则有没有被破坏”，不是回答“系统大概能不能跑”。
- 补充层中的每一类入口都必须继续复用统一状态观测和不变量断言，不能退回日志 grep。

第一版稳定性补充层固定为六条入口：

- `-race` 回归：
  - 目标：捕捉数据竞争、锁保护缺失、测试观测口本身的新竞态。
  - 做法：优先跑已确定性的核心场景子集，而不是直接全量随缘。
  - 通过口径：无 race report，且最终状态断言不变。
- 多 `GOMAXPROCS` 回归：
  - 目标：验证快慢 CPU、不同调度粒度下同一脚本得到同一状态结论。
  - 做法：固定同一场景脚本，至少覆盖 `1`、`2`、`4` 或等价分档。
  - 通过口径：步骤执行顺序可以不同，但 barrier/采样点的状态结论和不变量断言必须相同。
- 高频配置抖动回归：
  - 目标：验证连续 `RefreshGroup()`、连续快照切换、连续空配置/完整配置切换不会积累脏状态。
  - 做法：在 manual scheduler 下脚本化连续推多轮配置和快照变化，不靠真实高频 sleep。
  - 通过口径：无双 pending、无双活 listener、无旧 session 污染、最终可收敛。
- 长稳 soak 回归：
  - 目标：验证长时间轮询、heartbeat、反复恢复/失效后不会泄漏 goroutine、listener、session、frame 队列。
  - 做法：优先使用 fake time 驱动长时间逻辑周期；确需真实时间的部分必须有资源计数和失败阈值。
  - 通过口径：资源指标稳定在阈值内，最终断言仍成立，且失败时能指出哪类资源泄漏。
- 双平台回归：
  - 目标：验证同一剧本在 Windows 和 Linux/WSL 下保持相同业务结论。
  - 做法：统一复用同一脚本和断言，只允许底层实现差异，不允许业务结论差异。
  - 通过口径：最终 `ObservedState` 关键字段与不变量断言一致；若平台差异导致例外，必须先文档化并在脚本中显式分支。
- 资源压力回归：
  - 目标：验证大量 tunnel、频繁 bind/close、队列积压、频繁重连下不会提前失稳。
  - 做法：用 fake listener / fake transport / fake snapshot 放大资源量级，同时继续保留显式观测点。
  - 通过口径：资源上升受控、状态可收敛、不变量不断裂、失败时能定位具体瓶颈。

每一类补充层入口都必须有固定观测项和失败判定，不允许只写“跑一段时间看看日志”：

- `-race`：
  - 失败条件：任何 race report。
- 多 `GOMAXPROCS`：
  - 失败条件：同一场景脚本在不同调度配置下得到不同 `finalStatus`、不同恢复模式、不同 pending config 结论。
- 高频配置抖动：
  - 失败条件：出现双 pending、旧 ack 污染、listener 未回收、恢复顺序错乱、场景无法在限定步数内收敛。
- 长稳 soak：
  - 失败条件：goroutine、listener handle、延迟 frame、session 数量持续单调增长且不回落；或最终断言失败。
- 双平台：
  - 失败条件：平台间出现未文档化的业务差异。
- 资源压力：
  - 失败条件：资源达到阈值后无保护退化、场景死锁、关键状态不可观测或最终不收敛。

为了避免补充层重新退回非确定性，第一版规则还必须固定如下：

- 长稳 soak 优先用 fake time 跑逻辑长周期，只有资源释放和真实网络/进程生命周期必须依赖墙钟时，才允许少量真实等待。
- 多 `GOMAXPROCS` 与 `-race` 跑的是同一套已确定性场景脚本，不允许单独维护另一套“stress 专用脚本”。
- 高频配置抖动必须通过显式脚本声明每一次配置变化、每一次快照变化、每一次 frame 延迟，不允许依赖“高频随机 goroutine”。
- 资源压力回归必须继续采样结构化 `ObservedState` 和资源计数快照，不能只看进程是否还活着。

第一批建议纳入补充层的资源与健康指标固定如下：

- 服务端：
  - active session 数量
  - group slot 占用数
  - attach listener 数量
  - fake listener handle 未释放数
  - pending config 数量
  - runtime issue 数量
- 客户端：
  - active stream / udp session 数量
  - 当前连接尝试序号
  - 已应用快照版本与最后 ack 版本偏差
- 注入层：
  - 延迟 frame 队列长度
  - fake snapshot 剧本剩余步数
  - manual scheduler 待执行任务数
  - 未释放 barrier 数量
- 进程级：
  - goroutine 数
  - 文件句柄或 socket 句柄数量
  - 内存占用趋势

第一版验收口径固定如下：

- 任一补充层入口都必须复用同一套场景脚本、状态观测和不变量断言，不得退回日志驱动。
- 任一补充层失败都必须能指出是 race、平台差异、资源泄漏还是状态机不收敛，而不是一条笼统超时。
- 若某个补充层入口还需要大量 `sleep`、随机扰动或“多跑几次”，说明前置可测性仍不够，先补基建，不进入回归清单。

## 13. 可测性验收门槛

可测性验收后续统一按“先证明场景可重复、可观测、可定位，再允许进入正式测试矩阵”收口，任何做不到这一点的场景都必须回退到前置基建补能力：

- 可测性门槛是场景准入规则，不是建议项。
- 满足业务上“理论应该会发生”不算通过，必须满足“测试可以稳定控制、稳定观测、稳定断言、稳定失败”。
- 进入正式测试矩阵前，每个场景都必须回答三件事：
  - 这个场景如何稳定制造
  - 这个场景如何稳定判断通过/失败
  - 这个场景失败后如何快速定位

第一版准入门槛固定如下：

- 必须有显式场景脚本：
  - 不允许靠测试主体手写多处 goroutine 和临时 channel 即兴编排。
- 必须有显式 barrier 或显式 fake 控制：
  - 关键竞态点必须可卡住、可放行、可重复。
- 必须有统一结构化观测：
  - 至少能直接读到启动门闩、session/pending、listener、runtime issue、恢复模式、连接身份等关键状态。
- 必须有统一不变量断言：
  - 不能只看日志、错误字符串、端口是否能拨通。
- 必须有明确失败输出：
  - 至少能给出场景名、步骤名、actor、关键身份字段和状态 diff。
- 必须能单机连续复现：
  - 同一台机器连续跑若干次，得到相同状态结论。
- 必须能跨快慢机器保持同一结论：
  - 不要求执行时长完全相同，但要求 barrier/采样点上的状态结论一致。
- 必须能在有限时间内收敛：
  - 不能依赖“跑久一点也许会自己好”。

第一版明确不允许进入正式测试矩阵的场景类型如下：

- 依赖 `sleep` 窗口碰运气的场景。
- 依赖真实外部进程抢端口、真实网络抖动、真实网卡刷新，但没有对应 fake seam 的场景。
- 只能通过日志关键字、HTTP 拨测、端口探活侧面判断结果的场景。
- 失败时只能得到测试总超时，无法定位到 barrier、步骤或状态差异的场景。
- 同一脚本在 `GOMAXPROCS=1` 和默认配置下结论不同，但未文档化差异来源的场景。

场景进入正式矩阵前，建议固定执行以下验收清单：

1. 在单机上连续执行同一脚本多次，确认关键结论一致。
2. 在至少一组快机器和一组慢机器或等价调度配置下执行，确认关键结论一致。
3. 人为制造一次失败，确认失败输出能定位到具体步骤和状态差异。
4. 检查是否存在未释放 barrier、未清理 fake 资源、未消费 frame、未关闭 session。
5. 确认该场景依赖的故障原语都已在覆盖表中登记，且状态不是“未开始”。

为了让“通过/不通过”标准可执行，第一版验收结论建议固定为三档：

- `Ready`：
  - 场景可单机连续复现，可跨快慢机器保持结论一致，失败可定位，可纳入正式矩阵。
- `Blocked by Infra`：
  - 场景业务上重要，但缺 barrier、fake、观测或断言口；必须先回补基建。
- `Flaky / Rework Required`：
  - 现有脚本偶发、依赖时序碰撞、失败不可定位；不得进入正式矩阵。

可测性门槛与前面几层基建的边界固定如下：

- 故障注入覆盖表回答“能不能制造”。
- 场景编排基座回答“怎么稳定交错和采样”。
- 状态观测回答“能不能直接看到关键状态”。
- 不变量断言回答“怎么判断通过/失败”。
- 稳定性补充层回答“在更高压环境下是否仍然成立”。
- 可测性验收门槛负责把上述几层串成最终准入判断。

第一版验收口径固定如下：

- 任一进入正式测试矩阵的场景，都必须满足“可稳定制造、可稳定观测、可稳定断言、可稳定失败定位”四项条件。
- 任一不满足门槛的场景，都必须明确回流到哪一层基建补能力，而不是继续堆更多随机重试。
- 若后续出现“场景已经写完，但在 CI 上仍经常只能靠重跑过”的情况，默认视为门槛未通过，先回到基建层整改。
