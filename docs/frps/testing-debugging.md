# frps 测试与调试文档

## 1. 测试目标

`frps` 的测试重点不是单纯验证“能转发”，而是验证：

- 接入控制是否正确
- 转发链路是否稳定
- 管理配置是否热更新
- 实时观测是否可信
- 限速和抓包是否不破坏主链路

当前压测口径也按同一原则执行：优先看代码链路在受压时有没有异常，吞吐和延迟数字只作为发现退化的辅助手段，不作为首要目标。

## 1.1 当前已落地验证项

截至 2026-04-18，代码库里已经存在并应持续保留的第一阶段验证项包括：

- `storage.SQL` 的结构化查询结果测试
- `storage.SQL` 的 `Begin` / `BeginTx` / `WithTx` 事务测试
- MySQL 简写 DSN 规范化测试
- SQLite 数据目录自动创建测试
- 应用启动时自动建表和 schema 版本写入测试
- SQLite 现有表结构错误时启动立即失败测试
- 控制面单元测试：
  - token challenge/response
  - `config.push` / `config.ack`
  - listener 启停
  - 无效 `stream.open` 错误路径
- 最小 TCP 单端口端到端脚本：
  - `test/e2e_tcp_single.py`
  - `happy_path`
  - `bad_token`
  - `disabled_group`
  - `disabled_tunnel`
  - `local_unavailable`
- 最小 TCP 代码健康压测脚本：
  - `test/e2e_tcp_perf.py`
  - `stability`
  - `upload`
  - `download`

## 2. 测试分层

## 2.1 单元测试

覆盖模块：

- token 校验
- IP 规则匹配
- 端口范围映射
- 端口冲突检测
- 配置版本同步与应答状态机
- 速率统计窗口计算
- 限速器
- 路由匹配
- 单连接强制断开

建议命令：

```powershell
go test ./...
```

## 2.2 集成测试

覆盖链路：

- `frpc` 登录与配置下发
- `config.ack` 前后 listener 行为
- TCP 正向代理
- UDP 正向代理
- TCP 反向代理
- HTTP Host 分发
- HTTPS SNI 分发
- WebSocket 推送
- 在线连接强制断开

建议方式：

- 用 Go 集成测试拉起 `frps`
- 用假 `frpc` 或真实 `frpc` 二进制做接入
- 快速集成测试使用本地临时端口和临时 SQLite 数据库
- 数据库兼容性测试额外覆盖 MySQL

## 2.3 存储兼容性测试

必须验证：

- SQLite 和 MySQL 都能完成启动期 schema bootstrap 或版本推进
- 相同仓储接口在两种数据库下行为一致
- 唯一约束、索引、事务边界符合预期
- 管理员、分组、隧道等核心 CRUD 在两种数据库下结果一致

## 2.4 端到端测试

当前已落地的最小端到端测试目标：

- `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`
- 单客户端
- 冷启动
- SQLite 预注入
- TCP 单端口
- 加最小必要负向场景

当前脚本：

- `test/e2e_tcp_single.py`
- `test/e2e_tcp_perf.py`

当前脚本职责：

- 创建临时 SQLite 数据库
- seed `proxy_groups` / `tunnels`
- 启动真实 `frps` / `frpc`
- 启动 Python echo server
- 启动 Python 外网客户端
- 观察和断言真实控制面与数据面行为

性能脚本额外负责：

- 启动 Python 内网目标服务器
- 运行高并发稳定性 workload
- 运行上下行吞吐 workload
- 输出 `report.json` 和 `report.md`

当前固定场景：

- `happy_path`
- `bad_token`
- `disabled_group`
- `disabled_tunnel`
- `local_unavailable`

建议命令：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_tcp_single.py --scenario bad_token
python test/e2e_tcp_single.py --scenario disabled_group
python test/e2e_tcp_single.py --scenario disabled_tunnel
python test/e2e_tcp_single.py --scenario local_unavailable
```

```powershell
python test/e2e_tcp_perf.py
python test/e2e_tcp_perf.py --stability-concurrency 128 --stability-duration 30
python test/e2e_tcp_perf.py --transfer-concurrency 8 --transfer-bytes-per-connection 67108864
```

如需查看脚本实际写入的 SQLite 数据库，可加：

```powershell
python test/e2e_tcp_single.py --scenario happy_path --keep-temp
```

## 3. 最小测试环境

当前脚本默认动态分配测试端口，不要求固定本地端口。手工联调时可参考：

- `7000`：`frpc` 控制入口
- `7500`：WebUI/API
- `20000`：TCP 正向代理测试入口
- `20001`：UDP 正向代理测试入口
- `18080`：HTTP 反向代理测试入口
- `18443`：HTTPS 反向代理测试入口

建议本地上游服务：

- `127.0.0.1:8080`：测试 TCP echo
- `127.0.0.1:8081`：测试 HTTP echo
- 可选本地 MySQL 8.0 实例：用于数据库兼容性测试

## 4. 核心测试清单

## 4.1 `frpc` 接入控制

必须验证：

- token 正确可登录
- token 格式非法被拒绝
- `tokenId` 不存在被拒绝
- token 错误被拒绝
- challenge 过期被拒绝
- challenge 重放被拒绝
- 分组禁用被拒绝
- `client allow` 命中可登录
- `client deny` 命中被拒绝
- 白名单模式下未命中被拒绝

## 4.2 隧道入口控制

必须验证：

- 公网来源 IP 命中 `allow` 可访问
- 命中 `deny` 被拒绝
- 被拒绝连接不进入活跃连接列表
- 被拒绝连接写入拒绝事件

## 4.3 TCP 正向代理

必须验证：

- 单端口映射可用
- 当前已落地：`happy_path`
- 并发多个连接可用
- 当前已落地：禁用隧道时新连接不可进入 listener
- 当前已落地：本地目标不可达时触发真实 `stream.open` 后关闭连接
- Web 修改隧道配置后，在线 `frpc` 无需重启即可应用
- 修改本地目标地址后，新连接使用新目标，旧连接可自然结束
- 修改 ACL 或限速后，`frps` 立即生效且不要求 `frpc` 收到新配置
- 管理员强制断开某条连接时，只影响目标连接
- `frpc` 断线后连接失败且状态更新

## 4.4 UDP 正向代理

必须验证：

- 数据报可双向流动
- 会话超时后自动清理
- 限速配置生效

## 4.5 端口范围映射

必须验证：

- 区间长度一致时可创建
- 区间长度不一致时失败
- 单个端口映射正确
- 冲突区间创建失败

## 4.6 反向代理

TCP：

- 固定监听端口可转发
- 规则修改后热更新生效

HTTP：

- 不同 `Host` 指向不同上游
- 未匹配 `Host` 返回预期错误

HTTPS：

- 不同 `SNI` 选择不同证书和路由
- 证书热更新后新连接立即使用新证书

## 4.7 观测与抓包

必须验证：

- 建立连接后出现在连接列表
- 关闭连接后状态回收
- 管理员强制断开后关闭原因正确显示为 `admin_terminated`
- 速率统计随传输变化
- 抓包任务可生成文件
- 达到大小或时长限制后自动停止

## 5. 调试建议

当前第一阶段最小联调时，优先先确认数据库启动链路，再继续排查 API 或控制端口问题。

如果当前问题出在最小端到端链路，优先保留脚本临时目录并查看：

- 脚本打印出的 `db_path`
- `frps.log`
- `frpc.log`
- 失败输出中的 `expected` / `actual` 摘要

## 5.1 日志调试

推荐日志级别：

- `info`：联调默认
- `debug`：定位控制协议、热更新、路由匹配问题

重点关注日志：

- `control`
- `proxy.forward`
- `proxy.reverse`
- `traffic`
- `capture`
- `audit`

## 5.2 网络调试

常用命令：

```powershell
Test-NetConnection 127.0.0.1 -Port 7000
Test-NetConnection 127.0.0.1 -Port 20000
```

```powershell
curl http://127.0.0.1:18080/ -H "Host: demo.local"
```

HTTPS 可用：

```powershell
curl https://demo.local:18443/ --resolve demo.local:18443:127.0.0.1 -k
```

## 5.3 WebSocket 调试

应验证：

- 首次连接成功
- 心跳正常
- 后台事件到达前端
- 断线后自动重连
- 重连后能恢复最新状态

## 5.4 抓包调试

建议核对：

- 抓包任务是否建立
- 文件是否持续增长
- 任务结束后是否可下载
- 审计日志是否记录谁发起抓包

## 6. 故障排查手册

## 6.0 启动即退出

优先排查：

- 配置文件 JSON 是否合法
- SQLite `path` 或 MySQL `dsn` 是否配置正确
- 数据库是否可连通
- 现有库结构是否和服务内嵌 schema 一致
- `schema_migrations.version` 是否高于当前服务支持版本

## 6.1 `frpc` 无法上线

优先排查：

- `7000` 是否监听
- token 是否正确
- 分组是否启用
- 来源 IP 是否被 `client` 黑白名单拒绝
- 控制协议版本是否兼容

## 6.2 公网端口无法访问

优先排查：

- 隧道是否启用
- 端口是否监听
- 来源 IP 是否被 `tunnel` 黑白名单拒绝
- `frpc` 是否在线
- 本地目标服务是否可连接

## 6.3 WebUI 看不到实时状态

优先排查：

- WebSocket 是否连接成功
- 事件总线是否有输出
- 连接注册表是否正常更新
- 前端订阅事件类型是否匹配

## 6.4 反向代理异常

HTTP：

- 是否匹配到正确 `Host`
- 上游是否可达

HTTPS：

- `SNI` 是否正确
- 证书是否已加载
- 路由是否冲突

## 6.5 配置已保存但 `frpc` 未热更新

优先排查：

- `config.push` 是否已发送
- `frpc` 是否返回 `config.ack`
- 分组当前在线客户端是否已切换到最新版本
- 配置是否只写入数据库但未触发运行态重载

## 7. 当前可执行检查

第一阶段开发期间，最常用的检查命令是：

```powershell
cd frps
go test ./...
go build -o NUL ./cmd/frps
go run ./cmd/frps --config ./configs/frps.json.example
```

如果只想验证服务是否已经正常启动，可直接访问：

```powershell
curl http://127.0.0.1:7500/healthz
curl http://127.0.0.1:7500/readyz
curl http://127.0.0.1:7500/api/v1/healthz
```

## 8. 代码健康压测基线

截至 2026-04-18，当前仓库已经有一套最小可执行的 TCP 受压验证基线：

- `test/e2e_tcp_perf.py`

当前脚本固定边界：

- 只测 TCP 单端口正向代理
- 只编排真实 `frps` / `frpc`
- Python 只模拟内网目标服务器和外网客户端
- 不实现鉴权和代理产品逻辑
- 默认在本机 loopback 环境执行

当前内置 workload：

- `stability`
- `upload`
- `download`

含义：

- `stability`：高并发短连接 echo 稳定性
- `upload`：外网客户端到内网目标的上行传输正确性与方向性退化检查
- `download`：内网目标到外网客户端的下行传输正确性与方向性退化检查

这一轮压测不以“冲到多高吞吐”作为首要目标，而以“受压时有没有明确代码异常、协议错误、转发错误或资源异常”作为首要目标。

默认命令：

```powershell
python test/e2e_tcp_perf.py
```

常用放大命令：

```powershell
python test/e2e_tcp_perf.py --stability-concurrency 128 --stability-duration 30
python test/e2e_tcp_perf.py --transfer-concurrency 8 --transfer-bytes-per-connection 67108864
```

当前报表输出：

- `report.json`
- `report.md`
- `frps.log`
- `frpc.log`

当前默认记录指标：

- 稳定性请求总数、成功数、失败数、成功率
- 稳定性 `req/s`
- 稳定性延迟 `min/avg/p50/p95/p99/max`
- 上行吞吐 `bytes/s` 与 `Mbps`
- 下行吞吐 `bytes/s` 与 `Mbps`
- 每连接传输耗时分布
- `frps` / `frpc` 峰值 RSS
- `frps` / `frpc` 近似 CPU 百分比

这些指标的当前用途是：

- 先判断是否出现真实代理链路失败
- 再判断是否出现方向性退化、尾延迟异常或资源异常
- 最后才看吞吐数字是否值得继续深挖优化

当前已知限制：

- 这是单机 loopback 基线，不代表真实公网带宽或弱网质量
- 进程 CPU 采样是基于累计 CPU 秒的近似值，不是高精度 profiler
- 还没有 goroutine、heap、pprof 等内部观测
- Windows 本机高 churn 短连接测试可能触发 `WinError 10048`，通常意味着客户端临时端口 / TIME_WAIT 压力，需要和 `frps/frpc` 代理链路失败分开分析

当前建议使用方式：

- 先跑默认基线
- 再跑更长时间和更高并发的阶梯压测
- 如果高 churn 短连接出现 `WinError 10048`，先降低新建连接速率、延长冷却时间或改用长连接 workload 复核
- 如果只有宿主机 `WinError 10048` 一类错误，而 `frps` / `frpc` 日志没有对应运行时异常，不要直接判定为代理代码问题
- 先比较同机同参数前后版本差异，再决定是否需要优化核心数据面
