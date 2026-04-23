# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 继续逐步完成 `frps/internal/control` 的第一阶段重构，并把后续运行态能力扩展所需的稳定框架一起收口；当前第三刀 `runtime coordinator` 已完成并归档，接下来转入第四刀：把 `config.push/config.ack`、`ensureTunnelListeners`、listener startup / probe 之间的编排继续收口成更清晰的 `operation / apply` 边界，在不破坏当前跨平台稳定性基线的前提下，把“配置生效”“runtime 动作”“观测读面”再往前拆开。
- 当前总目标覆盖的后续工作包括：
  - 在 `control` 内继续收紧配置编排、运行态编排、恢复路径和观测面的边界，逐步把“状态存取”“动作计划”“动作执行”分离开。
  - 为管理面后续补齐运行态能力预留结构，包括管理连接视图、查看所有连接、连接级操作入口，以及更清晰的 session / listener / stream / udp 观察面。
  - 为抓包、令牌桶限速和连接治理预留统一挂点，避免后续能力继续散落在 `config`、`listeners`、`observe`、`connections` 这些入口里。
  - 保持客户端登录外部 UX 为单个 `key`，内部继续沿 `client_id + client_secret_hash` 模型收口，并为后续必要的签名/认证接口调整留出空间。
  - 上述后续工作分阶段推进，不在当前第四刀里一次性并做；当前子步骤只围绕 `config apply / runtime operation` 边界展开。

子步骤：

- 1. 收紧 `config apply` / `runtime start` 边界
  - 把 `pushReloadConfig`、`handleConfigAck`、`allowTunnelRuntimeStart`、`ensureTunnelListeners` 之间的编排收成更清晰的内部 operation / apply 入口。
  - 避免 `config.go` 继续同时决定配置状态迁移和 runtime 恢复动作。
- 2. 整理 listener / probe / start 的 operation context
  - 为后续抓包、令牌桶限速、连接治理预留统一的 listener / stream / udp 挂点。
  - 避免未来能力继续直接散落在 `listenTCP`、`listenUDP`、`serveTunnelListener`、`handlePublicConnection`、`handlePublicUDPDatagram`。
- 3. 收口会话级观测与操作读面
  - 为管理面后续补齐“查看所有连接”“连接级操作”“运行态统计”准备稳定读模型。
  - 当前不直接实现管理 API / WebUI，只先整理内部边界。
- 4. 保持当前稳定性基线
  - 不改变现有恢复判定、竞争窗口处理、`testhooks` 语义和当前单个 `key` 外部 UX。
  - 拆分后仍需通过当前 `frps` / `frpc` 测试、`frps/internal/control` 定向 `-race` 与 WebUI 构建。

当前轮边界：

- 当前第一刀 `sessionState` 结构拆分、第二刀 `runtime issue store` / `runtime registry` 收口、第三刀 `runtime coordinator` 收口都已完成并归档，本 `todo` 只保留剩余未完成工作。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不把管理登录认证模型和 `auth.json + challenge + session cookie` 一起改掉。
- 本轮不引入旧版本兼容层，不回退到旧 `token` 语义，也不提前修改协议签名。
- 本轮先做结构收束，不在 `todo` 中混入临时调试结论或已完成事项。

当前唯一下一步：

- 先把 `pushReloadConfig`、`handleConfigAck` 到 `ensureTunnelListeners` 的接缝收口成独立的 `operation / apply` 边界。
