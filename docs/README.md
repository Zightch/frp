# 文档索引

## 1. 阅读顺序

建议按下面顺序阅读：

1. [总体项目概述](project-overview.md)
2. [frps/frpc 协议文档](protocol.md)
3. `frps` 子项目文档
4. `frpc` 子项目文档

## 2. 平台级文档

- [总体项目概述](project-overview.md)
- [frps/frpc 协议文档](protocol.md)

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
- 协议文档负责定义 `frps/frpc` 之间的传输层分包格式和业务消息语义。
- `frps` 文档负责定义服务端架构、管理面、控制面、数据面、观测面。
- `frpc` 文档负责定义客户端连接、配置领取、工作流量转发与重连机制。
- 具体代码实现时，如果设计有变化，应优先同步更新对应子项目文档，而不是只修改总体概览。

## 5. 当前阶段

当前仓库尚未开始正式实现，因此这些文档的目标是：

- 先固定边界，避免后续开发时职责漂移。
- 先固定目录和模块划分，避免实现阶段反复重构。
- 先固定测试与联调流程，避免只写功能、不写验证。
