# frpc 技术设计

## 1. 设计目标

`frpc` 需要满足以下技术目标：

- 保持二进制体积和依赖简单。
- 在服务端托管配置前提下稳定运行。
- 对网络抖动和断线具备自动恢复能力。
- 对本地 TCP/UDP 目标的转发开销尽量小。

## 2. 模块划分

当前代码拆为以下模块：

### 2.1 `cmd/frpc`

- 参数读取
- 日志初始化
- 客户端启动

### 2.2 `internal/client`

- 主生命周期管理
- 连接建立
- 断线重连
- 退出控制

当前第一轮骨架收束后，`internal/client` 已形成下面这组最小文件 ownership：

- `client.go`：`Run`、`runOnce`、`runSession`、通用帧读写和基础错误解码
- `login.go`：`auth.begin` / `auth.finish` / `server.hello` / 首次 `config.push` 登录闭环
- `session.go`：`sessionState`、request id / snapshot 基础设施、`readLoop`、`heartbeatLoop`
- `targets.go`：tunnel 查找、TCP/UDP 共用的 local target 解析和 range 端口换算
- `tcp_bridge.go`：TCP stream 打开、数据转发、关闭与本地 TCP copy
- `udp_bridge.go`：UDP session 打开、数据转发、关闭与本地 UDP copy
- `runtime.go`：主机名、OS、架构等运行时信息探测

### 2.3 `internal/config`

- 启动参数解析
- 最小运行配置结构

当前实现仍保持单一 `internal/client` package，不新增装配层或跨端共享 runtime 框架；后续继续只收紧 bridge 文件名和 ownership，不改变现有协议和行为边界。

## 3. 启动参数设计

业务参数只保留两个：

- `--server`
- `--token`

调试类配置建议通过环境变量控制，而不是增加业务参数。例如：

- `FRPC_LOG_LEVEL`
- `FRPC_DEBUG`

这样可以维持用户侧的简单启动方式。

`--token` 的内容采用固定长度拼接格式：

```text
token = token_id + token_secret
```

首版建议：

- `token_id` 为 32 位小写 hex
- `token_secret` 为 64 位小写 hex

客户端不依赖 `.` 等分隔符解析 token，而是按固定长度截取 `token_id` 和 `token_secret`。如果长度或字符集非法，启动阶段直接失败。

## 4. 控制连接设计

`frpc` 与 `frps` 之间至少需要一条长连接，用于：

- 登录
- 接收配置版本
- 回应配置版本
- 心跳保活
- 接收工作流打开请求
- 上报错误事件

建议流程：

```text
connect server
-> parse token_id and token_secret from token
-> send auth.begin with token_id
-> receive auth.challenge
-> calculate token_hash = sha256(token_secret)
-> send auth.finish with sha256(token_hash + challenge_nonce)
-> receive server.hello
-> receive config.push
-> send config.ack
-> start heartbeat
-> wait for stream.open
```

## 5. 配置同步设计

`frpc` 不持久化复杂配置，只维护运行期快照。

收到 `config.push` 后：

1. 校验配置版本是否更新
2. 构造新的运行时快照
3. 原子替换本地运行时配置
4. 返回 `config.ack`
5. 清理已删除隧道的空闲运行态资源
6. 保留仍有效的活跃流

需要注意：

- 配置更新不能中断无关流量
- 已建立 TCP 连接可自然结束
- 新建流必须使用最新配置
- 客户端进程不允许因为配置更新而重启
- `config.push` 不包含服务端 ACL、限速器状态或抓包策略，这些能力只在 `frps` 执行

活跃流处理原则：

- 每个 stream 在打开时绑定当时的隧道快照或已解析本地目标。
- 后续配置修改不回写已打开 stream 的目标地址。
- 隧道被删除或禁用后，新 stream 不再允许打开；已有 stream 是否关闭由 `frps` 通过 `stream.close` 明确控制。

## 6. TCP 工作流设计

服务端下发 `stream.open` 时，`frpc` 执行：

1. 根据 `tunnelId` 找到隧道配置
2. 计算目标本地地址
3. 拨号本地 TCP 服务
4. 建立双向转发
5. 上报打开成功或失败
6. 收到服务端 `stream.close` 时立即回收本地连接和 goroutine

目标地址计算：

- 单端口映射时直接使用配置的 `local_host:local_port`
- 端口范围映射时按 `remotePort` 计算偏移

固定约定：

- `remotePort` 始终表示公网侧真实命中的 `frps` 监听端口。
- TCP 与 UDP 范围映射共用同一套公式：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

- `frpc` 只依据当前配置快照和消息中的 `remotePort` 计算目标本地端口，不依赖 listener 创建顺序或额外拆分出来的子 tunnel 标识。

## 7. UDP 工作流设计

UDP 采用短会话模式，但当前生命周期约定已经固定：

- `sessionId` 由 `frps` 分配，`frpc` 按 `sessionId` 维护本地 UDP session
- `frpc` 在收到 `udp.open` 后建立真实本地 `UDPConn`
- `frpc` 收到 `udp.data` 后把单个 datagram 原样写给本地 UDP 服务
- 本地 UDP 回包由 `frpc` 按原 `sessionId` 回发给 `frps`
- UDP 范围映射的目标端口计算规则与 TCP 完全一致，同样依据 `udp.open.remotePort` 做偏移换算
- `frps` 是 UDP session 生命周期的唯一裁决方，空闲 `30s` cleanup 也由 `frps` 判断
- `frpc` 不做本地 idle timer；只在收到 `udp.close`、发生本地不可恢复错误，或控制连接结束时释放本地 UDP session

当前实现优先保证功能闭环，不引入更复杂的 UDP 优化、限速感知或本地定时器状态机。

## 8. 重连设计

必须支持自动重连。

建议策略：

- 初始 1 秒后重连
- 指数退避到 30 秒上限
- 登录成功后退避计数清零

断线后：

- 清理失效控制连接
- 停止依赖该控制连接的新工作流
- 重新登录并同步最新配置

## 9. 错误上报

建议上报以下事件给 `frps`：

- 本地目标拨号失败
- 本地连接中途断开
- 配置版本异常
- 配置应答失败
- 控制连接解析失败
- UDP 会话异常
- 收到管理端强制断开

上报目的不是替代日志，而是让 `frps` WebUI 可见。

## 10. 日志设计

日志建议至少区分：

- `client`
- `session`
- `proxy.tcp`
- `proxy.udp`

日志字段建议包含：

- 时间
- 级别
- 隧道 ID
- stream ID
- 错误摘要

## 11. 开发阶段版本策略

当前开发阶段不做 `frpc` 与 `frps` 的协议版本兼容协商。

约定：

- 登录时仍可上报 `clientVersion`，但它只用于日志和排查
- `server.hello` 返回的 `serverVersion` 也只用于诊断
- 如果协议有破坏性调整，按同仓同步升级 `frps` / `frpc` 处理，不维护兼容层

## 12. 安全要求

- token 不落盘。
- 客户端只在内存中保存 `token_secret` 和本次登录派生出的 `token_hash`。
- 登录响应必须绑定服务端下发的一次性 challenge nonce。
- 控制连接只保存必要上下文。
- 本地目标地址必须来自服务端下发的有效配置。
- 不对服务端限速做本地推断、缓存或二次实现。
- 对异常输入做长度和类型校验，避免协议解析被拖垮。

## 13. 推荐实现顺序

1. CLI 与配置解析
2. 控制连接和登录
3. 配置同步
4. TCP 工作流
5. 自动重连
6. UDP 工作流
7. 错误事件上报
