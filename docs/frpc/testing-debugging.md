# frpc 测试与调试文档

## 1. 测试目标

`frpc` 的测试重点是：

- 能否稳定登录 `frps`
- 能否正确执行服务端下发的隧道配置
- 断线后能否自动恢复
- TCP/UDP 本地转发是否正确

## 2. 测试分层

## 2.1 单元测试

建议覆盖：

- 参数解析
- 配置版本替换
- 端口偏移计算
- 重连退避策略
- 会话状态机

命令：

```powershell
go test ./...
```

## 2.2 集成测试

建议覆盖：

- 连接真实或模拟 `frps`
- 登录成功和失败
- 配置下发
- TCP 转发
- UDP 转发
- 重连恢复

## 2.3 手工联调

当前最小联调优先使用已落地脚本：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_tcp_single.py --scenario bad_token
python test/e2e_tcp_single.py --scenario disabled_group
python test/e2e_tcp_single.py --scenario disabled_tunnel
python test/e2e_tcp_single.py --scenario local_unavailable
```

脚本会自动：

- 创建临时 SQLite 数据库
- 写入 `proxy_groups` / `tunnels`
- 启动真实 `frps` / `frpc`
- 启动 Python echo server 和 Python 外网客户端
- 断言最小控制面与数据面行为

如需手工联调，最小环境为：

- 一个本地 `frps`
- 一个本地 TCP echo 服务
- 一个本地 UDP echo 服务
- 一个 `frpc`

建议流程：

1. 启动 `frps`
2. 在 `frps` 中创建分组和隧道
3. 启动 `frpc`
4. 访问 `frps` 暴露的公网端口
5. 查看服务端在线状态和连接变化

## 3. 核心测试清单

## 3.1 启动参数

必须验证：

- 缺少 `server` 时启动失败
- 缺少 `token` 时启动失败
- 参数正确时启动连接

## 3.2 登录

必须验证：

- 正确 token 可登录
- 错误 token 被拒绝
- token 长度非法时启动失败
- challenge 过期或重放时能重新发起登录
- 被 `client` 黑白名单拦截时能收到明确失败
- 服务端版本不兼容时能提示

## 3.3 配置同步

必须验证：

- 首次配置可接收
- 配置版本变更后可更新
- 收到新配置后能回 `config.ack`
- 配置热更新时进程不重启
- 已删除隧道不再接受新流
- 已存在连接可自然结束

## 3.4 TCP

必须验证：

- 单端口映射正确
- 当前已落地：`happy_path`
- 端口范围偏移计算正确
- 当前已落地：本地目标不可达时正确上报失败
- 服务端要求关闭某个 stream 时，只关闭目标 stream

## 3.5 UDP

必须验证：

- 数据报可正常来回转发
- 会话闲置后自动超时
- 会话回收后可重新建立

## 3.6 重连

必须验证：

- `frps` 重启后可恢复
- 短暂网络断开后可恢复
- 重连成功后会重新同步配置

## 4. 调试建议

## 4.1 日志调试

推荐设置：

```powershell
$env:FRPC_LOG_LEVEL="debug"
```

重点关注日志：

- 登录请求和响应
- 心跳发送
- 配置版本切换
- stream 打开与关闭
- 本地拨号失败
- 重连退避

## 4.2 本地服务调试

本地 TCP 服务可用简单 echo server。

本地 HTTP 服务可用：

```powershell
python -m http.server 8081
```

然后通过 `frps` 暴露端口访问，确认 `frpc` 是否正确把流量送到本地。

如果问题出在最小端到端链路，优先使用：

```powershell
python test/e2e_tcp_single.py --scenario happy_path --keep-temp
```

然后查看脚本输出的 `db_path`、`frps.log` 和 `frpc.log`。

## 4.3 网络调试

建议检查：

- 到 `server:7000` 的网络是否通
- 本地目标服务是否在监听
- Windows 防火墙是否拦截本地回环或外部端口

可用命令：

```powershell
Test-NetConnection 127.0.0.1 -Port 7000
Test-NetConnection 127.0.0.1 -Port 8080
```

## 5. 故障排查

## 5.1 无法登录

优先排查：

- `server` 地址是否正确
- token 是否正确
- token 是否符合固定长度拼接格式
- 分组是否启用
- `frpc` 来源 IP 是否被服务端黑白名单拦截

## 5.2 已登录但无法转发

优先排查：

- 是否已经收到隧道配置
- 本地目标地址是否可达
- `frps` 公网入口是否有请求进入
- 服务端隧道入口 ACL 是否拦截

## 5.3 频繁重连

优先排查：

- 网络抖动
- 服务端是否主动踢下线
- 心跳周期是否异常
- 协议解析错误

## 6. 性能测试建议

建议检查：

- 单连接大流量转发稳定性
- 多并发 stream 下内存占用
- 高频配置更新时是否出现异常抖动
- 高频配置热更新时是否出现 goroutine 或 socket 泄漏
