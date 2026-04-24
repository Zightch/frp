# frps 项目概览

## 1. 当前职责

`frps` 是当前仓库的服务端核心，实际承担下面几类职责：

- 读取本地 `data/config.json` 并启动服务。
- 打开 SQLite/MySQL，自动建当前必需表并校验表结构。
- 托管 WebUI 静态资源和管理 API。
- 管理本地 `auth.json`，提供初始化、challenge 登录、会话校验和删除后的自动复位。
- 接收 `frpc` 控制连接，完成基于 `client_id` 的 challenge/response 登录。
- 为每个分组维护单客户端槽位。
- 在 `config.ack` 后按隧道配置启动 TCP/UDP 公网 listener。
- 把公网 TCP/UDP 流量桥接到在线 `frpc`。
- 在分组在线且运行态配置变更时，触发整组 `config.push / config.ack` 热重载。
- 统一裁决 UDP session 生命周期和空闲清理。

## 2. 当前已交付范围

截至 2026-04-24，`frps` 已经具备：

- 健康检查：
  - `GET /healthz`
  - `GET /readyz`
  - `GET /api/v1/healthz`
- 管理认证：
  - `GET /api/v1/auth/state`
  - `POST /api/v1/auth/init`
  - `POST /api/v1/auth/challenge`
  - `POST /api/v1/auth/login`
  - `GET /api/v1/auth/session`
  - `POST /api/v1/auth/logout`
- 分组管理：
  - `GET /api/v1/proxy-groups`
  - `POST /api/v1/proxy-groups`
  - `PATCH /api/v1/proxy-groups/{id}`
  - `DELETE /api/v1/proxy-groups/{id}`
  - `POST /api/v1/proxy-groups/{id}/key`
  - `GET /api/v1/local-ips`
- 隧道管理：
  - `GET /api/v1/tunnels`
  - `POST /api/v1/tunnels`
  - `PATCH /api/v1/tunnels/{id}`
  - `DELETE /api/v1/tunnels/{id}`
- 管理 WebUI 与静态托管：
  - `frps` 按 `webui.dist_dir` 托管 `frps/webui/dist/`
  - 技术栈：`Node.js + Vue 3 + Element Plus`
  - 页面路由：`/init`、`/login`、`/proxy-groups`、`/` 重定向到 `/proxy-groups`
  - 已实现：管理密钥初始化、challenge 登录、分组 CRUD、登录 `key` 轮转、隧道 CRUD、`effective_ip` 下拉与状态展示
- 控制面：
  - `client_id` challenge/response 登录
  - 心跳
  - 首次 `config.push` / `config.ack`
  - 在线整组热重载
  - 单分组单客户端槽位
- 数据面：
  - TCP 单端口转发
  - TCP 连续范围映射
  - UDP 单端口转发
  - UDP 连续范围映射
  - `frps` 侧 UDP idle cleanup

## 3. 当前明确边界

下面这些内容还不属于 `frps` 的已完成范围：

- 反向代理。
- 隧道入口 ACL 执行。
- WebSocket、连接管理、实时速率统计、日志中心。
- 限速执行、抓包执行。
- 多客户端分组。

当前配置明确分成两层：

- 持久化配置：管理 API / WebUI 写入 `proxy_groups`、`tunnels` 表。
- 运行时配置：`internal/control/repository.go` 在 `frpc` 登录或在线热重载时读取持久化配置，构造 `GroupRuntime` / `ConfigSnapshot`，后续 listener 启停和数据转发都只消费这份内存快照。

当前 schema 中仍保留的扩展持久化字段只有 `proxy_groups.rate_limit`；其语义已收口为分组下所有隧道共享总限速，但运行时仍未消费。抓包相关控制当前未实现；如果后续引入，只应属于运行时配置，不应再持久化到 `tunnels` 表。

## 4. 当前子系统

```text
frps/
├── cmd/frps/            # 启动入口
├── internal/api/        # 管理 API 与 WebUI 托管
├── internal/app/        # 数据库打开、schema 校验、服务编排
├── internal/auth/       # auth.json 管理认证
├── internal/config/     # 配置加载与校验
├── internal/control/    # frpc 控制面与 TCP/UDP 数据面
├── internal/logging/    # 日志初始化
├── internal/storage/    # SQL 包装与驱动注册
├── pkg/protocol/        # 业务协议
├── pkg/transport/       # 长度前缀传输层
└── webui/               # WebUI 前端目录（Node.js + Vue 3 + Element Plus）
```

## 5. 当前外部接口

### 5.1 监听端口

- `control_listen_addr`：默认 `0.0.0.0:7000`
- `management_listen_addr`：默认 `127.0.0.1:7080`
- 动态 TCP/UDP 公网端口：由 `tunnels` 配置展开

### 5.2 管理认证

- 管理会话可通过 `frps_management_session` Cookie 传递。
- 也支持 `Authorization: Bearer <token>`。
- `auth.json` 默认路径是 `./data/auth.json`。
- 删除 `auth.json` 后，认证管理器会在删除检测周期内自动回到未初始化态，并清空旧 challenge 和旧 session。

### 5.3 控制面

- `frpc` 登录第一步只发送 `client_id`。
- `frps` 在数据库中按 `client_id` 定位分组，读取 `client_secret_hash` 做 challenge 校验。
- 管理面运行态字段写库成功且分组在线时，会触发 `RefreshGroup()`，复用现有 `config.push / config.ack` 推进整组热重载。

## 6. 当前核心数据

`frps` 当前依赖的业务表只有 2 张：

- `proxy_groups`
- `tunnels`

其中：

- `proxy_groups` 当前核心认证列为 `client_id`、`client_secret_hash`、`effective_ip`、`enabled`。
- `tunnels` 当前只支持 `tcp` / `udp` 正向代理。

## 7. 启动与关闭

当前启动顺序固定为：

1. 读取 `data/config.json`
2. 初始化日志
3. 初始化 `auth.Manager`
4. 打开数据库并校验 schema
5. 启动管理 API
6. 启动 `frpc` 控制监听器

关闭时依次收口：

1. 管理 API
2. 控制监听器和活动连接
3. 数据库
4. `auth.Manager`

## 8. 当前验证

当前 `frps` 已通过：

- `go test ./...`（`frps/`）
- `python test/e2e_tcp_single.py`
- `python test/e2e_tcp_range.py`
- `python test/e2e_udp_single.py`
- `python test/e2e_udp_range.py`
- `python test/e2e_management_webui.py`
- `python test/e2e_tcp_perf.py`

更细的开发和测试入口见：

- [frps 技术设计](technical-design.md)
- [frps 开发文档](development-guide.md)
- [frps 测试与调试文档](testing-debugging.md)
