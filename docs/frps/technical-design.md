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

- 管理密钥初始化、挑战登录和会话。
- 分组、隧道、反代、证书、抓包任务管理。
- 配置校验、端口冲突检测、热更新下发。
- 单连接断开、隧道级连接回收等连接管理入口。
- 审计日志写入。

### 2.2 Control Layer

负责：

- `frpc` 登录与 token 校验。
- 分组客户端来源 IP 黑白名单校验。
- 心跳维护。
- 配置版本同步。
- 配置版本应答跟踪。
- 客户端在线状态维护。
- 工作连接或多路流的生命周期管理。
- 管理端发起的 stream 强制关闭控制。

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
- 连接生效限速值计算与展示，全局/分组/规则级限速执行。
- 抓包写出。
- 日志事件和状态事件广播。

## 3. 进程内模块划分

建议模块职责如下：

### 3.0 当前已落地模块

截至 2026-04-18，下面这些模块已经有第一阶段实现：

- `cmd/frps`：启动入口、参数解析和应用生命周期接线
- `internal/config`：JSON 配置、默认值和校验
- `internal/logging`：`slog` 日志初始化
- `internal/api`：最小管理端 HTTP 服务和健康检查
- `internal/control`：token challenge/response、配置下发、配置确认和最小 TCP 单端口数据面
- `internal/storage`：数据库对象封装
- `internal/app`：数据库打开、schema bootstrap/校验、服务启动和关闭编排

其余模块目前仍处于设计阶段。

### 3.1 `internal/api`

- 暴露 REST API。
- 维护 WebSocket 会话。
- 将后台事件映射为前端可消费的消息。
- 提供在线连接查询和连接管理接口。

### 3.2 `internal/auth`

- `auth.json` 读取、初始化和管理密钥轮换。
- 管理端一次性盐 challenge 签发与消费。
- 管理端认证。
- API/WS 鉴权中间件。
- `token_id` 定位与一次性 challenge 校验。

### 3.3 `internal/control`

- `frpc` 登录协议。
- 心跳处理。
- 配置推送。
- 配置应答跟踪。
- 工作流打开/关闭。
- 管理端触发的 stream 关闭。

### 3.4 `internal/tunnel`

- 分组、隧道、端口范围的配置模型。
- 正向代理规则编排。
- 公网监听器和分组关系维护。
- 正向代理配置 diff 和监听器重建。

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
- 连接关闭句柄和关闭原因维护。

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

当前已经落地的最小边界是：

- `internal/storage/sql.go` 只负责包装已经打开好的 `*sql.DB` / `*sql.Tx`
- 查询结果统一返回 `map[string]any` 形式的 `Row`
- 同时提供 `Begin`、`BeginTx`、`WithTx`、`WithTxContext` 事务辅助
- 数据库驱动注册放在 `internal/storage/drivers`
- 数据库打开和 DSN 解析放在上层 `internal/app/database.go`
- schema bootstrap 和校验放在 `internal/app/schema.go`

## 4. 数据模型

## 4.1 `ProxyGroup`

```text
ProxyGroup
- id
- name
- token_id
- token_hash
- enabled
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
- stream_id
- opened_at
- closed_at
- rx_bytes
- tx_bytes
- rx_rate
- tx_rate
- rate_limit
- capture_state
- close_reason
- close_requested_by
```

`ActiveConnection` 只保存在内存运行态，不直接写数据库。关闭后将摘要写入审计或统计表。运行态注册表还需要保留关闭函数、所属会话和必要的 stream 元数据，以支持管理端按连接 ID 主动断开。

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
-> read auth.begin
-> parse token_id
-> load group by token_id
-> match group client ip rules
-> reject or send auth.challenge with one-time nonce
-> read auth.finish
-> validate sha256(token_hash + challenge_nonce)
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
-> open stream to frpc with current tunnel snapshot
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

第一阶段即采用二进制帧：底层用 `4` 字节长度前缀明确包边界，业务层用固定头和二进制 body 定义登录、配置同步、逻辑连接、数据读写和关闭语义。

### 6.1 基础消息

登录阶段拆成三类业务消息，但具体字段不再使用 JSON，而是按协议文档中的二进制 body 编码：

- `auth.begin`：携带 `tokenId` 原始字节、客户端版本、主机名、OS、架构和能力位。
- `auth.challenge`：携带一次性 `challengeId`、`nonce` 和过期时间。
- `auth.finish`：携带 `challengeId` 和 `sha256(token_hash + nonce)` 的原始摘要。

首版消息类型固定为：

- `auth.begin`
- `auth.challenge`
- `auth.finish`
- `server.hello`
- `config.push`
- `config.ack`
- `heartbeat.ping`
- `heartbeat.pong`
- `stream.open`
- `stream.opened`
- `stream.data`
- `stream.close`
- `udp.open`
- `udp.data`
- `udp.close`
- `event.report`
- `error`

### 6.2 配置版本

每次分组隧道配置变更后：

- `frps` 递增 `configVersion`
- 通过控制连接推送 `config.push`
- `frpc` 必须回 `config.ack`
- `frps` 记录每个在线会话的最后已确认版本
- 未应答则重试或强制重连

`config.push` 建议始终携带该分组的完整运行态快照，而不是增量 patch。这样 `frpc` 可以直接原子替换本地快照，避免补丁顺序错乱导致的状态漂移。

`config.push` 只携带 `frpc` 执行工作流所必需的字段，例如隧道启停、本地目标、端口映射、协议等。`frps` 独占执行的服务端策略，例如 `client/tunnel` ACL、限速、抓包策略，不应作为客户端配置下发。

协议和配置构造都遵守同一条字段治理规则：目标端只解析必要字段，不代表源端可以继续携带冗余字段。某个字段一旦确认无用或语义调整，必须同步从 `frps` 的配置构造、持久化、API 输出、测试数据和文档中收束，不能长期依赖“目标端忽略未知字段”维持兼容。

### 6.3 连接管理中的单条连接关闭

建议管理动作链路：

```text
POST /api/v1/connections/{id}/close
-> locate ActiveConnection in registry
-> mark close_requested_by=admin
-> publish connection.close_requested
-> close public side connection
-> send stream.close to frpc if stream is active
-> wait resource cleanup
-> publish connection.closed with close_reason=admin_terminated
-> write audit log
```

如果目标连接已经关闭，接口返回幂等成功或 `404` 都可以，但实现上必须保证不会误伤其他连接。

## 7. 热更新设计

`frps` 的配置更新分三类：

### 7.1 管理面配置

例如 `auth.json` 中的管理密钥 hash、日志等级、WebUI 端口。通常需要服务级重载，必要时可以允许重启。

### 7.2 正向代理配置

正向代理热更新需要区分两类配置：

- `frpc` 执行配置，例如分组启停、隧道启停、协议、远端端口映射、本地目标地址。
- `frps` 本地策略，例如客户端 ACL、隧道入口 ACL、限速、抓包开关。

要求热更新：

- 新增端口立即监听。
- 修改端口先校验冲突，再重建监听器。
- 删除端口先停止新接入，再等待旧连接自然关闭或强制回收。
- 执行配置保存成功后，立即向该分组在线 `frpc` 推送 `config.push`。
- `frpc` 不需要重启，收到后原子替换本地运行态快照。
- `frps` 需要追踪 `config.ack`，在 WebUI 展示最后同步版本和同步时间。
- 本地策略保存成功后，只更新 `frps` 运行态，不要求给 `frpc` 推送配置。

### 7.3 反向代理配置

例如域名、证书、上游地址。要求热更新：

- HTTP 路由表原子替换。
- HTTPS 证书表原子替换。
- TCP 固定端口监听器按规则重建。

### 7.4 热更新与活跃连接关系

- 修改本地目标地址等 `frpc` 执行配置时，不打断无关已有连接，新建连接立即使用新配置。
- 修改 ACL、限速、抓包开关等 `frps` 本地策略时，不要求 `frpc` 感知，新接入连接和后续转发立即按新策略执行。
- 修改远端监听端口、协议、启停状态时，`frps` 先完成监听器切换，新接入连接立即按新配置处理。
- 隧道被禁用或删除后，新连接必须立刻拒绝；已有连接默认允许自然结束。
- 管理端如果需要立即回收已有连接，应按连接 ID 或隧道维度显式触发关闭动作，而不是依赖配置删除隐式清空。

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
- `config.synced`
- `group.updated`
- `tunnel.updated`
- `reverse.updated`
- `connection.close_requested`
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

### 9.2 限速执行边界与生效值

限速只在 `frps` 数据面执行：

- `frpc` 不接收限速参数，不参与流量额度扣减，也不上报“当前正在被限速”的状态。
- 对正向代理来说，`frpc` 只会观察到经由 `frps` 转发的数据变慢，但无法区分这是网络抖动还是服务端节流。
- 限速配置的热更新只修改 `frps` 本地运行态，不通过 `config.push` 下发到客户端。
- 本文中的流量额度桶（quota bucket）只用于限速实现，表示可消费的流量预算；它不是登录鉴权使用的 token，也不会出现在认证协议中。
- 限速实现统一放在应用层 pacer，不依赖 Linux `tc`。这样可以保证 `frps` 在 Windows 和 Linux 上都具备一致能力，也避免为老旧且平台绑定的内核流控方案维护第二套实现路径。

建议支持以下层级：

- 全局限速
- 分组限速
- 隧道或反代规则限速

其中：

- `0` 或空值表示不限制。
- `ActiveConnection.rate_limit` 存储该连接当前生效值，用于连接管理页展示。
- 生效值取所有非零限制中的最严格值。

### 9.3 TCP 限速实现

TCP 限速建议在 `frps` 双向拷贝链路上执行：

- 为每条 `ActiveConnection` 创建两个方向的 limiter，分别对应入口方向和出口方向。
- 在 `io.Copy` 风格循环外包一层 paced reader 或 paced writer；实现重点不是“攒够一大块额度再写一大块”，而是按单调时钟连续补充额度，并计算下一次允许发送时间。
- 每次写出前按本次准备转发的字节数申请流量额度；如果当前额度不足，只发送允许的那一小部分，剩余部分继续等待，而不是整块数据一起憋到下一个时间窗。
- 流量额度不足时，当前 goroutine 在 `frps` 侧等待额度恢复，而不是通知 `frpc` 降速。
- limiter 内部维护按 `bytes` 计量的可消费额度，额度补充速率配置统一使用 `bytes/sec`；底层可用流量额度桶（quota bucket）做额度核算，但输出调度必须由 pacer 保证平滑，不能做成类似 PWM 的秒级 burst/sleep。
- TCP 写路径必须支持 partial write 和小块发送。低速场景例如 `10 bps` 时，可退化为接近 `1 byte / 800 ms` 的发送节拍；这已经是应用层字节流可达到的最细粒度。
- 为减少内核侧合并导致的额外突发，发送方向应限制单次 write 大小，并评估是否对对应 socket 启用 `TCP_NODELAY`。
- 修改全局、分组或规则限速后，运行态需要原子更新受影响连接的 limiter 参数，使新旧连接都能立即看到新的生效值。

### 9.4 UDP 限速实现

UDP 限速建议在 `frps` 的会话转发路径执行：

- 以 UDP 会话为最小运行态对象，按方向维护 limiter。
- 每次转发数据报前，按 datagram 大小申请流量额度。
- 流量额度不足时等待额度恢复；如果会话 context 已取消或超时，则放弃该次转发并记录统计。
- UDP 平滑度受 datagram 边界限制，无法细于“单个数据报”本身；如果一个数据报大于当前可用额度，只能等待额度累积后整包发出。
- 首版不需要把 UDP 限速状态同步给 `frpc`，客户端只按收到的数据报继续转发。

### 9.5 UDP 统计

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

- 配置型数据库数据：分组、隧道、反代、证书
- 本地认证文件：`auth.json` 中的管理密钥 hash
- 审计型数据：操作日志、拒绝事件、抓包任务
- 运行态数据：在线连接、实时速率、客户端会话

数据库设计约束：

- 表结构优先使用 SQLite 和 MySQL 的公共能力。
- 首版避免依赖 JSON 列、触发器、生成列、数据库枚举等方言特性。
- 数据库差异尽量收敛在 `internal/storage`，不向业务层扩散。
- 当前 schema 版本和建表语句以内嵌代码维护，不单独引入 `.sql` 迁移目录。
- `frps` 启动时必须先完成 `schema_migrations` bootstrap、版本推进和表结构校验；如果现有库结构不符合预期，服务立即退出。
- 空库允许自动初始化；非空库要求列定义、主键、自增属性和唯一索引与内置 schema 完全一致。

运行态数据放内存，不能让数据库成为数据面瓶颈。

## 12. 并发与生命周期

关键并发对象：

- 分组运行态
- 隧道监听器集合
- 反代路由表
- 活跃连接注册表
- WebSocket 会话集合
- 在线 `frpc` 会话的配置同步状态

建议使用：

- 原子替换只读快照，用于高频读场景
- 分片锁或细粒度锁，避免全局大锁
- context 控制监听器、连接、抓包任务生命周期
- 单分组串行化配置重载，避免同一分组的多次 Web 修改交错下发

## 13. 安全要求

- token 采用固定长度拼接结构：`token = token_id + token_secret`，不使用 `.` 分隔符。
- `token_id` 和 `token_secret` 必须固定长度，否则客户端无法可靠截取。
- 首版建议 `token_id` 为 32 位小写 hex，`token_secret` 为 64 位小写 hex。
- 服务端只在分组记录中持久化 `token_id + token_hash`。
- 登录阶段先用 `token_id` 定位分组，再下发一次性临时盐，也就是 challenge nonce。
- `token_hash` 建议固定使用 `sha256(token_secret)`。
- 客户端提交 `sha256(token_hash + challenge_nonce)`，服务端使用常量时间算法比较。
- challenge nonce 必须短时有效、只能使用一次，过期或重复使用必须拒绝。
- 当前方案下 `token_hash` 等价于可登录校验材料，必须按敏感凭据保护。
- 管理端不使用数据库 `admin/admins` 表，服务启动时必须先检查本地 `auth.json`。
- 如果 `auth.json` 不存在，管理面只能进入初始化流程，由用户设置管理密钥。
- `auth.json` 只保存管理密钥的 hash，不保存明文；建议首版固定使用 `sha256(secret)`。
- 浏览器登录时先获取一次性盐，再提交 `sha256(key_hash + salt)`；盐必须短时有效、只能使用一次，服务端比较时必须使用常量时间算法。
- `auth.json` 中的 `key_hash` 同样属于可登录校验材料，必须按敏感凭据保护。
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
