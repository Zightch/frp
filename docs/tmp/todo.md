# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 继续为端口冲突检查 / 热更新交互补齐剩余正式场景测试，覆盖未监听 tunnel 自动恢复、旧 session 晚到消息隔离和连续 `RefreshGroup()` / pending config 竞争。

子步骤：

- 未监听 tunnel 轮询补启动：外部占用恢复后重新评估并补启动，确认 runtime issue 清理、listener 恢复和最终状态回正。
- 旧 session 晚到消息隔离：`session#1` 被 `session#2` 接管后，旧 `config.ack` / `heartbeat` / `error` / `shutdown` 帧不得污染新 session。
- 连续 `RefreshGroup()` / pending config 竞争：在上一轮刷新尚未完成时继续触发配置变更，验证不会双推 `config.push`、不会回退快照、不会留下双 pending。

当前轮边界：

- 启动门闩与 `effective_ip` 主链路前 4 个确定性测试已完成并归档；当前 `todo` 只保留剩余测试实现项。
- 继续优先复用 `testhooks`、`ObservedState`、不变量断言和场景编排；旧式 `sleep/poll` helper 仅在现有夹具不可替代处少量复用。
- 不改业务逻辑，只补测试、进度归档和必要的测试辅助代码。

当前唯一下一步：

- 未监听 tunnel 轮询补启动：外部占用恢复后重新评估并补启动，确认 runtime issue 清理、listener 恢复和最终状态回正。
