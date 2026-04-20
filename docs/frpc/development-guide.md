# frpc 开发文档

## 1. 开发原则

开发 `frpc` 时优先守住两条边界：

- 客户端只做执行，不做策略决策。
- 客户端只保留当前真实需要的最小状态。

因此：

- 不把 ACL、限速、抓包搬到 `frpc`
- 不提前做本地配置中心
- 不为了未来模式扩展提前加复杂抽象

统一工作流见 [../workflow.md](../workflow.md)。

## 2. 当前目录

```text
frpc/
├── cmd/frpc/
├── internal/client/
├── internal/config/
└── go.mod
```

## 3. 环境要求

- Go 1.23 或更高
- 可访问一个测试 `frps`
- 可准备本地 TCP/UDP 测试服务

## 4. 本地运行

```powershell
cd frpc
go build -o ./frpc.exe ./cmd/frpc
.\frpc.exe --server 127.0.0.1:7000 --token <token>
```

日志级别：

```powershell
$env:FRPC_LOG_LEVEL="debug"
```

## 5. 当前主要修改落点

### 5.1 参数和 token 解析

涉及文件：

- `internal/config/config.go`

需要同步关注：

- token 长度
- 小写 hex 规则
- `server` 地址校验

### 5.2 登录或会话

涉及文件：

- `internal/client/login.go`
- `internal/client/session.go`
- `frps/pkg/protocol/*`

需要同步关注：

- 登录握手顺序
- 首次 `config.push` / `config.ack`
- 心跳语义

### 5.3 TCP/UDP bridge

涉及文件：

- `internal/client/targets.go`
- `internal/client/tcp_bridge.go`
- `internal/client/udp_bridge.go`

需要同步关注：

- 范围映射公式
- stream/session 回收
- UDP 生命周期边界

## 6. 当前最常用命令

```powershell
cd frpc
go test ./...
go build -o NUL ./cmd/frpc
```

常用联调命令：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_udp_single.py --scenario happy_path
```

## 7. 提交前检查

改动 `frpc` 时，至少检查：

- `go test ./...`（`frpc/`）
- 如果改了 TCP bridge，跑 TCP e2e
- 如果改了 UDP bridge，跑 UDP e2e
- 如果改了登录或协议头部，至少同时检查 `frps` 对应测试和一个真实 e2e
- 文档是否同步到当前真实边界

完整命令矩阵见 [../regression-entry.md](../regression-entry.md)。
