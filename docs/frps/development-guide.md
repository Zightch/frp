# frps 开发文档

## 1. 开发原则

`frps` 的开发必须坚持“核心极简优先”，所有实现都按最短闭环推进。

- 先打通最小可运行链路，再逐步补管理、观测和高级功能。
- 不为了后续可能存在的问题，提前堆兼容层、插件体系、复杂抽象、多节点方案。
- 当前阶段用不到的字段、状态机、配置项、扩展点一律不先做。
- 目标端只读取必要字段，不代表源端可以继续保留废字段；确认无用字段后，要尽早从 schema、仓储、API、WebUI、测试 seed 和文档中移除。
- 字段名、语义或边界一旦调整，源端写库、返回体、页面表单、测试数据和文档必须同步改到位；不能长期依赖“目标端忽略未知字段”维持表面兼容。
- 当前开发阶段默认不承诺数据库 schema 兼容、配置兼容或 `frps/frpc` 协议版本兼容；破坏性调整时，统一同步修改代码、测试、文档和本地开发数据。
- 轮换制 `todo` 必须做信息隔离：当前轮 `docs/tmp/todo.md` 只保留当前轮目标、边界、当前轮完成项和当前唯一下一步；上一轮内容先同步到 `docs/progress.md`，不继续混放在当前 `todo` 中。
- 如果一个设计同时满足“现在能用”和“以后可能更优”，优先选择现在更简单、更容易验证的方案。
- 任何新功能都要明确放在哪个边界层，不允许“临时先塞进去”。

开发时的判断标准：

- 没有直接服务当前阶段目标的代码，不写。
- 没有真实使用方的抽象，不提取。
- 没有验证需求的复杂容错，不提前实现。
- 已确认不再需要的字段，不继续保留在源端输出、写库或测试输入里。

## 1.1 当前进度

截至 2026-04-18，`frps` 已完成的当前主线包括：

- 独立 Go 子模块和 `cmd/frps` 启动入口
- 基础配置、日志和生命周期管理
- 管理端最小 HTTP 服务与健康检查
- SQLite/MySQL 双支持的数据库打开逻辑
- `internal/storage/sql.go` 统一数据库封装，支持结构化结果和事务
- 启动时内嵌当前必需表建表和严格表结构校验
- 分组运行态读取：`proxy_groups` / `tunnels`
- token challenge/response 登录
- `config.push` / `config.ack`
- `config.ack` 后启动启用状态的 TCP 单端口 listener
- `stream.open` / `stream.data` / `stream.close` 最小数据面
- 最小失败路径：
  - `bad_token`
  - `disabled_group`
  - `disabled_tunnel`
  - `local_unavailable`
- 已通过 `test/e2e_tcp_single.py` 打通：
  - `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`

当前仍未进入的范围包括：

- WebUI 业务页
- 在线改库热更新
- ACL
- UDP
- 端口范围
- 反向代理
- 抓包、限速、完整连接观测

## 2. 建议目录

```text
frps/
├── cmd/frps/
├── internal/api/
├── internal/auth/
├── internal/control/
├── internal/tunnel/
├── internal/reverse/
├── internal/proxy/
├── internal/traffic/
├── internal/capture/
├── internal/storage/
├── internal/eventbus/
├── internal/runtime/
├── internal/config/
├── webui/
└── configs/
```

共享库建议放在根目录：

```text
pkg/
├── protocol/
├── model/
├── transport/
└── limiter/
```

## 3. 环境要求

- Go 1.23 或更高版本
- SQLite 3 或 MySQL 8.0

当前极简 WebUI 首版不要求 Node.js、npm 或 pnpm；如果后续管理面复杂度真实上升，再评估是否引入独立前端工程。

推荐本地开发环境：

- Windows PowerShell 或 WSL
- Git
- curl
- Wireshark 或 tcpdump

## 4. 开发顺序

开发顺序必须固定，前一阶段没有形成可运行结果，不进入下一阶段。

### 4.1 第一步：`frps` 框架

- 初始化仓库目录和 `go.mod`。
- 建立 `cmd/frps` 启动入口。
- 建立最小配置、日志、生命周期管理。
- 建立 `7000` 控制端口和 `7500` 管理端口的基础监听框架。
- 建立最小 `internal/` 分层和空路由骨架。

阶段目标：

- `frps` 可以稳定启动、读取基础配置、暴露最小健康检查或空 API。

当前状态：

- 已完成。

### 4.2 第二步：极简 WebUI 起步

- 首版不引入独立前端工程。
- 由 `frps management api` 直接返回一个内嵌静态 HTML 页面。
- 页面只做最简单的列表、表单和按钮。
- 页面只调用最小 JSON API，不先做 WebSocket。
- 当前只覆盖 `proxy_groups` 和 `tunnels` 的数据库增删改查。

阶段目标：

- 浏览器可打开管理页。
- 可查看、创建、编辑、删除 `proxy_groups` / `tunnels`。
- token 创建或重置时只在当次显示原值，数据库仍只保存 `token_id + token_hash`。

### 4.3 第三步：数据库

- 引入统一存储接口和最小驱动适配层。
- 同时支持 SQLite 和 MySQL。
- 建立最小 schema 建表和校验机制。
- 先落最小核心表：分组、token、隧道。
- 管理认证不入库，统一改为本地 `auth.json` 保存管理密钥 hash。
- 先保证两种数据库下最小 CRUD 能跑，不提前做复杂 ORM、多方言优化、读写分离、冷热分层、归档体系。

阶段目标：

- `frps` 启动时可以完成当前必需表建表或校验，管理端可以读取和保存最小业务数据。

当前状态：

- 数据库打开、驱动注册、统一封装、当前必需表建表/校验已完成。
- 控制面运行时读取已开始并可支撑最小链路。
- 管理面业务 CRUD 尚未开始。

### 4.4 第四步：`frps/frpc` 协议设计

- 先定义最小二进制控制协议。
- 只覆盖登录、心跳、配置下发、配置应答、打开流、流量承载、关闭流。
- 第一版先按单连接、单客户端、单端口 TCP 设计。
- 不提前做复杂流控、压缩、版本兼容协商和扩展能力协商。

阶段目标：

- 协议文档和代码结构一致，并且足以支撑最小端到端链路。

### 4.5 第五步：`frpc` 最小实现

- 实现参数解析。
- 实现到 `frps` 的控制连接。
- 实现登录、心跳、配置接收。
- 保持客户端只做执行，不下沉策略逻辑。

阶段目标：

- `frpc` 可以用 `server + token` 接入 `frps` 并领取配置。

### 4.6 第六步：最小正向代理闭环

- 实现单端口 TCP 隧道。
- 实现分组 token 鉴权。
- 实现最小在线状态同步。
- 优先限制一个分组只允许一个活跃客户端，先降低复杂度。

阶段目标：

- 从公网入口到内网目标的 TCP 代理链路可以稳定跑通。

当前状态：

- 已完成。
- 已通过 `test/e2e_tcp_single.py` 固化单客户端、冷启动、SQLite 预注入、TCP 单端口闭环。

### 4.7 第七步：管理面接线

- 补齐分组和隧道管理 API。
- 补齐 WebUI 表单和列表页。
- 补齐 WebSocket 状态推送。
- 接入最基础的黑白名单和端口冲突校验。
- 接入配置变更后的 `config.push/config.ack` 同步链路。
- 接入按连接 ID 强制断开的管理接口。

阶段目标：

- 管理员可以通过 WebUI 完成最小配置闭环，并实时看到核心状态。

### 4.8 第八步：增强能力

- UDP 正向代理。
- 端口范围映射。
- TCP/HTTP/HTTPS 反向代理。
- 连接注册表、速率统计、限速。
- 抓包任务和文件下载。

阶段目标：

- 在不破坏核心链路稳定性的前提下，逐步补齐增强能力。

## 5. 编码约定

- 控制协议结构统一放到 `pkg/protocol`。
- 领域模型统一放到 `pkg/model` 或 `frps/internal/.../model.go`。
- 监听器、会话、连接等运行态对象必须接受 `context.Context`。
- 任何带 goroutine 的对象必须提供 `Start` 和 `Close`。
- 禁止在 handler 中直接操作数据库和网络监听器，统一走 service 层。
- 活跃连接注册表必须保留关闭句柄，支持管理端按连接 ID 精确回收。

## 6. WebUI 开发约定

`webui` 属于 `frps`，不是独立产品。

当前首版约定：

- 首版优先内嵌在 `internal/api`，不急着建立 `frps/webui/` 独立工程。
- 首版只做分组和隧道 CRUD，不做登录、不做 WebSocket、不做管理仪表盘。
- 后续接入登录时，不新增 `admins` 表，统一走 `auth.json` 初始化和一次性盐 challenge。
- 如果页面复杂度后续真实上升，再演进到独立前端目录。

后续如需独立前端工程，可参考目录：

```text
frps/webui/
├── src/api/
├── src/views/
├── src/stores/
├── src/components/
├── src/router/
└── src/styles/
```

前端约定：

- 首屏列表由 REST API 拉取。
- 当前首版不做运行态实时推送，先以刷新可见为准。
- 不在前端复刻复杂业务判断，校验以服务端为准。
- 管理页面状态模型尽量按“分组、隧道、反代、连接、抓包”划分。

## 7. API 约定

REST API 建议统一前缀：

```text
/api/v1
```

返回结构建议统一：

```json
{
  "code": 0,
  "message": "ok",
  "data": {}
}
```

错误处理约定：

- 参数错误返回 `400`
- 未认证返回 `401`
- 无权限返回 `403`
- 冲突返回 `409`
- 服务异常返回 `500`

建议补充的运行态接口：

- `GET /api/v1/connections`
- `GET /api/v1/tunnels/{id}/connections`
- `POST /api/v1/connections/{id}/close`

## 8. 配置管理约定

服务启动配置只负责基础参数：

- 监听地址
- 数据库驱动
- 数据库 DSN 或 SQLite 文件路径
- 管理端端口
- 日志级别
- 存储路径

业务配置不应该放在静态文件中，而应通过 WebUI/API 写入数据库：

- 分组
- 隧道
- 反向代理规则
- 证书
- IP 黑白名单

业务配置写库后，还必须触发运行态协调：

- 计算配置 diff
- 重建或回收相关监听器
- 向在线 `frpc` 下发执行配置对应的 `config.push`
- 跟踪 `config.ack`
- ACL、限速等服务端策略只更新 `frps` 本地运行态

## 9. 数据库与 schema

当前实现不单独维护 `frps/migrations/` 目录，而是把当前 schema 定义、建表语句和校验规则直接内嵌在服务端代码中。

当前约定：

- 数据库连接由上层 `internal/app/database.go` 按配置打开。
- `internal/storage/sql.go` 只封装数据库对象，不负责驱动打开和 schema 管理。
- 当前必需表的建表语句和校验规则统一维护在 `internal/app/schema.go`。
- 服务启动时直接执行当前必需表的 `CREATE TABLE IF NOT EXISTS` 语句，然后继续做严格表结构校验。
- 当前开发阶段不做独立 schema 版本记录、自动迁移或向后兼容；如果现有表结构与内置定义不一致，服务立即退出。
- SQLite 和 MySQL 的 schema 要保持同一业务语义，但允许使用各自稳定的公共类型映射。
- 空库允许自动初始化，非空库不允许“带病启动”。
- 只校验当前代码依赖的表；无关残留表例如历史 `admins` 表不影响启动。

后续新增表或字段时，开发流程固定为：

1. 在 `internal/app/schema.go` 中同时更新 SQLite/MySQL 的建表语句。
2. 同步更新对应的表结构校验定义。
3. 补充 SQLite 单元测试，必要时补充 MySQL 集成测试。
4. 同步更新 `README` 和 `docs/frps/*` 文档。
5. 如果旧开发库不再符合当前定义，直接重建或手工调整，不维护迁移兼容层。

字段治理补充约定：

- 新增字段只允许进入当前真实需要的层级，不为未来假设提前落库或出参。
- 删除字段时要一起清理 schema、仓储、API 入参、API 出参、WebUI 表单、测试 seed、测试断言和文档。
- 修改字段语义时要同步修改命名、校验、持久化和展示，不能只在单个目标端局部兼容。
- 额外入参可以按接口稳健性需要直接忽略，但源端自身不得继续生产已经确认无用的字段。

## 10. 日志约定

日志至少分为：

- `system`
- `control`
- `proxy.forward`
- `proxy.reverse`
- `traffic`
- `capture`
- `audit`

字段建议统一包含：

- timestamp
- level
- logger
- message
- requestId 或 connectionId

## 11. 新功能接入流程

### 11.1 新增正向代理能力

例如新增某类 UDP 细节能力时，需要同步检查：

- 存储模型
- API 模型
- 控制协议
- 运行态热更新链路
- `frpc` 执行逻辑
- 连接注册和速率统计
- WebUI 展示
- 测试用例

### 11.2 新增反向代理能力

例如新增 TLS 选项时，需要同步检查：

- 路由表结构
- 证书管理
- 热更新机制
- 端口冲突检测
- WebUI 表单
- 调试与审计

## 12. 联调建议

当前最小联调优先使用已经固化的脚本：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_tcp_single.py --scenario bad_token
python test/e2e_tcp_single.py --scenario disabled_group
python test/e2e_tcp_single.py --scenario disabled_tunnel
python test/e2e_tcp_single.py --scenario local_unavailable
```

脚本职责只限测试编排：

- 创建临时 SQLite 数据库并 seed `proxy_groups` / `tunnels`。
- 启动真实 `frps` / `frpc`。
- 模拟 Python 内网 echo server。
- 模拟 Python 外网客户端。
- 验证真实控制面和数据面行为。

手工联调也建议保持同样的最小配置：

- 一个 `frps`
- 一个 `frpc`
- 一个本地 TCP 服务，例如 `127.0.0.1:8080`
- 一个本地 HTTP 服务，例如 `127.0.0.1:8081`

联调顺序：

1. `frps` 启动。
2. 创建分组和 token。
3. 创建 TCP 隧道。
4. 启动 `frpc` 接入。
5. 用公网入口进行访问。
6. 当前阶段先看日志和实际 echo 结果；WebUI 连接和速率属于后续观测面。

## 13. 提交前检查

每次开发完一个功能后，至少检查：

- 单元测试是否通过
- 与当前功能相关的本地集成测试是否通过
- 如果改动控制面或数据面，`test/e2e_tcp_single.py` 的 5 个场景是否通过
- 如果改动 WebUI 首版，是否能通过页面和 SQLite 查询同时验证 `proxy_groups` / `tunnels` 写库结果
- 文档是否同步更新
