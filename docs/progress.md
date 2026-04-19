# 当前进度

本文档用于单独记录跨轮实现进度、已完成主线、当前停止线和已经收口的活动边界。长期设计仍以 `project-overview.md`、`frps/` 和 `frpc/` 下的正式设计文档为准。

当前轮执行只看 `docs/tmp/todo.md`。统一轮换、归档、`.gitignore` 检查和提交约定见 `docs/workflow.md`。

更新时间：2026-04-19

## 0. 进度与 Todo 分工

- `docs/workflow.md` 负责定义统一执行流程、`todo` 轮换规则、归档时机和 `.gitignore` / 提交约定。
- `docs/progress.md` 负责承接跨轮历史进度、停止线、已完成项和已确认边界。
- `docs/tmp/todo.md` 只负责当前轮未完成执行项，必须明确当前总目标、实现该目标的子步骤、当前轮边界和当前唯一下一步，不记录已完成内容。
- 进入新一轮时，先把上一轮收口同步到本文档，再重写当前 `todo`；当前轮如需引用历史，只引用本文档。

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

## 10. 当前代码架构文档

为避免后续继续按旧设计假设开发，当前代码现状已经单独收束成架构文档：

- `docs/frps-frpc-current-architecture.md`

该文档基于当前实际代码整理，已明确：

- 当前主链路是 `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`
- `frps` 管理面、控制面、数据面和数据库读写的当前进程内结构
- `frpc` 登录、配置接收、stream 转发和重连的当前进程内结构
- token challenge/response 与数据库 `token_id` / `token_hash` 的真实边界
- 单分组单客户端槽位由 `frps` 控制面内存态维护
- 当前配置是在登录阶段加载并下发，当前代码没有面向在线 `frpc` 的配置热推送通道
- 当前真正形成完整运行链路的只有 TCP 单端口正向转发

后续如果继续改核心链路，应优先以该文档和实际代码为准，不再按未落地设计扩展理解边界。

## 11. 压测轮归档结论

代码健康压测这一轮已经归档，不再继续保留在当前 `todo`。

这一轮的收口结论是：

- 当前最小 TCP 单端口正向代理主链路已经具备可重复执行的本机压测基线
- 默认基线下未压出明确的 `frps` / `frpc` 代码异常证据
- 放大样本下出现的短连接失败当前更接近 Windows 本机 `WinError 10048` 宿主机资源边界
- 当前结果适合作为后续代码修改后的回归对比基线
- 这一轮先停止在“已建立最小可靠压测基线”，不继续沿吞吐数字或复杂观测面扩张

## 12. 已选定的下一工作方向

下一轮方向切换为：最小配置生效闭环。

选择这个方向的原因是：

- 当前 WebUI 和管理 API 已经可以改数据库
- 但当前在线 `frpc` 不会因为数据库变更自动重新领取配置
- 如果这一点不补齐，管理面虽然可编辑对象，但产品闭环仍然停留在“改了库，等人工重连客户端”

这一轮采用的原则是：

- 不做复杂热更新
- 不新增配置 diff 协议
- 不做在线增量推送状态机
- 优先复用现有 `frpc` 自动重连、登录和 `config.push` 下发链路

当前倾向的最小实现方式是：

- 管理面完成 group / tunnel 写库后，只定位受影响的 group
- 如果该 group 当前有在线 `frpc` 会话，则由 `frps` 主动回收该会话
- 让 `frpc` 按现有重连机制重新登录并重新领取配置

这样可以先实现“配置最终生效”的闭环，而不把系统复杂度提前推高到真正的在线热更新协议。

## 13. WebUI 重构与管理认证闭环

WebUI 重构这一轮已经完成收口。

当前已确认落地的能力：

- `frps` 管理面已切换为独立的 `Vue 3 + Vite + TypeScript + Element Plus` 前端工程。
- 管理认证已固定为本地 `auth.json` 单一管理密钥方案，不再引入数据库 `admins` 表或管理员账号密码体系。
- 浏览器初始化和登录已固定为：
  - 初始化时提交 `sha256(secret)`
  - 登录时先申请一次性 challenge
  - 再提交 `sha256(key_hash + salt)`
- 管理 API 已全部置于真实管理会话门禁后。
- 分组管理已支持：
  - 列表
  - 新增
  - 编辑
  - 删除
  - token 重置
- 隧道管理已支持：
  - 列表
  - 新增
  - 编辑
  - 删除
- `frps` 已固定从当前工作目录下的 `data/config.json` 读取配置，并从配置中的 `webui.dist_dir` 直接托管构建产物。
- 直接启动 `frps.exe` 已不再依赖启动参数。

这一轮新增了真实联调脚本：

- `test/e2e_management_webui.py`

脚本固定验证以下闭环：

- 在隔离工作目录中写入固定 `data/config.json`
- 直接启动真实 `frps.exe`
- 托管真实 `webui/dist`
- 初始化管理密钥
- challenge 登录与会话恢复
- `proxy_groups` CRUD
- token 重置
- `tunnels` CRUD
- 登出与受保护接口拒绝

脚本还额外固定了两个执行边界：

- 联调使用隔离工作目录，不污染仓库下现有 `frps/data/` 的 `auth.json` 和 SQLite 数据。
- token 重置会直接回读 SQLite 校验 `token_id` / `token_hash`，确认服务端只保存校验材料，不保存 token 明文。

本轮已实际执行：

```powershell
python test/e2e_management_webui.py
```

当前结果是：

- 构建 `frps.exe` 和 `frps/webui/dist` 成功
- 直接启动 `frps.exe` 成功读取固定 `data/config.json`
- 管理认证与管理 CRUD 真实闭环通过
- 结果产物会落到 `test/tmp/management-e2e-<timestamp>/`

## 14. 当前停止线与下一步

当前 WebUI 重构与管理认证这一轮已经达到停止线。

当前不继续扩展：

- 管理员账号体系
- 多用户 / RBAC
- 多因素认证
- 密钥找回
- 在线客户端状态展示
- 连接管理页
- WebSocket 实时推送
- 真正的在线配置热更新协议

方向已调整为：维持单一本地管理密钥模型，并把重置路径固定为删除 `auth.json`。

选择这个方向的原因是：

- 当前初始化、challenge 登录、会话保护和管理 CRUD 已经闭环
- 现阶段不再提供管理密钥在线轮换接口
- 更直接且更可控的重置路径是删除本地 `auth.json` 后自动回到未初始化态

本次已落地的最小补齐方式是：

- 保持管理密钥只在未初始化态设置一次
- 增加 `auth.json` 删除监听，文件删除后自动清空内存中的管理认证状态
- 删除后立即使旧 challenge 和旧管理会话失效
- 前端说明统一改为“删除 `auth.json` 重置”，不再引入在线轮换入口
- `test/e2e_management_webui.py` 已扩展覆盖删除 `auth.json` 后自动重置与重新初始化

## 15. UDP 最小闭环归档

截至 2026-04-19，UDP 最小闭环这一轮已经完成并收口；以下内容为本轮归档结论：

- 管理面 `proxy_groups` / `tunnels` CRUD 已完成，隧道模型已支持 `protocol = tcp | udp`
- 协议文档已定义 UDP 会话时序、`sessionId` 语义和 `idleTimeoutMs` 字段
- UDP 生命周期边界已固定：`frps` 统一做空闲 `30s` 清理，`frpc` 不做本地 idle timeout 判断，只执行来自 `frps` 的 `udp.close`
- `frps/pkg/protocol` 已补齐 `udp.open` / `udp.close` 消息体与编解码，并补了 round-trip 单测
- `frps` / `frpc` 控制连接读循环已接入 `udp.*` 分支，UDP session 状态骨架已预留
- UDP 控制面未知会话处理已从默认 `unexpected message type` 收敛为会话级 `udp.close`
- 已补最小 UDP 控制面回归测试，覆盖 `frps/internal/control` 与 `frpc/internal/client` 的基础分支
- `frps` 已在 `config.ack` 后为启用的单端口 UDP tunnel 启动真实公网 UDP listener，并在控制会话结束时统一回收 listener
- `frps` 已按 `tunnelId + 公网客户端地址` 维护 UDP session；首个公网 datagram 会分配 `sessionId` 并顺序下发 `udp.open` / `udp.data`
- 同一公网客户端后续 datagram 会复用既有 `sessionId`；`frps` 也已支持把来自 `frpc` 的 `udp.data` 按 `sessionId` 回写给对应公网客户端
- `frps` 已在公网收包和 `frpc` 回包路径刷新 UDP session 活跃时间，并固定向 `frpc` 下发 `30s` idle timeout 元数据
- 已新增 `frps/internal/control/server_test.go` 的 UDP listener/session 回归测试，覆盖首包建会话、同客户端 session 复用和公网回包写出；`go test ./...` 已在 `frps/` 模块通过
- `frpc` 已按 `sessionId` 建立真实本地 `UDPConn`，把来自 `frps` 的 `udp.data` 写入本地 UDP 服务，并把本地 UDP 回包按原 `sessionId` 回发给 `frps`
- `frpc` 当前只在收到 `udp.close`、本地不可恢复错误或控制会话结束时释放本地 UDP session，不做本地 idle timer；已新增 `frpc/internal/client/client_test.go` 的 UDP happy path 回归测试，`go test ./...` 已在 `frpc/` 模块通过
- `frps` 已补上 UDP session 级 `30s` idle cleanup：在 UDP tunnel listener 启动后后台定期扫描空闲会话，超时后删除本地 session，并向 `frpc` 发送 `udp.close`
- 已新增 `frps/internal/control/server_test.go` 的 UDP idle cleanup 回归测试，覆盖真实 UDP listener 建会话、空闲超时清理和向 `frpc` 下发 `udp.close`；`go test ./internal/control` 与 `go test ./...` 已在 `frps/` 模块通过
- 已新增 `test/e2e_udp_single.py`，在隔离工作目录中直启真实 `frps`、seed 单端口 UDP tunnel，并分别用 Python UDP 客户端和 Python UDP 服务器跑通 `公网客户端 -> frps -> frpc -> 本地 UDP 服务 -> frps -> 公网客户端` 的 happy path；`python test/e2e_udp_single.py` 已于 2026-04-19 本地通过
- `test/e2e_udp_single.py` 已扩展 `idle_cleanup` 场景：同一公网 UDP 客户端在一次 datagram round-trip 后空闲约 `30s`，`frps` 会记录 idle cleanup 并向 `frpc` 发送 `udp.close`，`frpc` 会记录 `udp session closed`，随后同一公网客户端再次发包可重建新 UDP session 并重新打通闭环；`python test/e2e_udp_single.py --scenario idle_cleanup` 已于 2026-04-19 本地通过
- 至此，单端口 UDP 隧道这一轮的真实公网入口、本地 UDP 转发、回包、`frps` `30s` idle cleanup、`frpc` 收到清理通知后的收口，以及最小 Python e2e 验证已全部闭环；本轮 `todo` 已清空

## 16. TCP/UDP 范围隧道执行约定收束

截至 2026-04-19，TCP/UDP 范围隧道这一轮的第一个子步骤已经完成；本次先收束长期有效的执行约定，还没有进入代码实现。

本次固定下来的边界如下：

- 这一轮只做连续、跨度一致的一一对应范围映射，不做稀疏映射。
- TCP 和 UDP 共用同一套端口偏移公式：

```text
offset = remotePort - remoteStart
localPort = localStart + offset
```

- `frps` 在运行态可以把一个 range tunnel 展开成逐端口公网 listener，但协议仍只保留同一个 wire `tunnelId`。
- 因此，`stream.open.remotePort` 和 `udp.open.remotePort` 的语义已固定为“本次实际命中的公网端口”，不能在 range 模式下退化为固定写 `remoteStart`。
- `frpc` 目标端口计算只依据当前配置快照和消息中的 `remotePort`，不依赖 listener 创建顺序、配置顺序或额外的子 tunnel 标识。
- UDP range 的 session 键已固定为 `tunnelId + remotePort + public client addr`；单端口 UDP 只是 `remotePort` 固定不变的特例。
- UDP 范围模式下仍保持当前生命周期模型不变：`frps` 是唯一 session 裁决方，活动发生后立即刷新最后活跃时间，空闲约 `30s` 后清理，`frpc` 只响应 `udp.close`，不做本地 idle timeout。
- 这些约定已同步到正式文档：
  - `docs/protocol.md`
  - `docs/project-overview.md`
  - `docs/frps/technical-design.md`
  - `docs/frpc/technical-design.md`

本子步骤属于文档收束，不包含代码改动验证；本次未运行测试。

## 17. TCP range 的 frps 入口已打通

截至 2026-04-19，TCP/UDP 端口范围映射这一轮又完成了一个已归档子步骤：`frps` 侧 TCP range 入口已经进入真实执行链路。

本次已完成的实现和收束如下：

- `frps/internal/control` 的运行时 listener 容器已改为支持“一个 tunnel 挂多个 listener”，不再假设一个 tunnel 只能对应一个公网 listener。
- `frps` 在收到 `config.ack` 后，已经会为启用状态的 TCP range tunnel 按 `remoteStart..remoteEnd` 逐端口展开真实公网 TCP listener。
- TCP range listener 命中后，`frps` 下发的 `stream.open.remotePort` 已改为实际命中的公网端口，不再固定写 `remoteStart`。
- 当前 TCP range 仍然只使用原有同一个 wire `tunnelId`；`frpc` 继续按 `remotePort` 偏移计算本地目标端口。
- UDP range 运行态本次没有放开，当前仍保持“只支持 UDP 单端口 listener，UDP range 留到下一子步骤实现”的边界。

本次新增和更新的验证如下：

- `frps/internal/control/server_test.go` 已新增/更新覆盖：
  - 启用状态的 TCP range tunnel 会真正启动非 `remoteStart` 端口上的 listener
  - 命中 range 内实际公网端口后，`stream.open.remotePort` 会回填该实际端口
  - 既有单端口 TCP 监听行为仍保持可用
- 已执行：
  - `go test ./internal/control`
  - `go test ./...`（`frps/` 模块）

本次还同步了当前实现状态文档，避免继续按“只有单端口 TCP 数据面”理解代码现状：

- `docs/frps-frpc-current-architecture.md`
- `docs/frps/technical-design.md`

当前仍未归档为“TCP range 整体闭环完成”的内容：

- 最小 Python e2e 还未补
- TCP range 的端到端回归仍未按脚本方式收口
- UDP range 执行链路仍未开始实现

## 18. TCP range 最小 Python e2e 已归档

截至 2026-04-19，TCP/UDP 端口范围映射这一轮又完成了一个已归档子步骤：TCP range 的最小 Python e2e 已经收口。

本次已完成的实现和收束如下：

- 新增 `test/e2e_tcp_range.py`，在隔离工作目录中启动真实 `frps` / `frpc`，分别模拟公网客户端和内网 TCP 服务。
- `test/e2e_tcp_range.py` 已固定按当前产品边界启动 `frps`：使用 `workspace/data/config.json`、隔离 `data/auth.json` / SQLite，并复制 `webui/dist`，不再依赖旧的 `--config` 启动方式。
- 脚本会 seed 一个启用状态的 TCP single tunnel 和一个启用状态的 TCP range tunnel。
- 脚本会分别启动 1 个 single 本地 TCP 服务和 2 个 range 本地 TCP 服务；3 个本地服务使用不同响应前缀回包，因此可以直接验证实际命中的 `localPort`。
- 已验证 single remote port 仍会命中 single local port，确认 TCP 单端口最小链路没有因 range 改动回退。
- 已验证同一个 TCP range tunnel 命中两个不同的 `remotePort` 时，`frpc` 会按 `offset = remotePort - remoteStart` 转发到对应的两个 `localPort`。
- `test/e2e_tcp_single.py` 也已同步到当前 `frps` 直接从 `data/config.json` 启动的模型，并重新验证 `happy_path`，避免旧单端口脚本继续停留在失效启动方式上。

本次执行的验证如下：

- `python test/e2e_tcp_range.py`
- `python test/e2e_tcp_single.py --scenario happy_path`

本子步骤完成后，TCP range 当前已完成的闭环为：

- `frps` 在 `config.ack` 后按范围展开真实公网 TCP listener
- `stream.open.remotePort` 回填实际命中的公网端口
- `frpc` 按 `remotePort` 偏移计算目标 `localPort`
- 最小 Python e2e 已验证 range 映射和 single regression

当前仍未归档为“本轮总目标完成”的内容：

- UDP range 执行链路
- UDP range 的最小单测与 Python e2e
- TCP/UDP range 的总回归与本轮清尾
