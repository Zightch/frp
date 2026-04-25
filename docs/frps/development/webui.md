# frps WebUI 开发

## 当前技术栈

- Vue 3
- Element Plus
- Vite

## 当前目标

- 只围绕已进入真实后端链路的管理功能开发页面
- 不提前做壳层、概览页或未来功能导航
- 后端未落地的能力先写到 `docs/frps/design/`，不要直接做成页面事实

## 当前页面边界

当前 WebUI 主要承接：

- `/init`
- `/login`
- `/proxy-groups`
- 证书资产管理入口

## 命令

```powershell
cd frps/webui
npm.cmd install
npm.cmd run dev
npm.cmd run build
```

构建产物输出到 `frps/webui/dist/`，由 `frps` 按 `webui.dist_dir` 托管。

## 当前前端约束

- 路由和 API 基址要兼容 `webui.path_prefix`
- 新页面优先复用当前 API 模型，不提前引入状态管理层
- Element Plus 组件用法遵循 `docs/webui/` 下的文档，不自行发明新规则

优先参考：

- [docs/webui/overview.md](../../webui/overview.md)
- [docs/webui/style-guide.md](../../webui/style-guide.md)
- [docs/webui/certificate-assets.md](../../webui/certificate-assets.md)
