# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

## 总目标

搭建 `frps/webui` 最小框架，包含 Vue 3 + Element Plus 技术栈、最小目录结构、必要页面路由和基础 API 调用层，为后续业务页面开发提供稳定前端基线。

## 子步骤

1. **初始化项目结构**：创建 `package.json`、`vite.config.ts`、`tsconfig.json`、`index.html` 和最小入口文件，建立 `src/main.ts`、`src/App.vue`、`src/router/index.ts`、`src/api/index.ts`、`src/views/` 等最小目录。✅ 已完成

2. **配置 Element Plus 与基础样式**：引入 Element Plus 组件库和基础 CSS 变量，确保构建产物可被 `frps` 正确托管。✅ 已完成

3. **实现认证页面骨架**：创建 `/init` 初始化页和 `/login` 登录页的最小骨架，复用现有 `/api/v1/auth/*` 接口。✅ 已完成

4. **实现管理页面骨架**：创建 `/proxy-groups` 管理页骨架，包含分组列表、隧道列表的最小布局，复用现有 `/api/v1/proxy-groups` 和 `/api/v1/tunnels` 接口。

5. **验证构建与托管**：运行 `npm run build`，确认产物输出到 `dist/`，并验证 `frps` 可正确托管静态资源。

## 当前轮边界

- 只搭建前端框架和最小页面骨架，不实现完整 CRUD 逻辑。
- 不改后端 API、认证算法、数据库 schema 或控制协议。
- 不引入状态管理库（Pinia/Vuex），暂用组件内状态。
- 不添加日志页、监控页、连接管理页或占位导航。
- UI 表达遵循 `docs/frps/webui-redesign-plan.md` 最小基线约定。

## 当前唯一下一步

实现管理页面骨架：在 `src/views/ProxyGroupsView.vue` 中实现 `/proxy-groups` 管理页最小骨架，包含分组列表和隧道列表的最小布局，复用现有 `/api/v1/proxy-groups` 和 `/api/v1/tunnels` 接口。
