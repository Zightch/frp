# frps 第一阶段框架

当前目录提供 `frps` 第一阶段的最小可运行框架，包含：

- `cmd/frps` 启动入口
- `internal/config` 基础配置加载与校验
- `internal/logging` 结构化日志
- `internal/api` 管理端 HTTP 服务与健康检查
- `internal/control` `7000` 控制端口监听骨架
- `internal/storage` SQLite/MySQL 统一数据库封装
- `data/config.json` 固定启动配置文件

## 运行

```powershell
cd frps
go build -o ./frps.exe ./cmd/frps
.\frps.exe
```

`frps` 启动时不需要任何参数，始终读取当前工作目录下的 `data/config.json`。因此在 Windows 下只要当前工作目录里存在 `data/config.json` 和 `webui/dist/`，直接启动 `frps.exe` 就能加载本地管理面。

本地默认配置建议如下：

```json
{
  "control_listen_addr": "0.0.0.0:7000",
  "management_listen_addr": "127.0.0.1:7500",
  "read_header_timeout": "5s",
  "shutdown_timeout": "10s",
  "database": {
    "type": "sqlite",
    "path": "./frps.db"
  },
  "webui": {
    "dist_dir": "../webui/dist"
  },
  "log": {
    "level": "info",
    "format": "text"
  }
}
```

管理端健康检查：

```powershell
curl http://127.0.0.1:7500/healthz
curl http://127.0.0.1:7500/readyz
curl http://127.0.0.1:7500/api/v1/healthz
```

如上配置使用 SQLite 时，数据库文件会落在 `data/frps.db`。如果切到 MySQL，则改为在 `data/config.json` 中配置 `database.type = "mysql"` 与 `database.dsn`。

如需切换到 MySQL，可在配置中设置：

```json
{
  "database": {
    "type": "mysql",
    "dsn": "frps:123456@tcp(staticplant.top:3306)/frps"
  },
  "webui": {
    "dist_dir": "../webui/dist"
  }
}
```

`data/config.json` 中的相对路径按配置文件所在目录解析，因此上面的 `./frps.db` 与 `../webui/dist` 都是相对于 `frps/data/` 生效。

应用启动时会在上层按配置打开数据库，并自动执行当前必需表的建表与严格校验。
当前第一阶段内置的核心表包括：

- `proxy_groups`
- `group_client_ip_rules`
- `group_tunnel_ip_rules`
- `tunnels`

管理认证设计不再依赖数据库 `admins` 表。服务启动时应先检查本地 `auth.json`；如果文件不存在，管理面只允许用户初始化管理密钥。`auth.json` 只保存管理密钥的 hash，不保存明文，后续 WebUI 登录通过一次性盐 challenge 校验 `sha256(key_hash + salt)`。如需重置管理密钥，删除本地 `auth.json` 即可让服务端自动回到未初始化态。

当前最小认证接口包括：

- `GET /api/v1/auth/state`
- `POST /api/v1/auth/init`
- `POST /api/v1/auth/challenge`
- `POST /api/v1/auth/login`
- `GET /api/v1/auth/session`
- `POST /api/v1/auth/logout`

管理 CRUD 接口已经要求有效管理会话。

## WebUI 开发

当前仓库已创建独立前端工程 `frps/webui/`，用于后续管理密钥初始化、challenge 登录以及分组/隧道管理页面迁移。

首次安装依赖：

```powershell
cd frps/webui
npm.cmd install
```

如当前 PowerShell 未限制脚本执行，也可以直接使用 `npm install`。

启动前端开发环境：

```powershell
cd frps/webui
npm.cmd run dev
```

Vite 默认通过开发代理把 `/api`、`/healthz`、`/readyz` 转发到 `http://127.0.0.1:7500`。如需改到其他管理端地址，可设置环境变量 `VITE_MANAGEMENT_API_TARGET`。

执行前端构建：

```powershell
cd frps/webui
npm.cmd run build
```

构建产物输出到 `frps/webui/dist/`。当前阶段 `frps` 管理端会直接读取配置中的 `webui.dist_dir` 并托管该目录，浏览器访问 `http://127.0.0.1:7500/` 即可进入初始化 / 登录页面。

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
