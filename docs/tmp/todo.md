# 当前轮 Todo

- 目标已经固定为单一稳定包：`frps/internal/control/`
- `frps/internal/controlv2/` 只允许作为迁移中间态存在，最终必须整体删除
- 当前阶段不再给旧 `control/` 和 `controlv2/` 双轨继续补功能；后续代码改动只服务“去分叉”

- 推荐落地顺序：
  1. 在 `internal/control/session/` 固定最终 `state/events/actions/reducer/reconcile`
  2. 建 `internal/control/executor.go` / `runtime.go`，接管 control conn writer、stream/udp handle、timer 和 epoch 资源
  3. 建 `internal/control/bind/`，接管 claim、冲突裁决、listener 和 tunnel listen TLS
  4. 建 `internal/control/repo/`，拆开认证读取与 desired runtime 读取模型
  5. 把 `Server` / `Supervisor` 切到新结构后删除 `internal/controlv2/`
  6. 删除旧 `sessionState`、`runtimeRegistry`、`runtimeView`、`runtimeIssueStore` 和 `runtime scan` 主恢复路径

- 当前文档基线：
  - `docs/tmp/control-v2-design.md`

- 进度归档索引：`docs/progress/README.md`
