# frps 测试与调试文档

## 1. 当前测试目标

`frps` 当前测试重点是：

- 启动配置和数据库是否可正常进入运行态
- 管理认证是否符合 `auth.json` 模型
- `frpc` 登录与配置下发是否正确
- TCP/UDP 正向代理链路是否正确
- UDP idle cleanup 语义是否稳定

## 2. 当前自动化验证

### 2.1 Go 测试

常用命令：

```powershell
cd frps
go test ./...
```

当前重点覆盖：

- `internal/app`
  - SQLite 数据目录创建
  - MySQL DSN 透传
  - schema bootstrap 与校验
- `internal/auth`
  - 初始化
  - challenge
  - session
  - 删除 `auth.json` 后复位
- `internal/api`
  - 认证接口
  - 管理接口
- `internal/control`
  - token challenge/response
  - group slot
  - `config.push` / `config.ack`
  - listener 行为
  - TCP/UDP 协议错误路径
- `pkg/protocol`
- `pkg/transport`

### 2.2 Python e2e

仓库当前已固化的端到端脚本：

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
python test/e2e_management_webui.py
python test/e2e_tcp_perf.py
```

覆盖范围：

- TCP 单端口主链路和负向场景
- TCP 连续范围映射
- UDP 单端口主链路
- UDP 空闲清理
- UDP 连续范围映射
- 管理认证、分组 CRUD、token 重置、隧道 CRUD
- 最小 TCP 健康压测

### 2.3 Python 平台探针（免编译）

用于快速验证 wildcard 绑定差异和 Linux 本机地址采集行为：

```powershell
python test/wildcard_bind_matrix.py --output test/tmp/wildcard-bind/windows.json
wsl -d ubuntu -u root bash -lc "python3 /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/wildcard_bind_matrix.py --output /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/tmp/wildcard-bind/linux.json"
wsl -d ubuntu -u root bash -lc "python3 /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/linux_network_snapshot_probe.py --output /mnt/c/Users/Zightch/Desktop/Aicksaim/frp/test/tmp/wildcard-bind/linux-network-scan.json --expect-ip 192.168.8.2 --expect-ip 192.168.8.130"
```

说明：

- `wildcard_bind_matrix.py` 会输出 TCP/UDP 的 `wildcard-wildcard`、`wildcard-specific`、IPv4/IPv6 组合第二次绑定是否成功。
- `linux_network_snapshot_probe.py` 按 `frps/internal/system/network_collect.go` 的排序与去重规则产出 Linux 地址快照，并校验关键 IP 是否存在。
- 两个脚本均为 Python，无需临时编译。

### 2.4 测试做法

对于涉及状态机、并发、热更新、轮询恢复、会话切换和故障注入的测试，统一按下面做法执行：

1. 先做可测性基建，再做具体场景。
2. 先把关键竞态点变成“可阻塞、可放行、可注入、可重复”，再写故障测试、竞争态测试和压力测试。

测试前默认先准备以下能力：

- 关键路径测试钩子：首轮扫描完成、控制端口开放、管理 API 首次可见、`config.push`、`pending config`、`config.ack`、listener freeze/close/bind、runtime scan、session shutdown、本机网络快照刷新等位置要能被测试明确卡住和放行。
- 确定性并发编排：竞争态测试默认使用 barrier/hook 编排交错顺序，不以 `sleep` 或“多跑几次”作为主手段。
- 可控时钟：轮询周期、超时、重试、心跳等等待逻辑优先用 fake clock / manual ticker 驱动。
- 可控注入：listener/bind、网络快照、传输层、session 行为要能脚本化制造占用、释放、断线、重连、重复 ack、晚到 ack、乱序 ack、地址抖动等场景。
- 不变量断言：优先断言状态机结果；对当前端口冲突与热更新链路，至少包括“首轮扫描完成前不可登录”“任一时刻最多一个有效 pending config”“旧 session 晚到消息不能污染新 session”“静态 `冲突` 优先于 runtime `异常`”“空配置恢复必须先补推完整快照再恢复 listener”。

测试时禁止把以下方式当作主验证：

- 依赖 CPU 快慢或调度偶然性触发竞态。
- 依赖长时间 `sleep` 窗口碰运气。
- 只看日志，不校验状态机不变量。

在可测性基建完成前，`-race`、高频扰动、多 `GOMAXPROCS`、长稳 soak 只作为补充捞漏手段，不作为通过依据。

## 3. 当前手工调试入口

### 3.1 服务是否启动

```powershell
curl http://127.0.0.1:7080/healthz
curl http://127.0.0.1:7080/api/v1/auth/state
Test-NetConnection 127.0.0.1 -Port 7000
```

### 3.2 管理认证

优先检查：

- `frps/data/auth.json` 是否存在
- `key_hash` 是否为 `64` 位小写 hex
- 删除 `auth.json` 后 `/api/v1/auth/state` 是否回到 `initialized=false`

### 3.3 `frpc` 无法登录

优先检查：

- 分组是否启用
- token 是否正确
- 同分组是否已有在线客户端占用槽位

### 3.4 公网端口不可用

优先检查：

- `frpc` 是否已经完成登录并返回 `config.ack`
- 目标 tunnel 是否启用
- 端口是否真的被监听
- 本地目标是否可达

当前一个常见原因是：`config.ack` 之前 listener 不会启动。

### 3.5 UDP 行为

当前应按下面语义排查：

- `frps` 是 UDP 生命周期唯一裁决方
- 成功转发后立即刷新活跃时间
- 空闲约 `30s` 后由 `frps` 清理并发送 `udp.close`
- `frpc` 不做本地 idle timer

如果看到“30 秒轮询”这类表述，应以协议文档和当前实现为准，而不是旧文档或旧理解。

## 4. 当前最小测试环境

最小本地联调通常需要：

- 一个 `frps`
- 一个 `frpc`
- 一个本地 TCP echo server
- 一个本地 UDP echo server
- 一个外网侧 TCP/UDP 测试客户端

脚本默认会创建隔离工作目录和临时 SQLite 数据库，优先用脚本复现。

## 5. 常见故障

### 5.1 启动即退出

优先排查：

- `data/config.json` 是否合法
- SQLite 路径或 MySQL DSN 是否正确
- 现有库结构是否和内嵌 schema 一致

当前代码不会自动迁移旧 schema。

### 5.2 管理 API 返回 `409`

常见原因：

- 管理密钥未初始化
- challenge 已过期或已重放
- 重复初始化

### 5.3 MySQL 无法连接

优先排查：

- DSN 是否为驱动标准格式
- 数据库是否可达
- 用户是否有建表权限

当前不再支持 DSN 地址简写兼容。

## 6. 回归入口

仓库级命令矩阵见 [../regression-entry.md](../regression-entry.md)。
