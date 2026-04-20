# frpc 功能文档

## 1. 当前功能边界

`frpc` 当前支持：

- 连接 `frps`
- challenge/response 登录
- 接收配置
- 执行 TCP 转发
- 执行 UDP 转发
- 自动重连

`frpc` 当前不支持：

- 本地 WebUI
- 本地复杂配置
- 反向代理
- 本地 ACL
- 服务端限速感知
- 本地 UDP idle timer

## 2. 启动功能

当前命令：

```text
frpc --server 1.2.3.4:7000 --token <token>
```

当前要求：

- `server` 必填
- `token` 必填
- `server` 必须能被 `net.SplitHostPort` 解析
- `token` 必须是 `96` 位小写 hex

## 3. 登录功能

当前登录过程：

1. 从 token 里截取 `token_id` 和 `token_secret`
2. 发送 `auth.begin`
3. 接收 `auth.challenge`
4. 本地计算：
   - `token_hash = sha256(token_secret)`
   - `response = sha256(token_hash + nonce)`
5. 发送 `auth.finish`
6. 接收 `server.hello`
7. 接收首次 `config.push`
8. 回 `config.ack`

当前失败后行为：

- 输出明确错误
- 进入退避重连

## 4. 配置接收功能

当前 `frpc` 会接收：

- `configVersion`
- `generatedAtMs`
- `tunnels`

当前处理规则：

- 新快照直接替换旧快照
- 替换完成后回 `config.ack`
- 已存在的活跃 stream/session 继续由当前运行态自然收口
- 当前不接收 ACL、限速、抓包等服务端本地策略

## 5. TCP 转发功能

当前 TCP 行为：

1. 收到 `stream.open`
2. 根据 `tunnelId` 和 `remotePort` 解析本地目标
3. 拨号本地 TCP 服务
4. 成功后回 `stream.opened(status=ok)`
5. 失败后回 `stream.opened(status=error)`
6. 双向收发 `stream.data`
7. 收到或发送 `stream.close` 后回收本地连接

当前要求：

- 只关闭目标 `streamId`
- 不影响其他 stream

## 6. UDP 转发功能

当前 UDP 行为：

1. 收到 `udp.open`
2. 根据 `tunnelId` 和 `remotePort` 解析本地目标
3. 建立本地 `UDPConn`
4. 收到 `udp.data` 时把 datagram 原样写给本地服务
5. 本地回包时用同一 `sessionId` 回发给 `frps`
6. 收到 `udp.close` 后释放本地 UDP session

当前固定边界：

- 不做本地 idle timer
- 不主动按本地时间清理空闲 UDP session
- 生命周期由 `frps` 裁决

## 7. 范围映射功能

当前 TCP/UDP 范围映射都按固定偏移规则执行：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

当前要求：

- `remotePort` 必须落在当前 tunnel 范围内
- 计算后的 `localPort` 必须不超出本地范围

## 8. 重连功能

当前触发条件：

- 控制连接断开
- 登录阶段失败
- 读循环或心跳循环退出

当前行为：

- 清理本地 TCP stream
- 清理本地 UDP session
- 退避后重新连接
- 重新登录并重新同步配置

## 9. 当前不实现的能力

下面这些内容当前不能算作 `frpc` 已支持：

- 本地缓存复杂配置
- 多服务端接入
- 本地限速
- 本地抓包
- 本地规则系统
- 配置变更的主动热更新协商

这些能力如果后续开始实现，必须先更新本文档。
