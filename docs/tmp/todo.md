# 当前轮 Todo

进度归档入口：

- `docs/progress/README.md`
- `docs/workflow.md`

## 当前总目标

- 完成第一轮“初步框架重构”：在不改变现有协议、鉴权、管理密钥模型、TCP/UDP single/range 行为和 UDP idle cleanup 语义的前提下，把 `frps` / `frpc` 从“功能已跑通但职责集中”的实现整理为一套极简、清晰、可继续演进的整体框架，先搭出稳定骨架，再迁移现有实现。

## 子步骤

1. 继续收束 `frpc` 客户端骨架
- 让 `internal/client` 从“登录与 session 基础设施已收住”继续演进到“target 解析、TCP bridge、UDP bridge ownership 清晰”的状态。
- 保持 `frpc` 仍只接收 `--server` 和 `--token`，不引入额外客户端配置体系。
- 继续沿用当前 UDP 生命周期边界：`frpc` 不做本地 idle timeout，只响应 `frps` 的 `udp.close`。

2. 提炼最小共享规则与回归入口
- 只抽取已经稳定且确实重复的进程内规则，例如 tunnel 目标解析、range 端口换算、session 收口约束、消息分发边界。
- 不在 `frps` 和 `frpc` 之间强行建立复杂共享框架；能各自简单实现的，优先各自保持简单。
- 同步整理单测和 Python e2e 入口，保证重构过程持续可回归。

3. 完成总回归与收束
- 执行 TCP/UDP single/range 关键回归，确认行为不变。
- 把稳定边界同步到正式文档和 `docs/progress/`。
- 每完成一个子步骤后检查 `.gitignore` 并提交稳定状态；本轮总目标完成后先归档，再清空当前 `todo`。

## 当前轮边界

- 本轮是“框架收束与等价迁移”，不是新功能轮。
- 已冻结蓝图见 `docs/initial-architecture-refactor-blueprint.md`；后续拆分必须遵守其中定义的目录边界、迁移顺序和非目标。
- 不改 wire protocol、challenge auth、管理密钥单次初始化模型、range 映射公式、UDP idle cleanup 裁决权。
- 不引入插件系统、事件总线、DI 容器、通用中间件链或“同时覆盖 `frps` / `frpc` 的大一统抽象层”。
- 组件边界必须服务于当前真实代码复杂度；优先小接口、小文件、清晰 ownership。
- 迁移顺序必须允许每一步都能独立回归和提交，避免长时间大分支堆积。

## 当前子步骤细分

- 当前正在推进：继续收束 `frpc` 客户端骨架，优先把 `frpc/internal/client` 中仍然散落在 `streams.go` / `udp.go` 里的目标地址解析和 range 端口换算收口为独立 ownership。
1. 先抽离 target 解析 ownership，目标是新建 `frpc/internal/client/targets.go`，统一承接 `tunnelByID`、`localTarget`、`localUDPTarget` 和共享的 range 端口换算入口。
2. target 边界稳定后，再把 `streams.go` 收紧为纯 TCP bridge 文件，不再混入 tunnel 查找和地址计算细节。
3. TCP bridge 边界稳定后，再把 `udp.go` 收紧为纯 UDP bridge 文件，不再混入 tunnel 查找和地址计算细节。
4. 每完成一个小点后立即补 `frpc` 定向回归，检查 `.gitignore`，同步 `docs/progress/`，并提交稳定状态。

## 当前唯一下一步

- 先只新建 `frpc/internal/client/targets.go`，把 `tunnelByID`、`localTarget`、`localUDPTarget` 以及它们直接依赖的 range 端口换算辅助迁移过去；本步不改 `stream.open` / `stream.data` / `stream.close`、`udp.open` / `udp.data` / `udp.close` 或 `applyConfigPush` 行为。
