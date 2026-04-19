# 文档索引

## 1. 阅读顺序

建议按下面顺序阅读：

1. [总体项目概述](project-overview.md)
2. [当前进度](progress.md)
3. [当前轮 Todo](tmp/todo.md)
4. [frps/frpc 协议文档](protocol.md)
5. `frps` 子项目文档
6. `frpc` 子项目文档

## 2. 平台级文档

- [总体项目概述](project-overview.md)
- [当前进度](progress.md)
- [frps/frpc 协议文档](protocol.md)

## 2.1 当前轮执行文档

- [当前轮 Todo](tmp/todo.md)

## 3. 子项目文档

### 3.1 frps

- [项目概览](frps/project-overview.md)
- [技术设计](frps/technical-design.md)
- [开发文档](frps/development-guide.md)
- [功能文档](frps/functional-spec.md)
- [测试与调试文档](frps/testing-debugging.md)

### 3.2 frpc

- [项目概览](frpc/project-overview.md)
- [技术设计](frpc/technical-design.md)
- [开发文档](frpc/development-guide.md)
- [功能文档](frpc/functional-spec.md)
- [测试与调试文档](frpc/testing-debugging.md)

## 4. 文档边界

- 总体项目概述负责描述平台级目标、统一术语和核心边界。
- `progress.md` 负责记录跨轮历史进度、当前停止线和已收口内容。
- `tmp/todo.md` 只负责当前轮未完成执行项，必须明确当前总目标、实现该目标的子步骤、当前轮边界和当前唯一下一步；不能混放前几轮信息，也不记录已完成内容；需要追溯历史时只引用 `progress.md`。
- 协议文档负责定义 `frps/frpc` 之间的传输层分包格式和业务消息语义。
- `frps` 文档负责定义服务端架构、管理面、控制面、数据面、观测面。
- `frpc` 文档负责定义客户端连接、配置领取、工作流量转发与重连机制。
- 具体代码实现时，如果设计有变化，应优先同步更新对应子项目文档，而不是只修改总体概览。

## 5. 当前阶段

当前仓库已经进入实现阶段，跨轮进度和停止线统一记录在 [当前进度](progress.md)，当前轮执行任务统一记录在 [当前轮 Todo](tmp/todo.md)。

任一任务完成后先归档到 `progress.md`；当前总目标完成后，清空当前 `todo`，下一轮再写入新的总目标和子步骤。

正式设计文档继续负责固定中长期设计边界，避免后续开发时职责漂移。
