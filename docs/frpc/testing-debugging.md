# frpc 测试与调试文档

## 1. 当前测试目标

`frpc` 当前测试重点是：

- 是否能正确登录 `frps`
- 是否能正确应用当前配置快照
- TCP/UDP 本地转发是否正确
- 控制连接断开后是否能自动恢复

## 2. 当前自动化验证

### 2.1 Go 测试

```powershell
cd frpc
go test ./...
```

当前重点覆盖：

- 参数校验
- token 解析
- 登录逻辑
- target 解析
- 范围端口换算
- TCP/UDP bridge 关键路径

### 2.2 Python e2e

当前 `frpc` 依赖仓库级端到端脚本验证：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_tcp_single.py --scenario bad_token
python test/e2e_tcp_single.py --scenario disabled_group
python test/e2e_tcp_single.py --scenario disabled_tunnel
python test/e2e_tcp_single.py --scenario local_unavailable
python test/e2e_tcp_range.py
python test/e2e_udp_single.py --scenario happy_path
python test/e2e_udp_single.py --scenario idle_cleanup
python test/e2e_udp_range.py
```

覆盖范围：

- 登录成功和失败
- TCP 单端口与范围
- UDP 单端口与范围
- UDP idle cleanup
- 本地目标不可达

## 3. 当前手工调试

### 3.1 启动失败

优先排查：

- `server` 是否为空
- `token` 是否为空
- token 是否为 `96` 位小写 hex

### 3.2 无法登录

优先排查：

- `server:7000` 是否可达
- token 是否正确
- 分组是否启用
- 来源 IP 是否被 `group_client_ip_rules` 拒绝
- 同分组是否已有在线客户端

### 3.3 已登录但无转发

优先排查：

- 是否已经收到首次 `config.push`
- 是否已经回 `config.ack`
- 本地目标是否真的在监听
- `remotePort` 是否命中了预期 tunnel

### 3.4 UDP 行为异常

当前应按下面规则判断：

- `frpc` 不做本地 idle timer
- `udp.close` 应来自 `frps`
- 本地 UDP session 只在收到 `udp.close`、本地不可恢复错误或控制连接结束时释放

如果看到“`frpc` 自己判断 30 秒超时”，那不是当前实现。

## 4. 当前常用调试命令

```powershell
Test-NetConnection 127.0.0.1 -Port 7000
Test-NetConnection 127.0.0.1 -Port 8080
```

```powershell
$env:FRPC_LOG_LEVEL="debug"
.\frpc.exe --server 127.0.0.1:7000 --token <token>
```

## 5. 常见故障

### 5.1 频繁重连

优先排查：

- `frps` 是否在线
- 控制连接是否被服务端关闭
- 协议解析是否报错
- 心跳是否超时

### 5.2 TCP 范围映射错误

优先排查：

- `stream.open.remotePort` 是否是实际命中的公网端口
- tunnel 的 `remoteStart/remoteEnd` 与 `localStart/localEnd` 是否对齐

### 5.3 UDP 会话没有回收

优先排查：

- `frps` 是否真的发出了 `udp.close`
- `frpc` 是否因为控制连接断开而先退出

## 6. 回归入口

仓库级命令矩阵见 [../regression-entry.md](../regression-entry.md)。
