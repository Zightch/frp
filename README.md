# Managed FRP Platform

这是一个由服务端完全托管配置的多功能代理平台，包含两个 Go 子项目：

- `frps`：服务端核心，负责分组、隧道、反向代理、WebUI、实时连接观测、限速与抓包。
- `frpc`：正向代理客户端，只需要服务器地址和分组登录 token，即可连接服务端并领取配置。

WebUI 是 `frps` 的一部分，使用 Node.js + Element 构建，编译后由 `frps` 托管静态资源，并通过 WebSocket 与后端保持实时同步。

文档入口：

- [总体项目概述](docs/project-overview.md)
- [文档索引](docs/README.md)
- [frps 文档](docs/frps/project-overview.md)
- [frpc 文档](docs/frpc/project-overview.md)
