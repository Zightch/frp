# frpc 技术设计

## 1. 设计目标

`frpc` 需要满足以下技术目标：

- 保持二进制体积和依赖简单。
- 在服务端托管配置前提下稳定运行。
- 对网络抖动和断线具备自动恢复能力。
- 对本地 TCP/UDP 目标的转发开销尽量小。

## 2. 模块划分

建议拆为以下模块：

### 2.1 `cmd/frpc`

- 参数读取
- 日志初始化
- 客户端启动

### 2.2 `internal/client`

- 主生命周期管理
- 连接建立
- 断线重连
- 退出控制

### 2.3 `internal/session`

- 登录协议
- 心跳
- 配置同步
- 配置应答
- 服务端事件接收

### 2.4 `internal/proxy`

- TCP 工作流
- UDP 工作流
- 本地目标拨号
- 数据收发复制

### 2.5 `internal/runtime`

- 当前配置版本
- 当前隧道索引
- 当前活跃 stream
- 当前 UDP session
- 活跃 stream 绑定的隧道快照

### 2.6 `internal/config`

- 启动参数解析
- 最小运行配置结构

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

## 7. UDP 工作流设计

UDP 采用短会话模式：

- 按五元组或服务端分配的 session id 维护
- 长时间无活动自动回收
- 每次发送后刷新最后活跃时间

第一阶段可接受功能优先，不强求复杂 UDP 优化。

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

## 11. 协议兼容性

`frpc` 必须与 `frps` 明确版本协商。

建议：

- 登录时上报 `clientVersion`
- `frps` 返回 `minSupportedVersion` 或兼容范围
- 严重不兼容时直接拒绝登录

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
