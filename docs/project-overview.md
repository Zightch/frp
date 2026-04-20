# 项目概述

## 1. 项目定位

本仓库当前实现的是一个“服务端托管配置”的最小正向代理平台。

- `frps`：服务端，负责管理认证、管理 API、WebUI、`frpc` 控制面和公网 TCP/UDP 入口。
- `frpc`：轻量客户端，只接收 `--server` 和 `--token` 两个启动参数，从 `frps` 领取配置并执行本地转发。

当前主线只覆盖正向代理，不包含反向代理。

## 2. 当前已实现

- `frps` 零参数启动，固定读取当前工作目录下的 `data/config.json`。
- `frpc` 只接收 `--server` 和 `--token`。
- `frps/frpc` 协议固定为 `4` 字节长度前缀加二进制业务帧。
- 分组 token 固定为 `32` 位小写 hex `token_id` 加 `64` 位小写 hex `token_secret`。
- `frpc` 登录采用 challenge/response：`sha256(token_hash + nonce)`。
- 一个分组固定只允许 `1` 个在线 `frpc`。
- 登录成功后由 `frps` 下发首次 `config.push`，`frpc` 回 `config.ack`。
- `frps` 在 `config.ack` 后启动启用状态的 TCP/UDP 公网 listener。
- 已支持 TCP 单端口和连续端口范围映射。
- 已支持 UDP 单端口和连续端口范围映射。
- UDP 生命周期由 `frps` 裁决；任一路径有成功转发都会立即刷新活跃时间，最后一次活动结束后空闲约 `30s` 自动清理并下发 `udp.close`。
- 管理认证固定为本地 `auth.json` 单一管理密钥模型：只初始化一次，不做在线轮换；删除文件后服务端自动回到未初始化态。
- `auth.json` 删除检测已落地：认证管理器会按固定间隔轮询文件是否被删除，并清空旧 challenge 和旧会话。
- 管理面已具备最小 WebUI 和真实管理 API：
  - `/init`：一次性初始化管理密钥
  - `/login`：challenge 登录与管理会话恢复入口
  - `/`、`/proxy-groups`、`/tunnels`：统一管理壳层下的概览、分组管理和隧道管理
  - 管理壳层统一承接侧边导航、顶部认证状态、会话到期信息和全局错误提示
  - 页面只暴露当前真实已实现入口，不包装未来功能导航
  - 分组 CRUD
  - token 重置
  - 隧道 CRUD
- 存储层已支持 SQLite 和 MySQL 两种数据库。
- 启动时会自动建当前必需表，并对现有表结构做严格校验；不做 schema 迁移兼容。

## 3. 当前明确未实现

- 反向代理。
- 在线改库后主动推送到已在线 `frpc` 的热更新通道。
- 隧道入口 ACL 执行。
- WebSocket、在线连接注册表、实时速率页、日志页。
- 限速执行和抓包执行。
- 一个分组多个在线 `frpc`。
- 数据库 schema 迁移兼容层。

当前还存在但尚未进入真实执行链路的持久化字段或表：

- `proxy_groups.rate_limit`
- `tunnels.rate_limit`
- `tunnels.capture_enabled`
- `group_tunnel_ip_rules`

这些内容当前只保留在 schema 或管理返回体中，不代表已经实现对应运行时能力。

## 4. 当前运行模型

```text
Browser WebUI
  |
HTTP API
  |
frps management/api/auth
  |
SQLite / MySQL

frpc
  |
TCP control connection
  |
frps control
  |
TCP/UDP public listeners
  |
External clients
```

关键约定：

- 配置当前只在 `frpc` 登录阶段同步一次。
- `frps` 只在 `frpc` 确认配置后开放公网 listener。
- TCP/UDP 范围映射都按相同偏移规则计算目标本地端口。
- `frpc` 不做本地 UDP idle timer，只接受 `frps` 的 `udp.close`。

## 5. 当前仓库结构

```text
.
├── frps/
│   ├── cmd/frps/
│   ├── internal/api/
│   ├── internal/app/
│   ├── internal/auth/
│   ├── internal/config/
│   ├── internal/control/
│   ├── internal/logging/
│   ├── internal/storage/
│   ├── pkg/
│   ├── webui/
│   └── data/
├── frpc/
│   ├── cmd/frpc/
│   └── internal/
└── docs/
```

仓库根目录当前不是统一 Go 模块；`frps/` 和 `frpc/` 各自维护 `go.mod`。

## 6. 当前验证基线

当前已固定的最小验证包括：

- `go test ./...`（`frps/`）
- `go test ./...`（`frpc/`）
- `python test/e2e_tcp_single.py`
- `python test/e2e_tcp_range.py`
- `python test/e2e_udp_single.py`
- `python test/e2e_udp_range.py`
- `python test/e2e_management_webui.py`
- `python test/e2e_tcp_perf.py`

仓库级回归入口见 [regression-entry.md](regression-entry.md)。

## 7. 参考文档

- [frps/frpc 当前代码架构图](frps-frpc-current-architecture.md)
- [frps/frpc 协议文档](protocol.md)
- [frps 项目概览](frps/project-overview.md)
- [frps WebUI 管理面板基线](frps/webui-redesign-plan.md)
- [frps 技术设计](frps/technical-design.md)
- [frpc 项目概览](frpc/project-overview.md)
- [frpc 技术设计](frpc/technical-design.md)
