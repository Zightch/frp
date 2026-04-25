# frps 控制面技术设计

## 传输层

- `pkg/transport` 负责长度前缀收发
- `pkg/protocol` 负责业务帧解析
- 默认读写超时 `5s`
- challenge TTL `30s`
- heartbeat 间隔 `15s`

会话读超时当前按下面规则计算：

```text
max(3 * heartbeat_interval, minimum_read_timeout)
```

## 当前登录握手

当前 `frpc` 登录口（默认端口 `7000`）已经固定为“两段握手”：

```text
frpc -> transport.client_hello
frps -> transport.server_hello
if selected_security_mode == tls:
    TLS handshake
frpc -> auth.begin
frps -> auth.challenge
frpc -> auth.finish
frps -> server.hello
frps -> config.push
frpc -> config.ack
frps -> start listeners
```

服务端登录阶段当前会做：

1. 读取 `transport.client_hello`
2. 按 `client_id` 读取 `GroupRuntime`
3. 按分组 `control_transport_security` 决定 `plain` 还是 `tls`
4. 如需 TLS，则在同一 TCP 连接上升级到 TLS
5. 读取 `auth.begin`，并校验其中 `client_id` 与 hello 一致
6. 签发 challenge
7. 常量时间校验 challenge 响应
8. 抢占单分组单客户端槽位
9. 返回 `server.hello`
10. 下发首次 `config.push`

## 首轮扫描门闩

当前控制面在真正打开 `frpc` 登录监听口（默认端口 `7000`）前，已经先执行过一次 `EnsureInitialRuntimeScan()`。

这会保证：

- 首次外显的隧道状态已经过 runtime scan
- 首次 `frpc` 登录不会早于首轮扫描
- 管理面与控制面共用同一轮启动门闩

## `ConfigSnapshot`

当前 `ConfigSnapshot` 由仓储层投影生成：

- `Version`
- `GeneratedAtMs`
- `Tunnels`

使用边界：

- 首次登录与在线热重载复用同一快照路径
- `config.ack` 成功后才启动或重启 listener
- 管理面写库成功且命中运行态字段时，如果分组在线，会触发整组补推

## 在线热重载

当前固定采用：

- 整组冻结
- 整组完整快照下发
- `ack` 后整组 listener 重建

关键规则：

- 同一分组同一时刻只允许一个未确认的在线配置更新
- 如果上一版 `config.push` 仍未确认，又来了下一版变更，直接断开当前控制连接
- `config.ack` 超时或失败，也直接断开连接，交给 `frpc` 重连

## 运行态恢复

当前未监听轮询与刷新路径已经收口为统一恢复编排：

- 运行态问题记录在 `runtime issue store`
- 在线 session、group slot、listener 绑定状态集中在 `runtime registry`
- 轮询恢复和 `RefreshGroup()` 共用同一套恢复语义

## 当前 TLS 边界

当前 `frpc` 登录传输已支持分组级 TLS 策略。

- `transport.client_hello` 必须前置携带 `client_id`
- `frps` 先按分组的 `control_transport_security` 决定返回明文还是 TLS
- 分组要求 TLS 时，TLS 成功后才进入现有 `auth.begin` 链路
- 服务端证书来源于 `certificate_asset_usages.frpc_tls`

相关设计见 [../design/certificate-binding.md](../design/certificate-binding.md)。
