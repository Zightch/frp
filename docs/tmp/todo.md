# 限速最小闭环 Todo

## 当前总目标

- 完成“限速策略”最小闭环，并让自动化测试通过。
- 本轮闭环定义固定为：
  - 管理面可以创建、修改、删除限速策略。
  - 管理面可以把已有单端口隧道绑定到限速策略，也可以解绑或迁移绑定。
  - `frps` / `frpc` 在首次登录和在线热重载后，都能按最新策略执行限速。
  - 单端口 TCP / UDP 的上下行都真正进入限速执行链路。
  - `independent` 和 `shared` 两种策略模式都可用。
  - 所有限制条件都在唯一边界闸口拦住，而不是在运行时散落补判断。
  - `frps` / `frpc` Go 测试、控制面场景测试和 Python e2e 都通过。

## 当前轮边界

- 只做最小核心闭环，不做 WebUI 页面和交互。
- 只支持单端口隧道：
  - TCP single
  - UDP single
- 当前明确不做：
  - TCP range
  - UDP range
  - 限速统计页、实时观测页、图表页
  - 限速 burst 对外配置项
  - 多策略叠加
  - 一个隧道绑定多个策略
  - 旧版本兼容层
- 实现顺序必须先纵切再横扩：
  - 先做 schema / API / runtime snapshot / `independent` 最小闭环
  - 再在相同组件上补 `shared`
  - 不允许一开始同时铺两套执行结构
- 结构必须按多层收口，不允许把校验、持久化、快照投影、令牌桶和数据面编排糊在一个包里。
- 当前轮如果要改结构，优先删除旧残留、旧字段、旧入口，不保留中转兼容层。

## 子步骤

### 1. 冻结最小执行语义和分层方案

- 明确本轮唯一正式资源：
  - `proxy_group`
  - `tunnel`
  - `rate_policy`
  - `rate_policy_binding`
- 明确唯一状态源：
  - 持久化层：`rate_policies` + `rate_policy_bindings`
  - 运行时层：`ConfigSnapshot` 中每条隧道携带最终可执行限速配置
  - 会话执行层：session 内部 limiter registry 只消费快照，不回查数据库
- 明确最小组件层次：
  - 底层纯组件：速率单位归一、方向枚举、令牌桶、共享 limiter registry、等待/取消语义
  - 中层投影组件：schema、repo 查询、binding 解析、snapshot 拼装、API 输入归一
  - 上层业务拼装：管理 API、config push、TCP/UDP 数据面限速接入、在线热重载
- 明确最小执行点：
  - 下行限速：`frps` 公网入口收到数据后，在发往 `frpc` 前限速
  - 上行限速：`frpc` 本地后端返回数据后，在发往 `frps` 前限速
  - 不做双端双重限速，避免同方向重复扣桶
- 明确固定速率语义：
  - 存储单位：`bps`
  - 输入展示单位：`K/M/G`
  - 对外默认单位：`M`
  - 不新增 burst 字段
  - 桶容量固定为内部常量策略，必须保证小速率下 TCP 帧和 UDP datagram 仍能前进
- 明确 reload / shutdown / session close 语义：
  - 等待令牌时如果 session 关闭、reload 或 context cancel，必须立刻退出等待
  - 不允许因限速等待导致热重载卡死、关闭卡死或 goroutine 泄漏

### 2. 建立持久化模型和边界闸口

- 新增正式表：
  - `rate_policies`
  - `rate_policy_bindings`
- `rate_policies` 至少需要承接：
  - `id`
  - `name`
  - `mode`
  - `downlink_bps`
  - `uplink_bps`
  - `created_at`
  - `updated_at`
- `rate_policy_bindings` 至少需要承接：
  - `id` 或稳定唯一键
  - `rate_policy_id`
  - `tunnel_id`
  - `created_at`
  - `updated_at`
- 约束固定为：
  - 一个隧道最多只能绑定一个策略
  - 一个策略可以绑定多条隧道
  - 删除隧道时自动删除 binding
  - 删除 `proxy_group` 时，其下隧道删除并带走 binding，但策略本体保留
  - 删除仍有 binding 的策略默认拒绝
- schema 改动必须同步：
  - `frps/internal/dbschema/schema.go`
  - 启动 schema 校验测试
  - SQLite / MySQL 一致性测试
- 不把 `rate_policy_id` 正式落回 `tunnels` 表，避免重新把独立资源耦回隧道表。

### 3. 建立管理 API 最小闭环

- 新增独立限速策略 API 模块，和 `groupconfig` 分开，不混职责。
- 路由层至少需要：
  - `GET /api/v1/rate-policies`
  - `POST /api/v1/rate-policies`
  - `PATCH /api/v1/rate-policies/{id}`
  - `DELETE /api/v1/rate-policies/{id}`
  - `GET /api/v1/rate-policies/{id}/bindings`
  - `POST /api/v1/rate-policies/{id}/bindings`
  - `DELETE /api/v1/rate-policies/{id}/bindings/{tunnel_id}`
- 视图层至少需要返回：
  - 策略基础字段
  - 当前绑定隧道数量
  - 绑定隧道明细或最小绑定视图
- `TunnelView` 可以追加只读投影字段用于回显：
  - `rate_policy_id`
  - `rate_policy_name`
  - 但这些只作为查询投影，不作为正式持久化真实来源
- 所有组合约束必须在 API 入口收住：
  - 只允许绑定单端口 TCP / UDP 隧道
  - range 隧道不可绑定
  - 已绑定隧道不能再绑定第二个策略
  - 可先创建空策略
  - 速率输入必须能稳定归一到 `bps`
  - 名称、模式、速率范围、单位非法值都在入口拒绝
- 任何影响在线执行结果的策略或 binding 变更，都要触发对应 `proxy_group` 的 runtime refresh。

### 4. 把限速策略投影进运行时快照

- repo 层需要在加载 `GroupRuntime` 时，额外解析：
  - 该 `proxy_group` 下所有隧道
  - 这些隧道关联到的 `rate_policy`
  - 需要投影进快照的最终执行配置
- 快照层要明确：
  - 哪些字段属于“策略身份”
  - 哪些字段属于“执行参数”
  - 哪些字段属于“共享协作 key”
- `ConfigSnapshot` / `protocol.ConfigPush` / `protocol.TunnelEntry` 需要扩展最小限速字段。
- `frps/pkg/protocol` 必须同步：
  - marshal / unmarshal
  - 兼容当前单版本协议定义
  - 新旧测试全部改到当前真相
- `frpc` 快照替换逻辑必须把限速执行字段纳入“隧道执行配置是否变化”的判断，避免策略变了却被误判为未变化。
- reload 语义必须保持当前已有约定：
  - 命中运行态字段时整组冻结
  - 下发新快照
  - `ack` 后再恢复 listener / 数据面

### 5. 摊平最小限速组件

- 底层纯组件要先摊开，再由上层拼装：
  - `RateValue` / 单位归一
  - `Direction`
  - `Mode`
  - `BucketConfig`
  - `TokenBucket`
  - `LimiterRegistry`
  - `WaitN` / context cancel
- `independent` 和 `shared` 不能做两套完全分叉实现。
- 推荐收口方式：
  - `independent`：每条 tunnel / 每个方向各自持有 limiter
  - `shared`：session 级 registry 按 `rate_policy_id + direction` 复用 limiter
- `shared` 的共享边界固定为单个已登录 session 内部，不做跨 session、跨进程、跨节点共享。
- `shared` 不增加额外 `proxy_group` 锁定字段；管理入口仍然只做“逐条隧道绑定”。
- 低层组件必须可单测，不能一上来就绑到 TCP/UDP socket 或控制面 session 上。

### 6. 把限速真正接进 TCP 数据面

- `frps` 侧需要把下行 limiter 接到公网 TCP -> `stream.data` 这条链路。
- `frpc` 侧需要把上行 limiter 接到本地 TCP -> `stream.data` 这条链路。
- 需要明确：
  - 限速等待发生在读前还是写前
  - 大块数据切片后如何扣令牌
  - 等待中 session 关闭时如何退出
  - reload 关闭旧 stream 时如何打断等待
- 不能把限速逻辑直接散落到 `copy loop` 各处；需要通过清晰的 rate-limited reader / sender 组件接入。
- 任何接入都不能破坏现有：
  - `stream.open`
  - `stream.opened`
  - `stream.data`
  - `stream.close`
  - 热重载关闭旧 stream
  - 故障回收

### 7. 把限速真正接进 UDP 数据面

- `frps` 侧需要把下行 limiter 接到公网 UDP ingress -> `udp.data`。
- `frpc` 侧需要把上行 limiter 接到本地 UDP response -> `udp.data`。
- UDP 接入要额外处理：
  - 单个 datagram 大于当前瞬时令牌时的等待
  - session idle cleanup 与限速等待并存
  - `udp.close`、reload、session close 时取消等待
- 必须避免：
  - UDP 限速等待导致 idle cleanup 失真
  - 共享策略下多 UDP tunnel 争抢同一 limiter 时死锁
  - `udp.open` / `udp.data` / `udp.close` 顺序被破坏

### 8. 补齐 `shared` 模式最小闭环

- 在 `independent` 打通后，再复用同一套底层组件补 `shared`。
- `shared` 需要额外完成：
  - session 内同策略多 tunnel 共用同一方向 limiter
  - reload 后旧 registry 清理、新 registry 按新快照重建
- 不能为 `shared` 新做第二套 bucket、第二套 registry、第二套路由。

### 9. 测试收口

- 纯单测：
  - 速率单位换算
  - `bps` 边界值
  - bucket refill / wait / cancel
  - `independent` / `shared` registry 语义
  - 小速率下大帧 / 大 datagram 仍可前进
- schema / repo / API 测试：
  - 建表与校验
  - 策略 CRUD
  - binding CRUD
  - 单隧道唯一绑定
  - range 隧道拒绝绑定
  - 删除仍有 binding 的策略拒绝
- 协议与快照测试：
  - `ConfigPush` 新字段编解码
  - snapshot compare / reload 判定
- 控制面 / 数据面场景测试：
  - 在线 reload 后限速生效
  - session close / shutdown 时等待中的 limiter 退出
  - TCP `independent`
  - TCP `shared`
  - UDP `independent`
  - UDP `shared`
  - 多 tunnel 竞争同一共享 limiter
  - UDP idle cleanup 与限速并存
- Python e2e：
  - 单端口 TCP `independent`
  - 单端口 TCP `shared`
  - 单端口 UDP `independent`
  - 单端口 UDP `shared`
  - 绑定非法场景
  - 在线修改策略后的 reload 生效
- 最终验收命令至少包括：
  - `cd frps && go test ./...`
  - `cd frps && go test -tags testhooks ./...`
  - `cd frpc && go test ./...`
  - 对应 rate policy e2e 脚本
  - 现有 TCP / UDP 基线 e2e，确保没把旧链路打坏

### 10. 文档和归档收口

- 实现边界一旦落地，必须同步正式文档：
  - `docs/project-overview.md`
  - `docs/frps/design/rate-policy.md`
  - `docs/frps/technical/data-model.md`
  - `docs/frps/technical/management-api.md`
  - `docs/frps/features/control-and-data-plane.md`
  - 如有必要再补 `docs/frps/features/overview.md`
- 任一子步骤完成后，立即归档到 `docs/progress/YYYY-MM-DD.md`，不把完成项留在当前 `todo`。
- 每一子步骤完成后都要检查：
  - `.gitignore` 是否需要补临时产物
  - 是否形成一个可提交的稳定状态

## 当前唯一下一步

- 先冻结第 1-3 步的正式落点：确定 `rate_policies` / `rate_policy_bindings` 的表结构、管理 API 路由和所有边界校验规则，然后先实现这层 schema + API 闸口，再进入 runtime snapshot 和数据面限速。

## 进度归档入口

- 当前轮待办只看：`docs/tmp/todo.md`
- 已完成事项归档到：`docs/progress/YYYY-MM-DD.md`
- 轮换规则见：`docs/workflow.md`
- 归档说明见：`docs/progress/README.md`
