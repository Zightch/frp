# frps WebUI 当前基线

更新时间：2026-04-24

当前 WebUI 已经形成可用的最小管理闭环。本文档描述的是当前已落地状态，不覆盖仍在规划中的概览页设计。

## 1. 技术基线

- WebUI 仍作为 `frps/webui/` 下的独立前端目录维护。
- 开发和构建环境固定为 `Node.js + npm`。
- 前端框架固定为 `Vue 3`。
- UI 组件库固定为 `Element Plus`。
- 构建产物输出到 `frps/webui/dist/`，由 `frps` 按 `webui.dist_dir` 托管。
- 服务端会在运行时注入 WebUI 基址；默认挂在 `/`，配置 `webui.path_prefix` 后可整体挂到子路径。

## 2. 当前已实现页面

下面这些路由都相对 WebUI 基址生效；默认基址是 `/`，如果配置 `webui.path_prefix=/frps`，则整体变为 `/frps/...`。

| 路由 | 组件 | 说明 |
|------|------|------|
| `/` | - | WebUI 基址内的根路径重定向到 `GroupConfig` |
| `/init` | `InitView.vue` | 初始化管理密钥；如果已经初始化则跳转 `/login` |
| `/login` | `LoginView.vue` | challenge 登录；如果已登录则跳转 `GroupConfig` |
| `/proxy-groups` | `GroupConfigView.vue` | 主管理页；UI 名称为“分组配置” |

补充约定：

- 当前路由名是 `GroupConfig`。
- 认证页使用 `BlankLayout`，主管理页使用 `MainLayout`。
- UI 文案已改为“分组配置”，但路径仍保持 `/proxy-groups` 以兼容现有后端和入口。
- `docs/webui/dashboard.md` 仍作为后续概览页设计文档保留，不代表当前已接入路由。

## 3. 当前已实现功能

- 管理密钥初始化
  - 前端输入明文管理密钥后，先在浏览器内计算 `SHA-256`，再调用 `/auth/init`。
- challenge 登录
  - 前端先获取 challenge，再以 `sha256(key_hash + salt)` 生成 proof 登录。
- 会话状态检查与退出登录
  - 认证页和主管理页都会先检查 `/auth/state`。
- 分组管理
  - 支持列表、新建、编辑、删除、重置登录 `Key`。
  - 已接入 `effective_ip` 下拉、异常值回显和分组状态展示。
- 隧道管理
  - 支持按当前选中分组查看列表、新建、编辑、删除。
- 基础交互
  - 已实现加载态、空态、错误提示、删除确认和登录 `Key` 一次性展示。
- 前端表单校验
  - 分组名称必填，`effective_ip` 必选。
  - 隧道表单全部字段都在前端校验。
  - 端口必须为 `1-65535` 的整数。
  - 范围模式下结束端口必填，且开始端口必须小于结束端口。
  - 远端端口范围和本地端口范围的数量必须一致。
  - 端口相关字段在输入变化后会联动重验，不必等提交时才报错。

## 4. 当前界面结构

- 认证页使用 `BlankLayout`，全屏居中卡片展示。
- 主管理区使用顶部栏 + 侧边栏 + 主内容区布局。
- 当前侧边栏只有一个一级入口：“分组配置”。
- “分组配置”页由三块组成：
  - 页面标题和刷新按钮
  - 当前选中分组的操作栏
  - 左侧分组列表与右侧隧道列表的双栏内容区
- 分组列表和隧道列表都已经做了纵向撑满适配，底边会跟随浏览器可视高度变化。
- 当前主内容区设置了最小宽度，窄窗口下以内容区横向滚动为主，不等同于完整移动端适配。

## 5. 当前边界

- 不改后端 API、认证算法、Cookie 会话模型、数据库 schema 和控制协议。
- 不额外开放 `rate_limit` 等未进入真实运行时的持久化字段，也不承载抓包开关这类仅属于运行时的控制。
- 不单独拆出 `/tunnels` 页面；隧道管理继续收敛在“分组配置”页内。
- 概览页属于后续迭代项，当前不作为默认入口现状写入本页。

## 6. 代码组织

- 当前目录保持最小结构：`src/main.ts`、`src/router`、`src/api`、`src/runtime`、`src/layouts`、`src/views`。
- 业务状态主要收敛在页面组件内，暂未引入额外状态管理层。
- API 命名仍按后端契约保留 `proxyGroupsApi`、`ProxyGroup` 等名称。

## 7. 开发命令

- Node.js 环境建议使用 `20+`，npm 建议使用 `10+`。
- Windows PowerShell 下前端命令优先使用 `npm.cmd`。

```powershell
cd frps/webui
npm.cmd install
npm.cmd run dev
npm.cmd run build
```

后续如果扩展 WebUI 页面或默认入口，需要先同步更新本页和对应页面设计文档。
