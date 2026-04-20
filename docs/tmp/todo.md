# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

## 当前总目标

- 在不改后端 API、认证语义、现有 CRUD 行为和真实能力边界的前提下，继续优化 `frps/webui` 管理面板：移除冗余说明式内容，把管理登录页收简为极简认证入口；把分组管理与隧道管理合并为一张更高密度的管理页；参考 1Panel 调整管理壳层，让标签页贴住左侧边栏，并尽量把横向空间留给中间内容区。

## 子步骤

- 完成收束与验收：同步正式文档、执行构建检查、归档并清空当前轮 `todo`。

## 当前轮边界

- 本轮只做 `frps/webui` 前端信息架构、布局和交互表达调整，不改后端 API、认证算法、路由守卫语义、数据库 schema、控制协议或 CRUD 接口契约。
- 登录页虽然收简表现层，但仍保持现有 challenge 登录流程、初始化逻辑和 Cookie 会话模型不变。
- 合并页面已经完成并收口为当前统一接入管理页；后续壳层调整只继续优化导航与空间分配，不再回退到“分组页 / 隧道页”双主界面。
- 当前能力边界仍然只包含管理认证、分组 CRUD、隧道 CRUD、TCP/UDP 单端口与范围映射，不把未来功能包装成当前已实现能力。
- `todo` 只记录未完成项；后续每完成一个子步骤，立刻归档到 `docs/progress/`，检查 `.gitignore` 并提交当前稳定状态。

## 当前唯一下一步

- 先按当前代码实际同步正式文档，重点更新 `docs/project-overview.md`、`docs/frps/project-overview.md`、`docs/frps/functional-spec.md`、`docs/frps/development-guide.md` 与 `docs/frps/webui-redesign-plan.md` 中关于管理壳层、一级导航和统一接入管理入口的描述，然后完成最终验收、归档并清空当前轮 `todo`。
