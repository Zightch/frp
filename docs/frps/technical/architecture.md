# frps 架构与模块

## 进程内分层

```text
cmd/frps
  |
internal/app
  |
+----------------------+----------------------+------------------+
|                      |                      |                  |
internal/api           internal/control       internal/auth      internal/certassets
  |                      |                      |                  |
WebUI/API              frpc protocol         auth.json          asset runtime
  |                      |
internal/storage <------+
  |
SQLite / MySQL
```

## 启动顺序

当前 `app.App.Run()` 固定顺序：

1. 初始化 `auth.Manager`
2. 打开数据库并校验 schema
3. 准备证书资产运行时
4. 初始化本机网络快照服务
5. 创建 control server
6. 执行首轮 runtime scan
7. 创建 API server
8. 并发启动管理监听器和控制监听器

## 关闭顺序

当前关闭顺序：

1. 管理 API
2. 控制监听器与活动连接
3. 本机网络快照服务
4. 证书资产运行时
5. 数据库
6. `auth.Manager`

## 模块 ownership

### `internal/config`

- 默认值
- JSON 配置读取
- 监听地址、超时、日志格式、数据库配置校验
- 相对路径解析

### `internal/auth`

- `auth.json`
- 管理 challenge
- 管理 session
- 删除 `auth.json` 后自动复位

### `internal/api`

- HTTP server
- 路由注册
- 管理会话中间件
- 分组 / 隧道 / 证书资产 API
- WebUI 静态资源托管

### `internal/control`

- 根包只保留 app/api 入口 facade
- `protocol` 负责控制连接握手、认证、frame IO、错误回复和配置同步协议语义
- `session` 负责纯会话状态机、agent、supervisor 和 group slot
- `runtime` 负责 listener、TCP/UDP 数据面、runtime scan、恢复、issue 和观测投影
- `domain/runtime` 负责 `GroupRuntime`、`ConfigSnapshot` 和 desired/applied 转换
- `repo` 负责 SQL row 到运行时模型的投影
- `wiring` 负责业务拼装、运行态刷新和白盒场景测试

### `internal/certassets`

- 证书资产仓储与服务
- PEM 解析
- 上传与生成校验
- 系统 CA 与数据库资产准备
- 下载产物组装

### `internal/storage`

- SQL 抽象
- 事务包装
- SQLite / MySQL 驱动注册

## 当前扩展边界

- 没有事件总线
- 没有插件化数据面
- 没有统一运行时框架
- 没有独立证书绑定层；该层见 [../design/certificate-binding.md](../design/certificate-binding.md)
