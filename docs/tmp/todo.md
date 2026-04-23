# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 逐步完成 `frps/internal/control` 的第一阶段重构，并把后续运行态能力扩展所需的稳定框架一起收口；当前先落第一刀：拆分 `sessionState` 的配置态与运行态边界，在不破坏当前跨平台稳定性基线的前提下，为后续工作预留一致结构。
- 当前总目标覆盖的后续工作包括：
  - 在 `control` 内继续收紧配置编排、运行态编排、恢复路径和观测面的边界，并逐步引入 `runtime issue store`、`runtime registry` 这类稳定挂点。
  - 为管理面后续补齐运行态能力预留结构，包括管理连接视图、查看所有连接、连接级操作入口以及更清晰的 session / listener / stream 观察面。
  - 为抓包、限速和连接治理预留统一挂点，其中限速策略按令牌桶方向设计，不再沿用旧 `token` 语义。
  - 保持客户端登录外部 UX 为单个 `key`，内部继续沿 `client_id + client_secret_hash` 模型收口，并为后续必要的签名/认证接口调整留出空间。
  - 上述后续工作分阶段推进，不在当前第一刀里一次性并做；当前子步骤仍只围绕 `sessionState` 结构边界与访问接口收口展开。

子步骤：

- 1. 拆分 `sessionState` 的结构边界
  - 配置态只保留：
    - `current`
    - `pending`
    - `acked`
    - `recoveryMode`
  - 运行态只保留：
    - `done`
    - `frozen`
    - `generation`
    - `listeners`
    - `streams`
    - `udp`
- 2. 收口访问接口
  - 为配置态与运行态分别提供最小访问入口。
  - 避免 `refresh`、`runtime_scan`、`listeners`、`observe` 继续直接横跨读写杂糅状态。
- 3. 保持后续扩展挂点一致
  - 本步拆分时不直接引入 `runtime issue store` / `runtime registry` 实现。
  - 但结构命名必须允许下一步把它们平滑接入。
- 4. 保持当前稳定性基线
  - 不改变现有恢复判定、竞争窗口处理和跨平台稳定性修复语义。
  - 拆分后仍需通过当前 `frps` / `frpc` 测试与 WebUI 构建。

当前轮边界：

- 当前“客户端凭据命名与不兼容切换”以及后续“单个 `key` 外部 UX 收口”已完成并归档，本 `todo` 只保留剩余未完成工作。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不把管理登录认证模型和 `auth.json + challenge + session cookie` 一起改掉。
- 本轮先做结构收束，不在 `todo` 中混入临时调试结论或已完成事项。

当前唯一下一步：

- 先从 `sessionState` 的“配置态 / 运行态”数据结构拆分与访问接口收口开始。
