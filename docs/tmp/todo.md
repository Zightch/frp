# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 继续逐步完成 `frps/internal/control` 的第一阶段重构，并把后续运行态能力扩展所需的稳定框架一起收口；当前第四刀 `config apply / runtime operation` 已完成并归档，接下来转入第五刀：把 public stream / UDP session / runtime I/O 的编排继续收口成更清晰的 `connection / session io` 边界，在不破坏当前跨平台稳定性基线的前提下，把“listener 启动后进入运行态的数据面”“连接级观测读面”“未来策略挂点”再往前拆开。
- 当前总目标覆盖的后续工作包括：
  - 在 `control` 内继续收紧配置编排、运行态编排、恢复路径、数据面 I/O 与观测面的边界，逐步把“状态存取”“动作计划”“动作执行”“运行态读面”分离开。
  - 为管理面后续补齐运行态能力预留结构，包括管理连接视图、查看所有连接、连接级操作入口，以及更清晰的 session / listener / stream / udp 观察面。
  - 为抓包、令牌桶限速和连接治理预留统一挂点，避免后续能力继续散落在 `tcp_bridge`、`udp`、`session`、`observe`、`connections` 这些入口里。
  - 保持客户端登录外部 UX 为单个 `key`，内部继续沿 `client_id + client_secret_hash` 模型收口，并为后续必要的签名/认证接口调整留出空间。
  - 上述后续工作分阶段推进，不在当前第五刀里一次性并做；当前子步骤只围绕 `runtime io / connection view` 边界展开。

子步骤：

- 1. 收紧 `stream/udp runtime io` 边界
  - 把 `handlePublicConnection`、`handlePublicUDPDatagram`、`copyPublicToClient`、session stream/udp 绑定与关闭路径继续收成更清晰的 `connection / session io` 入口。
  - 避免 `tcp_bridge.go`、`udp.go`、`session.go` 继续同时决定 session 状态迁移、runtime I/O 门闩与连接级动作。
- 2. 抽出会话级 connection 读面
  - 为管理面后续补齐“查看所有连接”“连接级操作”“运行态统计”准备稳定的 stream / udp / session 读模型。
  - 当前不直接实现管理 API / WebUI，只先整理内部边界。
- 3. 预留 connection policy / traffic hook surface
  - 为抓包、令牌桶限速、连接治理预留统一的 connection / stream / udp 挂点。
  - 避免未来能力继续直接散落在 `handlePublicConnection`、`copyPublicToClient`、`handlePublicUDPDatagram`、`bindPublicUDPSession`。
- 4. 保持当前稳定性基线
  - 不改变现有恢复判定、竞争窗口处理、`testhooks` 语义和当前单个 `key` 外部 UX。
  - 拆分后仍需通过当前 `frps` / `frpc` 测试、`frps/internal/control` 定向 `-race` 与 WebUI 构建。

当前轮边界：

- 当前第一刀 `sessionState` 结构拆分、第二刀 `runtime issue store` / `runtime registry` 收口、第三刀 `runtime coordinator` 收口、第四刀 `config apply / runtime operation` 收口都已完成并归档，本 `todo` 只保留剩余未完成工作。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不把管理登录认证模型和 `auth.json + challenge + session cookie` 一起改掉。
- 本轮不引入旧版本兼容层，不回退到旧 `token` 语义，也不提前修改协议签名。
- 本轮先做结构收束，不在 `todo` 中混入临时调试结论或已完成事项。

当前唯一下一步：

- 先把 `handlePublicConnection`、`handlePublicUDPDatagram`、`copyPublicToClient` 到 session stream / udp 状态的接缝收口成统一的 `connection / session io` 边界。
