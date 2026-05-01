# frpc 技术设计

## 1. 设计目标

当前 `frpc` 的技术目标只有三条：

- 保持启动和配置面极简。
- 稳定执行 `frps` 下发的 TCP/UDP 转发。
- 在断线后自动恢复，不引入额外状态系统。

## 2. 当前模块设计

### 2.1 `cmd/frpc`

- 读取 `--server`、`--key`、`--version`
- 校验配置
- 初始化日志
- 处理 `Ctrl+C`

### 2.2 `internal/config`

负责：

- 校验 `server`
- 校验 `key` 长度和字符集
- 把 `key` 解析为：
  - `ClientID [16]byte`
  - `ClientSecret [32]byte`

当前 `key` 解析规则固定为：

- 总长度必须是 `96` 个 hex 字符
- 必须全为小写

### 2.3 `internal/client`

当前已按职责拆成：

- `client.go`
  - `Run`
  - `runOnce`
  - `runSession`
  - 通用帧读写
- `login.go`
  - 登录握手
  - 首次 `config.push` 应用
- `session.go`
  - `sessionState`
  - `readLoop`
  - `heartbeatLoop`
- `targets.go`
  - tunnel 查找
  - TCP/UDP 共用端口偏移换算
- `tcp_bridge.go`
  - `stream.*`
- `udp_bridge.go`
  - `udp.*`
- `runtime_info.go`
  - 主机名
  - OS
  - 架构

## 3. 控制连接设计

### 3.1 登录

当前登录流程：

```text
connect
-> transport.client_hello(client_id, supported security modes)
<- transport.server_hello(selected security mode)
if selected security mode == tls:
   TLS handshake on the same TCP connection
-> auth.begin(client_id, runtime info)
<- auth.challenge
-> auth.finish(sha256(client_secret_hash + nonce))
<- server.hello
<- config.push
-> config.ack
```

当前关键点：

- `client_secret_hash = sha256(client_secret)`
- challenge 响应通过 `protocol.ChallengeResponse` 计算
- `transport.server_hello` 决定控制连接是否先升级到 TLS
- 登录成功后立刻应用首次配置

### 3.2 会话状态

当前 `sessionState` 保存：

- 心跳间隔
- 读超时
- 写锁
- request id 计数器
- 已确认配置版本
- 活跃 stream 数
- 活跃 UDP session 数
- 当前配置快照
- 活跃 TCP stream 映射
- 活跃 UDP session 映射

当前没有：

- 本地配置持久化
- 复杂状态机
- 独立事件总线

### 3.3 心跳

当前 `heartbeatLoop` 周期发送：

- `clientUnixMs`
- `activeStreams`
- `activeUDPSessions`
- `lastAckedConfigVersion`

读超时使用：

```text
max(3 * heartbeat_interval, 5s)
```

## 4. 配置快照设计

当前 `frpc` 只维护运行时快照，不落盘。

收到 `config.push` 后当前行为：

1. 解析完整快照
2. 对比旧快照和新快照，形成新增 / 删除 / 替换分类，供日志和本地清理使用
3. 关闭当前 `proxy_group` 全部活动本地 TCP stream
4. 关闭当前 `proxy_group` 全部活动本地 UDP session
5. 原子替换 `sessionState.snapshot`
6. 返回 `config.ack`
7. 更新 `lastAckedConfigVersion`

当前边界：

- 配置会在登录阶段和同一控制连接后续在线热重载阶段重复接收，仍只消费整组完整快照
- 服务端会按“整组冻结 + 整组全量重建”推进热重载，因此 `frpc` 首版不要求保留未受影响 tunnel 的活动本地连接
- `config.ack` 只能表示“本地清理和运行态替换已经完成”，不能表示“先答应，后异步切换”
- tunnel 名称不在运行态快照内；因此单纯改名称不会触发 `frpc` 运行态变更

### 4.1 当前在线整组配置热重载规则

当前已固定为下面的行为：

- `frpc` 登录完成后仍可能再次收到新的 `config.push`；每次仍然是整组完整快照，而不是单 tunnel 增量事件
- `frpc` 会在本地对比旧快照和新快照，识别哪些 tunnel 是新增、删除或替换
- 首版仍按整组冻结收口，不承诺保留旧连接；本地活跃 TCP/UDP 运行态会在返回 `config.ack(status=ok)` 前先全部清理

当前时序固定为：

1. 收到新的整组 `config.push`。
2. 对比旧快照和新快照，形成新增 / 删除 / 替换分类，供本地清理和后续日志使用。
3. 关闭当前 `proxy_group` 全部活动本地 TCP stream。
4. 关闭当前 `proxy_group` 全部活动本地 UDP session。
5. 原子替换当前运行态快照。
6. 返回 `config.ack(status=ok)`。

端口范围在 `frpc` 侧的热重载判定固定为“按 tunnel 整体替换”：

- 只要 `remoteStart / remoteEnd / localStart / localEnd` 之一变化，就不能把该 range tunnel 当成“部分不变”。
- 例如旧配置 `1000-2000 -> 3000-4000` 改成新配置 `1500-2500 -> 3000-4000` 时，`1500-2000` 虽然仍位于新旧公网范围交集，但由于 `offset = remotePort - remoteStart` 的基准变化，该交集上的 `localPort` 计算结果也会整体变化，因此必须视为整 tunnel 替换。

## 5. TCP 设计

当前 TCP bridge 设计：

```text
stream.open
-> 按 tunnelId + remotePort 解析本地目标
-> net.DialTimeout(local target)
-> stream.opened
-> stream.data 双向转发
-> stream.close 收口
```

当前错误路径：

- tunnel 不存在
- tunnel 协议不匹配
- tunnel 已禁用
- 本地拨号失败
- 本地读写失败

每个活跃 stream 当前只保存一个 `net.Conn`，不额外持有复杂元数据。

## 6. UDP 设计

当前 UDP bridge 设计：

```text
udp.open
-> 按 tunnelId + remotePort 解析本地目标
-> net.DialUDP(local target)
-> udp.data 写本地
-> 本地回包后用同一 sessionId 发回 frps
-> udp.close 收口
```

当前固定语义：

- `sessionId` 由 `frps` 分配
- `frpc` 不做本地 idle timer
- 只在下面几种情况释放本地 UDP session：
  - 收到 `udp.close`
  - 本地不可恢复读写错误
  - 控制连接结束

## 7. 范围映射设计

TCP/UDP 共用同一套端口换算：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

`frpc` 只依赖：

- 当前快照中的 `tunnelId`
- `stream.open.remotePort`
- `udp.open.remotePort`

不依赖 listener 创建顺序，也不依赖额外的子 tunnel id。

## 8. 重连设计

当前重连策略：

- 初始退避：`1s`
- 指数退避上限：`30s`
- 每次成功登录后重新从最小退避开始

断线后当前会：

- 关闭控制连接
- 关闭所有本地 TCP stream
- 关闭所有本地 UDP session
- 等待下一次重连

## 9. 当前扩展边界

后续如果继续扩展，必须保持下面边界：

- ACL、限速、抓包仍放在 `frps`，不要下沉到 `frpc`
- 本地配置持久化不是当前默认方向
- 如果以后补在线热更新，应在当前快照替换机制上扩展，而不是新增第二套配置来源
- 如果以后补更复杂 UDP 策略，仍要维持“`frps` 是生命周期裁决方”的主边界
