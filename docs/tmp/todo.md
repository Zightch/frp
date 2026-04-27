# 当前轮 Todo

- 当前无待办。
- `frps/internal/control` 的 `control v2` 主链路已经接入旧 `control.Server`：
  - 登录成功后的会话建立已经进入 `SessionAgent`
  - `config.push/ack` 已通过 reducer/reconcile + executor 驱动
  - `RefreshGroup` 已切到新的 session/reconcile 语义
  - 同分组第二个 client 改为新会话接管旧会话
  - `runtime scan` 不再作为主恢复链路；旧测试中绑定旧语义的部分已显式标注为过时

- 进度归档索引：`docs/progress/README.md`
