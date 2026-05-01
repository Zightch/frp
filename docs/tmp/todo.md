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
  - `frpc` 保持轻量，只负责接收配置和转发数据，不执行限速
  - 权威限速逻辑全部放在 `frps`
- 后续结构仍必须按多层收口：
  - 底层纯组件
  - 中层快照投影和 limiter 协作
  - 上层控制面 / 数据面拼装
- 已完成归档见：`docs/progress/2026-05-01.md`

## 子步骤

### 1. 把限速真正接进 `frps` UDP 数据面

- 下行：公网 UDP ingress -> `udp.data`
- 上行：`udp.data` -> 公网 UDP listener
- 必须明确：
  - datagram 的扣令牌点
  - 大 datagram 等待和分片策略
  - `udp.close` / reload / shutdown / session close 时的等待取消
- 不能把限速逻辑散落到现有 UDP 转发分支各处。

### 2. 补齐最小闭环测试

- 纯单测：
  - bucket refill / wait / cancel
  - `independent` / `shared` registry 语义
  - 小速率下大帧 / 大 datagram 仍可前进
- 控制面 / 数据面场景测试：
  - 在线 reload 后限速生效
  - session close / shutdown 时等待中的 limiter 退出
  - TCP `independent` / `shared`
  - UDP `independent` / `shared`
  - 多 tunnel 竞争同一共享 limiter
  - UDP idle cleanup 与限速并存
- Python e2e：
  - 单端口 TCP `independent`
  - 单端口 TCP `shared`
  - 单端口 UDP `independent`
  - 单端口 UDP `shared`
  - 在线修改策略后的 reload 生效

### 3. 文档和归档收口

- runtime / protocol / 数据面落地后，同步：
  - `docs/project-overview.md`
  - `docs/frps/design/rate-policy.md`
  - `docs/frps/technical/data-model.md`
  - `docs/frps/technical/management-api.md`
  - `docs/frps/features/control-and-data-plane.md`
- 每完成一层就归档到 `docs/progress/YYYY-MM-DD.md`，并提交稳定状态。

## 当前唯一下一步

- 先把限速真正接进 `frps` UDP 数据面，明确 datagram 限速执行点、等待推进策略，以及 `udp.close / reload / shutdown / session close` 的取消语义，暂不碰新的 e2e 和文档扩写。

## 进度归档入口

- 当前轮待办只看：`docs/tmp/todo.md`
- 已完成事项归档到：`docs/progress/YYYY-MM-DD.md`
- 轮换规则见：`docs/workflow.md`
- 归档说明见：`docs/progress/README.md`
