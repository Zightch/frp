# 管理面与 WebUI

## 管理认证

当前管理认证只依赖本地 `auth.json`，不依赖数据库管理员表。

当前管理认证接口：

- `GET /api/v1/auth/state`
- `POST /api/v1/auth/init`
- `POST /api/v1/auth/challenge`
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/takeover`
- `GET /api/v1/auth/session`
- `POST /api/v1/auth/logout`

当前固定语义：

- `auth.json` 不存在时，系统处于未初始化态。
- `POST /api/v1/auth/init` 只接受 `key_hash`，要求 `64` 位小写 hex。
- 登录流程正式收口为：`challenge -> proof -> 管理位判定 -> session / waiting`。
- `challenge -> proof` 通过后，不会立刻无条件进入管理面；服务端还会在签发管理会话前检查当前是否已有在线管理员。
- 如果当前无人占用，则签发管理会话并进入管理面。
- 如果当前已有管理员在线，则登录页只进入拒绝等待态，并允许新登录方显式选择顶掉当前管理员。
- 等待态会绑定一次性的 `pending_login_ticket` 和等待页看到的 `observed_generation`。
- 等待态页面不实时感知目标占用者是否已下线；顶掉按钮不会自动禁用、自动转成功，也不会把空位信息泄露给等待页。
- `POST /api/v1/auth/takeover` 只能替换等待页看到的那一版管理员占用；如果目标已下线或已经换人，则统一返回“页面已失效，请重新登录”。
- 这类拒绝不形成排队，不保留等待资格，也不会在当前管理员下线后自动给先前被拒绝的页面分配管理位。
- 当前管理员下线后，管理位直接回到空闲；旧等待页不能顺手抢空位，必须重新登录。
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
- `proxy_group` 列表、新建、编辑、删除、登录 `key` 重置
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
