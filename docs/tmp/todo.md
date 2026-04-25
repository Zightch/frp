# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 落地 `frps` 的入口级证书绑定能力，先覆盖两条链路：
  - WebUI 选中证书后切到 HTTPS，并让前端自动跳转到新的 `https://` 基址
- `frpc` 登录监听口支持分组级 TLS 策略：`frpc` 先发送 `hello + client_id`，`frps` 按分组决定明文还是 TLS，再进入现有登录会话

子步骤：

- 数据模型与后端接口：
  - 为入口证书绑定补 usage 模型与管理接口
- 为 `proxy_groups` 补分组级 `frpc` 登录传输安全策略字段
  - 收口删除约束、绑定约束和运行时装配边界
- WebUI HTTPS：
  - 管理面支持绑定 `webui_https`
  - `frps` 管理监听器支持按证书切到 HTTPS
  - WebUI 在绑定成功后自动跳转到新的 `https://host:port/...`
- `frpc` 登录组级 TLS：
  - 协议增加 `client_hello / server_hello`
  - `client_hello` 必须前置携带 `client_id`
  - `frps` 按分组的 `control_transport_security` 返回 `plain` 或 `tls_required`
  - 若要求 TLS，则在同一连接上完成 TLS 握手后再进入 `auth.begin -> auth.challenge -> auth.finish -> server.hello -> config.push`
- 回归与收口：
  - 补齐后端、控制面、客户端和 WebUI 的最小验证
  - 同步正式文档与进度归档

当前轮边界：

- 当前只承接 WebUI HTTPS 与 `frpc` 登录组级 TLS，不扩到 tunnel 级证书、SNI 多证书或 mTLS。
- `frpc` 登录监听器的服务端证书仍是全局监听器级；“某个分组是否强制 TLS”单独作为分组字段管理。
- 当前协议已经收口为：
  - `frpc -> hello + client_id`
  - `frps -> hello + 是否 TLS`
  - 若要求 TLS，则先握手，再进入现有登录会话
- 当前这一步只更新 `todo` 文档，不动代码。

当前结果：

- 本轮总目标已完成。
- 后端已落地 `certificate_asset_usages`、`proxy_groups.control_transport_security`、WebUI HTTPS 热切换、`frpc` 登录 hello 协商与组级 TLS。
- WebUI 已落地证书绑定、组级 TLS 配置和协议跳转。
- 自动化验证已覆盖：
  - `go test ./...`（`frps/`、`frpc/`）
  - `npm run build`（`frps/webui/`）

本文件保留为当轮完成记录，后续若有新目标再重写。
