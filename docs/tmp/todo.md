# 当前 Todo

## 当前目标

- 完成 `TCP` 数据面从 `control connection` 摘离的最小闭环。
- 当前轮目标不是做“局部微调”，而是把 `TCP` 从 `stream.data` 高开销路径切到独立 `work connection + raw relay` 新路径。
- 本轮完成后，`TCP` 不再复用控制连接承载业务字节流；控制连接只负责登录、心跳、配置和少量控制消息。

## 当前范围

- 只做 `TCP`。
- `UDP` 本轮不动，继续沿用当前实现。
- 只做后端：`frps`、`frpc`、协议、测试、文档。
- 前端本轮不涉及。

## 原则边界

- 不保留 `TCP` 旧 `stream.data / stream.close` 路径与新路径双轨并存。
- 不为了兼容旧版本保留过渡层、桥接层、临时开关。
- `frps` 继续做唯一权威方；`frpc` 只保留执行链路必需的最小状态。
- 先完成最小可运行闭环，再决定是否继续做更激进的性能优化。
- 本轮不顺手扩展：
  - `UDP` 数据面重构
  - 零拷贝 / `splice` / `sendfile`
  - 额外对外配置项
  - work pool 平台化、插件化、可切换策略

## 完成定义

- `frps` 能区分 `control connection` 与 `tcp work connection`。
- `frpc` 登录后能建立并维持可复用的 `TCP work pool`。
- 公网 `TCP` 连接到达后，能通过 `stream.open / stream.opened` 完成一次性配对，然后切到 raw byte relay。
- 单端口 `TCP` 和 `TCP range` 都能跑通。
- `TCP` 隧道现有限速能力继续成立，并挂到新 raw relay 路径上。
- 热重载、会话替换、断线重连、服务端关闭时，不残留 busy work connection。
- 旧 `TCP stream.data / stream.close` 路径和相关状态分叉被删除。
- 回归测试通过，文档同步到当前实现。

## 七刀计划

### 刀一：协议切口

- 补 `tcp work connection` 的最小握手 / 注册语义。
- 在 `server.hello` 中下发当前 session 所需的 `work pool` 参数和会话级凭据。
- 保留现有 `stream.open / stream.opened` 作为 `TCP` 建链消息，不再用 `stream.data` 承载业务流量。

完成标准：

- `frps` 与 `frpc` 都能明确识别“这是一条 control 连接”还是“这是一条 TCP work 连接”。
- 新协议字段和消息有单元测试覆盖。

### 刀二：frps work 连接注册与池化

- 在 `frps` 增加 session 级 `work connection registry`。
- 接收、校验并注册 idle work connection。
- 提供从 idle 池分配、标记 busy、释放、统一关闭的最小接口。

完成标准：

- `frps` 能按 `session_id` 管理 idle / busy work connection。
- 会话关闭时，work connection 能统一回收。

### 刀三：frpc work pool

- `frpc` 登录成功后，预热固定数量的 idle work connection。
- work connection 用尽或关闭后自动补池。
- `frpc` 只负责维护最小池状态，不做策略裁决。

完成标准：

- `frpc` 能稳定维持目标数量的 idle work connection。
- 重连后旧池释放，新池按新 session 重建。

### 刀四：TCP 主链切换

- `frps` 公网 TCP accept 后，不再走 `stream.data` 多路复用。
- 改为：分配一条 idle work connection -> 发送 `stream.open` -> 等待 `stream.opened` -> 切 raw relay。
- `frpc` 收到 `stream.open` 后复用现有 tunnel / target 解析和后端拨号逻辑。

完成标准：

- 单端口 `TCP` 正常转发。
- `TCP range` 正常按现有 offset 规则转发。
- 后端拨号失败能按新路径正确关闭连接。

### 刀五：限速与关闭语义挂接

- 将 `TCP` 上下行限速从 `stream.data` 分块写迁移到 raw relay copy 路径。
- 收紧 EOF、半关闭、写失败、后端失败、公网侧提前关闭的语义。
- 确保共享限速和独享限速在新路径上都成立。

完成标准：

- 单端口 `TCP` 限速效果仍可观测。
- 关闭语义单一，不出现新旧路径分叉。

### 刀六：生命周期收口并删除旧 TCP 路径

- 处理热重载、freeze、session replace、shutdown、frpc 重连时的 busy work connection 清理。
- 删除 `TCP stream.data / stream.close` 控制路径。
- 删除因旧 TCP 多路复用而存在的多余状态和写锁依赖。

完成标准：

- 系统中只剩一条 `TCP` 数据面执行路径。
- 热重载和断链后不残留忙连接、旧 stream 状态或无主资源。

### 刀七：回归测试与文档同步

- 重写原来按 `stream.data` 帧观察的 `TCP` 测试。
- 补 work pool、无 idle work connection、热重载、会话替换、服务关闭、重连、限速和性能回归场景。
- 同步架构文档、回归入口和当前轮进展记录。

完成标准：

- `Go` 回归覆盖新 TCP 数据面主链。
- `Python e2e` 继续覆盖单端口和 range。
- 文档只描述当前唯一实现，不保留旧 TCP 路径描述。

## 关键验证场景

- `TCP` 单端口 happy path
- `TCP range` happy path
- 后端拨号失败
- work pool 暂时无 idle 连接
- `frpc` 重连后 work pool 重建
- `frps` 刷新配置后的热重载
- session replace / group offline / server shutdown
- 独享限速
- 共享限速
- 性能回归：同机 loopback 下确认新路径明显高于旧 `stream.data` 路径

## 当前不在本轮排期的事项

- `UDP` 数据面摘离控制连接
- 更激进的零拷贝优化
- 动态可配的 work pool 策略
- 多监听入口拆分
- 面向旧实现的兼容模式

## 归档说明

- 上一轮“限速策略最小闭环”已完成，归档见：`docs/progress/2026-05-01.md`
- 当前轮待办只看：`docs/tmp/todo.md`
- 已完成事项归档到：`docs/progress/YYYY-MM-DD.md`
- 轮换规则见：`docs/workflow.md`
- 归档说明见：`docs/progress/README.md`
