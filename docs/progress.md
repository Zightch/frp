# 当前进度

本文档用于单独记录项目实现进度和当前活动边界。长期设计仍以 `project-overview.md`、`frps/` 和 `frpc/` 下的正式设计文档为准。

更新时间：2026-04-18

## 1. 当前已完成

上一轮最小正向代理联调已经完成并收口。

已跑通的目标链路：

```text
Python 外网客户端
<-> frps remote_port
<-> frps / frpc 真实控制面与数据面
<-> frpc 本地 TCP 回连
<-> Python 内网主机
```

当前已落地能力：

- `frps` / `frpc` 支持 token challenge/response 登录。
- `frps` 可从 SQLite/MySQL schema 中读取 `proxy_groups` / `tunnels` 运行数据。
- `frps` 在收到 `config.ack` 后启动启用状态的 TCP 单端口 listener。
- `frpc` 可接收 `config.push`，返回 `config.ack`，并处理 `stream.open` / `stream.data` / `stream.close`。
- `frpc` 可回连本地 TCP 目标，并把公网侧 TCP 字节流转发到内网目标。
- 数据库支持 SQLite/MySQL、统一 `storage.SQL` 封装、事务封装、启动期 schema bootstrap 和严格校验。

当前端到端脚本：

- `test/e2e_tcp_single.py`

该脚本只负责测试编排和场景构造，不实现产品逻辑。

## 2. 已验证场景

当前固定保留 5 个最小场景：

- `happy_path`
- `bad_token`
- `disabled_group`
- `disabled_tunnel`
- `local_unavailable`

这些场景覆盖的是单客户端、冷启动、SQLite 预注入、TCP 单端口的最小闭环。

已经验证过的关键行为：

- `config.ack` 之前不会启动公网 TCP 监听。
- session 结束后会关闭已启动的 tunnel listener。
- 只有启用的单端口 TCP 隧道会启动 listener。
- `frpc` 收到未知 `tunnelId` 的 `stream.open` 会返回明确错误。
- `bad_token` 会被真实 `frps` / `frpc` challenge 校验拒绝。
- `disabled_group` 会由真实 `frps` 按数据库分组状态拒绝登录。
- `disabled_tunnel` 会完成登录和配置确认，但不会启动 remote listener。
- `local_unavailable` 会触发真实 `stream.open`，随后因本地目标不可达关闭公网连接，不返回伪造 echo。

## 3. Token 与数据库边界

测试程序可以直接向数据库插入现成的分组和隧道信息。

分组 token 的当前边界：

- `frpc` 启动参数中的 token = `token_id` + `token_secret`。
- 数据库只保存 `token_id` 和 `token_hash`。
- 数据库不保存 `token_secret` 明文。
- `token_hash = sha256(token_secret)`。
- 登录阶段的一次性 `challenge nonce` 由 `frps` 下发。
- 登录响应由真实 `frps` / `frpc` 按 challenge 机制完成校验。

因此，测试脚本保留原始 token 并传给 `frpc` 是正确的；数据库中只有 hash 也是正确的。

## 4. 错误输出增强

当前脚本已经补齐最小必要失败摘要输出。

失败时会输出：

- 当前 `scenario`
- 分配到的端口信息
- 临时 SQLite DB 路径
- `frps` / `frpc` 日志
- 当前场景的期望结果摘要
- 当前场景的实际结果摘要

`local_unavailable` 还会保留外网客户端观测摘要：

- `send_error`
- `response_len`
- `response_preview`
- `recv_error`
- `connection_closed`
- `timed_out`

实现原则仍然是极简核心优先：只补必要诊断信息，不引入日志框架、测试 DSL 或额外模块拆分。

## 5. 当前停止线

上一轮最小联调已经达到当前停止线。

当前不继续扩展：

- 测试矩阵
- Python 脚本模块拆分
- 额外日志框架
- 额外状态机
- 额外测试 DSL

当前也不把以下能力并入已完成主线：

- 在线改库热更新
- ACL
- 多客户端 / `max_clients`
- 同 token 顶号 / 防顶号
- 反向代理
- UDP
- 端口范围
- 抓包
- 限速

## 6. 下一轮目标

下一轮目标已经收束为极简 WebUI。

目标：

- 没有登录功能。
- 只有分组编辑和隧道编辑。
- 完成从 WebUI 对数据库的增删改查。
- WebUI 调用 `frps management api`。
- `frps` 负责实际读写数据库。
- WebUI 不直接连接 SQLite 文件。

首版页面范围：

- 分组列表
- 分组新增 / 编辑 / 删除
- 分组 token 创建或重置
- 隧道列表
- 隧道新增 / 编辑 / 删除

首版实现边界：

- 不引入独立前端工程。
- 不引入 Node、Vue、Element 或打包链。
- 不做登录。
- 不做管理员表和管理员体系。
- 不做 WebSocket。
- 不做在线热更新。
- 不做连接表、统计图、日志页。
- 不做 IP 规则编辑。
- 不做 ACL、限速、抓包、反向代理配置页。

## 7. 下一步编码入口

如果开始下一轮编码，第一步应只做最小管理面 CRUD：

- 在 `frps management api` 增加 `proxy_groups` / `tunnels` 最小 CRUD JSON API。
- 根路径或单一路径返回一个内嵌静态 HTML 页面。
- 页面用最简单的 JS 调用这些 API。

编码时继续保持：

- 不动 `frpc` CLI。
- 不动 Python e2e 主线。
- 不把热更新、登录、管理员体系混进这一轮。
- 不因为“未来可能需要”提前引入复杂抽象。
