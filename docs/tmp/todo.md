# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成 `frps` 端口冲突检测的最小闭环，在已落地 `specific-specific` 的基础上补齐 wildcard 相关平台语义，再决定下一阶段扩展实现。

子步骤：

- 基于平台实验结果，确定下一阶段静态冲突判定是否继续采用保守策略，以及矩阵/规则如何扩展到 wildcard。
- 在规则收口后，再继续实现 wildcard 相关冲突检测和后续矩阵化检查。

当前轮边界：

- 当前轮只处理 `frps` 公网监听端口冲突，不处理 `frpc` 本地目标端口冲突。
- `specific-specific` 静态冲突检测、隧道四态和 listener 启动失败到 `异常` 的运行态回传已经归档到 `docs/progress/2026-04-22.md`，后续不回退这一步。
- Linux / Windows wildcard 行为探针与 Linux 网卡扫描验证已经归档到 `docs/progress/2026-04-22.md`，后续不重复做同一轮平台采样。
- 当前下一步只做 wildcard 静态规则收口，不直接扩展实现，更不提前混入共享逻辑、外部进程扫描矩阵或全量矩阵化执行。
- 当前轮优先把管理面与控制面的统一保守策略定义清楚，再进入 wildcard 实现。
- 按 `docs/workflow.md` 执行：每完成一个子步骤，先归档、检查 `.gitignore`、提交并重写 `todo`，然后停下等待确认。

当前唯一下一步：

- 基于已归档的平台实验结果，收口 wildcard 的静态冲突判定规则（`wildcard-specific`、`wildcard-wildcard`、IPv4 / IPv6 交叉组合），并明确管理面与控制面的统一保守策略。
