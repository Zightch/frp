# Managed FRP Platform

这是一个由服务端完全托管配置的多功能代理平台，包含两个 Go 子项目：

- `frps`：服务端核心，负责管理认证、分组、隧道、WebUI、`frpc` 控制面和当前已落地的 TCP/UDP 正向代理数据面。
- `frpc`：正向代理客户端，只需要服务器地址和分组登录 token，即可连接服务端并领取配置。

WebUI 是 `frps` 的一部分，使用 Node.js + Element 构建，编译后由 `frps` 托管静态资源；当前通过 HTTP 管理 API 维护持久化配置，运行时配置则在 `frpc` 登录时由 `frps` 下发快照。

文档入口：

- [总体项目概述](docs/project-overview.md)
- [文档索引](docs/README.md)
- [frps 文档](docs/frps/project-overview.md)
- [frpc 文档](docs/frpc/project-overview.md)
