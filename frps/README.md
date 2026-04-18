# frps 第一阶段框架

当前目录提供 `frps` 第一阶段的最小可运行框架，包含：

- `cmd/frps` 启动入口
- `internal/config` 基础配置加载与校验
- `internal/logging` 结构化日志
- `internal/api` 管理端 HTTP 服务与健康检查
- `internal/control` `7000` 控制端口监听骨架
- `internal/storage` SQLite/MySQL 统一数据库封装
- `configs/frps.json.example` 默认配置样例

## 运行

```powershell
cd frps
go run ./cmd/frps
```

使用自定义配置：

```powershell
cd frps
go run ./cmd/frps --config ./configs/frps.json.example
```

管理端健康检查：

```powershell
curl http://127.0.0.1:7500/healthz
curl http://127.0.0.1:7500/api/v1/healthz
```

数据库默认使用 SQLite 文件 `./data/frps.sqlite`。如需切换到 MySQL，可在配置中设置：

```json
{
  "database": {
    "type": "mysql",
    "dsn": "frps:123456@staticplant.top:3306/frps"
  }
}
```

应用启动时会在上层按配置打开数据库并注入 `internal/storage/sql.go` 的封装对象。
MySQL 支持简写地址格式，示例中的 `frps:123456@staticplant.top:3306/frps` 会被规范化后再交给驱动。
