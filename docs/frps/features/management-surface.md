# 管理面与 WebUI

## 管理认证

当前管理认证只依赖本地 `auth.json`，不依赖数据库管理员表。

已实现接口：

- `GET /api/v1/auth/state`
- `POST /api/v1/auth/init`
- `POST /api/v1/auth/challenge`
- `POST /api/v1/auth/login`
- `GET /api/v1/auth/session`
- `POST /api/v1/auth/logout`

当前固定语义：

- `auth.json` 不存在时，系统处于未初始化态。
- `POST /api/v1/auth/init` 只接受 `key_hash`，要求 `64` 位小写 hex。
- 登录流程固定为 `challenge -> proof -> session`。
- challenge 只能使用一次。
- session 默认有效期 `12h`。
- 会话同时支持 Cookie 和 Bearer Token。
- 删除 `auth.json` 等价于重置管理认证；旧 challenge 和旧 session 一并失效。

## 管理 API 与 WebUI 托管

当前 `frps` 在同一个管理监听器上同时提供：

- 健康检查
- 管理 API
- WebUI 静态资源

当前配置：

- `management_listen_addr` 默认 `127.0.0.1:7080`
- `webui.dist_dir` 默认 `../webui/dist`
- `webui.path_prefix` 默认为空

`webui.path_prefix` 生效后：

- WebUI 入口整体挂到该前缀下
- 同前缀下同步暴露 `/healthz`、`/readyz` 和 `/api/v1/*`
- 根路径 API 仍继续可用

## WebUI 当前页面

当前页面路由都相对 WebUI 基址：

- `/init`
- `/login`
- `/proxy-groups`

WebUI 基址内的 `/` 会重定向到 `/proxy-groups`。

当前已落地功能：

- 管理密钥初始化
- challenge 登录与会话恢复
- 分组列表、新建、编辑、删除、登录 `key` 重置
- 隧道列表、新建、编辑、删除
- 证书资产列表、导入、粘贴、生成、详情、编辑名称/备注、删除、下载
- 入口证书绑定：`webui_https`、`frpc_tls`

WebUI 开发约束和 Element Plus 用法规范见：

- [frps WebUI 开发](../development/webui.md)
- [docs/webui/overview.md](../../webui/overview.md)
- [docs/webui/style-guide.md](../../webui/style-guide.md)

## 当前 HTTPS 边界

当前管理面已经支持通过证书绑定在同一监听地址上切换 HTTP / HTTPS。

- 绑定 `webui_https` 后，管理监听器会热切到 HTTPS。
- WebUI 证书页绑定成功后会自动跳转到同地址的 `https://`。
- 解绑 `webui_https` 后，管理监听器会切回 HTTP；当前页面也会自动回跳 `http://`。
- 仅切换证书内容时，HTTPS 模式下复用同一监听器，依赖运行时证书回调完成替换。

相关设计与实现边界见 [../design/certificate-binding.md](../design/certificate-binding.md)。
