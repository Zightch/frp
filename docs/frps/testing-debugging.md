# frps 测试与调试文档

## 1. 当前测试目标

`frps` 当前测试重点是：

- 启动配置和数据库是否可正常进入运行态
- 管理认证是否符合 `auth.json` 模型
- `frpc` 登录与配置下发是否正确
- TCP/UDP 正向代理链路是否正确
- UDP idle cleanup 语义是否稳定

## 2. 当前自动化验证

### 2.1 Go 测试

常用命令：

```powershell
cd frps
go test ./...
```

当前重点覆盖：

- `internal/app`
  - SQLite 数据目录创建
  - MySQL DSN 透传
  - schema bootstrap 与校验
- `internal/auth`
  - 初始化
  - challenge
  - session
  - 删除 `auth.json` 后复位
- `internal/api`
  - 认证接口
  - 管理接口
- `internal/control`
  - token challenge/response
  - group slot
  - `config.push` / `config.ack`
  - listener 行为
  - TCP/UDP 协议错误路径
- `pkg/protocol`
- `pkg/transport`

### 2.2 Python e2e

仓库当前已固化的端到端脚本：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_tcp_single.py --scenario bad_token
python test/e2e_tcp_single.py --scenario disabled_group
python test/e2e_tcp_single.py --scenario disabled_tunnel
python test/e2e_tcp_single.py --scenario local_unavailable
python test/e2e_tcp_range.py
python test/e2e_udp_single.py --scenario happy_path
python test/e2e_udp_single.py --scenario idle_cleanup
python test/e2e_udp_range.py
python test/e2e_management_webui.py
python test/e2e_tcp_perf.py
```

覆盖范围：

- TCP 单端口主链路和负向场景
- TCP 连续范围映射
- UDP 单端口主链路
- UDP 空闲清理
- UDP 连续范围映射
- 管理认证、分组 CRUD、token 重置、隧道 CRUD
- 最小 TCP 健康压测

### 2.3 Python 平台探针（免编译）

用于快速验证 wildcard 绑定差异和 Linux 本机地址采集行为：

```powershell
python test/wildcard_bind_matrix.py --output test/tmp/wildcard-bind/windows.json
wsl -d ubuntu -u root bash -lc "python3 /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/wildcard_bind_matrix.py --output /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/tmp/wildcard-bind/linux.json"
wsl -d ubuntu -u root bash -lc "python3 /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/linux_network_snapshot_probe.py --output /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/tmp/wildcard-bind/linux-network-scan.json --expect-ip 192.168.8.2 --expect-ip 192.168.8.130"
```

说明：

- `wildcard_bind_matrix.py` 会输出 TCP/UDP 的 `wildcard-wildcard`、`wildcard-specific`、IPv4/IPv6 组合第二次绑定是否成功。
- `linux_network_snapshot_probe.py` 按 `frps/internal/system/network_collect.go` 的排序与去重规则产出 Linux 地址快照，并校验关键 IP 是否存在。
- 两个脚本均为 Python，无需临时编译。

### 2.4 测试做法

对于涉及状态机、并发、热更新、轮询恢复、会话切换和故障注入的测试，统一按下面做法执行：

1. 先做可测性基建，再做具体场景。
2. 先把关键竞态点变成“可阻塞、可放行、可注入、可重复”，再写故障测试、竞争态测试和压力测试。

测试前默认先准备以下能力：

- 关键路径测试钩子：首轮扫描完成、控制端口开放、管理 API 首次可见、`config.push`、`pending config`、`config.ack`、listener freeze/close/bind、runtime scan、session shutdown、本机网络快照刷新等位置要能被测试明确卡住和放行。
- 确定性并发编排：竞争态测试默认使用 barrier/hook 编排交错顺序，不以 `sleep` 或“多跑几次”作为主手段。
- 可控时钟：轮询周期、超时、重试、心跳等等待逻辑优先用 fake clock / manual ticker 驱动。
- 可控注入：listener/bind、网络快照、传输层、session 行为要能脚本化制造占用、释放、断线、重连、重复 ack、晚到 ack、乱序 ack、地址抖动等场景。
- 不变量断言：优先断言状态机结果；对当前端口冲突与热更新链路，至少包括“首轮扫描完成前不可登录”“任一时刻最多一个有效 pending config”“旧 session 晚到消息不能污染新 session”“静态 `冲突` 优先于 runtime `异常`”“空配置恢复必须先补推完整快照再恢复 listener”。

### 2.4.1 测试钩子骨架

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

测试时禁止把以下方式当作主验证：

- 依赖 CPU 快慢或调度偶然性触发竞态。
- 依赖长时间 `sleep` 窗口碰运气。
- 只看日志，不校验状态机不变量。

在可测性基建完成前，`-race`、高频扰动、多 `GOMAXPROCS`、长稳 soak 只作为补充捞漏手段，不作为通过依据。

### 2.4.2 可控调度骨架

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

### 2.4.3 listener 故障注入骨架

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

## 3. 当前手工调试入口

### 3.1 服务是否启动

```powershell
curl http://127.0.0.1:7080/healthz
curl http://127.0.0.1:7080/api/v1/auth/state
Test-NetConnection 127.0.0.1 -Port 7000
```

### 3.2 管理认证

优先检查：

- `frps/data/auth.json` 是否存在
- `key_hash` 是否为 `64` 位小写 hex
- 删除 `auth.json` 后 `/api/v1/auth/state` 是否回到 `initialized=false`

### 3.3 `frpc` 无法登录

优先检查：

- 分组是否启用
- token 是否正确
- 同分组是否已有在线客户端占用槽位

### 3.4 公网端口不可用

优先检查：

- `frpc` 是否已经完成登录并返回 `config.ack`
- 目标 tunnel 是否启用
- 端口是否真的被监听
- 本地目标是否可达

当前一个常见原因是：`config.ack` 之前 listener 不会启动。

### 3.5 UDP 行为

当前应按下面语义排查：

- `frps` 是 UDP 生命周期唯一裁决方
- 成功转发后立即刷新活跃时间
- 空闲约 `30s` 后由 `frps` 清理并发送 `udp.close`
- `frpc` 不做本地 idle timer

如果看到“30 秒轮询”这类表述，应以协议文档和当前实现为准，而不是旧文档或旧理解。

## 4. 当前最小测试环境

最小本地联调通常需要：

- 一个 `frps`
- 一个 `frpc`
- 一个本地 TCP echo server
- 一个本地 UDP echo server
- 一个外网侧 TCP/UDP 测试客户端

脚本默认会创建隔离工作目录和临时 SQLite 数据库，优先用脚本复现。

## 5. 常见故障

### 5.1 启动即退出

优先排查：

- `data/config.json` 是否合法
- SQLite 路径或 MySQL DSN 是否正确
- 现有库结构是否和内嵌 schema 一致

当前代码不会自动迁移旧 schema。

### 5.2 管理 API 返回 `409`

常见原因：

- 管理密钥未初始化
- challenge 已过期或已重放
- 重复初始化

### 5.3 MySQL 无法连接

优先排查：

- DSN 是否为驱动标准格式
- 数据库是否可达
- 用户是否有建表权限

当前不再支持 DSN 地址简写兼容。

## 6. 回归入口

仓库级命令矩阵见 [../regression-entry.md](../regression-entry.md)。
