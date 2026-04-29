# frpc 项目概览

## 1. 当前定位

`frpc` 当前是一个极简正向代理客户端，只做三件事：

- 连接 `frps`
- 领取配置
- 执行本地 TCP/UDP 转发

当前不承担管理、策略、观测或反向代理职责。

## 2. 当前已交付范围

当前 `frpc` 已具备：

- CLI 参数：
  - `--server`
  - `--key`
  - `--version`
- 固定长度 `key` 解析：
  - `client_id`：`32` 位小写 hex
  - `client_secret`：`64` 位小写 hex
- 登录握手：
  - `transport.client_hello`
  - `transport.server_hello`
  - 按服务端选择在同一 TCP 连接上升级 TLS
  - `auth.begin`
  - `auth.challenge`
  - `auth.finish`
  - `server.hello`
- 首次 `config.push` / `config.ack`
- 同一控制连接上的后续在线 `config.push` / `config.ack`
- 心跳保活
- TCP 单端口转发
- TCP 连续范围映射
- UDP 单端口转发
- UDP 连续范围映射
- 自动重连
- 本地拨号或本地读写失败后的错误回传

## 3. 当前明确边界

下面这些内容当前不属于 `frpc` 的职责：

- 本地管理页面
- 本地复杂配置文件
- 反向代理
- 本地 ACL
- 服务端限速执行
- 服务端抓包执行
- 本地 UDP idle timer
- 一个分组多个并发客户端的竞争语义

`frpc` 当前只执行来自 `frps` 的指令，不自行决定隧道规则。

## 4. 当前目录

```text
frpc/
├── cmd/frpc/
├── internal/client/
├── internal/config/
└── go.mod
```

`internal/client` 当前稳定文件边界：

- `client.go`
- `login.go`
- `session.go`
- `targets.go`
- `tcp_bridge.go`
- `udp_bridge.go`
- `runtime_info.go`

共享协议和传输层复用 `frps/pkg/protocol` 与 `frps/pkg/transport`。

## 5. 当前运行方式

```text
frpc --server 1.2.3.4:7000 --key <key>
```

日志级别通过环境变量控制：

- `FRPC_LOG_LEVEL=debug|info|warn|error`

## 6. 当前生命周期

当前启动后流程固定为：

1. 校验 `server`
2. 解析 `key`
3. 建立控制连接
4. 登录
5. 接收首次配置
6. 回 `config.ack`
7. 启动心跳和读循环
8. 等待后续 `config.push`、`stream.*` / `udp.*`
9. 断线后指数退避重连

## 7. 当前验证

`frpc` 当前已通过：

- `go test ./...`（`frpc/`）
- `python test/e2e_tcp_single.py`
- `python test/e2e_tcp_range.py`
- `python test/e2e_udp_single.py`
- `python test/e2e_udp_range.py`

更细说明见：

- [frpc 技术设计](technical-design.md)
- [frpc 开发文档](development-guide.md)
- [frpc 测试与调试文档](testing-debugging.md)
