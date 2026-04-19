# 当前轮 Todo

进度归档入口：

- `docs/progress/README.md`
- `docs/workflow.md`

## 当前总目标

- 完成第一轮“初步框架重构”：在不改变现有协议、鉴权、管理密钥模型、TCP/UDP single/range 行为和 UDP idle cleanup 语义的前提下，把 `frps` / `frpc` 从“功能已跑通但职责集中”的实现整理为一套极简、清晰、可继续演进的整体框架，先搭出稳定骨架，再迁移现有实现。

## 子步骤

1. 收束 `frps` 服务端骨架
- 把 `internal/app`、`internal/api`、`internal/control` 的职责重新梳理为清晰装配层和服务层。
- 将控制面内部至少拆出登录/鉴权、配置快照与会话、TCP bridge、UDP bridge / session runtime 等最小角色。
- 先完成边界和调用关系，再做等价迁移，不改现有外部行为。

2. 收束 `frpc` 客户端骨架
- 把 `internal/client` 拆成连接/重连、登录握手、配置快照、TCP stream bridge、UDP session bridge 等最小角色。
- 保持 `frpc` 仍只接收 `--server` 和 `--token`，不引入额外客户端配置体系。
- 继续沿用当前 UDP 生命周期边界：`frpc` 不做本地 idle timeout，只响应 `frps` 的 `udp.close`。

3. 提炼最小共享规则与回归入口
- 只抽取已经稳定且确实重复的进程内规则，例如 tunnel 目标解析、range 端口换算、session 收口约束、消息分发边界。
- 不在 `frps` 和 `frpc` 之间强行建立复杂共享框架；能各自简单实现的，优先各自保持简单。
- 同步整理单测和 Python e2e 入口，保证重构过程持续可回归。

4. 完成总回归与收束
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

- 当前正在推进：开始把 `frps/internal/control/data_plane.go` 中的 TCP stream 生命周期逻辑迁到 `tcp_bridge.go`，继续收束 listener / tcp bridge / udp bridge 的文件边界。
1. 先只迁移 `handleStreamOpened`，保持 request id 校验、`stream.opened` 状态分支和 ready 信号语义不变。
2. 再迁移 `handleStreamData`，保持 `stream.data` 帧校验、public conn 写入失败关闭路径和 `sendStreamClose` 调用条件不变。
3. 再迁移 `handleStreamClose` 与 `sendStreamClose`，保持关闭原因编码、initiator 和错误回写路径不变。
4. 最后评估 `copyPublicToClient`、`publicStream` 结构与 `shutdownSession` 的归属，决定是否继续拆出更完整的 TCP bridge 收口。

## 当前唯一下一步

- 先只把 `(*Server).handleStreamOpened` 迁移到 `frps/internal/control/tcp_bridge.go`；不移动 `handleStreamData`、不移动 `handleStreamClose`、不移动 `sendStreamClose`、不移动 `copyPublicToClient`、不移动 `publicStream`、不移动任何 `udp.*` 或 `shutdownSession` 逻辑，不改调用点，不改行为。
