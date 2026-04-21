# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 收口代码巡检中剩余的外部调用契约与配套回归脚本问题。

子步骤：

- 统一 `PATCH /api/v1/proxy-groups/:id` 的实际语义与调用契约。
- 修正 `test/e2e_management_webui.py` 与当前 `effective_ip` 契约不一致的问题。

当前轮边界：

- 当前只继续处理 `frps` 管理 API 契约和配套回归脚本，不回到已完成的 `webui/dist` 启动降级问题。
- 每次只完成一个独立子步骤；完成后立即归档、提交并停下，等待用户确认。
- 先收口对外接口语义，再按最终契约修正 e2e 脚本，避免重复修改测试。

当前唯一下一步：

- 统一 `PATCH /api/v1/proxy-groups/:id` 的实际语义与调用契约，并完成归档与提交。
