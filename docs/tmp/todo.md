# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 全量重构 `frps/internal/control`，建立一套新的 `control v2` 架构，直接替换现有“多锁共享状态 + 外部协同修补 + 后台扫描补洞”的模型。
- 新模型以显式状态、单写者 ownership 和统一 reconcile 为核心，不继续兼容旧的内部组织方式；等新实现跑通后，旧 `control/` 方案整体删除。

当前前提：

- 不做旧实现兼容，不保留旧的内部状态组织、刷新路径、恢复路径或并发模型。
- 不引入第三方状态机库；状态机只作为内部建模方式，采用仓库内自定义的 typed state/reducer/reconcile 方案。
- 新架构允许直接替换当前“同分组新连接被拒绝”的策略；如果新模型更合理，可以改成“新会话接管旧会话”。
- `runtime scan` 不再默认作为主恢复链路；新模型优先通过事件驱动和显式 reconcile 保持状态收敛，扫描最多降级为审计/兜底。
- 本轮聚焦 `frps/internal/control`；如需调整 `frpc` 或共享协议，只以支撑 `control v2` 为准，不为旧实现保留过渡层。

子步骤：

- 新架构总图收口：
  - 明确 `Server`、`Supervisor`、`SessionAgent`、`BindManager`、`RuntimeRepo` 的职责边界
  - 固定“谁拥有什么状态、谁执行什么 side effect、谁能修改谁”
- 运行态模型重建：
  - 把当前隐式状态拆成显式状态对象
  - 收口 `SessionPhase`、`RuntimePhase`、`BlockReason`、`ConfigEpoch`、`PendingConfigPush`
  - 删除旧的 `frozen + listeners.started + generation + 多 map` 拼装式状态表达
- 事件与动作模型：
  - 定义 session 内部事件
  - 定义 reducer 输出的 action 集合
  - 固定“状态转移纯计算，I/O 在状态机外执行”
- 全局协调层重建：
  - 新建 `Supervisor`
  - 统一接收登录成功、配置变更、本机网络变化、shutdown 等事件
  - 不再让 API 刷新、后台扫描和运行态恢复分别直接修改活跃 session
- listener ownership 重建：
  - 新建 `BindManager`
  - 让 TCP/UDP listener、端口 claim、冲突裁决、释放逻辑统一收口
  - 不再依赖“扫描 active runtime groups 再推断冲突”
- session actor 化：
  - 每个在线 group 只有一个 `SessionAgent`
  - 所有 session 可变状态在单 goroutine 内串行处理
  - TCP stream、UDP session、config push/ack、listener attach/detach 全部走 agent inbox
- 配置同步链路重建：
  - 以 `DesiredRuntime -> PendingConfig -> AckedRuntime` 为主线
  - 明确首次登录、在线热重载、空配置收缩、恢复后重放的统一流程
  - 用显式 epoch 护栏替代旧 runtime I/O 写保护散落逻辑
- 恢复路径重建：
  - 把管理面刷新、本机 IP 变化、listener 异常恢复统一纳入同一 reconcile 路径
  - 区分“重新绑定 runtime”“重新下发配置”“关闭会话”三类恢复动作
- 协议与接入层收口：
  - `server.go` 只保留 accept、transport 协商、认证、帧收发和事件投递
  - 把业务状态推进移出连接读循环
- 可观测性与测试重建：
  - 新观测接口直接反映显式状态
  - 单测以 reducer/reconcile 为主
  - 集成测试覆盖接管、热重载、listener 冲突、effective_ip 变化、TCP/UDP 转发与恢复
- 切换与删除：
  - 新实现接入 `app.App`
  - 用 `control v2` 替换旧 `control`
  - 删除旧的 runtime coordinator/scan/registry 拼装路径和过时代码

当前轮边界：

- 当前轮只承接 `frps/internal/control` 架构重写，不顺手扩展 WebUI、数据库 schema 或额外产品功能。
- 当前轮允许重写 `frps <-> frpc` 控制协议，但前提是确实有助于 `control v2` 收口，而不是为“未来可能有用”做抽象。
- 当前轮不引入插件系统、事件总线框架、DI 容器或通用 workflow engine。
- 当前轮不把 TCP/UDP 抽成一个“万能统一 tunnel runtime”；协议桥接仍按 TCP/UDP 分开实现。
- 当前轮状态机只做内部建模，不把 side effect 塞进第三方状态机回调。
- 当前已经进入 `controlv2` 代码骨架 + `app.App` 影子装配阶段：
  - 已接入真实 SQL repo 适配
  - 已在 `app.App` 内实例化 `controlv2.Server`
  - 管理面 `RefreshGroup` 已 fanout 到旧 `control` 和 `controlv2`
  - 但仍未替换旧 `control/` 的真实 `net.Listener` / transport / auth / 帧循环执行链路

当前状态：

- 现状分析已经完成，已确认当前 `control` 的主要复杂度来自多层状态叠加、资源 ownership 分散和异步恢复路径交叉。
- `docs/tmp/control-v2-design.md` 已完成，明确了 `Supervisor + SessionAgent + BindManager + RuntimeRepo + reducer/reconcile` 的目标总图、ownership、状态、事件、动作和实施顺序。
- `frps/internal/controlv2/session/` 第一批骨架已落地：
  - `state.go`
  - `events.go`
  - `actions.go`
  - `reducer.go`
  - `reconcile.go`
  - `reducer_test.go`
- `frps/internal/controlv2/session/agent.go` 已落地，当前 reducer/reconcile 已经接入最小 inbox、follow-up event 和 executor 壳层。
- `frps/internal/controlv2/supervisor.go` 已落地，当前已经有最小的 group/session agent 注册、会话接管、期望配置广播和网络变化广播骨架。
- `frps/internal/controlv2/bind/` 已落地最小 `BindManager` 内存实现与测试骨架。
- `frps/internal/controlv2/repo.go` 已收口为 `DesiredRuntimeRecord + RuntimeRepo`，不再丢失 `groupID`。
- `frps/internal/controlv2/sqlrepo.go` 已落地真实 SQL repo 适配，当前通过旧 `control.Repository` 投影为 `controlv2` 的 `DesiredRuntimeSnapshot`。
- `frps/internal/controlv2/server.go` 已补上 `HandleAuthenticatedClient`，当前已经可以从认证后的 `clientID` 直接加载期望配置并创建 session 初始状态。
- `frps/internal/app/app.go` 已接入 `controlv2.Server` 的最小装配，并把管理面 `RefreshGroup` fanout 到旧 `control` 和 `controlv2`。
- 当前环境已升级到可工作的 Go 新版本，`frps` 模块全量 `go test ./...` 已通过。
- 当前已经完成“状态建模起点 + 最小执行壳层 + 真实 repo 适配 + app 影子装配”这一步，下一步进入真实控制连接装配与旧 `control.Server` 拆线层。

当前唯一下一步：

- 拆开旧 `frps/internal/control/server.go` 的 transport/auth/会话读循环，把“认证完成后的会话建立”改成先进入 `controlv2.Server.HandleAuthenticatedClient`，再逐步把 `config.push/ack`、binding executor、TCP/UDP runtime 和网络变化迁到 `SessionAgent + Executor` 链路。
