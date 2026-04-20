# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

## 总目标

根据 `docs/frps/webui-proxy-groups-spec.md` 规范，重构接入管理页面，实现分组和隧道的完整 CRUD 功能。

## 子步骤

1. **API 层重构** ✅：重写 `src/api/index.ts`，对齐后端 API 形状（ProxyGroup、Tunnel 接口定义，返回值结构）

2. **ProxyGroupsView 重构** ✅：按双栏布局重写页面组件，左侧分组表格 + 右侧隧道表格 + 底部操作栏

3. **分组管理功能** ✅：实现新建、编辑、删除、重置 Token 的弹窗和操作逻辑

4. **隧道管理功能** ✅：实现新建、编辑、删除的抽窗和操作逻辑

5. **验证构建与效果** ✅：运行构建，启动 frps，验证 CRUD 功能完整可用

## 状态

**全部完成** ✅

## 当前轮边界

- 只做接入管理页面功能实现，不改其他页面
- 不改后端 API
- 不引入额外依赖
- UI 表达遵循 `docs/frps/webui-proxy-groups-spec.md`
