# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

## 总目标

为 `frps/webui` 新增仪表盘页面 `/dashboard`，在现有 API 能力范围内展示管理密钥状态、会话信息、分组与隧道统计概览，提升首次登录后的信息获取效率。

## 子步骤

1. ~~**更新设计文档**：在 `docs/frps/webui-redesign-plan.md` 中补充仪表盘页面的路由位置、信息架构和边界约定~~ **已完成**
   - 设计文档已迁移至 `docs/webui/` 目录
   - 仪表盘设计文档已创建：`docs/webui/dashboard.md`

2. **路由与导航调整**：修改 `src/router/index.ts` 增加 `/dashboard` 路由，调整 `MainLayout.vue` 导航项顺序，将仪表盘作为首项

3. **实现 DashboardView**：创建新页面组件，调用现有 API 获取认证状态、分组列表、隧道列表，展示统计卡片和概览信息

4. **构建验证**：运行 `npm run build`，启动 frps 验证页面渲染正常，数据加载正确

## 当前轮边界

- 只做信息展示，不新增后端 API
- 不引入图表库或实时数据功能
- 统计数据来源于现有 `/proxy-groups` 和 `/tunnels` 列表接口
- `/` 重定向改为 `/dashboard`，`/proxy-groups` 保持为一级导航入口
- 遵循现有 Element Plus 视觉风格，复用 `main.css` 变量

## 当前唯一下一步

修改 `src/router/index.ts` 增加 `/dashboard` 路由，调整 `MainLayout.vue` 导航项顺序，将仪表盘作为首项。
