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

### 5.1 安装依赖

```powershell
cd frps/webui
npm.cmd install
```

### 5.2 开发模式

```powershell
cd frps/webui
npm.cmd run dev
```

Vite 开发代理默认转发到 `http://127.0.0.1:7500`。

### 5.3 构建

```powershell
cd frps/webui
npm.cmd run build
```

构建产物输出到 `frps/webui/dist/`，由 `frps` 按 `webui.dist_dir` 直接托管。

### 5.4 当前页面组织

- `webui/src/router/index.ts`
  - 路由守卫、页面标题和管理态导航元数据都在这里收口；一级导航现在只保留“概览”“接入管理”，`/tunnels` 兼容高亮也通过这里的 `managementNavActiveName` 收口。
- `webui/src/views/AppShellView.vue`
  - 已登录管理壳层，只负责侧边导航、薄顶栏会话状态、全局提示和页面容器；不承接分组或隧道业务逻辑。
- `webui/src/views/AccessManagementView.vue`
  - 统一接入管理主页面，上半区负责分组 CRUD 与 token 重置，下半区只负责当前选中分组下的隧道 CRUD。
- `webui/src/views/ProxyGroupsView.vue` / `webui/src/views/TunnelsView.vue`
  - 当前都只是兼容包装层，统一复用 `AccessManagementView.vue`；不要再在这两个文件里分叉出第二套业务界面。
- `webui/src/views/InitSecretView.vue`
  - 未初始化态认证页，只负责一次性初始化管理密钥。
- `webui/src/views/LoginView.vue`
  - 极简 challenge 登录页，只负责建立管理会话和跳回受保护页。
- `webui/src/styles/main.css`
  - 统一维护 design tokens、管理壳层、概览页、接入管理页和认证页样式。

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
- `webui/src/api/management.ts`
- `webui/src/views/AccessManagementView.vue`

当前管理 API 只覆盖分组和隧道最小字段；新增管理字段时要先确认运行时是否真的消费。改动接入管理页时，还要保持“当前选中分组”这一单一业务上下文，不要把页面重新拆回两套独立主界面。

### 6.3 控制面或数据面相关

涉及文件：

- `internal/control/*`
- `pkg/protocol/*`
- `pkg/transport/*`

当前稳定边界：

- `config.push` 只在登录阶段发送
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
- 如果改了 `webui` 路由、管理壳层、认证页或接入管理页，运行 `npm.cmd run build`（`frps/webui/`）
- 文档是否同步到真实实现边界

完整回归入口见 [../regression-entry.md](../regression-entry.md)。
