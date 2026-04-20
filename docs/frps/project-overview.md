# frps 项目概览

## 1. 项目定位

`frps` 是整个平台的核心子项目，负责以下四类职责：

- 管理面：WebUI、REST API、WebSocket、管理密钥鉴权、配置管理。
- 控制面：`frpc` 登录、分组鉴权、心跳、配置版本同步、客户端状态维护。
- 数据面：TCP/UDP 正向代理、TCP/HTTP/HTTPS 反向代理、端口监听、连接转发。
- 连接管理与观测面：在线连接、实时速率、双端 IP、限速、抓包、日志、事件推送。

本项目是“服务端完全托管”的设计，因此复杂配置、访问策略、限速、抓包、反向代理规则都只存在于 `frps`，而不是下沉到 `frpc`。

## 1.1 当前实现状态

截至 2026-04-19，`frps` 已经落地的当前主线能力包括：

- `cmd/frps` 启动入口，零参数启动并固定读取当前工作目录下的 `data/config.json`
- `internal/config` 基础配置加载、默认值和校验
- `internal/logging` 结构化日志
- `internal/api` 管理端 HTTP 服务，当前已提供 WebUI 静态资源托管、健康检查、管理认证、`proxy_groups` / `tunnels` 最小 CRUD 和 token 重置
- 管理认证固定为本地 `auth.json` 单一管理密钥模型：只初始化一次，删除 `auth.json` 后自动回到未初始化态
- `internal/control` 控制端口监听、token challenge/response 登录、心跳、配置下发和配置确认
- `internal/control` 已落地 TCP/UDP 单端口与连续范围数据面，`config.ack` 后启动公网 listener
- `internal/control` 可处理 TCP `stream.open` / `stream.opened` / `stream.data` / `stream.close`
- `internal/control` 可处理 UDP `udp.open` / `udp.data` / `udp.close`，并由 `frps` 统一做空闲 `30s` cleanup
- 分组禁用、隧道禁用、本地目标不可达等最小失败路径已经有确定性行为
- `internal/storage/sql.go` 统一数据库封装，支持 `*sql.DB` / `*sql.Tx`、结构化查询结果和事务
- SQLite/MySQL 启动期建连、内嵌 schema bootstrap 与严格表结构校验
- 已通过 `test/e2e_tcp_single.py` 验证 `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`
- 已通过 `test/e2e_tcp_range.py` 验证 TCP range 偏移映射与 TCP 单端口回归
- 已通过 `test/e2e_udp_single.py` 验证 `Python 外网 UDP 客户端 <-> frps <-> frpc <-> Python 内网 UDP 服务`
- 已通过 `test/e2e_udp_range.py` 验证 UDP range 偏移映射与 UDP 单端口回归
- 已通过 `test/e2e_management_webui.py` 验证管理认证、`auth.json` 删除重置、分组 CRUD、token 重置和隧道 CRUD
- 已通过 `test/e2e_tcp_perf.py` 建立最小 TCP 代码健康压测基线

当前已经完成的主线是：管理认证最小闭环、TCP/UDP 单端口与连续范围正向代理最小闭环，以及最小 Python e2e/压测基线。当前仍未进入已完成主线的范围包括在线改库热更新、ACL、反向代理、抓包、限速和完整观测面。

## 2. 目标

`frps` 需要同时满足以下目标：

- 作为 `frpc` 的统一接入点，默认监听 `7000`。
- 作为正向代理公网入口，动态暴露 TCP/UDP 端口。
- 作为反向代理网关，对外提供 TCP/HTTP/HTTPS 入口。
- 作为运维控制台后端，承载 WebUI、API 和实时事件推送。
- 作为流量观测中心，展示当前所有在线 TCP 连接、速率、两端 IP 与抓包状态。
- 在分组、隧道执行配置变更后，把最新正向代理执行快照即时同步到在线 `frpc`。
- 在 ACL、限速等服务端策略变更后，只更新 `frps` 本地运行态。
- 支持管理员按连接 ID 强制断开指定在线连接。

## 3. 范围

### 3.1 在范围内

- 分组管理。
- token 鉴权。
- `frpc` 客户端来源 IP 黑白名单。
- 正向代理公网入口来源 IP 黑白名单。
- TCP/UDP 正向代理。
- TCP/HTTP/HTTPS 反向代理。
- 端口范围映射。
- 隧道级、分组级限速。
- 连接级、隧道级抓包。
- 正向代理配置热更新。
- 单连接断开控制。
- WebSocket 驱动的实时状态同步。
- 审计日志与拒绝事件记录。

### 3.2 不在第一阶段范围内

- 分布式多节点 `frps` 集群。
- 多租户结算与计费。
- 自动 ACME 证书签发。
- 复杂 RBAC。
- L7 高级策略引擎。

## 4. 核心对象

`frps` 管理的核心对象包括：

- `ProxyGroup`：正向代理的分组单元。
- `Tunnel`：属于某个分组的 TCP/UDP 隧道。
- `ReverseProxy`：服务端独立执行的反向代理规则。
- `Certificate`：HTTPS 证书与私钥元数据。
- `ActiveConnection`：运行期连接注册表项。
- `CaptureTask`：抓包任务。
- `AuditLog`：后台操作和拒绝事件日志。

## 5. 外部接口

### 5.1 网络入口

- `7000`：`frpc` 控制连接入口。
- `7500`：WebUI、REST API、WebSocket 管理入口。
- `80/443`：HTTP/HTTPS 反向代理入口。
- 动态端口：正向代理公网入口。
- 可配置固定 TCP 端口：TCP 反向代理入口。

### 5.2 管理接口

- REST API：配置增删改查、管理密钥初始化与挑战登录、证书上传、抓包任务管理、在线连接管理。
- WebSocket：在线状态、速率、连接列表、日志和抓包进度的实时推送。

当前代码中已实现的基础健康检查包括：

- `GET /`
- `GET /healthz`
- `GET /readyz`
- `GET /api/v1/healthz`

当前代码中已额外实现：

- 管理认证初始化与 challenge 登录
- `proxy_groups` 最小 CRUD
- `proxy_groups/{id}/token` token 重置
- `tunnels` 最小 CRUD

当前固定边界：

- 管理密钥只初始化一次，不做在线轮换；重置路径固定为删除 `auth.json`
- 当前不做 WebSocket、连接管理、抓包、限速或反向代理管理页
- 当前 WebUI 只管理 `proxy_groups` 和 `tunnels`

## 6. 与 frpc 的关系

`frps` 是控制端，`frpc` 是执行端。关系边界如下：

- `frps` 决定分组、隧道、限速、访问控制策略。
- `frpc` 只负责领取配置并执行本地转发。
- `frps` 负责把执行配置变更推送到在线 `frpc` 并跟踪配置同步状态。
- 限速只在 `frps` 数据面执行，`frpc` 不感知服务端限速状态。
- `frps` 负责判断是否允许某个 `frpc` 来源 IP 登录。
- `frps` 负责判断某个公网用户是否允许进入某个隧道。
- `frps` 负责记录全局连接状态和推送事件。
- UDP session 生命周期由 `frps` 统一裁决：`sessionId` 由 `frps` 分配，空闲超时由 `frps` 触发并通过 `udp.close` 通知 `frpc`。

## 7. 当前目录

```text
frps/
├── cmd/frps/
├── internal/api/
├── internal/auth/
├── internal/app/
├── internal/config/
├── internal/control/
├── internal/logging/
├── internal/storage/
├── pkg/
├── webui/
└── data/
```

目录设计原则：

- 以当前已落地代码为准，不按尚未出现的目录预留抽象位。
- 管理面、控制面、数据面尽量按当前真实边界拆分，避免互相污染。
- 协议与传输层共享代码放在 `frps/pkg/`，服务端内部实现留在 `frps/internal/`。

## 8. 启动流程

当前实现中的启动顺序如下：

1. 零参数启动并读取当前工作目录下的 `data/config.json`。
2. 初始化日志。
3. 按数据库类型打开 SQLite/MySQL 连接。
4. 执行内嵌 schema bootstrap、版本检查和表结构校验；校验失败立即退出。
5. 初始化管理 API。
6. 初始化 `frpc` 控制连接监听器。
7. 进入最小运行态。

后续随着控制协议、仓储层和真实代理链路落地，再在该顺序上继续扩展运行态加载和监听器初始化。

## 9. 核心原则

- 配置由服务端统一托管。
- 热更新优先，不依赖服务重启。
- 正向代理配置修改后，不依赖 `frpc` 重启。
- 连接相关动作统一归入连接管理，而不是散落在观测页面。
- 所有 TCP 连接先注册，再转发。
- 访问控制发生在业务链路最前面。
- 流量观测和抓包作为转发链路插件实现。
- 目标端只读取必要字段，不构成源端长期保留冗余字段的理由；确认无用字段后，要尽早从 `frps` 的 schema、仓储、API、WebUI、测试数据和文档中移除。
- 字段名、语义或边界发生变化时，`frps` 源端实现必须同步落实；允许忽略额外入参，但不允许继续写出、返回或文档化已经废弃的旧字段。
