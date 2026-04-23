# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成 `frps/internal/control` 第一阶段重构第六刀的剩余工作：把“决定执行什么动作”“真正执行动作”“对外读模型与内部写目标对齐”继续拉开，形成可直接承接后续管理面、查看所有连接、连接级操作、抓包、令牌桶限速和连接治理的稳定 runtime admin/action surface。
- 当前总目标仍保持以下边界：
  - 只做 `control` 内部结构收束，不直接开放管理 API / WebUI 按钮，不提前实现抓包或限速策略本身。
  - 继续保持客户端登录外部 UX 为单个 `key`，内部继续沿 `client_id + client_secret_hash` 模型收口；登录/签名接口调整留在当前总目标之外单独推进。
  - 不改变现有恢复判定、竞争窗口处理、`testhooks` 语义、管理登录模型，也不引入旧版本兼容层。

子步骤：

- 1. 抽出 runtime admin action / operation 边界
  - 为后续“关闭连接”“关闭 tunnel”“冻结/恢复 session”“触发连接级治理动作”预留统一内部执行入口。
  - 当前先整理内部动作边界，不直接开放管理 API / WebUI 按钮，也不提前实现抓包或限速策略。
- 2. 对齐观测读面与动作写面
  - 让 connection / session / tunnel 的读模型与动作目标共用稳定 ID 和基础元数据，避免未来管理层再自己做二次映射。
  - 为后续统计、抓包、限速和连接治理补齐更可持续的结构接缝。
- 3. 保持当前稳定性基线并完成第一阶段收尾
  - 不改变现有恢复判定、竞争窗口处理、`testhooks` 语义、管理登录模型和当前单个 `key` 外部 UX。
  - 第六刀完成后仍需通过当前 `frps` / `frpc` 测试、`frps/internal/control` 定向 `-race` 与 WebUI 构建，并完成归档提交，作为第一阶段结束点。

当前轮边界：

- 本 `todo` 只保留第六刀剩余未完成工作，不回填已完成步骤。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不把管理登录认证模型和 `auth.json + challenge + session cookie` 一起改掉。
- 本轮不引入旧版本兼容层，不回退到旧 `token` 语义，也不提前修改协议签名。
- 本轮最后一刀继续先做结构收束，不在 `todo` 中混入临时调试结论。

当前唯一下一步：

- 先基于现有 `runtime target / selector`，抽出统一的 `runtime admin action / operation` 边界，把 session / tunnel / connection 的动作入口从 live runtime 裸访问里收出来。
