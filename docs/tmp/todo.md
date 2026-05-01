# 限速最小闭环 Todo

## 当前总目标

- 完成“限速策略”最小闭环，并让自动化测试通过。
- 当前闭环定义固定为：
  - `frps` 在首次登录和在线热重载后，能按最新策略执行限速。
  - 单端口 TCP / UDP 都真正进入 `frps` 限速执行链路。
  - `independent` 和 `shared` 两种策略模式都可用。
  - `frps` Go 测试、控制面场景测试和 Python e2e 都通过。

## 当前轮边界

- 当前继续只做最小核心闭环，不做：
  - WebUI 页面和交互
  - TCP range / UDP range 限速
  - 限速统计页、实时观测页、图表页
  - burst 对外配置项
  - 多策略叠加
  - 一个隧道绑定多个策略
  - 旧版本兼容层
- 当前执行边界再明确收口：
  - `frpc` 保持轻量，只负责接收配置和转发数据
  - `frpc` 不执行限速
  - `frpc` 不接收限速字段
  - 权威限速逻辑全部放在 `frps`
- 后续结构仍必须按多层收口：
  - 底层纯组件
  - 中层快照投影和 limiter 协作
  - 上层控制面 / 数据面拼装
- 已完成归档见：`docs/progress/2026-05-01.md`

## 子步骤

### 1. 补齐最小闭环测试

- Go 场景测试继续补：
  - 在线 reload 后限速生效
  - session close / shutdown 时等待中的 limiter 退出
  - TCP `independent` / `shared`
  - UDP `independent` / `shared`
  - 多 tunnel 竞争同一共享 limiter
  - UDP idle cleanup 与限速并存
- Python e2e 继续补：
  - 单端口 TCP `independent`
  - 单端口 TCP `shared`
  - 单端口 UDP `independent`
  - 单端口 UDP `shared`
  - 在线修改策略后的 reload 生效

### 2. 文档和归档收口

- 测试闭环落地后，同步：
  - `docs/project-overview.md`
  - `docs/protocol.md`
  - `docs/frps/design/rate-policy.md`
  - `docs/frps/features/control-and-data-plane.md`
  - 其他仍残留分叉的正式文档
- 每完成一层就归档到 `docs/progress/YYYY-MM-DD.md`，并提交稳定状态。

## 当前唯一下一步

- 先补限速最小闭环测试，优先覆盖单端口 TCP / UDP 的 `independent` / `shared`、共享桶竞争，以及在线 reload 生效路径，暂不碰 range 和观测页。

## 进度归档入口

- 当前轮待办只看：`docs/tmp/todo.md`
- 已完成事项归档到：`docs/progress/YYYY-MM-DD.md`
- 轮换规则见：`docs/workflow.md`
- 归档说明见：`docs/progress/README.md`
