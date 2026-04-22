# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 继续为端口冲突检查 / 热更新交互补齐正式测试矩阵，在已完成前 4 条确定性场景的基础上，把剩余测试任务按“确定性场景、异常输入、竞争窗口、恢复路径、稳定性入口”完整推进。

子步骤：

- 确定性场景补齐：
  - 管理 API 首次可见不仅要验证端口时机，还要直接验证首次返回内容不是首轮扫描中的半成品状态。
  - 未监听 tunnel 在外部占用恢复后要被重新评估并补启动，确认 runtime issue 清理、listener 恢复和最终状态回正。
  - 首次登录握手期间若仓库配置变化，验证服务端不会放行过期快照、客户端不会启动旧 tunnel 集合。
  - 首次 `config.ack` 成功后立刻遭遇外部抢占或 `effective_ip` 失效，验证 listener 清理、runtime issue 标记和 session 活性边界。
- 异常输入补齐：
  - `config.ack` 非法 `requestId`、非法 `streamId`、版本不匹配、重复 ack、未知 ack 状态，要补矩阵化回归。
  - `frpc` 对非法 `config.push` 帧头和非法快照的拒绝路径，要补系统性异常输入回归。
  - 冷启动脏库下的 `effective_ip` 为空、格式异常、地址族不匹配、当前不在本机，要分别覆盖首轮扫描标记和首次登录拒绝口径。
  - 极端非法 tunnel 配置组合，包括空端口范围、倒置端口范围、越界端口、缺协议、重复 tunnel、group 启用但 tunnel 全禁用、tunnel 启用但 group 禁用，要补专项覆盖。
  - 客户端侧“请联系管理员解决”的最终用户可见错误路径，要补端到端断言。
- 竞争窗口补齐：
  - `RefreshGroup()` 与 `scanNonListeningTunnelRuntimeIssues()` 并发，验证不会双推 `config.push`、不会把新快照回退为旧快照。
  - 空配置下发与轮询恢复并发，验证必须先等空配置生效，再判断是否补推完整快照。
  - `config.push` A 未 ack 时又生成 `config.push` B，验证重复 ack、旧 ack 晚到、ack 乱序不会污染 listener 集合或 pending 状态。
  - `session#1` 被 `session#2` 接管后，旧 `config.ack` / `heartbeat` / `error` / `shutdown` 帧不得污染新 session。
  - listener 启动与 session shutdown、tunnel disable/delete、外部端口抢占并发，验证不会遗留半开 listener、脏 map、悬挂 goroutine 或错误恢复。
  - 轮询扫描自身重叠、轮询扫描与管理端配置修改并发、轮询扫描与本机网络快照刷新并发，要补确定性回归。
  - 服务端关闭过程与首轮扫描、轮询、热更新、listener 启停并发的稳定性，要补直接竞态回归。
- 恢复路径与故障补齐：
  - 空配置保活后的客户端断线重连，验证新旧 session 交接时不会残留旧 listener、旧 runtime issue、旧 pending config。
  - 外部占用高频抖动同一端口，验证运行态异常不会在一次或多次扫描中被错误清理或错误恢复。
  - `effective_ip` 在本机地址集合内外高频抖动，验证空配置与完整配置不会交叉覆盖，也不会留下脏 listener。
  - 恢复路径上的资源层故障，包括权限不足、文件句柄紧张、短时间大量 listener 启停、心跳与恢复配置同时发生，要补系统性覆盖。
- 稳定性补充层：
  - 在确定性场景补齐后，再补 `-race`、多 `GOMAXPROCS`、高频配置抖动、长稳 soak、双平台回归和资源压力回归入口。
  - 这些入口必须复用现有观测和失败判定，不允许退回“跑一晚看日志”。
- 可测性验收门槛：
  - 任一准备进入正式测试矩阵的场景，先要求能在单机连续复现、在快慢机器上保持同一状态结论、在失败时输出明确状态断言差异；做不到这一点的场景，先回到前置基建补能力。

当前轮边界：

- 启动门闩与 `effective_ip` 主链路前 4 个确定性场景测试已完成并归档；当前 `todo` 只保留剩余测试任务，不回填已完成项。
- 当前轮继续优先复用 `testhooks`、`ObservedState`、不变量断言和场景编排；旧式 `sleep/poll` helper 仅在现有夹具不可替代处少量复用。
- 当前轮仍聚焦端口冲突检查与热更新交互，不扩展到其他独立功能优化。
- 测试工作不局限于现有 `*_test.go`，允许包含脚本化双进程场景、异常注入、双平台验证与长稳回归。

当前唯一下一步：

- 未监听 tunnel 在外部占用恢复后要被重新评估并补启动，确认 runtime issue 清理、listener 恢复和最终状态回正。
