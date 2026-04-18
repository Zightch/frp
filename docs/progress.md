# 当前进度

本文档用于单独记录跨轮实现进度、已完成主线、当前停止线和已经收口的活动边界。长期设计仍以 `project-overview.md`、`frps/` 和 `frpc/` 下的正式设计文档为准。

当前轮执行只看 `docs/tmp/todo.md`。`docs/tmp/todo.md` 必须只保留当前轮目标、边界、当前轮完成项和当前唯一下一步；上一轮及更早内容统一收口到本文档，不回填到当前轮 `todo`，当前轮只保留必要引用。

更新时间：2026-04-18

## 0. 进度与 Todo 分工

- `docs/progress.md` 负责承接跨轮历史进度、停止线、已完成项和已确认边界。
- `docs/tmp/todo.md` 只负责当前轮执行，不记录前几轮的目标、完成项和讨论细节。
- 进入新一轮时，先把上一轮收口同步到本文档，再重写当前 `todo`；当前轮如需引用历史，只引用本文档。

## 0.1 Todo 轮换工作流

固定按下面 4 步执行：

1. 当前轮收口：把本轮已完成事项、当前停止线、已经稳定的边界和验收结果同步到 `docs/progress.md`；如果形成了长期有效规则，再同步到正式文档。
2. 提炼下一轮：只保留下一轮仍然要推进的最小目标、边界和下一步，不复制上一轮已完成项，不搬运上一轮讨论过程。
3. 重写当前 todo：直接重写 `docs/tmp/todo.md`，只保留当前轮目标、当前轮边界、当前轮已完成、当前唯一下一步，以及对 `docs/progress.md` 的引用。
4. 按当前轮持续更新：本轮进行中的新增完成项只写入当前 `todo`；需要追溯历史时回看 `docs/progress.md`，不把前几轮内容回贴到当前 `todo`。

## 1. 当前已完成

上一轮最小正向代理联调已经完成并收口。

已跑通的目标链路：

```text
Python 外网客户端
<-> frps remote_port
<-> frps / frpc 真实控制面与数据面
<-> frpc 本地 TCP 回连
<-> Python 内网主机
```

当前已落地能力：

- `frps` / `frpc` 支持 token challenge/response 登录。
- 一个分组始终只允许 1 个在线 `frpc` 客户端；同分组第二个客户端会在登录阶段被拒绝。
- `frps` 可从 SQLite/MySQL schema 中读取 `proxy_groups` / `tunnels` 运行数据。
- `frps` 在收到 `config.ack` 后启动启用状态的 TCP 单端口 listener。
- `frpc` 可接收 `config.push`，返回 `config.ack`，并处理 `stream.open` / `stream.data` / `stream.close`。
- `frpc` 可回连本地 TCP 目标，并把公网侧 TCP 字节流转发到内网目标。
- 数据库支持 SQLite/MySQL、统一 `storage.SQL` 封装、事务封装、启动期 schema bootstrap 和严格校验。

当前端到端脚本：

- `test/e2e_tcp_single.py`

该脚本只负责测试编排和场景构造，不实现产品逻辑。

## 2. 已验证场景

当前固定保留 5 个最小场景：

- `happy_path`
- `bad_token`
- `disabled_group`
- `disabled_tunnel`
- `local_unavailable`

这些场景覆盖的是单客户端、冷启动、SQLite 预注入、TCP 单端口的最小闭环。

已经验证过的关键行为：

- `config.ack` 之前不会启动公网 TCP 监听。
- session 结束后会关闭已启动的 tunnel listener。
- 只有启用的单端口 TCP 隧道会启动 listener。
- `frpc` 收到未知 `tunnelId` 的 `stream.open` 会返回明确错误。
- `bad_token` 会被真实 `frps` / `frpc` challenge 校验拒绝。
- `disabled_group` 会由真实 `frps` 按数据库分组状态拒绝登录。
- `disabled_tunnel` 会完成登录和配置确认，但不会启动 remote listener。
- `local_unavailable` 会触发真实 `stream.open`，随后因本地目标不可达关闭公网连接，不返回伪造 echo。

## 3. Token 与数据库边界

测试程序可以直接向数据库插入现成的分组和隧道信息。

分组 token 的当前边界：

- `frpc` 启动参数中的 token = `token_id` + `token_secret`。
- 数据库只保存 `token_id` 和 `token_hash`。
- 数据库不保存 `token_secret` 明文。
- `token_hash = sha256(token_secret)`。
- 登录阶段的一次性 `challenge nonce` 由 `frps` 下发。
- 登录响应由真实 `frps` / `frpc` 按 challenge 机制完成校验。

因此，测试脚本保留原始 token 并传给 `frpc` 是正确的；数据库中只有 hash 也是正确的。

## 4. 错误输出增强

当前脚本已经补齐最小必要失败摘要输出。

失败时会输出：

- 当前 `scenario`
- 分配到的端口信息
- 临时 SQLite DB 路径
- `frps` / `frpc` 日志
- 当前场景的期望结果摘要
- 当前场景的实际结果摘要

`local_unavailable` 还会保留外网客户端观测摘要：

- `send_error`
- `response_len`
- `response_preview`
- `recv_error`
- `connection_closed`
- `timed_out`

实现原则仍然是极简核心优先：只补必要诊断信息，不引入日志框架、测试 DSL 或额外模块拆分。

## 5. 当前停止线

上一轮最小联调已经达到当前停止线。

当前不继续扩展：

- 测试矩阵
- Python 脚本模块拆分
- 额外日志框架
- 额外状态机
- 额外测试 DSL

当前也不把以下能力并入已完成主线：

- 在线改库热更新
- ACL
- 多客户端扩展
- 反向代理
- UDP
- 端口范围
- 抓包
- 限速

## 6. 当前轮 WebUI 进度

本轮极简 WebUI 首版已经完成。

当前已落地能力：

- `frps management api` 新增 `proxy_groups` / `tunnels` 最小 CRUD JSON API。
- 根路径 `/` 返回单页 WebUI。
- 管理界面不展示任何 `maxclient` / `max_clients` 选项；单分组单客户端由 `frps` 控制面的固定客户端槽位保证。
- 页面只管理两个对象：
  - `proxy_groups`
  - `tunnels`
- 分组支持：
  - 列表
  - 新增
  - 编辑
  - 删除
  - 创建或重置 token
- 隧道支持：
  - 列表
  - 新增
  - 编辑
  - 删除

当前接口入口：

- `/api/v1/proxy-groups`
- `/api/v1/proxy-groups/{id}`
- `/api/v1/proxy-groups/{id}/token`
- `/api/v1/tunnels`
- `/api/v1/tunnels/{id}`

当前页面和接口仍遵守以下边界：

- WebUI 归属于 `frps management api`。
- WebUI 不直接连接数据库。
- `frps` 负责实际数据库读写。
- 管理界面不展示任何 `maxclient` / `max_clients` 配置项。
- 不引入独立前端工程。
- 不引入 Node / Vue / 打包链。
- 不做登录和管理员体系。
- 不做 WebSocket、热更新、统计页、日志页。
- 不把 ACL、限速、抓包、反向代理配置页混进这一轮。

## 7. WebUI 验证结果

首版 WebUI 当前已经完成两层验证。

自动化验证：

- `frps/internal/api/server_test.go` 已覆盖：
  - 页面入口
  - 页面不展示 `maxclient` / `max_clients` 选项
  - group CRUD
  - API 额外字段会被直接忽略，例如传入 `max_clients`
  - 数据库 schema 已删除 `max_clients`
  - token reset
  - tunnel CRUD
- `frps/internal/control/server_test.go` 已覆盖：
  - 同分组第二个 `frpc` 登录被拒绝
- `go test ./...` 已通过

真实 smoke 验证：

- 已用临时 SQLite 配置启动真实 `frps`
- 已确认页面入口 `/` 可访问
- 已通过真实 HTTP 请求完成 `proxy_groups` 最小 CRUD
- 已通过真实 HTTP 请求完成 `tunnels` 最小 CRUD
- 已验证 token reset 能返回新的 token 原值
- 已直接核对 SQLite：
  - 数据库中只有 `token_id` 和 `token_hash`
  - 不存在 `token_secret` 明文字段
  - 返回给调用方的 token 原值与库内 `token_id` / `token_hash` 可对应校验

## 8. 当前停止线与下一步

当前 WebUI 首版已经达到本轮停止线。

当前不继续扩展：

- 登录
- 管理员体系
- 独立前端工程
- 在线热更新
- 连接态展示
- 统计图
- 日志页
- ACL
- 限速
- 抓包
- 反向代理页面

该轮已经收口，后续工作已切到代码健康压测基线，不再沿着 WebUI 首版继续扩展。

## 9. 代码健康压测基线

本轮目标不是继续扩功能，而是给当前最小 TCP 正向代理主线补一套可重复执行的受压验证基线，优先判断代码链路在压力下是否出现异常。

当前新增脚本：

- `test/e2e_tcp_perf.py`

脚本职责保持极简：

- 创建临时 SQLite 数据库
- seed `proxy_groups` / `tunnels`
- 启动真实 `frps` / `frpc`
- 启动 Python 内网目标服务器
- 启动 Python 外网客户端压测
- 输出 JSON 原始结果和 Markdown 报表

脚本当前覆盖 3 个 workload：

- `stability`
- `upload`
- `download`

含义分别是：

- `stability`：高并发短连接 echo 稳定性
- `upload`：`Python 外网客户端 -> frps -> frpc -> Python 内网主机` 的上行传输正确性与方向性退化检查
- `download`：`Python 内网主机 -> frpc -> frps -> Python 外网客户端` 的下行传输正确性与方向性退化检查

这一轮不以“冲到多高吞吐”作为首要目标，而以“有没有明确代码异常、协议错误、转发错误或资源异常”作为首要目标。

为了避免测试端噪声，本轮还收束了两个边界：

- `test/e2e_tcp_single.py` 已移除旧的 `max_clients` seed 列，和当前 schema 保持一致
- `test/e2e_tcp_perf.py` 的 Python 内网目标服务器已把 `request_queue_size` 提升到 `1024`，避免高并发时 backlog 过小导致的假性拒连

当前脚本输出内容：

- `report.json`
- `report.md`
- `frps.log`
- `frpc.log`
- 临时 SQLite 数据库

当前默认本地基线结果如下：

- 参数：
  - `stability_concurrency = 64`
  - `stability_duration_seconds = 5`
  - `stability_payload_bytes = 1024`
  - `transfer_concurrency = 4`
  - `transfer_bytes_per_connection = 8388608`
- 结果：
  - 稳定性：`8405 / 8405` 成功，`0` 失败
  - 稳定性速率：约 `1671 req/s`
  - 延迟：`p50 ≈ 38.11ms`，`p95 ≈ 41.97ms`，`p99 ≈ 49.78ms`
  - 上行吞吐：约 `1880.74 Mbps`
  - 下行吞吐：约 `1358.90 Mbps`
- `frps` 峰值 RSS：约 `27.6 MB`
- `frpc` 峰值 RSS：约 `23.5 MB`
- 当前判断：这轮没有压出明确的 `frps/frpc` 代码问题

当前还额外跑过一轮放大样本：

- 参数：
  - `stability_concurrency = 128`
  - `stability_duration_seconds = 30`
  - `transfer_concurrency = 8`
  - `transfer_bytes_per_connection = 67108864`
- 结果：
  - 上行吞吐：约 `2157.94 Mbps`
  - 下行吞吐：约 `1706.26 Mbps`
  - 吞吐 workload 全部成功
  - 短连接稳定性：`22256 / 45684` 成功
  - 短连接失败全部为 Windows 本机 `WinError 10048`

对这一轮放大样本的当前判断是：

- `frps` / `frpc` 日志没有对应异常 `WARN/ERROR`
- `upload` / `download` workload 能正常完成，说明在同一轮压力下，真实代理链路并未先出现明显协议级失败
- 当前失败更像是 Windows 本机 loopback 短连接 churn 触发了客户端临时端口 / TIME_WAIT 压力
- 因此，这一轮结果可以说明“当前短连接稳定性压力已经撞到宿主机环境边界”，但不能直接说明 `frps/frpc` 自身在 `128` 并发下已经失败

当前日志观察：

- 压测期间 `frps` 没有出现异常 `WARN/ERROR`
- `frpc` 没有出现异常 `WARN/ERROR`
- 停止时只出现预期的 control session EOF 和正常 shutdown 日志

当前结论只限定在本机 loopback 基线：

- 当前最小 TCP 单端口正向代理链路，在本机环境下已经具备可重复执行的并发稳定性和方向性传输测试能力
- 本轮数字可作为后续代码改动后的回归比较基线
- 现阶段还没有压出明确的 `frps/frpc` 代码缺陷证据
- 当前结果不能直接外推到真实公网网络质量、跨机房 RTT 或弱网环境
- 当前高 churn 短连接测试在 Windows 本机上还会受到临时端口 / TIME_WAIT 压力影响，后续需要单独隔离这个变量

当前还未纳入已完成主线：

- 长时间稳定性测试
- 128 / 256 并发阶梯测试
- 多轮基线对比
- 性能回归硬阈值
- `pprof` / goroutine / heap 级内部观测
