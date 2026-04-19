# frpc 项目概览

## 1. 项目定位

`frpc` 是本平台的正向代理客户端，只负责连接 `frps`、通过 token 领取配置、执行本地 TCP/UDP 转发。

它的设计目标是极简：

- 业务启动参数只需要 `server` 和 `token`
- 不提供本地 WebUI
- 不维护复杂配置文件
- 不承载管理逻辑

## 1.1 当前实现状态

截至 2026-04-19，`frpc` 当前已落地：

- 只接受 `server` 和 `token` 两个启动参数
- 按固定长度拆分 `token_id` / `token_secret`
- challenge/response 登录
- 接收 `config.push` 并返回 `config.ack`
- 最小 TCP 单端口 `stream.open` / `stream.data` / `stream.close`
- 最小 UDP 单端口 `udp.open` / `udp.data` / `udp.close`
- 真实本地 UDP 转发与会话回收
- 收到 `udp.close` 后释放本地 UDP session，不做本地 idle timer
- 本地拨号失败时返回确定性错误
- 已通过 `test/e2e_tcp_single.py` 与真实 `frps` 打通：
  - `Python 外网客户端 <-> frps <-> frpc <-> Python 内网主机`
- 已通过 `test/e2e_udp_single.py` 与真实 `frps` 打通：
  - `Python 外网 UDP 客户端 <-> frps <-> frpc <-> Python 内网 UDP 服务`
  - `happy_path`
  - `idle_cleanup`

当前还没有进入的范围包括：

- 端口范围
- 多客户端竞争语义
- 本地观测面
- 任何本地管理页面

## 2. 核心职责

`frpc` 只负责以下事情：

- 连接服务端 `7000`
- 发起登录
- 保持心跳
- 接收服务端下发的隧道配置
- 根据服务端指令打开工作流
- 把流量转发到本地或内网目标
- 对 UDP 会话只执行服务端指令，不自行做空闲超时裁决
- 上报基础状态与错误

## 3. 不负责的事情

`frpc` 不负责：

- 保存分组配置
- 决定哪些隧道存在
- 管理黑白名单
- 管理限速策略
- 管理证书
- 提供可视化界面

这些全部属于 `frps`。

## 4. 运行方式

标准启动方式：

```text
frpc --server 1.2.3.4:7000 --token your-token
```

其中：

- `server` 是 `frps` 的控制连接地址
- `token` 对应某个分组

## 5. 生命周期

启动后典型流程如下：

1. 读取 `server` 与 `token`
2. 建立到 `frps` 的控制连接
3. 发送登录请求
4. 登录成功后接收配置版本和隧道列表
5. 维持心跳
6. 等待服务端打开工作流
7. 将流量转发到本地目标
8. 断线后自动重连并重新同步配置

## 6. 支持能力

### 6.1 支持

- TCP 正向代理
- UDP 正向代理
- 自动重连
- 配置热同步
- 基础错误上报

### 6.2 不支持

- 反向代理
- 域名证书管理
- 本地配置中心
- 本地规则系统

## 7. 当前目录

```text
frpc/
├── cmd/frpc/
├── internal/client/
├── internal/config/
└── go.mod
```

其中：

- `internal/client` 负责启动和重连、控制协议、配置同步、TCP 转发、UDP 转发和运行态管理
- `internal/config` 负责启动参数和 token 解析

## 8. 设计原则

- 客户端尽量无状态。
- 协议实现与服务端同仓同步演进，当前开发阶段不做版本兼容层。
- 失败后优先重连而不是等待人工修复。
- 所有业务规则都由服务端下发。
