# 限速最小闭环 Todo

## 当前总目标

- 完成“限速策略”最小闭环，并让自动化测试通过。
- 本轮剩余闭环定义固定为：
  - `frps` / `frpc` 在首次登录和在线热重载后，都能按最新策略执行限速。
  - 单端口 TCP / UDP 的上下行都真正进入限速执行链路。
  - `independent` 和 `shared` 两种策略模式都可用。
  - `frps` / `frpc` Go 测试、控制面场景测试和 Python e2e 都通过。

## 当前轮边界

- 当前继续只做最小核心闭环，不做：
  - WebUI 页面和交互
  - TCP range / UDP range 限速
  - 限速统计页、实时观测页、图表页
  - burst 对外配置项
  - 多策略叠加
  - 一个隧道绑定多个策略
  - 旧版本兼容层
- 后续结构仍必须按多层收口：
  - 底层纯组件
  - 中层快照投影和 limiter 协作
  - 上层控制面 / 数据面拼装
- 已完成归档见：`docs/progress/2026-05-01.md`

## 子步骤

### 1. 摊平最小限速组件

- 继续下沉纯组件：
  - `Direction`
  - `BucketConfig`
  - `TokenBucket`
  - `LimiterRegistry`
  - `WaitN / context cancel`
- `independent` 和 `shared` 必须共用同一套底层组件，不允许做两套分叉实现。
- `shared` 当前只允许在单个已登录 session 内共享，不做跨 session / 跨进程 / 跨节点共享。

### 2. 把限速真正接进 TCP 数据面

- `frps` 下行：公网 TCP -> `stream.data`
- `frpc` 上行：本地 TCP -> `stream.data`
- 必须明确：
  - 扣令牌点
  - 大块数据切片策略
  - reload / shutdown / session close 时的等待取消
- 不能把限速逻辑散落到现有 copy loop 各处。

### 3. 把限速真正接进 UDP 数据面

- `frps` 下行：公网 UDP ingress -> `udp.data`
- `frpc` 上行：本地 UDP response -> `udp.data`
- 必须额外处理：
  - 大 datagram 等待
  - idle cleanup 与限速等待并存
  - `udp.close` / reload / session close 时取消等待

### 4. 补齐 `shared` 模式最小闭环

- 在 `independent` 打通后，再复用同一套组件补 `shared`。
- `shared` 需要额外确认：
  - session 内同策略多 tunnel 共用同一方向 limiter
  - reload 后旧 registry 清理、新 registry 按新快照重建

### 5. 测试收口

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

### 6. 文档和归档收口

- runtime / protocol / 数据面落地后，同步：
  - `docs/project-overview.md`
  - `docs/frps/design/rate-policy.md`
  - `docs/frps/technical/data-model.md`
  - `docs/frps/technical/management-api.md`
  - `docs/frps/features/control-and-data-plane.md`
- 每完成一层就归档到 `docs/progress/YYYY-MM-DD.md`，并提交稳定状态。

## 当前唯一下一步

- 先摊平最小 limiter 纯组件：`Direction / BucketConfig / TokenBucket / WaitN / LimiterRegistry`，让 `independent` 和 `shared` 只共用这一套底层语义，暂不接 TCP / UDP 数据面。

## 进度归档入口

- 当前轮待办只看：`docs/tmp/todo.md`
- 已完成事项归档到：`docs/progress/YYYY-MM-DD.md`
- 轮换规则见：`docs/workflow.md`
- 归档说明见：`docs/progress/README.md`
