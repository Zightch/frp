# frps 功能总览

`frps` 当前是仓库里的服务端核心，负责：

- 读取 `data/config.json`
- 打开数据库并校验 schema
- 准备证书资产运行时
- 启动本机网络快照服务
- 托管管理 API 与 WebUI
- 接收 `frpc` 控制连接并下发整组配置
- 维护 TCP/UDP 公网 listener 与数据转发

## 当前已交付范围

- 管理认证：初始化、challenge 登录、会话校验、退出
- WebUI：初始化、登录、分组管理、隧道管理、证书资产管理
- 分组管理：`name`、`effective_ip`、`enabled`、登录 `key` 重置
- 隧道管理：TCP/UDP 单端口和范围映射
- 控制面：`client_id + client_secret` challenge/response 登录、心跳、`config.push / config.ack`
- 在线热重载：整组冻结、整组完整快照下发、`ack` 后 listener 重建
- 证书资产：上传、粘贴、生成、编辑名称/备注、删除影响分析、下载选项和下载产物

## 当前明确不承诺

- 反向代理
- tunnel 级 ACL 执行
- WebSocket 实时态
- 连接列表和连接级管理
- 限速执行
- 抓包执行
- 证书绑定到入口监听器

证书绑定属于下一阶段设计，见 [../design/certificate-binding.md](../design/certificate-binding.md)。

## 当前功能文档

- [管理面与 WebUI](./management-surface.md)
- [分组与隧道管理](./group-and-tunnel-management.md)
- [控制面与数据面](./control-and-data-plane.md)
- [证书资产](./certificate-assets.md)
