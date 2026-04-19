# frps 第一阶段框架

当前目录提供 `frps` 第一阶段的最小可运行框架，包含：

- `cmd/frps` 启动入口
- `internal/config` 基础配置加载与校验
- `internal/logging` 结构化日志
- `internal/api` 管理端 HTTP 服务与健康检查
- `internal/control` `7000` 控制端口监听骨架
- `internal/storage` SQLite/MySQL 统一数据库封装
- `configs/frps.json` 默认配置样例

## 运行

```powershell
cd frps
go run ./cmd/frps
```

使用自定义配置：

```powershell
cd frps
go run ./cmd/frps --config ./configs/frps.json
```

管理端健康检查：

```powershell
curl http://127.0.0.1:7500/healthz
curl http://127.0.0.1:7500/readyz
curl http://127.0.0.1:7500/api/v1/healthz
```

数据库默认使用 SQLite 文件 `./data/frps.sqlite`。如需切换到 MySQL，可在配置中设置：

```json
{
  "database": {
    "type": "mysql",
    "dsn": "frps:123456@tcp(staticplant.top:3306)/frps"
  }
}
```

应用启动时会在上层按配置打开数据库，并自动执行当前必需表的建表与严格校验。
当前第一阶段内置的核心表包括：

- `proxy_groups`
- `group_client_ip_rules`
- `group_tunnel_ip_rules`
- `tunnels`

管理认证设计不再依赖数据库 `admins` 表。服务启动时应先检查本地 `auth.json`；如果文件不存在，管理面只允许用户初始化管理密钥。`auth.json` 只保存管理密钥的 hash，不保存明文，后续 WebUI 登录通过一次性盐 challenge 校验 `sha256(key_hash + salt)`。

空库会在启动时自动创建当前必需表；如果现有 SQLite/MySQL 表结构与服务内置 schema 不一致，`frps` 会直接退出，避免带着错误库结构继续运行。当前开发阶段不做数据库 schema 兼容或自动迁移。
`internal/storage/sql.go` 仍只负责统一封装数据库对象，不承载驱动打开和 schema 迁移逻辑。
当前 `sql.go` 已支持：

- 基于已有 `*sql.DB` 的统一包装
- `Query` / `QueryOne` / `Exec` / `Execute` 结果统一返回
- 按列名返回 `map[string]any` 形式的行数据
- `Begin` / `BeginTx` / `WithTx` / `WithTxContext` 事务封装

数据库职责边界固定为：

- `internal/storage/sql.go`：数据库对象和事务对象封装
- `internal/storage/drivers`：驱动注册
- `internal/app/database.go`：数据库打开、DSN 处理、连通性检查
- `internal/app/schema.go`：当前必需表的建表与校验

当前开发阶段也不做 `frps/frpc` 版本兼容协商；如果协议或数据结构有破坏性调整，按同仓代码、测试、文档和本地开发数据一起同步更新处理。

当前 `/readyz` 与 `/healthz` 等价，都是最小健康检查；后续如果引入更细的运行态就再扩展 readiness 语义。
MySQL 仍支持简写地址格式，例如 `frps:123456@staticplant.top:3306/frps` 会被规范化后再交给驱动。
