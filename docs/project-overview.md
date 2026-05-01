# 项目概述

## 1. 项目定位

本仓库当前实现的是一个“服务端托管配置”的最小正向代理平台。

- `frps`：服务端，负责管理认证、管理 API、WebUI、`frpc` 控制面和公网 TCP/UDP 入口。
- `frpc`：轻量客户端，只接收 `--server` 和 `--key` 两个启动参数，从 `frps` 领取配置并执行本地转发。

当前主线只覆盖正向代理，不包含反向代理。

## 2. 当前已实现

- `frps` 零参数启动，固定读取当前工作目录下的 `data/config.json`。
- `frpc` 只接收 `--server` 和 `--key`。
- `frps/frpc` 协议固定为 `4` 字节长度前缀加二进制业务帧。
- `proxy_group` 登录 `key` 固定为 `32` 位小写 hex `client_id` 加 `64` 位小写 hex `client_secret`。
- `frpc` 登录采用 challenge/response：`sha256(client_secret_hash + nonce)`。
- 一个 `proxy_group` 固定只允许 `1` 个在线 `frpc`。
- 登录成功后由 `frps` 下发首次 `config.push`，`frpc` 回 `config.ack`。
- `frps` 在 `config.ack` 后启动启用状态的 TCP/UDP 公网 listener。
- 管理面命中运行态字段且 `proxy_group` 在线时，会复用现有 `config.push / config.ack` 触发整组热重载。
- 已支持 TCP 单端口和连续端口范围映射。
- 已支持 UDP 单端口和连续端口范围映射。
- UDP 生命周期由 `frps` 裁决；任一路径有成功转发都会立即刷新活跃时间，最后一次活动结束后空闲约 `30s` 自动清理并下发 `udp.close`。
- 管理认证固定为本地 `auth.json` 单一管理密钥模型：只初始化一次，不做在线轮换；删除文件后服务端自动回到未初始化态。
- `auth.json` 删除检测已落地：认证管理器会按固定间隔轮询文件是否被删除，并清空旧 challenge 和旧会话。
- 管理 API 已具备最小闭环，WebUI 当前主线为：
  - `frps` 按 `webui.dist_dir` 托管静态目录
  - 支持按 `webui.path_prefix` 挂到子路径，并同步提供同前缀下的健康检查与管理 API 别名
  - 前端技术栈收口为 `Node.js + Vue 3 + Element Plus`
  - 已实现管理密钥初始化、challenge 登录、`proxy_group` 管理、登录 `key` 重置和隧道管理
  - 当前主管理页已经接入 `effective_ip` 下拉、`proxy_group` / 隧道状态展示和一次性 `key` 展示弹窗
- 存储层已支持 SQLite 和 MySQL 两种数据库。
- 启动时会自动建当前必需表，并对现有表结构做严格校验；不做 schema 迁移兼容。

## 3. 当前明确未实现

- 反向代理。
- 隧道入口 ACL 执行。
- WebSocket、在线连接注册表、实时速率页、日志页。
- 限速策略管理、限速执行和抓包执行。
- 一个 `proxy_group` 多个在线 `frpc`。
- 数据库 schema 迁移兼容层。

当前配置明确分成两层：

- 持久化配置：管理 API / WebUI 写入 SQLite / MySQL 中的 `proxy_groups`、`tunnels`。
- 运行时配置：`frpc` 登录或在线热重载时，`frps` 从数据库读取持久化配置并构造内存里的 `GroupRuntime` / `ConfigSnapshot`，后续 listener 启停和实际转发只消费这份运行时快照。

当前已确认的后续限速业务模型改为独立的 [限速策略设计](frps/design/rate-policy.md)。当前正式 schema 中已经不再保留旧的 `proxy_groups.rate_limit` 字段。

抓包相关控制当前未实现；如果后续引入，只应属于运行时配置层，不应再落成 `tunnels` 表字段。

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

- 配置在首次登录和后续在线热重载阶段都复用整组 `config.push / config.ack` 同步。
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
- `go test -tags testhooks ./internal/control/...`（`frps/`）
- `go test -tags testhooks ./...`（`frps/`，发布前补跑）
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
- [开发原则](development-principles.md)
- [frps/frpc 协议文档](protocol.md)
- [frps 文档索引](frps/README.md)
- [frps WebUI 最小基线](webui/overview.md)
- [frps 架构与模块](frps/technical/architecture.md)
- [frpc 项目概览](frpc/project-overview.md)
- [frpc 技术设计](frpc/technical-design.md)
