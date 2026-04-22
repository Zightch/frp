# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成 `frps` 端口冲突检测的最小闭环，在已落地 `specific-specific` 的基础上补齐 wildcard 相关平台语义，再决定下一阶段扩展实现。

子步骤：

- 在管理面 wildcard 冲突检测落地后，把同一套冲突矩阵继续收口到控制面运行态入口与回归验证。

当前轮边界：

- 当前轮只处理 `frps` 公网监听端口冲突，不处理 `frpc` 本地目标端口冲突。
- `specific-specific` 静态冲突检测、隧道四态和 listener 启动失败到 `异常` 的运行态回传已经归档到 `docs/progress/2026-04-22.md`，后续不回退这一步。
- Linux / Windows wildcard 行为探针与 Linux 网卡扫描验证已经归档到 `docs/progress/2026-04-22.md`，后续不重复做同一轮平台采样。
- wildcard 静态规则（含 Lin/Win 差异和统一保守策略）已经归档到 `docs/progress/2026-04-22.md`，后续不回退到“只讨论规则”的阶段。
- 跨平台唯一方案（V1，固定冲突矩阵）已经写入 `docs/frps/technical-design.md` 并归档到 `docs/progress/2026-04-22.md`，后续实现不得偏离该矩阵。
- 管理面 wildcard 冲突检测已归档完成；下一步聚焦控制面运行态入口收口，不提前混入外部进程扫描矩阵或全量矩阵化执行。
- 按 `docs/workflow.md` 执行：每完成一个子步骤，先归档、检查 `.gitignore`、提交并重写 `todo`，然后停下等待确认。

当前唯一下一步：

- 按已归档的统一冲突矩阵，把冲突语义补到控制面入口（`ensureTunnelListeners()` 与 `effective_ip` 重绑路径）的运行态错误收口，并补对应回归测试。
