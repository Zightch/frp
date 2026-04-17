# frps 技术设计

## 1. 设计目标

`frps` 的技术设计需要同时满足四个要求：

- 代理链路稳定，转发逻辑简单可控。
- 管理逻辑和流量逻辑边界清晰。
- 运行时状态可实时观测。
- 配置修改后可热更新生效。

## 2. 分层架构

```text
WebUI
  |
REST API / WebSocket
  |
Management Layer
  |
Control Layer <----> frpc
  |
Data Plane
  |
Observability Layer
  |
Storage / Runtime State
```

### 2.1 Management Layer

负责：

- 管理员登录和会话。
- 分组、隧道、反代、证书、抓包任务管理。
- 配置校验、端口冲突检测、热更新下发。
- 审计日志写入。

### 2.2 Control Layer

负责：

- `frpc` 登录与 token 校验。
- 分组客户端来源 IP 黑白名单校验。
- 心跳维护。
- 配置版本同步。
- 客户端在线状态维护。
- 工作连接或多路流的生命周期管理。

### 2.3 Data Plane

负责：

- TCP/UDP 正向代理转发。
- TCP 反向代理。
- HTTP Host 分发。
- HTTPS/SNI 分发。
- 公网监听器动态启停。

### 2.4 Observability Layer

负责：

- 连接注册表。
- 实时速率统计。
- 连接级/隧道级/分组级限速。
- 抓包写出。
- 日志事件和状态事件广播。

## 3. 进程内模块划分

建议模块职责如下：

### 3.1 `internal/api`

- 暴露 REST API。
- 维护 WebSocket 会话。
- 将后台事件映射为前端可消费的消息。

### 3.2 `internal/auth`

- 管理员认证。
- API/WS 鉴权中间件。
- `tokenId` 定位与加盐 hash 校验。

### 3.3 `internal/control`

- `frpc` 登录协议。
- 心跳处理。
- 配置推送。
- 工作流打开/关闭。

### 3.4 `internal/tunnel`

- 分组、隧道、端口范围的配置模型。
- 正向代理规则编排。
- 公网监听器和分组关系维护。

### 3.5 `internal/reverse`

- TCP/HTTP/HTTPS 反向代理规则。
- Host/SNI 路由。
- 证书选择与热更新。

### 3.6 `internal/proxy`

- 双向拷贝。
- 本地连接与远程连接装配。
- TCP/UDP 统一转发抽象。

### 3.7 `internal/traffic`

- 连接注册表。
- 字节计数。
- 速率计算。
- 限速器绑定。

### 3.8 `internal/capture`

- 抓包会话管理。
- pcap 文件写出。
- 截断策略。

### 3.9 `internal/eventbus`

- 后台统一事件总线。
- WebSocket 订阅与推送。
- 事件版本号递增。

### 3.10 `internal/storage`

- SQLite/MySQL 存储适配。
- 配置实体的仓储接口。
- 审计与抓包元数据持久化。

## 4. 数据模型

## 4.1 `ProxyGroup`

```text
ProxyGroup
- id
- name
- token_id
- token_salt
- token_hash
- enabled
- max_clients
- rate_limit
- client_access_mode
- tunnel_access_mode
- created_at
- updated_at
```

与分组关联的子表：

- `group_client_ip_rules`
- `group_tunnel_ip_rules`
- `tunnels`

建议不要把 IP 列表直接存成单字段 JSON，第一阶段就按规则表建模，后续检索和审计更稳定。

## 4.2 `group_client_ip_rules`

```text
- id
- group_id
- action        # allow | deny
- cidr
- comment
- created_at
```

作用时机：

- `frpc` 连接 `7000` 后。
- token 识别出分组后。
- 登录成功前。

## 4.3 `group_tunnel_ip_rules`

```text
- id
- group_id
- action        # allow | deny
- cidr
- comment
- created_at
```

作用时机：

- 外部用户连接正向代理公网入口后。
- 建立 `ActiveConnection` 前。

## 4.4 `Tunnel`

```text
- id
- group_id
- name
- protocol      # tcp | udp
- remote_type   # single | range
- remote_start
- remote_end
- local_host
- local_start
- local_end
- enabled
- rate_limit
- capture_enabled
- created_at
- updated_at
```

端口范围要求：

- `remote_end - remote_start == local_end - local_start`
- 范围不可与现有正向代理端口重叠
- 范围不可与固定反向代理端口冲突

## 4.5 `ReverseProxy`

```text
- id
- name
- type          # tcp | http | https
- listen_addr
- listen_port
- server_name
- tls_cert_id
- upstream_host
- upstream_port
- enabled
- rate_limit
- capture_enabled
- created_at
- updated_at
```

规则约束：

- TCP 反代靠监听端口唯一定位。
- HTTP 反代靠 `listen_port + host` 唯一定位。
- HTTPS 反代靠 `listen_port + sni` 唯一定位。

## 4.6 `ActiveConnection`

```text
- id
- source_type       # forward | reverse
- protocol          # tcp | udp_session
- group_id
- tunnel_id
- reverse_proxy_id
- client_ip
- client_port
- server_ip
- server_port
- upstream_ip
- upstream_port
- opened_at
- closed_at
- rx_bytes
- tx_bytes
- rx_rate
- tx_rate
- rate_limit
- capture_state
```

`ActiveConnection` 只保存在内存运行态，不直接写数据库。关闭后将摘要写入审计或统计表。

## 4.7 `CaptureTask`

```text
- id
- source_type
- tunnel_id
- reverse_proxy_id
- active_connection_id
- file_path
- state
- max_bytes
- max_duration_sec
- started_at
- finished_at
```

## 5. 网络设计

## 5.1 `frpc` 控制入口

- 默认监听 `0.0.0.0:7000`
- 长连接模式
- 登录成功后进入心跳和配置同步状态

推荐流程：

```text
accept
-> read ClientHello
-> parse tokenId and secret
-> load group by tokenId
-> validate salted token hash
-> match group client ip rules
-> reject or accept
-> register session
-> push ServerHello + ConfigPush
```

## 5.2 正向代理公网入口

每个正向代理隧道对应一个或一组公网监听器。

TCP 流程：

```text
accept public tcp connection
-> resolve tunnel by listen port
-> match group tunnel ip rules
-> create ActiveConnection
-> open stream to frpc
-> proxy both directions
-> close and flush stats
```

UDP 流程：

```text
receive datagram
-> resolve tunnel by listen port
-> match group tunnel ip rules
-> bind or reuse udp session
-> forward datagram to frpc
-> update session ttl
```

## 5.3 反向代理入口

TCP：

- 固定监听端口。
- 一条规则对应一个上游。

HTTP：

- 共享监听端口。
- 按 `Host` 匹配。

HTTPS：

- TLS 握手阶段按 `SNI` 选择证书和路由。
- HTTP 层可再次校验 `Host`。

## 6. 控制协议设计

共享协议建议下沉到 `pkg/protocol`。

第一阶段可采用 JSON 帧，字段明确、易调试；后续高频消息再切换为二进制编码。

### 6.1 基础消息

```json
{
  "type": "client.hello",
  "requestId": "uuid",
  "payload": {
    "token": "group-token",
    "clientVersion": "0.1.0",
    "hostname": "host-a",
    "os": "linux",
    "arch": "amd64"
  }
}
```

建议定义的消息类型：

- `client.hello`
- `server.hello`
- `config.push`
- `heartbeat`
- `stream.open`
- `stream.ready`
- `stream.close`
- `traffic.report`
- `event.report`

### 6.2 配置版本

每次分组隧道配置变更后：

- `frps` 递增 `configVersion`
- 通过控制连接推送 `config.push`
- `frpc` 应答已接收版本
- 未应答则重试或强制重连

## 7. 热更新设计

`frps` 的配置更新分三类：

### 7.1 管理面配置

例如管理员密码、日志等级、WebUI 端口。通常需要服务级重载，必要时可以允许重启。

### 7.2 正向代理配置

例如分组、隧道、IP 规则、限速。要求热更新：

- 新增端口立即监听。
- 修改端口先校验冲突，再重建监听器。
- 删除端口先停止新接入，再等待旧连接自然关闭或强制回收。

### 7.3 反向代理配置

例如域名、证书、上游地址。要求热更新：

- HTTP 路由表原子替换。
- HTTPS 证书表原子替换。
- TCP 固定端口监听器按规则重建。

## 8. 事件总线与 WebSocket

事件总线建议统一事件模型：

```json
{
  "type": "connection.updated",
  "version": 1024,
  "timestamp": "2026-04-17T09:00:00Z",
  "payload": {}
}
```

建议事件类型：

- `client.online`
- `client.offline`
- `group.updated`
- `tunnel.updated`
- `reverse.updated`
- `connection.opened`
- `connection.updated`
- `connection.closed`
- `capture.updated`
- `audit.created`
- `reject.created`

前端只订阅 WebSocket，不直接读取内存状态；状态列表仍通过 REST 拉首屏，后续由 WS 增量更新。

## 9. 流量统计与限速

### 9.1 字节统计

在代理链路的读写边包装统计器：

- 每次读写累计 `rx_bytes` 和 `tx_bytes`
- 以 1 秒窗口计算 `rx_rate` 和 `tx_rate`
- 定期向事件总线广播增量

### 9.2 限速优先级

建议三层同时支持：

- 全局限速
- 分组限速
- 隧道或反代规则限速

最终生效值取最严格值。

### 9.3 UDP 统计

UDP 以会话维度记录：

- 源 IP
- 源端口
- 目标端口
- 最后活动时间
- 收发字节数

## 10. 抓包设计

第一阶段先支持 TCP。

建议设计：

- 每个连接挂接一个可选 capture writer
- 写出格式优先标准 pcap
- 支持最大时长和最大文件大小
- 任务结束后写入元数据表并可下载

抓包权限要求：

- 只有具备抓包权限的管理员可创建任务
- 抓包行为必须落审计日志

## 11. 存储设计

第一阶段即支持 SQLite 和 MySQL。

- SQLite 用于单机、开发和快速联调。
- MySQL 用于独立数据库部署。
- 存储层只抽象到支撑 SQLite/MySQL 双支持所需的最小层级。

存储层需要区分：

- 配置型数据：分组、隧道、反代、证书、管理员
- 审计型数据：操作日志、拒绝事件、抓包任务
- 运行态数据：在线连接、实时速率、客户端会话

数据库设计约束：

- 表结构优先使用 SQLite 和 MySQL 的公共能力。
- 首版避免依赖 JSON 列、触发器、生成列、数据库枚举等方言特性。
- 数据库差异尽量收敛在 `internal/storage`，不向业务层扩散。

运行态数据放内存，不能让数据库成为数据面瓶颈。

## 12. 并发与生命周期

关键并发对象：

- 分组运行态
- 隧道监听器集合
- 反代路由表
- 活跃连接注册表
- WebSocket 会话集合

建议使用：

- 原子替换只读快照，用于高频读场景
- 分片锁或细粒度锁，避免全局大锁
- context 控制监听器、连接、抓包任务生命周期

## 13. 安全要求

- token 采用 `tokenId.secret` 结构。
- 服务端只保存 `token_id + token_salt + token_hash`。
- token 校验比较必须使用常量时间算法。
- 管理员密码只存 hash。
- 私钥独立存储并限制权限。
- 控制连接登录前先做最小解析，避免被恶意输入拖垮。
- 对 `7000`、API、WS 都要做基础限流。
- 所有拒绝事件要可审计。

## 14. 推荐实现顺序

1. 存储模型和运行态模型。
2. `7000` 控制连接与 token 登录。
3. 单端口 TCP 正向代理。
4. 管理 API 和最小 WebUI。
5. 实时连接列表和速率统计。
6. UDP 与端口范围映射。
7. 反向代理。
8. 抓包和高级限速。
