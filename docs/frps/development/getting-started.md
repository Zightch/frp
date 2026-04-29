# frps 开发入口

统一开发原则见 [../../development-principles.md](../../development-principles.md)。

## 环境要求

- Go `1.23+`
- Node.js `20+`
- npm `10+`
- SQLite 3 或 MySQL 8

Windows PowerShell 如果受执行策略影响，前端命令优先用 `npm.cmd`。

## 本地运行

```powershell
cd frps
go build -o ./frps.exe ./cmd/frps
.\frps.exe
```

`frps` 不接收启动参数，固定读取：

```text
frps/data/config.json
```

## 当前默认配置

```json
{
  "control_listen_addr": "0.0.0.0:7000",
  "management_listen_addr": "127.0.0.1:7080",
  "read_header_timeout": "5s",
  "shutdown_timeout": "10s",
  "database": {
    "type": "sqlite",
    "path": "./frps.db"
  },
  "webui": {
    "dist_dir": "../webui/dist",
    "path_prefix": ""
  },
  "log": {
    "level": "info",
    "format": "text"
  }
}
```

相对路径按 `config.json` 所在目录解析。

## MySQL

当前 MySQL 只接受驱动标准 DSN，例如：

```json
{
  "database": {
    "type": "mysql",
    "dsn": "frps:123456@tcp(127.0.0.1:3306)/frps"
  }
}
```

## 常用命令

```powershell
cd frps
go test ./...
go build -o NUL ./cmd/frps
```

```powershell
curl http://127.0.0.1:7080/healthz
curl http://127.0.0.1:7080/api/v1/auth/state
```

## 提交前检查

- 改了 `frps` 后端：`go test ./...`
- 改了控制面或数据面：补跑相关 Python e2e
- 改了管理认证、管理 API 或 WebUI：补跑 `python test/e2e_management_webui.py`
- 改了前端：补跑 `npm.cmd run build`
- 文档要与真实实现同步

更多开发说明见：

- [WebUI 开发](./webui.md)
- [测试与调试](./testing-debugging.md)
