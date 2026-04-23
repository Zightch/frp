# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成 `frps/internal/control` 第一阶段重构的最后一刀，把前五刀已经拆开的配置态、运行态、runtime I/O、连接读面继续收束成可供后续管理面直接复用的稳定“运行态管理动作边界”；当前第五刀 `connection / session io` 已完成并归档，接下来转入第六刀：把 session / tunnel / connection 的定位、观测与动作入口进一步统一，避免未来管理面、查看所有连接、连接级操作、抓包、令牌桶限速和连接治理再次回退到 `runtimeRegistry`、`session`、`tcp_bridge`、`udp` 的裸状态上直接拼接。
- 当前总目标覆盖的剩余工作包括：
  - 在 `control` 内把“定位目标对象”“决定执行什么动作”“真正执行动作”“输出稳定读面”继续拉开，形成面向后续管理面的统一 runtime admin/action surface。
  - 为后续补齐“查看所有连接”“关闭指定连接”“查看 tunnel / listener / session 运行态”“连接级抓包/限速/治理”预留稳定入口，但当前不直接实现这些功能本身。
  - 继续保持客户端登录外部 UX 为单个 `key`，内部继续沿 `client_id + client_secret_hash` 模型收口，并把登录/签名接口调整留在当前总目标之外单独推进。
  - 上述剩余工作集中到最后一刀完成；当前子步骤只围绕 `runtime admin / management action boundary` 展开。

子步骤：

- 1. 抽出 runtime target / selector 边界
  - 把后续管理面会用到的 session / tunnel / connection 定位逻辑，从 `runtimeRegistry`、`session.observeState()`、`tcp_bridge.go`、`udp.go` 的散点访问中收成统一目标模型。
  - 避免未来管理操作直接依赖 runtime map、listener map、stream map、udp session map 的即时结构。
- 2. 抽出 runtime admin action / operation 边界
  - 为后续“关闭连接”“关闭 tunnel”“冻结/恢复 session”“触发连接级治理动作”预留统一内部执行入口。
  - 当前先整理内部动作边界，不直接开放管理 API / WebUI 按钮，不提前实现抓包或限速策略。
- 3. 对齐观测读面与动作写面
  - 让 connection / session / tunnel 的读模型与动作目标共用稳定 ID 和基础元数据，避免未来管理层再自己做二次映射。
  - 为后续统计、抓包、限速和连接治理补齐更可持续的结构接缝。
- 4. 保持当前稳定性基线并完成第一阶段收尾
  - 不改变现有恢复判定、竞争窗口处理、`testhooks` 语义、管理登录模型和当前单个 `key` 外部 UX。
  - 第六刀完成后仍需通过当前 `frps` / `frpc` 测试、`frps/internal/control` 定向 `-race` 与 WebUI 构建，并完成归档提交，作为第一阶段结束点。

当前轮边界：

- 当前第一刀 `sessionState` 结构拆分、第二刀 `runtime issue store` / `runtime registry` 收口、第三刀 `runtime coordinator` 收口、第四刀 `config apply / runtime operation` 收口、第五刀 `connection / session io` 收口都已完成并归档，本 `todo` 只保留最后一刀未完成工作。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不把管理登录认证模型和 `auth.json + challenge + session cookie` 一起改掉。
- 本轮不引入旧版本兼容层，不回退到旧 `token` 语义，也不提前修改协议签名。
- 本轮最后一刀仍先做结构收束，不在 `todo` 中混入临时调试结论或已完成事项。

当前唯一下一步：

- 先把面向管理面的 session / tunnel / connection 定位与动作入口，从 `runtimeRegistry` / `session` 的裸访问进一步收成统一的 `runtime admin / management action` 边界。
