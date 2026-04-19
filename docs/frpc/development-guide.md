# frpc 开发文档

## 1. 开发原则

开发 `frpc` 时要始终守住两条边界：

- 客户端只做执行，不做策略决策。
- 客户端只做当前阶段必须的最小能力，不提前为未来复杂场景堆结构。

因此任何新增需求，都要先判断是否应放在 `frps` 实现，而不是直接加到 `frpc`。

额外约束：

- `frps` 框架、WebUI 框架、数据库基础和首版协议没有稳定前，不提前扩展 `frpc` 功能面。
- 不为未来的多服务端、多工作模式、离线缓存、复杂本地配置提前加抽象。
- `frpc` 启动参数始终优先保持为最小集合：`server` 和 `token`。
- 轮换制 `todo` 遵循统一工作流：统一按 `docs/workflow.md` 执行；当前轮 `docs/tmp/todo.md` 只保留当前总目标、实现该目标的子步骤、当前轮边界和当前唯一下一步，不写已完成内容。

## 1.1 当前进度

截至 2026-04-19，`frpc` 当前已完成：

- `server` / `token` 参数解析与校验
- 固定长度 token 解析
- challenge/response 登录
- `config.push` / `config.ack`
- 最小 TCP 单端口 stream 生命周期处理
- 最小 UDP 单端口 session 生命周期处理
- 真实本地 UDP 转发与 `udp.close` 收口
- 本地拨号失败错误回传
- 与真实 `frps` 的最小端到端脚本联调：
  - `test/e2e_tcp_single.py`
  - `happy_path`
  - `bad_token`
  - `disabled_group`
  - `disabled_tunnel`
  - `local_unavailable`
  - `test/e2e_udp_single.py`
  - `happy_path`
  - `idle_cleanup`

当前仍未进入：

- 端口范围
- 在线热更新的更完整矩阵
- 多客户端竞争场景

## 2. 当前目录

```text
frpc/
├── cmd/frpc/
├── internal/client/
├── internal/config/
└── go.mod
```

共享协议与传输层代码当前复用 `frps/pkg/`。

## 3. 环境要求

- Go 1.23 或更高版本
- 可访问测试 `frps`
- 可准备本地 TCP/UDP 测试服务

本地联调常用环境变量：

- `FRPC_LOG_LEVEL=debug`
- `FRPC_DEBUG=1`

## 4. 开发顺序

`frpc` 的开发顺序服从整体主线，不能脱离 `frps` 单独堆功能。

### 4.1 第一阶段：等待服务端基础稳定

- 对齐 `frps` 的启动方式、日志方式和最小配置入口。
- 明确客户端 CLI 只接受 `server` 和 `token`。
- 不提前引入本地复杂配置文件或额外运行模式。

### 4.2 第二阶段：等待最小协议确定

- 对齐登录、心跳、配置下发消息。
- 对齐 `config.push/config.ack` 和 `stream.close` 的语义。
- 只实现当前已用到的消息类型。
- 协议先按 `4` 字节长度前缀和二进制业务帧处理。

### 4.3 第三阶段：实现最小接入链路

- 参数解析
- 控制连接
- 登录与心跳
- 配置接收

当前状态：

- 已完成。

### 4.4 第四阶段：实现 TCP 正向代理

- TCP 工作流
- 本地拨号
- 自动重连
- 单端口先跑通

当前状态：

- 最小 TCP 单端口链路已完成并已纳入端到端脚本验证。

### 4.5 第五阶段：补增强能力

- 运行态管理
- 错误事件上报
- 端口范围映射配合支持

当前状态：

- UDP 最小闭环已完成。
- 当前 UDP 约定固定为：`frps` 统一裁决 session 生命周期和空闲 `30s` cleanup，`frpc` 不做本地 idle timeout 判断，只执行 `udp.close` 收口。

## 5. 编码约定

- 不在 `cmd/frpc` 中写业务逻辑。
- 协议编解码统一放 `pkg/protocol`。
- 每个活跃流都必须可取消、可回收。
- 每个活跃流都必须持有打开瞬间的隧道快照或已解析目标，避免被后续配置热更新污染。
- 任何 goroutine 都要有明确退出条件。
- TCP/UDP 转发层不要依赖管理层包，避免耦合反向扩散。

## 6. 与 frps 协作约定

### 6.1 登录

登录前只知道：

- `server`
- `token`

`token` 必须按固定长度拆成：

- `token_id`
- `token_secret`

登录流程固定为：

1. 发送 `token_id`。
2. 接收服务端一次性临时盐。
3. 本地计算 `token_hash = sha256(token_secret)`。
4. 发送 `sha256(token_hash + challenge_nonce)`。
5. 登录成功后再接收分组配置。

登录后才能知道：

- 所属分组
- 配置版本
- 隧道列表
- 心跳间隔

### 6.2 配置更新

当 `frps` 更新分组配置时，`frpc` 只能：

- 接收
- 替换
- 应用
- 确认

不能自行保留已删除业务配置。

应用要求：

- 收到 `config.push` 后原子替换运行态配置。
- 替换完成后回 `config.ack`。
- 配置变更不允许通过重启进程实现。

UDP 额外约定：

- `sessionId` 由 `frps` 分配。
- `frps` 是 UDP session 生命周期唯一裁决方。
- `frpc` 只在收到 `udp.close`、发生本地不可恢复错误，或控制连接结束时释放本地 UDP session。

## 7. 本地测试服务建议

用于联调的本地服务：

- TCP echo server
- UDP echo server
- 简单 HTTP server

这样可以覆盖：

- TCP 正向代理
- UDP 正向代理
- HTTP 类应用透传

## 8. 调试约定

开发期允许增加调试日志，但不建议扩展业务参数。

调试输出重点：

- 连接建立
- 登录成功与失败
- 配置版本变化
- stream 打开和关闭
- 本地拨号失败
- 重连退避

## 9. 提交前检查

每次改动后至少检查：

- 能否正常登录 `frps`
- TCP 透传是否正常
- 如果改动 UDP 数据面，`python test/e2e_udp_single.py` 和 `python test/e2e_udp_single.py --scenario idle_cleanup` 是否通过
- 重连是否正常
- 配置热更新是否无需重启即可生效
- 日志是否可读
