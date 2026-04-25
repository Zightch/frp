# 控制面与数据面

## 启动门闩

当前 `frps` 在真正开放管理面和 `7000` 控制端口前，会先完成两件事：

1. 证书资产运行时准备
2. 首轮“未监听启用 tunnel”运行态扫描

因此外部第一次看到的：

- 管理 API 状态
- 隧道 `status`
- `7000` 控制端口可见性

都已经晚于首轮扫描完成。

## frpc 登录

当前 `7000` 还是裸 TCP，登录握手是：

```text
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
frps -> start listeners
```

当前会检查：

- `client_id` 是否存在
- 分组是否启用
- challenge 是否有效且未重放
- challenge 响应是否匹配
- 当前分组单客户端槽位是否已被占用

`7000 TLS` 预协商和证书绑定还未落地，见 [../design/certificate-binding.md](../design/certificate-binding.md)。

## 配置快照

当前登录和在线热重载都走同一条 `ConfigSnapshot` 路径。

快照当前包含：

- `configVersion`
- `generatedAtMs`
- `tunnels`

当前不下发：

- ACL
- 分组总限速
- 抓包控制
- 反向代理配置
- 证书绑定信息

## 运行态恢复

启动后后台会固定轮询“当前没有 listener 的启用 tunnel”：

- 对离线分组，只负责写入和清理 runtime issue
- 对在线但只拉起了部分 tunnel 的分组，恢复后会直接在现有 session 上补启动缺失 tunnel
- 如果该 session 之前因 `effective_ip` 失效收到的是空配置，恢复时会先补发完整快照，再等待 `ack`，之后才恢复 listener

## TCP 转发

当前 TCP 行为：

- listener 只在对应 `config.ack` 后启动
- 公网连接进入后，`frps` 分配 `streamId`
- `frps` 发送 `stream.open`
- `frpc` 返回 `stream.opened`
- 双方通过 `stream.data` 传输原始字节
- 任一侧结束时用 `stream.close` 收口

## UDP 转发

当前 UDP 行为：

- 第一次看到某个公网会话时，`frps` 发送 `udp.open`
- 首包和后续包都通过 `udp.data` 转发
- `sessionId` 由 `frps` 分配
- `frps` 按 `tunnelId + remotePort + public client addr` 识别会话
- `frpc` 不做本地 idle timer
- `frps` 空闲约 `30s` 后删除会话并发送 `udp.close`
