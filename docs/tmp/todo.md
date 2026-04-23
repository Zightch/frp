# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 继续逐步完成 `frps/internal/control` 的第一阶段重构，并把后续运行态能力扩展所需的稳定框架一起收口；当前先落第二刀：把 `runtime issue store` 与 `runtime registry` 的集中读写边界收出来，在不破坏当前跨平台稳定性基线的前提下，为后续管理面、连接观察与连接治理提供统一挂点。
- 当前总目标覆盖的后续工作包括：
  - 在 `control` 内继续收紧配置编排、运行态编排、恢复路径和观测面的边界，逐步把“状态存取”和“恢复动作”分离开。
  - 为管理面后续补齐运行态能力预留结构，包括管理连接视图、查看所有连接、连接级操作入口，以及更清晰的 session / listener / stream / udp 观察面。
  - 为抓包、令牌桶限速和连接治理预留统一挂点，避免后续能力继续散落在 `refresh`、`runtime_scan`、`listeners`、`observe` 这些入口里。
  - 保持客户端登录外部 UX 为单个 `key`，内部继续沿 `client_id + client_secret_hash` 模型收口，并为后续必要的签名/认证接口调整留出空间。
  - 上述后续工作分阶段推进，不在当前第二刀里一次性并做；当前子步骤只围绕 `runtime issue store` / `runtime registry` 的内部收口展开。

子步骤：

- 1. 抽出 `runtime issue store`
  - 把 `Server.tunnelRuntimeIssues` 的读写和版本保护语义收口成独立内部边界。
  - 避免 `listeners`、`runtime_scan`、`refresh`、`observe` 各自直接拼接 issue 读写规则。
- 2. 抽出 `runtime registry`
  - 把 active session 的运行态快照、listener 归属和后续可扩展统计入口收成统一读模型。
  - 避免 `activeRuntimeGroups()`、`observeSessionState()` 等路径继续各自重复组装 runtime 视图。
- 3. 为后续管理/治理能力预留一致命名
  - 当前不直接实现“查看所有连接 / 连接级操作 / 抓包 / 限速”。
  - 但 `registry` / `store` 的结构命名必须允许这些能力平滑挂接。
- 4. 保持当前稳定性基线
  - 不改变现有恢复判定、竞争窗口处理、`testhooks` 语义和当前单个 `key` 外部 UX。
  - 拆分后仍需通过当前 `frps` / `frpc` 测试、`frps/internal/control` 定向 `-race` 与 WebUI 构建。

当前轮边界：

- 当前第一刀 `sessionState` 结构拆分已完成并归档，本 `todo` 只保留剩余未完成工作。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不把管理登录认证模型和 `auth.json + challenge + session cookie` 一起改掉。
- 本轮不引入旧版本兼容层，不回退到旧 `token` 语义，也不提前修改协议签名。
- 本轮先做结构收束，不在 `todo` 中混入临时调试结论或已完成事项。

当前唯一下一步：

- 先把 `Server.tunnelRuntimeIssues` 和 active session 运行态读面收口成独立的 `runtime issue store` / `runtime registry` 雏形。
