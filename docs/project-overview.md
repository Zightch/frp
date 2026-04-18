# 项目概述

## 1. 项目定位

本项目是一个服务端集中托管的多功能代理平台，目标是把传统 FRP 类工具的配置、接入、流量观测和运维能力统一到 `frps` 服务端管理。

平台包含两个 Go 子项目：

- `frps`：服务端核心，负责所有管理面、控制面、数据面和 WebUI。
- `frpc`：轻量客户端，只接收 `server` 和 `token` 两个启动参数，然后从服务端拉取该 token 所属分组的代理配置。

子项目详细文档见：

- [frps 项目概览](frps/project-overview.md)
- [frps 技术设计](frps/technical-design.md)
- [frps 开发文档](frps/development-guide.md)
- [frps 功能文档](frps/functional-spec.md)
- [frps 测试与调试文档](frps/testing-debugging.md)
- [frps/frpc 协议文档](protocol.md)
- [frpc 项目概览](frpc/project-overview.md)
- [frpc 技术设计](frpc/technical-design.md)
- [frpc 开发文档](frpc/development-guide.md)
- [frpc 功能文档](frpc/functional-spec.md)
- [frpc 测试与调试文档](frpc/testing-debugging.md)

本项目中的“正向代理”指 `frpc -> frps` 的内网穿透模式：客户端主动连接服务端 `7000` 端口，服务端在公网暴露 TCP/UDP 入口，并把流量转发到客户端所在内网。

本项目中的“反向代理”指完全由 `frps` 承担的服务端代理模式：外部请求进入 `frps`，再由 `frps` 转发到服务端配置的上游地址，不依赖 `frpc`。

## 1.1 当前实现状态和短期边界

截至 2026-04-18，当前已完成的是正向代理最小核心链路，不是完整平台能力：

- `frps` / `frpc` 已支持 token challenge/response 登录。
- `frps` 可从 SQLite/MySQL schema 中读取 `proxy_groups` / `tunnels` 运行数据。
- `frps` 可在 `config.ack` 后启动启用状态的 TCP 单端口 listener。
- `frpc` 可接收配置、处理 `stream.open` / `stream.data` / `stream.close`，并回连本地 TCP 目标。
- 极简 WebUI 首版已支持 `proxy_groups` / `tunnels` 最小 CRUD，不做登录和在线状态展示。
- 已通过 `test/e2e_tcp_single.py` 验证：
  - `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`
  - `happy_path`
  - `bad_token`
  - `disabled_group`
  - `disabled_tunnel`
  - `local_unavailable`
- 已通过 `test/e2e_tcp_perf.py` 建立最小 TCP 代码健康压测基线：
  - 高并发短连接稳定性
  - 上行传输正确性与方向性退化检查
  - 下行传输正确性与方向性退化检查

当前已明确暂不纳入上一轮最小链路的能力包括：

- 在线改库热更新
- ACL
- 多客户端扩展
- 反向代理
- UDP
- 端口范围
- 抓包与限速

当前短期目标已经转为代码健康压测基线：先在不扩产品功能的前提下，持续验证 `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机` 在压力下是否出现明确代码异常；吞吐和延迟数字只作为发现退化的辅助手段，避免在核心链路未验证前继续堆叠高级功能。

## 2. 能力范围

### 2.1 正向代理能力

正向代理由 `frpc` 接入 `frps` 完成，支持：

- TCP 隧道。
- UDP 隧道。
- 单端口映射。
- 端口范围映射，例如 `20000-20100 -> 127.0.0.1:30000-30100`。
- 按分组管理多个隧道。
- 分组 token 鉴权。
- 分组级 `frpc` 客户端接入源 IP 黑白名单。
- 分组级隧道入口源 IP 黑白名单，用于限制公网用户访问该分组下的隧道。
- 服务端下发配置，客户端不保存复杂代理配置。
- 分组、隧道执行配置热更新，在线 `frpc` 不需要重启。
- ACL 与限速策略热更新，仅在 `frps` 本地运行态生效。
- 隧道级限速。
- 在线连接观测。
- TCP 抓包与连接流量统计。

### 2.2 反向代理能力

反向代理只在 `frps` 内运行，不通过 `frpc`，支持：

- TCP 反向代理。
- HTTP 反向代理。
- HTTPS/SSL 入口。
- 基于域名的 HTTP Host 分发。
- 基于 TLS SNI 的 HTTPS 域名分发。
- 证书配置与后续自动证书扩展。
- 反向代理连接观测、限速与抓包。

反向代理不支持 UDP。

### 2.3 管理与观测能力

`frps` 需要提供完整管理面：

- WebUI 管理分组、token、隧道和反向代理规则。
- WebUI 管理分组级 `frpc` 接入黑白名单和隧道入口黑白名单。
- WebUI 修改正向代理执行配置后，后端立即同步到对应在线 `frpc`。
- WebUI 与后端保持 WebSocket 长连接。
- 前端实时展示在线客户端、在线隧道、在线 TCP 连接、传输速率、连接两端 IP。
- 支持按隧道查看连接，并在连接管理中主动断开某一条在线连接。
- 支持隧道级、分组级流量统计。
- 支持分组级、隧道级限速，并展示连接当前生效限速值。
- 支持对指定连接或隧道开启抓包。
- 支持查看运行日志、事件日志和告警事件。

## 3. 推荐仓库结构

```text
.
├── frps/
│   ├── cmd/frps/                 # frps 启动入口
│   ├── internal/api/             # REST API 与 WebSocket API
│   ├── internal/auth/            # 管理端登录、token 校验、权限控制
│   ├── internal/control/         # frpc 控制连接、心跳、配置下发
│   ├── internal/proxy/           # 正向代理与反向代理统一数据面
│   ├── internal/reverse/         # TCP/HTTP/HTTPS 反向代理规则和分发
│   ├── internal/tunnel/          # 分组、隧道、端口范围映射
│   ├── internal/traffic/         # 速率统计、限速、连接注册表
│   ├── internal/capture/         # 抓包、pcap 输出、过滤规则
│   ├── internal/storage/         # 数据持久化
│   ├── internal/eventbus/        # 后端事件总线，推送 WebSocket
│   ├── webui/                    # 后续复杂 WebUI 目录，当前首版可先内嵌在 management api
│   └── configs/                  # frps 示例配置
├── frpc/
│   ├── cmd/frpc/                 # frpc 启动入口
│   ├── internal/client/          # 连接、鉴权、心跳、配置同步
│   ├── internal/proxy/           # 本地 TCP/UDP 转发
│   └── configs/                  # frpc 示例配置，仅保留最小参数
├── pkg/
│   ├── protocol/                 # frps/frpc 共享协议、消息结构
│   ├── transport/                # 复用连接、加密、帧协议
│   ├── model/                    # 共享领域模型
│   └── limiter/                  # 通用限速组件
├── docs/
│   └── project-overview.md
└── README.md
```

`webui` 归属于 `frps`，因为它只服务于服务端管理，不应成为第三个独立子项目。

## 4. 核心架构

### 4.1 整体分层

```text
WebUI
  |
REST API + WebSocket API
  |
frps 管理面
  |
控制面 ------------------ frpc
  |
数据面
  |
公网 TCP/UDP/HTTP/HTTPS 入口
```

`frps` 内部建议拆成四层：

- 管理面：WebUI、REST API、WebSocket、权限、审计。
- 控制面：客户端注册、分组鉴权、心跳、配置版本同步、隧道启停。
- 数据面：TCP/UDP 转发、HTTP/HTTPS 反代、连接生命周期管理。
- 观测面：连接列表、速率统计、限速、抓包、事件推送。

### 4.2 frps 职责

`frps` 是平台核心，负责：

- 监听 `7000` 端口，接受 `frpc` 控制连接。
- 在 `frpc` 登录阶段校验该分组的客户端来源 IP 黑白名单。
- 为每个分组维护客户端在线状态。
- 为每个分组维护多个代理隧道。
- 根据配置动态监听公网端口或端口范围。
- 在分组、隧道执行配置变更后，把 `frpc` 需要的最新执行快照推送给在线 `frpc`。
- 在 ACL、限速等服务端策略变更后，只更新 `frps` 本地运行态。
- 在公网用户访问隧道入口时校验该分组的隧道来源 IP 黑白名单。
- 接收公网 TCP/UDP 流量并转发到对应 `frpc`。
- 管理不依赖 `frpc` 的 TCP/HTTP/HTTPS 反向代理。
- 维护全局连接注册表。
- 统计连接速率、总流量、连接双方地址。
- 执行限速策略。
- 对指定连接或隧道执行抓包。
- 将状态变化通过 WebSocket 推送到 WebUI。

### 4.3 frpc 职责

`frpc` 必须保持简单：

```text
frpc --server 1.2.3.4:7000 --token xxx
```

客户端启动后只做以下事情：

- 连接 `frps` 的 `7000` 控制端口。
- 使用 token 登录到代理分组。
- 维持心跳。
- 接收服务端下发的隧道配置。
- 收到新配置后原子替换运行时快照，不重启进程。
- 按服务端指令建立 TCP/UDP 工作连接。
- 将流量转发到本机或内网目标地址。
- 上报基础状态和错误事件。
- 不参与服务端限速执行，也不感知 `frps` 当前是否正在限速。

frpc 不提供本地 WebUI，不负责管理配置，不应该保存复杂业务规则。

## 5. 端口与入口设计

默认建议：

```text
7000  frpc 控制连接入口
7500  frps WebUI/API 入口
80    HTTP 反向代理入口，可配置
443   HTTPS 反向代理入口，可配置
N/A   正向代理公网端口，由隧道配置动态监听
```

公网入口分为三类：

- 动态 TCP/UDP 端口：正向代理隧道使用。
- 固定 TCP 入口：TCP 反向代理使用。
- HTTP/HTTPS 入口：HTTP Host 和 TLS SNI 域名分发使用。

端口冲突必须由 `frps` 统一检测，任何新增隧道或反代规则在生效前都需要完成端口占用校验。

## 6. 领域模型

### 6.1 分组 ProxyGroup

分组是正向代理的管理单位。

```text
ProxyGroup
- id
- name
- tokenId
- tokenHash
- enabled
- clientAccessMode: disabled | allowlist | denylist | allowlist_and_denylist
- clientAllowList
- clientDenyList
- tunnelAccessMode: disabled | allowlist | denylist | allowlist_and_denylist
- tunnelAllowList
- tunnelDenyList
- rateLimit
- createdAt
- updatedAt
```

一个分组可以包含多个隧道，但始终只允许一个在线 `frpc` 客户端。`frps` 控制面为每个分组维护固定客户端槽位；槽位已占用时，新客户端登录直接拒绝，不做顶号切换。管理界面不展示任何客户端数量配置项；请求里的额外字段不会参与处理，例如传入 `max_clients` 也只会被忽略，不再落库。

分组 token 采用固定长度拼接结构，不使用分隔符：

```text
token = token_id + token_secret
```

- `token_id` 是固定长度公开定位段，用于快速定位分组或 token 记录。
- `token_secret` 是固定长度高熵随机密钥，只在创建或重置时展示一次。
- 首版建议 `token_id` 使用 16 字节随机值并编码为 32 位小写 hex，`token_secret` 使用 32 字节随机值并编码为 64 位小写 hex。
- `frpc` 按固定长度从 `token` 中截取 `token_id`，登录第一步只发送 `token_id`。
- 服务端在分组记录中保存 `token_id + token_hash`，不保存明文 `token_secret`。
- `token_hash = sha256(token_secret)`，由客户端本地计算并由服务端在创建或重置 token 时持久化。
- 登录时服务端生成一次性临时盐，也就是 challenge nonce，客户端提交 `sha256(token_hash + challenge_nonce)`。
- 临时盐只保存在服务端内存运行态，必须有过期时间且只能使用一次。

分组 IP 黑白名单必须拆成两类：

#### 6.1.1 `frpc` 客户端接入黑白名单

这类规则用于限制哪些来源 IP 可以作为该分组的 `frpc` 客户端接入 `frps:7000`。

例如某分组 token 只允许公司出口 IP 登录：

```text
client allow:
- 203.0.113.10
- 198.51.100.0/24
```

当 `frpc` 携带该分组 token 连接 `7000` 时，`frps` 在 token 识别出分组后，必须校验该控制连接的来源 IP；若未通过则直接拒绝登录，不下发任何配置。

如果后续工作连接和控制连接分离，也应沿用同一套客户端接入策略，避免控制连接放行但工作连接可被其他 IP 冒用。

#### 6.1.2 隧道入口黑白名单

这类规则用于限制哪些公网来源 IP 可以访问该分组下的正向代理隧道入口。

例如某个分组下有 `20000 -> 192.168.1.10:22` 隧道，则外部用户连接 `frps:20000` 时，`frps` 会在建立转发前检查该用户的来源 IP。

IP 规则建议同时支持单 IP 和 CIDR：

```text
allow:
- 203.0.113.10
- 198.51.100.0/24

deny:
- 192.0.2.50
- 10.0.0.0/8
```

规则优先级建议为：

```text
deny 命中 -> 拒绝
allowlist 模式且 allow 未命中 -> 拒绝
其他情况 -> 放行
```

这样可以在白名单大范围放行时继续用黑名单屏蔽少量异常 IP。

两类规则的判定逻辑保持一致，但作用对象不同：

- `client*` 字段作用于 `frpc -> frps:7000` 的客户端接入。
- `tunnel*` 字段作用于公网用户访问该分组隧道监听端口的入口连接。

### 6.2 隧道 Tunnel

隧道属于某个分组。

```text
Tunnel
- id
- groupId
- name
- protocol: tcp | udp
- remotePort
- remotePortRangeStart
- remotePortRangeEnd
- localHost
- localPort
- localPortRangeStart
- localPortRangeEnd
- enabled
- rateLimit
- captureEnabled
```

端口范围映射要求远端范围和本地范围长度一致，例如：

```text
remote: 20000-20010
local:  30000-30010
```

则 `20003` 会映射到 `30003`。

### 6.3 反向代理 ReverseProxy

反向代理完全由服务端执行。

```text
ReverseProxy
- id
- name
- type: tcp | http | https
- listenPort
- domain
- tlsCertId
- upstreamHost
- upstreamPort
- enabled
- rateLimit
- captureEnabled
```

TCP 反代主要按监听端口分发。HTTP 按 Host 分发。HTTPS 优先按 SNI 选择证书和路由，再按 HTTP Host 做二次校验。

### 6.4 在线连接 ActiveConnection

所有 TCP 连接都进入统一连接注册表。

```text
ActiveConnection
- id
- sourceType: forward | reverse
- tunnelId
- reverseProxyId
- protocol
- clientIp
- clientPort
- serverIp
- serverPort
- upstreamIp
- upstreamPort
- openedAt
- rxBytes
- txBytes
- rxRate
- txRate
- rateLimit
- captureState
- closeReason
- closeRequestedBy
```

UDP 没有天然连接生命周期，建议按五元组维护短生命周期会话，超时后自动清理。

## 7. frps 与 frpc 通信协议

更具体的分包格式、业务帧头和消息类型定义，见 [frps/frpc 协议文档](protocol.md)。

### 7.1 连接类型

建议区分控制连接和工作连接：

- 控制连接：长连接，负责登录、心跳、配置同步、事件上报。
- 工作连接：承载具体 TCP/UDP 隧道数据。

第一阶段可以使用一个 TCP 长连接上实现多路复用，降低端口占用。协议层建议预留 stream id，后续支持更好的复用和限速控制。

### 7.2 基础消息

```text
ClientHello
- clientVersion
- token
- hostname
- os
- arch

ServerHello
- serverVersion
- configVersion
- heartbeatInterval

ConfigPush
- configVersion
- tunnels

ConfigAck
- configVersion
- appliedAt
- status

Heartbeat
- timestamp
- activeStreams

OpenStream
- streamId
- tunnelId
- protocol
- remotePort

CloseStream
- streamId
- reason

TrafficReport
- streamId
- rxBytes
- txBytes
- rxRate
- txRate
```

协议编码固定使用二进制业务帧。底层传输使用 `4` 字节长度前缀明确帧边界，业务层再用消息类型和定长字段定义登录、配置同步、逻辑连接、数据读写和关闭语义。

## 8. WebUI 与实时同步

WebUI 归属于 `frps` 管理面。当前极简首版已完成：不做登录，不引入独立前端工程，只用 `frps management api` 提供一个内嵌静态页面和最小 JSON CRUD API。

当前首版只覆盖：

- 分组列表、新增、编辑、删除。
- 隧道列表、新增、编辑、删除。
- 创建或重置分组 token 时只在当次返回原始 token，数据库仍只保存 `token_id + token_hash`。
- 改库后以“刷新可见、SQLite 查询可验证”为验收，不要求在线 session 热更新。
- 管理界面不展示任何 `maxclient` / `max_clients` 配置项。

当管理面复杂度真实上升后，再演进到独立前端工程。长期方案可以使用 Vue 3 + Element Plus。

后端提供：

- REST API：用于增删改查、登录、配置保存。
- WebSocket：用于实时状态、连接列表、速率、日志、抓包进度推送。

WebSocket 事件建议统一格式：

```json
{
  "type": "connection.updated",
  "version": 123,
  "timestamp": "2026-04-17T00:00:00Z",
  "payload": {}
}
```

前端页面建议包括：

- 仪表盘：在线客户端、活跃连接、总吞吐、告警。
- 分组管理：分组、token、客户端在线状态、`frpc` 接入黑白名单、隧道入口黑白名单。
- 隧道管理：TCP/UDP 隧道、端口范围、限速、抓包开关。
- 反向代理：TCP/HTTP/HTTPS、域名、证书、上游。
- 连接管理：所有在线 TCP 连接、双端 IP、速率、总流量、所属隧道。
- 连接管理支持按分组、隧道筛选，并允许管理员主动断开单条连接。
- 抓包中心：任务列表、下载 pcap、过滤条件。
- 系统设置：监听端口、管理员账号、存储配置、日志级别。

## 9. 流量管理设计

### 9.1 连接注册表

所有进入 `frps` 的 TCP 连接必须先注册，再开始转发：

```text
accept connection
-> create ActiveConnection
-> attach traffic counter
-> attach limiter
-> optional capture writer
-> start bidirectional copy
-> update registry on close
```

这样 WebUI 可以实时看到连接，而不是只能看到聚合统计。

连接注册表除了统计字段，还需要保留运行时关闭句柄和对应 stream id。这样管理员在 WebUI 选择一条连接后，`frps` 可以精确关闭该连接，并把 `closeReason` 标记为 `admin_terminated`。

`frpc` 客户端接入准入必须发生在登录成功之前：

```text
accept control connection on 7000
-> receive auth.begin with token_id
-> resolve source IP
-> locate group by token_id
-> match client IP access policy
-> reject or issue one-time challenge nonce
-> receive auth.finish with sha256(token_hash + challenge_nonce)
-> verify challenge response by constant-time compare
-> mark client online
-> push config
```

公网隧道入口连接的准入必须发生在注册和转发之前：

```text
accept connection
-> resolve source IP
-> match group tunnel IP access policy
-> reject or continue
-> create ActiveConnection
-> start forwarding
```

被黑白名单拒绝的连接不进入活跃连接列表，但应该记录拒绝事件并推送到 WebUI，便于管理员判断是 `frpc` 接入被拦截，还是公网用户访问隧道被拦截。

正向代理配置热更新链路建议为：

```text
save group/tunnel config from WebUI
-> validate conflict and build new runtime snapshot
-> apply listener diff in frps
-> increment group configVersion
-> push config.push to online frpc sessions
-> receive config.ack
-> mark session synced
```

要求在线 `frpc` 无需重启。端口、协议、启停、本地目标等会影响 `frpc` 执行路径的改动，先在 `frps` 完成监听器或运行态切换，再向 `frpc` 推送新的执行快照；ACL、限速等纯服务端策略只在 `frps` 本地热更新，不需要通过协议通知 `frpc`。

### 9.2 传输速率

建议在转发层封装 `io.Reader` / `io.Writer`：

- 每次读写累加字节数。
- 每秒计算窗口速率。
- 按连接、隧道、分组三层聚合。
- 通过事件总线推送给 WebSocket。

### 9.3 限速

限速建议支持三层：

- 全局限速。
- 分组限速。
- 隧道或反代规则限速。

限速只在 `frps` 数据面执行，`frpc` 不接收限速配置，也不感知服务端当前是否正在限速。最终生效值取最严格限制，记录到连接运行态用于展示。底层实现统一称为流量额度桶（quota bucket）：这里的“额度”仅指限速链路里可消费的字节预算，与 `frpc` 登录使用的分组 token 完全无关。限速目标不是秒级积累额度后一次性突发放行，而是由 `frps` 按单调时钟持续 pacing，尽量平滑地把速率从高值降到低值。TCP 在 `frps` 双向转发链路上按字节节流，UDP 在 `frps` 会话转发路径上按包大小和发送节奏限速。

### 9.4 抓包

抓包分两类：

- 连接级抓包：对某个在线连接抓取一段时间。
- 隧道级抓包：对某个隧道新产生的连接自动抓取。

输出建议使用 pcap 文件，记录：

- 抓包任务 id。
- 关联连接 id 或隧道 id。
- 开始时间、结束时间。
- 文件大小。
- 截断策略，例如最大 100MB 或最长 5 分钟。

第一阶段可以先实现 TCP payload 级记录和下载，后续再增强为标准 pcap 封装。

## 10. 存储设计

当前数据库支持范围限定为 SQLite 和 MySQL：

- SQLite 适合单机部署、开发环境和快速联调。
- MySQL 适合独立数据库部署和更标准的生产环境。
- 存储层从第一阶段开始就要兼容这两种数据库，但只做支持这两种数据库所必需的最小抽象。

- 管理员账号。
- 分组。
- 分组 tokenId、tokenHash。
- `frpc` 客户端接入黑白名单。
- 隧道入口黑白名单。
- 隧道配置。
- 反向代理配置。
- 证书配置。
- 审计日志。
- 抓包任务元数据。

在线连接、实时速率、客户端心跳不建议直接写数据库，应保存在内存中，并通过事件总线推送。

第一阶段不把 PostgreSQL 纳入实现范围，避免为未使用数据库提前增加复杂度。

## 11. 安全设计

基础安全要求：

- 分组 token 只保存 `tokenId + tokenHash`，不明文保存。
- token 登录使用一次性临时盐挑战，服务端校验 `sha256(tokenHash + challengeNonce)` 时必须使用常量时间比较。
- token 的私密段只在创建和重置时展示一次，不写入普通日志。
- 当前 challenge 方案下，`tokenHash` 是可用于生成登录响应的校验材料，必须按敏感凭据保护；如果数据库泄漏，攻击者可用泄漏的 `tokenHash` 伪造登录。
- WebUI 管理端使用独立管理员账号和 session/JWT。
- frpc 控制连接必须鉴权成功后才能领取配置。
- WebSocket 复用管理端鉴权。
- 抓包权限单独控制，避免普通管理员误抓敏感流量。
- HTTPS 证书私钥加密存储或限制文件权限。
- 管理 API 默认只监听内网地址，生产环境再通过 HTTPS 暴露。

建议后续增加：

- mTLS 客户端认证。
- 操作审计。
- IP 白名单。
- 登录失败限流。

## 12. MVP 实施阶段

### 阶段一：可用内网穿透核心

目标是先跑通最核心链路：

- `frps` 监听 `7000`。
- `frpc --server --token` 登录。
- 服务端创建分组和 TCP 隧道。
- TCP 单端口映射可用。
- WebUI 可以看到客户端在线和隧道状态。

截至 2026-04-18，当前已经完成的实际收口是：

- `frps` / `frpc` 最小 challenge 登录
- SQLite 冷启动配置读取
- TCP 单端口映射
- `config.push` / `config.ack`
- `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`
- 4 个最小负向场景
- 最小 TCP 代码健康压测基线

其中“WebUI 可以看到客户端在线和隧道状态”尚未进入这一轮完成定义，被明确后移。

### 阶段二：管理面成型

- WebUI 增删改查分组和隧道。
- REST API 完整化。
- WebSocket 推送客户端、隧道、连接状态。
- 端口冲突检测。
- 正向代理配置热更新，在线 `frpc` 无需重启。

当前已经完成这个阶段里的最小子集：

- 无登录的极简 WebUI
- `proxy_groups` / `tunnels` 最小 CRUD
- 通过 `frps management api` 写库

该最小子集不包含：

- WebSocket
- 在线热更新
- 连接管理
- 端口冲突检测完善

### 阶段三：协议扩展

- UDP 隧道。
- 端口范围映射。
- 工作连接多路复用。
- 连接断线重连。
- 客户端版本兼容检查。

### 阶段四：反向代理

- TCP 反向代理。
- HTTP Host 分发。
- HTTPS/SNI 分发。
- 证书管理。
- 反代规则热更新。

### 阶段五：流量管理

- 全量 TCP 连接注册表。
- 连接双端 IP 展示。
- 实时速率统计。
- 分组、隧道维度限速，连接维度展示生效限速值。
- 管理端可按连接 ID 强制断开单条在线连接。
- 抓包任务。
- pcap 下载。

## 13. 非目标

第一版不建议做：

- 分布式 frps 集群。
- 多租户计费系统。
- 自动 ACME 证书签发。
- 复杂 RBAC。
- 高级四层负载均衡。
- 全协议 DPI 分析。

这些能力可以预留接口，但不应该进入 MVP，否则会拖慢核心代理链路的稳定性。

## 14. 技术选型建议

服务端：

- Go 标准库网络栈作为基础。
- `net/http` 提供 API 和 WebSocket 升级入口。
- SQLite/MySQL 双支持作为存储方案，开发默认 SQLite，部署可选 MySQL。
- 内存事件总线驱动实时状态。
- 可插拔 limiter 和 capture 组件。

客户端：

- Go 单二进制。
- 无本地配置文件要求。
- 只依赖 server 与 token。
- 自动重连和配置版本同步。

前端：

- 当前极简 WebUI 首版可直接内嵌静态 HTML/CSS/JS，不引入 Node 或打包链。
- 当页面和状态复杂度真实上升后，再引入 Vue 3、Element Plus 和轻量状态管理。
- WebSocket 驱动实时数据刷新属于后续管理面成型阶段，不属于当前极简 CRUD 首版。
- REST API 负责配置型操作。

## 15. 关键设计原则

- 服务端托管配置：复杂规则只存在于 `frps`，`frpc` 尽量无状态。
- 控制面和数据面分离：登录、心跳、配置同步不能被大流量转发阻塞。
- 所有连接先注册再转发：这是实时观测、限速和抓包的基础。
- 热更新优先：WebUI 修改隧道或反代规则后，不应要求重启服务或在线 `frpc`。
- 限速只在服务端执行：`frps` 负责节流，`frpc` 只负责转发。
- 限速和抓包做成转发链路插件：避免代理核心逻辑被观测逻辑污染。
- MVP 先 TCP 后 UDP，先单端口后端口范围，先可用后高级观测。
- 字段及时收束：目标端只消费必要字段，不代表源端可以继续保留废字段；确认无用的字段要尽早从 schema、仓储、API、WebUI、测试数据和文档中移除，避免堆积。
- 字段变更必须落实：字段名、语义或归属边界一旦调整，源端写库、出参、测试 seed 和文档必须同步改到位；额外入参可以忽略，但旧字段不能继续由源端产出。
- `todo` 轮换信息隔离：`docs/tmp/todo.md` 只保留当前轮目标、边界、当前轮完成项和当前唯一下一步；进入新一轮前，必须先把上一轮收口同步到 `docs/progress.md`，当前轮 `todo` 不混放前几轮内容，只保留必要引用。
