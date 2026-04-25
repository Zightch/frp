# 文档索引

## 1. 阅读顺序

建议按下面顺序阅读：

1. [总体项目概述](project-overview.md)
2. [工作流](workflow.md)
3. [进度归档](progress/README.md)
4. [当前轮 Todo](tmp/todo.md)
5. [仓库级回归入口](regression-entry.md)
6. [初步框架重构蓝图](initial-architecture-refactor-blueprint.md)
7. [frps/frpc 协议文档](protocol.md)
8. `frps` 子项目文档
9. `frpc` 子项目文档

## 2. 平台级文档

- [总体项目概述](project-overview.md)
- [工作流](workflow.md)
- [进度归档](progress/README.md)
- [仓库级回归入口](regression-entry.md)
- [初步框架重构蓝图](initial-architecture-refactor-blueprint.md)
- [frps/frpc 协议文档](protocol.md)

## 2.1 当前轮执行文档

- [当前轮 Todo](tmp/todo.md)

## 3. 子项目文档

### 3.1 frps

- [文档索引](frps/README.md)
- [设计文档](frps/design/README.md)
- [功能总览](frps/features/overview.md)
- [技术文档](frps/technical/architecture.md)
- [开发文档](frps/development/getting-started.md)

### 3.2 WebUI

- [概览](webui/overview.md)
- [视觉风格规范](webui/style-guide.md)
- [分组配置页](webui/proxy-groups.md)
- [仪表盘页面](webui/dashboard.md)

### 3.3 frpc

- [项目概览](frpc/project-overview.md)
- [技术设计](frpc/technical-design.md)
- [开发文档](frpc/development-guide.md)
- [功能文档](frpc/functional-spec.md)
- [测试与调试文档](frpc/testing-debugging.md)

## 4. 文档边界

- 总体项目概述负责描述平台级目标、统一术语和核心边界。
- `workflow.md` 负责定义统一执行流程、`todo` 轮换、归档时机和 `.gitignore` / 提交约定。
- `regression-entry.md` 负责收口仓库级最小 Go / Python 回归入口，不展开子项目内部测试设计细节。
- `progress/README.md` 负责说明进度归档目录和索引；`progress/*.md` 负责记录跨轮历史进度、当前停止线和已收口内容。
- `tmp/todo.md` 只负责当前轮未完成执行项，必须明确当前总目标、实现该目标的子步骤、当前轮边界和当前唯一下一步；不能混放前几轮信息，也不记录已完成内容；需要追溯历史时只引用 `progress/README.md` 和对应日期归档。
- 协议文档负责定义 `frps/frpc` 之间的传输层分包格式和业务消息语义。
- `frps` 文档现在按 `design/`、`features/`、`technical/`、`development/` 四类组织，统一从 `docs/frps/README.md` 进入。
- `docs/webui/` 目录负责固定 `frps` WebUI 的设计文档，包括最小基线、视觉风格、各页面设计规范。
- `frpc` 文档负责定义客户端连接、配置领取、工作流量转发与重连机制。
- 具体代码实现时，如果设计有变化，应优先同步更新对应子项目文档，而不是只修改总体概览。

## 5. 当前阶段

当前仓库已经进入实现阶段，统一执行流程见 [工作流](workflow.md)，跨轮进度和停止线统一记录在 [进度归档](progress/README.md)，当前轮执行任务统一记录在 [当前轮 Todo](tmp/todo.md)。

正式设计文档继续负责固定中长期设计边界，避免后续开发时职责漂移。
