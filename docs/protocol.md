# frps/frpc 协议文档

## 1. 文档目标

本文档只定义 `frps` 与 `frpc` 之间的通信协议，分为两层：

- 传输层协议：固定使用 `4` 字节长度前缀加业务帧，用来明确数据包边界。
- 业务协议：所有业务消息都使用二进制编码，用来提高信息密度和有效数据占比。

本文档不定义：

- WebUI 的 REST API / WebSocket 协议。
- `frps` 对公网用户提供的 TCP/UDP/HTTP/HTTPS 转发协议。
- TLS/mTLS 细节。若后续启用 TLS，则长度前缀协议运行在 TLS 之上。

## 2. 术语

- `connection`：`frpc` 与 `frps` 之间的那条底层 TCP 长连接。
- `frame`：一条完整的业务帧，对应一个长度前缀包。
- `requestId`：请求响应关联 ID，只在当前 `connection` 内有效。
- `streamId`：TCP 工作流 ID，由 `frps` 分配；在 UDP 相关帧中复用为 `sessionId`。
- `configVersion`：某个分组的正向代理执行配置版本号。
- `wire tunnel id`：`config.push` 中下发的运行态隧道 ID，只服务于 `frps/frpc` 协议，不要求等同于数据库 ID。

## 3. 传输层协议

## 3.1 分包格式

每一帧都采用下面的格式：

```text
+-------------------+----------------------+
| length (4 bytes)  | business frame bytes |
+-------------------+----------------------+
```

约束如下：

- `length` 为无符号 `uint32`，固定使用小端序。
- `length` 的值只包含后面的 `business frame bytes` 长度，不包含前面的 `4` 字节长度字段本身。
- 读取端必须先完整读取 `4` 字节长度，再按该长度继续读取一整帧。
- 单帧最小长度为 `12` 字节，因为业务帧头固定为 `12` 字节。
- 单帧最大长度固定为 `4 MiB`；超过该值直接判定为传输层 framing 错误并立即关闭连接。

## 3.2 长度校验

任一端收到帧时，必须严格按下面顺序处理：

```text
read 4-byte length
-> length < 12 ? transport fatal error, close connection
-> length > 4 MiB ? transport fatal error, close connection
-> read full business frame
-> body not fully read due to EOF/timeout ? close connection
-> parse business frame header
```

稳健性规则：

- `length` 非法时，接收端必须立即关闭底层连接，不得按该长度继续读取 body，不得尝试丢弃声称的超长数据再恢复同步。
- 协议没有额外 `magic` 或同步标记；一旦长度前缀失真，后续字节流无法可靠重同步，因此不定义“跳过坏包继续收”的行为。
- `length` 非法时不得发送业务层 `error` 帧，因为此时尚未形成合法业务帧，写回错误帧只会增加资源占用和阻塞风险。
- 只有在 `length` 已通过校验后，接收端才允许为业务帧分配缓冲区或进入业务解析流程。
- 若 `length` 合法，且读取完该帧后底层连接里仍有更多字节，这些字节自然属于下一帧；只要下一帧长度继续合法，就不视为协议错误。
- 读取端应同时给“读取长度前缀”和“读取整帧 body”设置读超时；合法长度但迟迟读不满整帧时，按连接读失败处理并关闭连接。

## 3.3 发送约束

- 控制类帧与数据类帧共用同一条底层 TCP 连接。
- 控制类帧必须优先于数据类帧发送，避免大流量把心跳、配置同步和关闭指令饿死。
- `stream.data` 单帧 body 最大 `64 KiB`。
- `udp.data` 单帧 body 最大 `64 KiB`。
- 发送端必须自行拆分 TCP 字节流，不允许把很大的 TCP 数据块一次性塞进一个帧。

## 4. 业务帧格式

## 4.1 固定头

`business frame bytes` 的前 `12` 字节固定为业务帧头：

```text
offset  size  field
0       1     version
1       1     type
2       2     flags
4       4     requestId
8       4     streamId
12      N     body
```

约束如下：

- 除传输层最外侧的 `4` 字节长度前缀外，业务帧内部所有多字节整数都使用大端序。
- `version` 首版固定为 `1`。
- `type` 表示业务消息类型。
- `flags` 首版保留，发送时必须为 `0`；接收端若看到非 `0`，直接返回协议错误并关闭连接。
- `requestId` 为当前 `connection` 内的请求响应关联 ID。
- `streamId` 为逻辑流 ID；非流级消息必须填 `0`。
- `body` 全部使用二进制编码，具体结构由 `type` 决定。

## 4.2 基础类型

| 类型 | 编码 |
| --- | --- |
| `u8` | 1 字节无符号整数 |
| `u16` | 2 字节无符号整数，大端序 |
| `u32` | 4 字节无符号整数，大端序 |
| `u64` | 8 字节无符号整数，大端序 |
| `bool` | `u8`，`0=false`，`1=true` |
| `unixMs` | `u64`，Unix epoch 毫秒 |
| `shortstr` | `u16 byteLen` + UTF-8 bytes，允许 `byteLen=0` |
| `bytes16` | 固定 16 字节 |
| `bytes32` | 固定 32 字节 |

这里的基础类型只适用于业务帧头和业务 body，不适用于最外层传输长度前缀。

`shortstr` 只用于少量诊断信息、版本号、主机名、域名等低频字段。转发数据不得使用 `shortstr` 包装。

## 4.3 Host 编码

`Host` 用于配置中的本地目标主机，支持 IP 和域名：

```text
hostType u8
payload  variable
```

| hostType | payload |
| --- | --- |
| `1` | IPv4，固定 4 字节 |
| `2` | IPv6，固定 16 字节 |
| `3` | DNS/hostname，`shortstr` |

## 4.4 SockAddr 编码

`SockAddr` 用于公网访问者地址：

```text
ipType u8
ip     4 or 16 bytes
port   u16
```

| ipType | ip 长度 |
| --- | --- |
| `1` | IPv4，固定 4 字节 |
| `2` | IPv6，固定 16 字节 |

## 4.5 通用枚举

协议类型：

| 值 | 含义 |
| --- | --- |
| `1` | TCP |
| `2` | UDP |

状态：

| 值 | 含义 |
| --- | --- |
| `1` | ok |
| `2` | error |

发起方：

| 值 | 含义 |
| --- | --- |
| `1` | frps |
| `2` | frpc |

操作系统：

| 值 | 含义 |
| --- | --- |
| `1` | windows |
| `2` | linux |
| `3` | darwin |
| `255` | unknown |

CPU 架构：

| 值 | 含义 |
| --- | --- |
| `1` | amd64 |
| `2` | arm64 |
| `3` | 386 |
| `4` | arm |
| `255` | unknown |

## 4.6 requestId 规则

- 只有“请求 -> 响应”型消息需要使用非零 `requestId`。
- 响应消息必须回填与请求一致的 `requestId`。
- `stream.data`、`udp.data`、单向事件上报这类消息可以使用 `0`。
- 同一条底层连接上，发送方在某个请求尚未完成前，不得复用该请求的 `requestId`。

## 4.7 streamId 规则

- `streamId` 由 `frps` 分配，范围为正整数。
- 对 TCP 工作流，`streamId` 表示一条逻辑 TCP 连接。
- 对 UDP 相关帧，`streamId` 复用为 UDP `sessionId`。
- `auth.*`、`heartbeat.*`、`config.*`、`error` 这类连接级消息必须使用 `streamId = 0`。

## 5. 消息类型总览

| type | 名称 | 方向 | body |
| --- | --- | --- | --- |
| `0x01` | `auth.begin` | `frpc -> frps` | binary |
| `0x02` | `auth.challenge` | `frps -> frpc` | binary |
| `0x03` | `auth.finish` | `frpc -> frps` | binary |
| `0x04` | `server.hello` | `frps -> frpc` | binary |
| `0x05` | `heartbeat.ping` | `frpc -> frps` | binary |
| `0x06` | `heartbeat.pong` | `frps -> frpc` | binary |
| `0x10` | `config.push` | `frps -> frpc` | binary |
| `0x11` | `config.ack` | `frpc -> frps` | binary |
| `0x20` | `stream.open` | `frps -> frpc` | binary |
| `0x21` | `stream.opened` | `frpc -> frps` | binary |
| `0x22` | `stream.data` | 双向 | binary |
| `0x23` | `stream.close` | 双向 | binary |
| `0x30` | `udp.open` | `frps -> frpc` | binary |
| `0x31` | `udp.data` | 双向 | binary |
| `0x32` | `udp.close` | 双向 | binary |
| `0x40` | `event.report` | `frpc -> frps` | binary |
| `0x41` | `error` | 双向 | binary |

首版不单独定义“读请求”和“写请求”消息。对于 TCP：

- `stream.open` 表示创建逻辑连接。
- `stream.data` 表示该逻辑连接上的字节流数据。
- 数据方向由发送方决定，不再拆成 `read` 和 `write` 两种消息。

## 6. 登录与建链协议

## 6.1 `auth.begin`

用途：

- `frpc` 发起登录第一步，只发送公开定位段 `tokenId` 与基础环境信息。

头字段要求：

- `requestId` 必须非 `0`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `tokenId` | `bytes16` | token 公开定位段的原始 16 字节，不传 32 位 hex 文本 |
| 2 | `clientVersion` | `shortstr` | 客户端构建标识，仅用于日志和排查 |
| 3 | `hostname` | `shortstr` | 客户端主机名 |
| 4 | `os` | `u8` | 操作系统枚举 |
| 5 | `arch` | `u8` | CPU 架构枚举 |
| 6 | `capabilityBits` | `u32` | 首版固定为 `0`，后续能力协商使用 |

## 6.2 `auth.challenge`

用途：

- `frps` 返回一次性 challenge。

头字段要求：

- `requestId` 必须等于触发它的 `auth.begin.requestId`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `challengeId` | `u32` | 服务端内存中的一次性 challenge ID |
| 2 | `nonce` | `bytes16` | 一次性随机数 |
| 3 | `expiresInMs` | `u32` | challenge 有效期 |

## 6.3 `auth.finish`

用途：

- `frpc` 提交 challenge 响应。

头字段要求：

- `requestId` 必须非 `0`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `challengeId` | `u32` | 对应 `auth.challenge.challengeId` |
| 2 | `response` | `bytes32` | `sha256(token_hash + challenge_nonce)` 的原始 32 字节 |

## 6.4 `server.hello`

用途：

- `frps` 在登录成功后返回连接级运行参数。

头字段要求：

- `requestId` 必须等于触发它的 `auth.finish.requestId`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `heartbeatIntervalMs` | `u32` | 心跳间隔 |
| 2 | `sessionId` | `u64` | 当前控制连接运行态 ID |
| 3 | `capabilityBits` | `u32` | 服务端能力位，首版固定为 `0` |
| 4 | `serverVersion` | `shortstr` | 服务端版本 |
登录失败时，`frps` 不发送 `server.hello`，而是发送 `error` 后关闭连接。

当前开发阶段不做 `frps/frpc` 版本兼容协商；`clientVersion` 和 `serverVersion` 只用于诊断，同仓代码按同步升级处理。

## 6.5 登录时序

```text
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
```

## 7. 心跳协议

## 7.1 `heartbeat.ping`

用途：

- `frpc` 周期性上报在线状态并保活。

头字段要求：

- `requestId` 必须非 `0`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `clientUnixMs` | `unixMs` | 客户端当前时间 |
| 2 | `activeStreams` | `u32` | 当前 TCP stream 数 |
| 3 | `activeUdpSessions` | `u32` | 当前 UDP session 数 |
| 4 | `lastAckedConfigVersion` | `u64` | 客户端已确认配置版本 |

## 7.2 `heartbeat.pong`

用途：

- `frps` 响应 `heartbeat.ping`。

头字段要求：

- `requestId` 必须等于 `heartbeat.ping.requestId`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `clientUnixMs` | `unixMs` | 回显客户端时间 |
| 2 | `serverUnixMs` | `unixMs` | 服务端当前时间 |

## 7.3 超时规则

- `frpc` 必须按 `server.hello.heartbeatIntervalMs` 发送 `heartbeat.ping`。
- `frps` 连续多个心跳周期未收到 `heartbeat.ping` 时，可判定该客户端离线并关闭连接。
- 心跳超时后，该底层连接上的所有 `streamId` 一并失效。

## 8. 配置同步协议

## 8.1 `config.push`

用途：

- `frps` 下发完整执行快照。

头字段要求：

- `requestId` 必须非 `0`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `configVersion` | `u64` | 配置版本 |
| 2 | `generatedAtMs` | `unixMs` | 快照生成时间 |
| 3 | `tunnelCount` | `u16` | 隧道数量 |
| 4 | `tunnels` | `TunnelEntry[tunnelCount]` | 隧道执行配置 |

`TunnelEntry`：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `tunnelId` | `u32` | wire tunnel id，`0` 非法 |
| 2 | `protocol` | `u8` | `1=tcp`，`2=udp` |
| 3 | `tunnelFlags` | `u8` | bit0=`enabled`，bit1=`range`，其余位固定为 `0` |
| 4 | `reserved` | `u16` | 固定为 `0` |
| 5 | `remoteStart` | `u16` | 远端起始端口 |
| 6 | `remoteEnd` | `u16` | 远端结束端口；单端口时等于 `remoteStart` |
| 7 | `localHost` | `Host` | 本地目标主机 |
| 8 | `localStart` | `u16` | 本地起始端口 |
| 9 | `localEnd` | `u16` | 本地结束端口；单端口时等于 `localStart` |

约束如下：

- `config.push` 始终发送完整快照，不发送增量 patch。
- body 只包含 `frpc` 执行转发所需字段。
- `client/tunnel` ACL、限速、抓包策略等只在 `frps` 执行的字段，不得下发到 `frpc`。
- `wire tunnel id` 由 `frps` 分配，只要求在当前连接与当前配置快照下稳定。

## 8.2 `config.ack`

用途：

- `frpc` 确认某个配置版本已成功或失败。

头字段要求：

- `requestId` 必须等于对应 `config.push.requestId`。
- `streamId` 必须为 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `configVersion` | `u64` | 对应配置版本 |
| 2 | `appliedAtMs` | `unixMs` | 应用完成时间 |
| 3 | `status` | `u8` | `1=ok`，`2=error` |
| 4 | `errorCode` | `u16` | 成功时为 `0` |
| 5 | `message` | `shortstr` | 成功时应为空 |

## 8.3 配置同步规则

- `frpc` 收到新的 `config.push` 后，必须先构造新快照，再原子替换运行态。
- `config.ack.status = ok` 后，表示后续新建工作流必须使用该版本。
- 已经打开的 TCP `stream` 继续绑定其打开瞬间的隧道快照，不因配置更新被强制改写。

## 9. TCP 工作流协议

## 9.1 `stream.open`

用途：

- `frps` 通知 `frpc` 为某个公网接入连接创建对应的本地 TCP 连接。

头字段要求：

- `requestId` 必须非 `0`。
- `streamId` 必须为 `frps` 新分配的正整数。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `tunnelId` | `u32` | 来自当前已确认配置快照的 wire tunnel id |
| 2 | `remotePort` | `u16` | 公网访问的 `frps` 端口 |
| 3 | `clientAddr` | `SockAddr` | 公网访问者地址 |
| 4 | `openedAtMs` | `unixMs` | 服务端接入时间 |

`frpc` 收到 `stream.open` 后必须：

1. 用 `streamId` 创建运行态记录。
2. 依据当前已确认的 `configVersion` 查找 `tunnelId`。
3. 计算本地目标地址。
4. 拨号本地 TCP 服务。
5. 成功则回复 `stream.opened`。
6. 失败则回复 `stream.opened` 失败结果，随后结束该 `streamId`。

## 9.2 `stream.opened`

用途：

- `frpc` 告知 `frps` 该逻辑连接是否已准备好。

头字段要求：

- `requestId` 必须等于对应 `stream.open.requestId`。
- `streamId` 必须等于对应 `stream.open.streamId`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `status` | `u8` | `1=ok`，`2=error` |
| 2 | `errorCode` | `u16` | 成功时为 `0` |
| 3 | `message` | `shortstr` | 成功时应为空 |

## 9.3 `stream.data`

用途：

- 在某个 `streamId` 上承载原始 TCP 字节流。

头字段要求：

- `requestId` 固定为 `0`。
- `streamId` 必须为目标逻辑流 ID。
- body 为原始字节，不做任何二次包装。

规则如下：

- `stream.data` 的方向由发送方决定。
- 同一发送方在同一 `streamId` 上发送的数据顺序必须保持不变。
- 不同 `streamId` 之间允许交错发送。
- `frps` 与 `frpc` 都不得假设一个 `stream.data` 对应对端的一次 `read` 或一次 `write`；它只是一段连续字节。

## 9.4 `stream.close`

用途：

- 关闭某个 `streamId`。

头字段要求：

- `requestId` 固定为 `0`。
- `streamId` 必须为目标逻辑流 ID。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `reasonCode` | `u16` | 关闭原因 |
| 2 | `initiator` | `u8` | `1=frps`，`2=frpc` |
| 3 | `message` | `shortstr` | 可为空 |

首版不支持半关闭。任一端发送 `stream.close` 后，该 `streamId` 立即进入终态：

- 发送方不得再继续发送该 `streamId` 的 `stream.data`。
- 接收方收到后必须尽快释放相关资源。

## 9.5 TCP 时序

```text
public tcp accepted on frps
-> frps allocates streamId
-> frps sends stream.open
-> frpc dials local target
-> frpc sends stream.opened(status=ok)
-> both sides exchange stream.data
-> either side sends stream.close
-> both sides release stream resources
```

## 10. UDP 会话协议

## 10.1 `udp.open`

用途：

- `frps` 在首次看到某个公网 UDP 会话时，通知 `frpc` 建立对应的本地 UDP 会话上下文。

头字段要求：

- `requestId` 必须非 `0`。
- `streamId` 必须为 `frps` 为该 UDP 会话分配的 `sessionId`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `tunnelId` | `u32` | 来自当前已确认配置快照的 wire tunnel id |
| 2 | `remotePort` | `u16` | 公网访问的 `frps` 端口 |
| 3 | `clientAddr` | `SockAddr` | 公网访问者地址 |
| 4 | `idleTimeoutMs` | `u32` | UDP 会话空闲超时 |

## 10.2 `udp.data`

用途：

- 在某个 UDP 会话上转发单个原始数据报。

头字段要求：

- `requestId` 固定为 `0`。
- `streamId` 填对应 `sessionId`。
- body 为一个完整 datagram 的原始字节。

规则如下：

- 一个 `udp.data` 帧恰好对应一个 UDP datagram。
- 首版不对 datagram 再做额外切片；如果单个数据报超过上限，发送方必须丢弃并记录错误。
- `frpc` 依据 `sessionId` 找到对应的本地 UDP 会话，把 body 原样写给本地目标。

## 10.3 `udp.close`

用途：

- 关闭某个 UDP 会话。

头字段要求：

- `requestId` 固定为 `0`。
- `streamId` 填对应 `sessionId`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `reasonCode` | `u16` | 关闭原因 |
| 2 | `initiator` | `u8` | `1=frps`，`2=frpc` |
| 3 | `message` | `shortstr` | 可为空 |

## 10.4 UDP 时序

```text
frps receives first public datagram
-> frps allocates sessionId
-> frps sends udp.open
-> frps sends udp.data
-> frpc forwards datagram to local udp target
-> local response returns to frpc
-> frpc sends udp.data back with same sessionId
-> idle timeout or error
-> either side sends udp.close
```

## 10.5 UDP 生命周期约定

- `frps` 是 UDP session 生命周期的唯一裁决方：`sessionId` 由 `frps` 分配，空闲超时也由 `frps` 判断并下发 `udp.close`。
- `frpc` 不做本地 idle timeout 判断；只有在收到 `udp.close`、发生本地不可恢复错误，或底层控制连接结束时才释放本地 UDP session。
- `idleTimeoutMs` 的语义是“距离最后一次成功转发 datagram 已经空闲多久”，不是固定 `30s` 轮询窗口。
- 只要该 session 有任一路径的数据成功转发，`frps` 就必须立即刷新该 session 的活跃时间；也就是最新 datagram 发完后重新开始计算空闲时间。
- 实现可以用周期性 sweep 来检查超时，但行为语义必须等价于“按最后一次活动时间计算空闲时长”；因此实际清理可以发生在阈值附近，而不要求精确绑定某个独立定时器。

## 11. 事件与错误协议

## 11.1 `event.report`

用途：

- `frpc` 上报关键运行事件，便于 `frps` 记录日志并在 WebUI 可见。

头字段要求：

- `requestId` 固定为 `0`。
- `streamId` 通常为 `0`；若事件明确归属某个 `streamId`，可以填对应值。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `eventUnixMs` | `unixMs` | 事件时间 |
| 2 | `level` | `u8` | `1=info`，`2=warn`，`3=error` |
| 3 | `category` | `u8` | `1=auth`，`2=config`，`3=stream`，`4=udp`，`5=runtime` |
| 4 | `eventCode` | `u16` | 事件码 |
| 5 | `relatedTunnelId` | `u32` | 无关联时为 `0` |
| 6 | `relatedStreamId` | `u32` | 无关联时为 `0` |
| 7 | `message` | `shortstr` | 可为空 |

## 11.2 `error`

用途：

- 传递通用错误，通常用于连接级协议错误或请求失败。

头字段要求：

- 请求失败时，`requestId` 应尽量回填触发它的请求 ID。
- 若错误与某个逻辑流相关，`streamId` 填对应值；否则填 `0`。

body：

| 顺序 | 字段 | 类型 | 说明 |
| --- | --- | --- | --- |
| 1 | `errorCode` | `u16` | 错误码 |
| 2 | `retryable` | `bool` | 是否建议重试 |
| 3 | `message` | `shortstr` | 可为空 |

## 11.3 错误码

| code | 名称 |
| --- | --- |
| `1001` | `protocol_invalid_length` |
| `1002` | `protocol_unknown_type` |
| `1003` | `protocol_invalid_version` |
| `1004` | `protocol_invalid_flags` |
| `1005` | `protocol_bad_body` |
| `1101` | `auth_invalid_token` |
| `1102` | `auth_denied_by_ip` |
| `1103` | `auth_group_disabled` |
| `1104` | `auth_challenge_expired` |
| `1105` | `auth_challenge_replayed` |
| `1107` | `auth_client_limit_reached` |
| `1201` | `config_apply_failed` |
| `1301` | `stream_tunnel_not_found` |
| `1302` | `stream_local_dial_failed` |
| `1303` | `stream_not_found` |
| `1401` | `udp_session_not_found` |

`1001 protocol_invalid_length` 主要用于日志、指标和连接关闭原因归类；当最外层长度前缀非法时，不要求在线上发送 `error` 帧。

## 11.4 关闭原因码

| code | 名称 |
| --- | --- |
| `1` | `eof` |
| `2` | `local_dial_failed` |
| `3` | `read_error` |
| `4` | `write_error` |
| `5` | `admin_terminated` |
| `6` | `client_offline` |
| `7` | `server_shutdown` |
| `8` | `idle_timeout` |
| `9` | `protocol_error` |

## 11.5 错误处理规则

- 传输层 framing 错误，例如长度非法、长度前缀读取失败、合法长度但整帧 body 因 EOF/超时未收满，必须立即关闭底层连接，不发送业务层 `error` 帧。
- 业务协议错误，例如头字段非法、未知版本、保留位非零、body 解码失败，发生在完整业务帧已收齐之后；此时应尽量发送 `error`，随后关闭底层连接。若发送 `error` 本身失败，则直接关闭连接。
- 逻辑流错误，例如本地拨号失败、UDP 会话找不到，只关闭对应 `streamId` 或 `sessionId`，不应直接打断整条底层连接。
- 登录阶段的业务错误发送 `error` 后应立即关闭底层连接。

## 12. 并发与顺序语义

- 同一条 `connection` 上允许多个 `streamId` 并发复用。
- 每个 `streamId` 的数据顺序必须在发送侧保持 FIFO。
- 不同 `streamId` 之间的发送顺序不做全局保证。
- `config.push` 与 `stream.open` 可以并发存在，但 `frpc` 在处理某个 `stream.open` 时，必须使用它收到并确认的最新配置快照。
- 发送队列实现上必须保证控制帧优先级高于数据帧。

## 13. 实现边界建议

建议代码上拆成两层：

- `pkg/transport`：只负责 `[4-byte length][frame]` 的读写、最大长度校验、读超时和非法长度快速断连。
- `pkg/protocol`：负责 `12` 字节业务头、二进制 body 编解码、错误码和时序约束。

这样可以把“包边界”与“业务消息”分开实现，避免未来调整业务协议时把底层分包逻辑一起改坏。
