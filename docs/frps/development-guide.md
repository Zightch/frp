# frps 开发文档

## 1. 开发原则

`frps` 当前仍按“极简核心”推进，开发时遵守下面几条：

- 只写当前真实执行链路需要的代码。
- 不把未来规划写成当前实现。
- 不引入事件总线、插件系统、统一运行时框架这类提前抽象。
- 字段一旦确认无用，要一起从 schema、仓储、API、WebUI、测试和文档里清理。
- 破坏性调整当前默认按同仓同步升级处理，不维护兼容层。

统一工作流见 [../workflow.md](../workflow.md)。

## 2. 当前目录

```text
frps/
├── cmd/frps/
├── internal/api/
├── internal/app/
├── internal/auth/
├── internal/config/
├── internal/control/
├── internal/logging/
├── internal/storage/
├── pkg/
├── webui/
└── data/
```

## 3. 环境要求

- Go 1.23 或更高
- Node.js 20 或更高
- npm 10 或更高
- SQLite 3 或 MySQL 8

Windows PowerShell 如果受执行策略影响，前端命令优先使用 `npm.cmd`。

## 4. 本地运行

### 4.1 服务端

```powershell
cd frps
go build -o ./frps.exe ./cmd/frps
.\frps.exe
```

`frps` 不接收启动参数，固定读取：

```text
frps/data/config.json
```

### 4.2 当前默认配置

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

相对路径按 `config.json` 所在目录解析。

### 4.3 MySQL

当前 MySQL 只能使用驱动标准 DSN，例如：

```json
{
  "database": {
    "type": "mysql",
    "dsn": "frps:123456@tcp(127.0.0.1:3306)/frps"
  }
}
```

## 5. WebUI 开发

当前 `frps/webui/` 只保留重建基线，不再沿用旧版页面结构。当前固定约定如下：

### 5.1 技术栈

- Node.js `20+`
- npm `10+`
- Vue 3
- Element Plus

### 5.2 当前目标

- 只先恢复最小管理前端。
- 必要功能只保留管理密钥初始化、challenge 登录、分组 CRUD、token 重置和隧道 CRUD。
- 不先做复杂壳层、额外概览页或未来功能导航。

### 5.3 目录约定

- `frps/webui/` 作为独立前端目录。
- 当前目录如果为空，先补最小 `package.json`、基础入口和必要页面。
- 推荐最小结构只有 `src/main`、`src/router`、`src/api`、`src/views`。
- 只有在确实复用时，再补 `src/components` 或状态管理层。

### 5.4 前端命令约定

前端工程重建完成后，统一使用：

```powershell
cd frps/webui
npm.cmd install
npm.cmd run dev
npm.cmd run build
```

构建产物仍输出到 `frps/webui/dist/`，由 `frps` 按 `webui.dist_dir` 直接托管；如果目录或 `index.html` 缺失，服务会自动回退到内置占位页，不阻塞管理 API 启动。

## 6. 当前修改落点

### 6.1 管理认证相关

涉及文件：

- `internal/auth/manager.go`
- `internal/api/auth.go`

需要同步关注：

- `auth.json` 初始化
- challenge TTL
- session TTL
- 删除检测语义

### 6.2 管理 CRUD 相关

涉及文件：

- `internal/api/management.go`
- `internal/app/schema.go`
- `internal/control/repository.go`
- `webui/src/api/*`
- `webui/src/views/*`

当前管理 API 只覆盖分组和隧道最小字段；新增管理字段时要先确认运行时是否真的消费。WebUI 重建时也只围绕最小必要工作流组织页面，不要先扩成多套壳层或未来功能矩阵。

### 6.3 控制面或数据面相关

涉及文件：

- `internal/control/*`
- `pkg/protocol/*`
- `pkg/transport/*`

当前稳定边界：

- 首次登录和后续在线热重载都复用整组 `config.push / config.ack` 路径
- listener 在 `config.ack` 后才启动
- UDP 生命周期由 `frps` 裁决

## 7. 当前最常用命令

```powershell
cd frps
go test ./...
go build -o NUL ./cmd/frps
```

```powershell
curl http://127.0.0.1:7500/healthz
curl http://127.0.0.1:7500/api/v1/auth/state
```

## 8. 提交前检查

改动 `frps` 时，至少检查：

- `go test ./...`（`frps/`）
- 如果改了控制面或数据面，运行相关 Python e2e
- 如果改了管理认证或管理 API，运行 `python test/e2e_management_webui.py`
- 如果新建或修改了 `frps/webui/` 前端工程，运行该工程当前 `package.json` 定义的最小构建命令
- 文档是否同步到真实实现边界

完整回归入口见 [../regression-entry.md](../regression-entry.md)。
