# frps 技术设计

## 1. 设计目标

当前 `frps` 的技术设计只围绕已经落地的能力展开：

- 用最小结构跑通管理面、控制面和正向代理数据面。
- 把管理认证、本地配置、控制协议和公网 listener 的边界收清楚。
- 在不引入额外框架的前提下，给后续迭代保留明确扩展点。

## 2. 当前进程内分层

```text
cmd/frps
  |
internal/app
  |
+----------------------+----------------------+
|                      |                      |
internal/api           internal/control       internal/auth
  |                      |                      |
WebUI/API              frpc protocol         auth.json
  |                      |
internal/storage <------+
  |
SQLite / MySQL
```

当前没有单独的事件总线、连接注册表或插件化数据面。

## 3. 当前模块 ownership

### 3.1 `cmd/frps`

- 解析当前工作目录。
- 固定拼出 `data/config.json` 路径。
- 初始化 logger。
- 处理 `Ctrl+C`。

### 3.2 `internal/config`

- 提供默认值。
- 读取 JSON 配置。
- 校验监听地址、超时、日志格式和数据库配置。
- 解析相对路径。

当前默认值：

- `control_listen_addr = 0.0.0.0:7000`
- `management_listen_addr = 127.0.0.1:7080`
- `read_header_timeout = 5s`
- `shutdown_timeout = 10s`
- `database.type = sqlite`
- `database.path = ./frps.db`
- `webui.dist_dir = ../webui/dist`

MySQL 只接受驱动标准 DSN，不再兼容地址简写。

### 3.3 `internal/auth`

`auth.Manager` 负责：

- 从 `auth.json` 读取 `key_hash`
- 在未初始化时写入 `auth.json`
- 签发管理 challenge
- 校验 `sha256(key_hash + salt)` 证明
- 维护管理 session
- 轮询检测 `auth.json` 是否被删除

当前固定参数：

- challenge TTL：`2m`
- session TTL：`12h`
- 删除监听间隔：`1s`

删除 `auth.json` 后会立即清空：

- `initialized` 状态
- challenge 集合
- session 集合

### 3.4 `internal/app`

`app.App` 当前负责：

- 初始化 `auth.Manager`
- 打开数据库
- 执行 schema bootstrap 和严格校验
- 初始化本机地址快照服务并在关闭时回收
- 并发启动管理 API 和控制监听器
- 统一 shutdown

### 3.5 `internal/storage`

当前只提供最小 SQL 封装：

- `storage.SQL`
- `storage.Tx`
- `QueryContext`
- `QueryOneContext`
- `ExecContext`
- `WithTxContext`

数据库驱动注册放在 `internal/storage/drivers`。

### 3.6 `internal/api`

当前拆成 4 个部分：

- `server.go`：HTTP server、路由注册、日志中间件
- `auth.go`：管理认证路由与 Cookie/Bearer 会话
- `management.go`：分组和隧道 CRUD
- `webui.go`：静态资源托管和 SPA fallback

### 3.7 `internal/control`

当前稳定文件边界如下：

- `server.go`：accept loop、shutdown、session 读循环、心跳和基础帧读写
- `auth.go`：`frpc` 登录、challenge 生命周期、group slot
- `config.go`：runtime 加载、`config.push`、`config.ack`
- `connections.go`：控制连接跟踪
- `session.go`：`sessionState`、session 写锁、listener/stream/session 收口
- `listeners.go`：TCP/UDP listener 展开和 serve loop
- `tcp_bridge.go`：TCP `stream.*` 生命周期
- `udp.go`：UDP `udp.*` 生命周期和 idle cleanup
- `sockaddr.go`：地址转换辅助
- `repository.go`：从数据库构造 `GroupRuntime` 和 `ConfigSnapshot`

### 3.8 `internal/ports`

- 承接管理面当前已经落地的共享监听占位比较 helper。
- 当前已实现跨平台统一静态冲突检测（`specific` + `wildcard`），固定按单一冲突矩阵比较监听空间。
- 输出冲突双方和冲突端口区间，供管理 API 生成稳定错误文案和状态原因。

## 4. 当前数据模型

当前配置明确分成两层：

- 持久化配置：管理面写入数据库中的 `proxy_groups`、`tunnels`。
- 运行时配置：`internal/control/repository.go` 在 `frpc` 登录或在线热重载时把持久化配置投影成 `GroupRuntime` 和 `ConfigSnapshot`；`frps` listener 和 `frpc sessionState.snapshot` 后续都只消费这份运行时快照。
- 抓包相关控制当前未实现；如果后续引入，只能进入运行时配置层，不能再设计成 `tunnels` 表字段。

### 4.1 `proxy_groups`

当前控制面和管理面共用字段：

- `id`
- `name`
- `client_id`
- `client_secret_hash`
- `effective_ip`
- `enabled`
- `rate_limit`
- `created_at`
- `updated_at`

其中 `rate_limit` 语义固定为该分组下所有隧道共享总限速。

当前运行时实际消费：

- `id`
- `name`
- `effective_ip`
- `client_secret_hash`
- `enabled`
- `updated_at`

### 4.2 `tunnels`

当前字段：

- `id`
- `group_id`
- `name`
- `protocol`
- `remote_type`
- `remote_start`
- `remote_end`
- `local_host`
- `local_start`
- `local_end`
- `enabled`
- `created_at`
- `updated_at`

当前运行时实际消费：

- `id`
- `protocol`
- `remote_type`
- `remote_start`
- `remote_end`
- `local_host`
- `local_start`
- `local_end`
- `enabled`
- `updated_at`

## 5. 管理 API 设计

### 5.1 认证接口

认证接口不依赖数据库管理员表，只依赖本地 `auth.json`。

当前行为：

- `GET /api/v1/auth/state`
  - 返回是否初始化
  - 返回当前请求是否已认证
- `POST /api/v1/auth/init`
  - 只接受 `key_hash`
  - 未初始化时写入 `auth.json`
- `POST /api/v1/auth/challenge`
  - 返回 `challenge_id`、`salt`、`expires_at`
- `POST /api/v1/auth/login`
  - 验证 proof
  - 写 Cookie
- `GET /api/v1/auth/session`
  - 返回会话状态
- `POST /api/v1/auth/logout`
  - 删除服务端 session
  - 清理 Cookie

### 5.2 分组接口

当前入参支持：

- `name`
- `effective_ip`
- `enabled`

创建或轮转登录 `key` 时：

- 返回完整明文 `key`
- 数据库只保存 `client_id + client_secret_hash`

当前 `effective_ip` 校验规则：

- 只允许单个 IP 字面量，不接受 hostname、CIDR、端口或空字符串。
- 只允许服务端当前本机 IPv4、服务端当前本机 IPv6，以及特殊值 `0.0.0.0`、`::`。
- 当前管理 API 与 WebUI 都已要求显式提交 `effective_ip`。
- `PATCH /api/v1/proxy-groups/{id}` 当前按局部更新处理：未提交字段保持数据库现值，只有显式提交的字段才重新校验并写回。
- 空 `PATCH` 对象会直接返回 `400`，要求至少提交 `name`、`effective_ip`、`enabled` 之一。
- 当前分组返回模型还会附带运行态派生字段 `status` 和可选 `status_reason`，当前 WebUI 已直接展示状态标签和异常原因。

### 5.3 隧道接口

当前支持字段：

- `group_id`
- `name`
- `protocol`
- `remote_type`
- `remote_start`
- `remote_end`
- `local_host`
- `local_start`
- `local_end`
- `enabled`

校验规则：

- `protocol` 只能是 `tcp` 或 `udp`
- `remote_type` 只能是 `single` 或 `range`
- 端口范围必须在 `1..65535`
- `single` 模式下起止端口必须相同
- `range` 模式下本地和远端跨度必须一致
- `local_host` 使用 `protocol.ParseHost` 校验
- 管理面当前已接入跨平台统一静态端口冲突校验（已实现）：
  - 只在分组和隧道都启用时参与比较
  - 只比较同协议监听空间（`tcp` 与 `udp` 隔离）
  - 只在远端端口区间相交时继续比较监听空间
  - 监听空间比较固定按唯一方案（V1）冲突矩阵执行
- 跨平台唯一方案（V1）：
  - 管理面与控制面共用同一判定语义：`同协议 + 端口区间相交 + 监听空间重叠 => 冲突`。
  - 监听空间只按 `effective_ip` 类型比较：`Any4(0.0.0.0)`、`Any6(::)`、`Specific4`、`Specific6`。
  - 固定冲突矩阵如下（与平台无关，不做 Linux/Windows 分支）：

| 左/右 | Any4 | Any6 | Specific4 | Specific6 |
| --- | --- | --- | --- | --- |
| Any4 | 冲突 | 冲突 | 冲突 | 不冲突 |
| Any6 | 冲突 | 冲突 | 不冲突 | 冲突 |
| Specific4 | 冲突 | 不冲突 | 同 IP 冲突 | 不冲突 |
| Specific6 | 不冲突 | 冲突 | 不冲突 | 同 IP 冲突 |

  - 该矩阵适用于 `single-single`、`single-range`、`range-range` 全部组合；端口只要相交即按矩阵比较。
  - 统一保守策略：即使某一平台/调用顺序下某组 `bind` 偶然成功，只要命中矩阵冲突，管理面仍拒绝写库。
  - 控制面现在也接入同一套运行态冲突语义：`ensureTunnelListeners()` 与 `effective_ip` 本地重绑前会先按当前在线运行态比较监听声明；若命中运行态冲突或外部进程抢占导致 `listen` 失败，都会统一回传明确的端口冲突原因，但不反向放宽静态规则。
  - 控制面还会在启动阶段先对“当前没有 listener 的启用隧道”做一次全量运行态扫描，完成隧道异常标记后才开放 `frpc` 控制端口；启动后继续固定轮询同一批“当前没有 listener”的隧道，用于清理已恢复的旧异常或补记新的运行态失败。
  - 如果某个在线分组当前只拉起了部分 tunnel，轮询会继续扫描该分组剩余“启用但未监听”的 tunnel；一旦冲突/占用消失，就直接在现有 session 上补启动这些已恢复 tunnel。
  - 如果某个在线分组之前因 `effective_ip` 暂时不可用而被热更新收缩到空配置，轮询在确认该 `effective_ip` 重新可绑定后，会先向当前 session 补发仓库里的完整快照；只有等新的 `config.ack` 生效后，才继续按常规路径恢复 listener。
- Linux / Windows 差异与采用该唯一方案的原因（规范依据 + 本机实验）：
  - Linux `ipv6(7)`：`IPV6_V6ONLY` 默认值来自 `/proc/sys/net/ipv6/bindv6only`，通常默认为 `0`；`0` 时 IPv6 wildcard socket 可覆盖 IPv4-mapped IPv6。
  - Windows Winsock：IPv6 socket 默认 `IPV6_V6ONLY=1`，只有显式设为 `0` 才 dual-stack；因此同一组地址在 Win/Lin 上可能出现不同的二次绑定结果。
  - Go `net` 默认 listener 选项在 Win/Lin 也不同（Windows 默认不设置 listener `SO_REUSEADDR`，Linux 默认设置），会进一步放大平台差异。
  - 本机实验（2026-04-22，`test/wildcard_bind_matrix.py`）已确认 `wildcard-specific`、`wildcard-wildcard` 在 Win/Lin 上存在差异；因此必须固定一套跨平台静态规则，避免离线可写、上线失败或跨系统迁移后行为变化。
- `createTunnel()`、`updateTunnel()`、`updateProxyGroup()` 在写库前重算受影响隧道集合；命中冲突时直接返回 `409`

参考资料：

- Linux `ipv6(7)`：https://www.man7.org/linux/man-pages/man7/ipv6.7.html
- Linux `socket(7)`：https://man7.org/linux/man-pages/man7/socket.7.html
- RFC 3493 `IPV6_V6ONLY`：https://www.ietf.org/rfc/rfc3493.txt
- Windows dual-stack sockets：https://learn.microsoft.com/en-us/windows/win32/winsock/dual-stack-sockets
- Windows `SO_REUSEADDR` / `SO_EXCLUSIVEADDRUSE`：https://learn.microsoft.com/en-us/windows/win32/winsock/using-so-reuseaddr-and-so-exclusiveaddruse

当前仍不暴露分组 `rate_limit` 管理入口；抓包控制也不作为 `tunnels` 持久化字段或管理入口出现。

### 5.4 隧道状态派生

管理 API 当前会给隧道返回一个运行态派生 `status` 与可选 `status_reason`，但不把它持久化入库。

状态优先级固定为：

1. `禁用`：分组禁用或隧道禁用
2. `冲突`：命中当前已实现的跨平台统一静态端口冲突
3. `异常`：隧道启用且不冲突，但启动期扫描、未监听轮询、listener 启动或 `effective_ip` 重绑发现运行态失败
4. `启用`：其余情况

其中 `status_reason` 当前只用于：

- 指向冲突对端隧道与冲突监听空间
- 承接启动期全量扫描、未监听轮询、listener 启动或 `effective_ip` 重绑发现的运行态失败原因

## 6. 控制面设计

### 6.1 传输与超时

- 传输层由 `pkg/transport` 提供 `4` 字节长度前缀收发。
- 业务帧由 `pkg/protocol` 解析。
- 默认读写超时：`5s`
- 登录 challenge TTL：`30s`
- 心跳间隔：`15s`

会话读超时使用：

```text
max(3 * heartbeat_interval, minimum_read_timeout)
```

### 6.2 登录握手

当前时序：

```text
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
frps -> start listeners
```

登录过程中当前会执行：

1. 读取 `auth.begin`
2. 按 `client_id` 读取 `GroupRuntime`
3. 校验分组是否启用
4. 签发 challenge
5. 用 `protocol.ChallengeResponse` 计算期望值
6. 常量时间比较响应
7. 抢占单分组单客户端槽位
8. 返回 `server.hello`
9. 下发首次 `config.push`

补充启动期门闩：

- 控制面在打开 `frpc` 控制监听端口前，会先完成一次“全部未监听启用隧道”的运行态扫描。
- 这次扫描会尝试解析 `effective_ip`、比较与当前在线 listener 的运行态冲突，并对无 listener 的启用 tunnel 逐个做临时 `bind` 探测。
- 扫描完成后才真正开放控制端口，因此新 `frpc` 登录不会早于首轮隧道状态标记。
- 管理 API 也会等待同一轮首轮扫描完成后才开始监听；因此外部首次看到的管理状态已经包含首轮扫描结果，不会先暴露“未扫描”的初始视图。
- 启动完成后仍有一个后台轮询，只继续扫描“当前没有 listener”的启用 tunnel；已经真实监听中的 tunnel 不参与这条轮询路径。
- 对于离线分组，这条轮询只负责写入和清理 runtime issue；对于在线但仅部分 tunnel 已监听的分组，这条轮询还会在恢复后复用当前 session 补启动缺失 tunnel。
- 如果在线 session 当前生效快照是“因 `effective_ip` 失效而下发的空配置”，轮询在确认 `effective_ip` 已恢复可绑定后，不会直接在服务端本地补开 listener，而是先重新下发仓库里的完整快照，等待客户端 `ack` 后再恢复 listener，避免两端运行态快照分裂。

### 6.3 配置快照

当前 `ConfigSnapshot` 由 `repository.go` 构造：

- `Version`：取分组和隧道最新 `updated_at` 的 Unix microseconds
- `GeneratedAtMs`：快照时间
- `Tunnels`：当前分组所有隧道的协议快照

当前行为边界：

- 配置在首次登录和后续在线热重载阶段都复用同一 `ConfigSnapshot` 路径构造并下发
- `config.ack` 成功后才启动或重启 listener
- 管理 API 改库后，如果对应分组在线且命中运行态字段，会主动向该在线 `frpc` 下发新的整组完整快照
- 当前快照只承载真实执行链路需要的隧道字段，不承载 `rate_limit` 或抓包控制

### 6.4 在线整组配置热重载当前边界

下面这些规则已经确认并已进入当前代码，包括 `frps` 侧整组冻结、`config.push / config.ack` 重载状态管理、`frpc` 侧本地资源清理与快照替换，以及 `ack` 后 listener 重建：

- 分组离线时，管理面配置变更只写数据库，不触发任何运行态动作。
- 分组在线时，运行态相关变更统一复用现有 `config.push / config.ack`，继续下发整组完整快照，不新增 `add/remove/rename/replace` 增量协议事件。
- 首版按“整组冻结 + 整组全量重建”收口，不承诺在热重载期间保留该分组下未受影响 tunnel 的现有连接。

运行态相关变更固定包括：

- 分组 `enabled` 变更
- 分组 `effective_ip` 变更
- 隧道新增或删除
- 隧道 `protocol`
- 隧道 `remote_type`
- 隧道 `remote_start`
- 隧道 `remote_end`
- 隧道 `local_host`
- 隧道 `local_start`
- 隧道 `local_end`
- 隧道 `enabled`

下面这些变更不进入运行态热重载：

- 分组名称修改
- 隧道名称修改

首版在线整组热重载时序固定为：

1. 管理 API 先提交数据库事务。
2. 如果分组当前没有在线 `frpc`，流程到此结束。
3. `frps` 重新读取该分组最新 `GroupRuntime / ConfigSnapshot`，生成新的 `configVersion`。
4. 如果本次只改名称等非运行态字段，不下发 `config.push`。
5. 如果存在运行态变更，`frps` 先把该分组置为 `reloading`。
6. `frps` 在下发前先冻结整组运行态：
   - 停止该分组全部公网 listener
   - 清理该分组全部活动 TCP `stream`
   - 清理该分组全部活动 UDP session
7. `frps` 下发新的整组完整 `config.push`。
8. `frpc` 收到后在本地对比旧快照和新快照，形成新增 / 删除 / 替换分类；但首版行为边界仍按整组冻结收口，不要求保留旧连接。
9. `frpc` 完成本地资源清理和运行态替换后，才允许返回 `config.ack(status=ok)`。
10. `frps` 只有在收到对应 `config.ack(status=ok)` 后，才按新快照重新启动该分组 listener，并退出 `reloading`。

失败路径固定为：

- 同一分组同一时刻只允许一个 in-flight 配置更新。
- 如果刷新时前一版 `config.push` 仍未确认，`frps` 直接关闭该分组控制连接，不做排队热重载。
- 如果 `config.ack` 超时、返回 `error`，或配置更新期间控制连接断开，`frps` 直接关闭该分组控制连接，交给 `frpc` 现有自动重连和首次全量登录流程收口。
- 登录 `key` 轮转和分组删除不走热重载，继续直接断开当前控制连接。

截至 2026-04-21，当前代码已经落地到下面这一步：

- `frps/internal/control/refresh.go` 会在在线运行态变更时先冻结整组公网 listener、活动 TCP `stream` 和活动 UDP session，再下发新的整组完整 `config.push`。
- `sessionState` 现在区分“已生效快照”和“等待 `config.ack` 的 pending 快照”；收到匹配的 `config.ack(status=ok)` 之前，不会把新快照直接视为已生效配置。
- `frpc/internal/client` 现在会在收到新的整组 `config.push` 后对比旧/新快照，先关闭当前全部活动本地 TCP stream 和 UDP session，再原子替换本地快照，并且只在清理完成后返回 `config.ack(status=ok)`。
- `frps` 只有在对应 `config.ack(status=ok)` 成功后，才按新快照重新启动 listener；冻结期间旧 listener 即使仍有并发 accept / read，也会按快照代际被拒绝继续打开新的 `stream.open` / `udp.open`。
- 如果刷新时已有未确认 `config.push`，服务端继续按当前边界直接断开控制连接，而不是排队叠加第二版热重载。
- 因此，当前 `config.ack(status=ok)` 已经可以明确表示：客户端本地资源清理和运行态快照替换已经完成；`frps` 收到后才会重新开放 listener。
- 如果某个在线分组因为 `effective_ip` 暂时不在本机而被收缩成空配置，后续不会要求客户端必须重连；后台未监听轮询在确认该 `effective_ip` 已重新可绑定后，会复用当前 session 再补发一次完整 `config.push`，把客户端和服务端一起拉回仓库当前快照。

### 6.5 listener 启动失败的运行态回传

- `listeners.go` 在按快照启动 tunnel listener 前，会先把“当前待启动分组”和“其他在线且 listener 已启动的分组”统一投影成 `ports.Claim`，按同一套冲突矩阵做运行态预检查。
- 如果运行态预检查命中冲突，当前待启动分组会直接拒绝启动 listener，不再依赖底层 `listen` 报错才发现问题。
- 如果底层 `net.Listen` / `net.ListenUDP` 真实失败，`listeners.go` 会包装成 `tunnelListenerStartError`；其中端口已占用会统一收口为明确的“端口冲突，无法启动”原因。
- `server.go` 维护 `tunnelRuntimeIssues map[int64]string`，记录最近一次 tunnel listener 启动失败原因。
- 同一 tunnel 后续成功启动 listener 时，会清理对应运行态异常记录。
- `internal/api` 通过 `TunnelRuntimeStatusReader` 读取这份运行态异常，并把满足条件的隧道状态派生为 `异常`。
- 静态配置冲突仍然优先显示为 `冲突`；启动期扫描、后续轮询和 listener 启动失败都不会把这类 tunnel 再额外覆盖成 runtime `异常`。

端口范围在热重载中的判定固定为“按 tunnel 整体替换”，不做重叠区间复用优化：

- 只要 `remoteStart / remoteEnd / localStart / localEnd` 之一变化，就视为该 tunnel 的执行配置已变化。
- 即使新旧范围存在数值重叠，也不能把重叠端口视为“无需处理”，因为映射基准可能已经变化。
- 例如旧配置是 `1000-2000 -> 3000-4000`，新配置是 `1500-2500 -> 3000-4000`，虽然 `1500-2000` 这部分公网端口仍存在，但 `offset = remotePort - remoteStart` 的基准已经从 `1000` 变成 `1500`，本地目标端口映射整体改变，因此该 tunnel 必须按完整替换处理。

## 7. 数据面设计

### 7.1 TCP

当前 TCP 数据面流程：

```text
public tcp accept
-> allocate streamId/requestId
-> send stream.open
-> wait stream.opened
-> copy public -> frpc with stream.data
-> copy frpc -> public with stream.data
-> either side sends stream.close
```

当前没有独立工作连接池；所有 `stream.*` 都复用同一条控制连接。

### 7.2 UDP

当前 UDP 数据面流程：

```text
public datagram
-> resolve or create publicUDPSession
-> first packet sends udp.open + udp.data
-> next packets send udp.data
-> frpc local reply returns as udp.data
-> frps writes reply back to public client
```

`publicUDPSession` 当前键为：

```text
tunnelId + remotePort + public client addr
```

当前 idle cleanup 语义：

- `frps` 是唯一生命周期裁决方
- 任何一次成功转发都会立即 `touch`
- 清理判断基于 `lastActive + idleTimeout`
- 默认 idle timeout 为 `30s`
- 默认 sweep 周期为 `1s`
- 清理后发送 `udp.close(reason=idle_timeout)`

### 7.3 范围映射

TCP/UDP 范围映射统一按下面公式执行：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

`frps` 在运行态按逐端口展开 listener，但协议里仍只保留一个 `tunnelId`。

## 8. 当前扩展边界

后续如果继续扩展，必须以当前边界为前提：

- 反向代理应新增独立运行态，而不是塞回现有正向代理结构。
- 连接注册表、速率统计、抓包、限速应建立在当前 TCP/UDP bridge 之上。
- 在线热重载当前已经收口在现有 `config.push / config.ack` 边界上；后续如果继续优化 listener diff 或连接保留策略，也不能退回成“仅写库”模型。
- `proxy_groups.rate_limit` 只有进入真实执行链路后，文档才允许改口为“已实现”。
- 抓包控制如果后续落地，必须先定义运行时控制面和生效边界，不能再回填为 `tunnels` 持久化字段。

## 9. 分组生效 IP 当前边界

下面这些边界已经确认，其中字段持久化、管理 API CRUD、本机地址快照校验、listener 绑定和分组状态字段都已经进入真实代码链路。

### 9.1 运行态异常派生

- 如果配置的 `effective_ip` 后续不再存在于本机，管理接口当前会返回单一分组状态字段和异常原因；状态值只允许“启用 / 禁用 / 异常”。
- 状态派生规则固定为：`enabled=false` 时显示“禁用”；`enabled=true` 且 `effective_ip` 位于当前可用 IP 列表时显示“启用”；其他情况显示“异常”。
- 不再额外拆出独立的“当前 IP 是否可用”状态字段，避免和分组状态本身重复表达。
- “分组异常”是运行态派生状态，不新增持久化异常字段。
- 数据库值不自动改写，控制面也不得静默切换到其他地址。

### 9.2 WebUI 展示边界

- WebUI 分组列表和编辑弹窗当前已消费后端返回的 `status` / `status_reason`，展示“启用 / 禁用 / 异常”三态与异常原因。
- 分组弹窗当前已接入“生效 IP”下拉，选项来源只允许当前本机地址与特殊值 `0.0.0.0`、`::`。
- 如果数据库已有值当前失效，表单仍需以异常态回显原值，避免用户丢失上下文。
