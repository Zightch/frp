# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成 `frps` 端口冲突检测的最小闭环，先在管理面拦住可判定冲突，再补控制面运行态兜底，并同步测试与正式文档。

子步骤：

- 实现共享监听占位 helper，预计落点在 `frps/internal/ports`，让管理面和控制面都走同一套冲突比较逻辑。
- 在管理面落地静态校验：`createTunnel()`、`updateTunnel()`、`updateProxyGroup()` 在写库前重算受影响的监听声明并拒绝冲突配置。
- 在控制面补充运行态兜底：`ensureTunnelListeners()` 和 `effective_ip` 重绑路径在真正绑定前复用同一套声明模型，并把底层 bind 失败收敛成明确的端口冲突语义。
- 补齐回归测试，并把最终已实现口径同步到 `docs/progress/`、`docs/frps/functional-spec.md`、`docs/frps/technical-design.md`。

当前轮边界：

- 当前轮只处理 `frps` 公网监听端口冲突，不处理 `frpc` 本地目标端口冲突。
- 当前轮优先处理仓库内可由配置静态判定的冲突；对操作系统上其他外部进程的端口占用，当前至少要在控制面做 listener 启动兜底，但是否在管理面提前探测，不在本轮第一步直接拍板。
- 端口冲突判定矩阵和共享监听占位模型已归档到 `docs/progress/2026-04-22.md`；后续实现按该口径推进，不在代码阶段再临时改规则。
- 当前轮优先复用服务端已有错误返回与日志链路，不新增前端专用预测逻辑；WebUI 先消费管理 API 的明确报错。
- 当前轮如果发现“禁用分组/禁用隧道是否占位”会显著影响实现范围，先在本轮口径梳理阶段定清楚，再进入代码实现，不边写边改规则。
- 按 `docs/workflow.md` 执行：每完成一个子步骤，先归档、检查 `.gitignore`、提交并重写 `todo`，然后停下等待确认。

当前唯一下一步：

- 先实现共享监听占位 helper，并把它接入管理面的 `createTunnel()`、`updateTunnel()`、`updateProxyGroup()` 写库前校验，先收口配置层静态冲突拒绝。
