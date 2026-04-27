# 当前轮 Todo

- 当前无待办。
- `frps/internal/control` 的 `control v2` 主链路已经接入旧 `control.Server`：
  - 登录成功后的会话建立已经进入 `SessionAgent`
  - `config.push/ack` 已通过 reducer/reconcile + executor 驱动
  - `RefreshGroup` 已切到新的 session/reconcile 语义
  - 同分组第二个 client 改为新会话接管旧会话
  - `runtime scan` 不再作为主恢复链路；旧测试中绑定旧语义的部分已显式标注为过时
  - `ObserveState` / `runtime scan` 现在直接读取 `controlv2` 会话状态构建 runtime view
  - 旧的 `runtime_coordinator` / `runtime_target` 已删除，不再保留第二套管理动作规划模型
  - `Server.Shutdown` 已显式联动关闭 `controlv2.Supervisor`

- 进度归档索引：`docs/progress/README.md`
