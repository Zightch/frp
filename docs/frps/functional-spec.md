# frps 功能文档

## 1. 当前功能总览

当前 `frps` 只定义已经进入真实代码和测试链路的功能：

- 管理密钥初始化、challenge 登录和会话管理
- 分组最小 CRUD 与登录 `key` 重置
- 隧道最小 CRUD
- `frpc` `key` challenge/response 登录
- TCP/UDP 单端口与连续范围转发
- `frps` 侧 UDP idle cleanup

## 2. 管理认证

### 2.1 初始化

当前规则：

- 服务启动时检查本地 `auth.json`
- 文件不存在时，管理面处于未初始化态
- 未初始化态下不能访问需要管理会话的业务接口
- `POST /api/v1/auth/init` 只接受 `key_hash`
- `key_hash` 必须是 `64` 位小写 hex

### 2.2 登录

当前登录流程：

1. `POST /api/v1/auth/challenge`
2. 前端本地计算 `proof = sha256(key_hash + salt)`
3. `POST /api/v1/auth/login`
4. 服务端写入管理 session

当前行为：

- challenge 只能使用一次
- challenge 过期后立即失效
- session 默认有效 `12h`
- 会话可通过 Cookie 或 Bearer Token 使用

### 2.3 删除 `auth.json`

当前固定语义：

- 删除 `auth.json` 等价于重置管理认证
- 认证管理器通过固定间隔轮询删除状态
- 探测到删除后，服务端自动回到未初始化态
- 探测到删除后，已签发 challenge 失效
- 探测到删除后，已签发 session 失效
- 不支持在线轮换管理密钥

### 2.4 管理 WebUI

当前 WebUI 已完成重建，实际页面如下：

- 技术栈：`Node.js + Vue 3 + Element Plus`
- 下面路由都相对 WebUI 基址生效；默认基址是 `/`，如果配置 `webui.path_prefix=/frps`，则整体挂到 `/frps/...`
- `/init`：一次性写入首个管理密钥
- `/login`：challenge 登录
- `/proxy-groups`：主管理页，承接分组和隧道管理
- WebUI 基址内 `/` 重定向到 `/proxy-groups`
- 如果配置了 `webui.path_prefix`，同前缀下会同步暴露 `/healthz`、`/readyz` 和 `/api/v1/*`

已实现功能：
- 管理密钥初始化
- challenge 登录与会话状态检查
- 分组列表、新建、编辑、删除、重置登录 `Key`（el-dialog 弹窗）
- 隧道列表、新建、编辑、删除（el-drawer 抽屉）
- 分组表单中的 `effective_ip` 选择与异常值回显
- 分组/隧道状态展示与原因提示
- 创建/重置分组时登录 `Key` 明文单次展示
- 基础加载态、空态、表单校验

UI 规范详见 `docs/webui/style-guide.md`，接入管理页布局详见 `docs/webui/proxy-groups.md`。

## 3. 分组管理

### 3.1 当前可管理字段

当前管理 API 已直接管理下面这些字段：

- `name`
- `effective_ip`
- `enabled`

创建分组时：

- 自动生成完整 `key`
- 返回完整明文 `key` 一次
- 数据库只保存 `client_id` 和 `client_secret_hash`
- WebUI 列表页只展示 `client_id`，不会历史回显明文 `key`

当前 WebUI 已暴露 `name` / `effective_ip` / `enabled` 三个分组字段，并接入异常值回显。
分组查询返回当前还会附带运行态派生字段 `status` 和可选 `status_reason`，当前 WebUI 已展示“启用 / 禁用 / 异常”三态。
分组更新接口 `PATCH /api/v1/proxy-groups/{id}` 当前按局部更新处理，未提交字段保持原值；空对象会被拒绝，要求至少提交 `name`、`effective_ip`、`enabled` 之一。

### 3.2 当前业务规则

- 分组名全局唯一
- `key = client_id + client_secret`
- `client_id` 固定为 `32` 位小写 hex
- `client_secret` 固定为 `64` 位小写 hex
- `effective_ip` 必须是服务端当前本机 IPv4、服务端当前本机 IPv6，或特殊值 `0.0.0.0`、`::`
- `effective_ip` 是分组级绑定 IP，约束该分组下全部公网 listener 的监听地址
- 一个分组固定只允许 `1` 个在线 `frpc`
- 禁用分组后，新登录必须被拒绝

### 3.3 当前未开放管理的分组字段

下面这些字段当前不会通过管理 API/WebUI 编辑：

- `rate_limit`

其中：

- `rate_limit` 当前不生效
- `rate_limit` 语义固定为分组下所有隧道共享总限速，不再定义单隧道限速字段

## 4. 隧道管理

### 4.1 当前支持

当前隧道只支持：

- `protocol = tcp`
- `protocol = udp`
- `remote_type = single`
- `remote_type = range`

### 4.2 创建与修改规则

当前校验：

- `group_id > 0`
- `name` 不能为空
- 端口必须位于 `1..65535`
- `single` 隧道要求远端和本地都为单端口
- `range` 隧道要求远端和本地跨度一致
- `local_host` 必须是合法 IP 或 hostname
- 当前管理面已接入跨平台统一端口冲突校验：
  - 只在分组和隧道都启用时参与比较
  - 只比较同协议监听空间；`tcp` 和 `udp` 完全隔离
  - 仅当远端端口区间相交时才判定冲突
  - `effective_ip` 比较固定按统一矩阵执行：
    - `0.0.0.0` 与 `0.0.0.0` / `::` / 任意具体 IPv4 冲突
    - `::` 与 `::` / `0.0.0.0` / 任意具体 IPv6 冲突
    - 具体 IPv4 仅与 `0.0.0.0` 或“同 IP 具体 IPv4”冲突
    - 具体 IPv6 仅与 `::` 或“同 IP 具体 IPv6”冲突
  - `create tunnel`、`update tunnel`、`update proxy group(effective_ip/enabled)` 命中冲突时返回 `409`

当前不做：

- 隧道入口 ACL 配置
- 分组总限速执行
- 抓包配置

当前 WebUI 最小基线还固定下面这些约束：

- UI 中管理隧道时，必须明确隶属于某个现有分组
- 不开放分组 `rate_limit` 等未进入真实执行链路的持久化字段，也不承载抓包开关这类仅属于运行时的控制

### 4.3 当前返回字段

当前隧道查询会返回：

- `id`
- `group_id`
- `group_name`
- `name`
- `protocol`
- `remote_type`
- `remote_start`
- `remote_end`
- `local_host`
- `local_start`
- `local_end`
- `enabled`
- `status`
- `status_reason`
- `created_at`
- `updated_at`

其中隧道 `status` 当前固定为四态：

- `禁用`：分组禁用或隧道禁用
- `冲突`：命中当前已实现的跨平台统一静态端口冲突
- `异常`：隧道本身启用且不冲突，但启动期扫描、未监听轮询、`frpc` 登录后 listener 启动或 `effective_ip` 本地重绑发现运行态失败
- `启用`：其余情况

`status_reason` 只在 `冲突` 或 `异常` 时返回，用于直接展示冲突对象、运行态端口冲突或 listener 启动失败原因。这个状态是运行态派生值，不单独持久化入库。

管理 API 当前返回“持久化隧道配置 + 运行态派生状态”；分组 `rate_limit` 不会通过管理 API / WebUI 编辑，抓包这类运行时控制也不在隧道返回模型内。

## 5. frpc 登录与配置下发

### 5.1 当前登录规则

`frpc` 登录时，`frps` 当前会检查：

- `client_id` 是否存在
- 分组是否启用
- challenge 是否有效且未重放
- challenge 响应是否匹配
- 当前分组单客户端槽位是否已被占用

补充当前启动门闩：

- `frps` 在真正开放 `frpc` 控制端口前，会先完成一次“当前没有 listener 的启用隧道”全量运行态扫描，并先把异常原因写入隧道状态。
- 管理 API 也会等待同一轮首轮扫描完成后才开始监听，因此首次可见的隧道状态已经是首轮扫描后的结果。
- 启动完成后，控制面会固定轮询同一批“当前没有 listener”的启用隧道；已真实监听中的 tunnel 不参与这条轮询路径。
- 这条轮询用于补记新的运行态失败，也用于在外部端口占用恢复、`effective_ip` 恢复可绑定后清理旧的 `异常` 状态。
- 如果某个在线分组当前只有部分 tunnel 已成功监听，轮询会继续扫描该分组剩余“启用但未监听”的 tunnel；当对应冲突或占用消失后，会直接在现有活动 session 上补启动这些已恢复 tunnel。
- 如果某个在线分组之前因为 `effective_ip` 暂时不可用而被收缩成空配置，只要后续轮询发现该 `effective_ip` 已重新可绑定，服务端会先向当前 session 重新下发仓库里的完整快照；待客户端重新 `config.ack` 后，再按常规 listener 恢复路径启动该组 tunnel。

### 5.2 当前配置下发规则

当前在登录阶段和后续在线热重载阶段都下发整组完整快照。

当前配置边界：

- 管理 API / WebUI 只编辑持久化配置；命中运行态字段且分组在线时，`frps` 会在写库成功后主动触发整组热重载。
- `frps` 在 `frpc` 登录和后续在线热重载时，都会把持久化配置投影成运行时快照，下发给 `frpc` 并在双方内存中生效。
- 抓包相关控制当前未实现；如果后续引入，只应属于运行时配置，不应作为 `tunnels` 持久化字段。

当前快照包含：

- `configVersion`
- `generatedAtMs`
- `tunnels`

当前不下发：

- ACL
- 分组总限速
- 抓包运行时控制
- 任何反向代理配置

### 5.3 在线整组配置热重载当前规则

下面这些规则已经确认，并且 `frps` 侧整组冻结、pending 重载状态管理、`frpc` 侧本地资源清理与快照替换，以及 `ack` 后 listener 重建都已落地：

- 分组离线时，配置变更只更新数据库，不触发热重载。
- 分组在线时，`frps` 继续使用现有 `config.push / config.ack` 下发整组完整快照，不新增单 tunnel 增量事件。
- 首版固定按“整组冻结 + 整组全量重建”实现，不承诺保留该分组下未受影响 tunnel 的现有连接。

触发在线热重载的变更包括：

- 分组 `enabled`
- 分组 `effective_ip`
- 隧道新增 / 删除
- 隧道 `protocol`
- 隧道 `remote_type`
- 隧道 `remote_start`
- 隧道 `remote_end`
- 隧道 `local_host`
- 隧道 `local_start`
- 隧道 `local_end`
- 隧道 `enabled`

下面这些变更不触发在线热重载：

- 分组名称修改
- 隧道名称修改

首版在线热重载的产品语义固定为：

1. 管理面先写库成功。
2. `frps` 如果发现该分组当前在线，先停止该分组全部 listener，并关闭该分组全部活动 TCP/UDP 连接。
3. `frps` 下发新的整组完整快照。
4. `frpc` 在本地对比新旧快照，完成删除 / 替换 / 新增分类，并完成本地资源清理和运行态替换。
5. `frpc` 处理完成后返回 `config.ack(status=ok)`。
6. `frps` 收到 `ok` 后，再按新快照重新启动该分组 listener。

失败路径固定为：

- 同一分组同一时刻只允许一个未确认的在线配置更新。
- 如果刷新时前一版 `config.push` 仍未确认，`frps` 直接断开当前控制连接，不排队等待第二版更新。
- 如果 `config.ack` 超时或返回失败，`frps` 直接断开该分组控制连接，交给 `frpc` 自动重连后重新领取完整快照。
- 登录 `key` 重置和分组删除继续直接断开当前控制连接，不走热重载。

截至 2026-04-21，当前产品行为已经落地到：

- 在线分组发生运行态配置变更时，`frps` 会先冻结整组 listener，并关闭该分组当前活动 TCP / UDP 运行态，再下发新的整组快照。
- `frpc` 收到新的整组快照后，会先完成本地新旧快照对比，关闭当前全部活动 TCP stream / UDP session，再替换本地运行态快照，并在完成后返回 `config.ack(status=ok)`。
- `frps` 收到对应 `config.ack(status=ok)` 前，不会重新开放 listener；因此热重载窗口内不会继续接受该分组新的公网连接。
- 如果此时又收到第二次运行态变更，而上一版 `config.push` 仍未确认，服务端直接断开该分组控制连接，交给 `frpc` 重连后重新领取完整快照。
- 因此当前 `config.ack(status=ok)` 已可对外承诺：客户端运行态已完成本地资源清理和快照切换；`frps` 收到后才重新开放 listener。
- 如果 `frps` 在 `config.ack(status=ok)` 后按新快照启动 listener，或在仅修改 `effective_ip` 的本地重绑时，命中在线运行态端口冲突、外部端口占用或其他 bind 失败，当前不会改写数据库配置；对应隧道会在管理 API 中显示为 `异常`，并携带明确的冲突/启动失败原因。
- 对于当前没有 listener 的启用 tunnel，启动期全量扫描和后续轮询也会复用同一套异常标记语义；如果后续外部占用消失或 `effective_ip` 恢复可绑定，轮询会清掉对应旧异常；若该 tunnel 所属分组当前在线且 control session 仍有效，还会直接补启动已恢复 tunnel。
- 如果该在线 session 之前因 `effective_ip` 失效已经 ack 了空配置，轮询在确认 `effective_ip` 重新可绑定后，会先补发当前仓库完整快照，而不是只在服务端本地补开 listener；这样客户端运行态和服务端 listener 会重新回到同一份快照上。

端口范围在在线热重载中的规则与单端口一致，只是作用对象换成整个范围 tunnel：

- 只要 range tunnel 的范围边界或本地映射基准变化，就视为该 tunnel 已整体替换。
- 旧范围 `1000-2000` 改为新范围 `1500-2500` 时，虽然 `1500-2000` 这段公网端口仍有重叠，但由于 `remoteStart` 改变，`offset = remotePort - remoteStart` 的换算基准已经变化，映射关系不再等价，因此必须按“旧 tunnel 全量下线，新 tunnel 全量上线”处理。

## 6. TCP 转发

当前 TCP 行为：

- 只有启用状态的 tunnel 会监听公网端口
- listener 在对应 `config.ack` 后才启动
- 公网连接进入后，`frps` 分配 `streamId`
- 通过 `stream.open` 通知 `frpc`
- `frpc` 回 `stream.opened`
- 双方通过 `stream.data` 传输原始字节
- 任一侧结束后通过 `stream.close` 收口

当前已验证的失败路径：

- 错误 key
- 禁用分组
- 禁用隧道
- 本地目标不可达

## 7. UDP 转发

当前 UDP 行为：

- 第一次看到某个公网 UDP 会话时，`frps` 发送 `udp.open`
- 首包和后续包都通过 `udp.data` 转发
- `sessionId` 由 `frps` 分配
- `frps` 按 `tunnelId + remotePort + public client addr` 识别 session
- `frpc` 不做本地 idle timer
- `frps` 空闲约 `30s` 后删除 session 并发送 `udp.close`

当前关键语义：

- 不是固定 `30s` 轮询窗口
- 只要有成功转发，立即刷新活跃时间
- 空闲时间从最后一次成功转发结束时开始重新计算

## 8. 当前未实现清单

下面这些功能当前不能视为 `frps` 已支持：

- 反向代理管理
- 证书管理
- WebSocket 实时态
- 连接列表和强制断开
- 限速执行
- 抓包执行
- 隧道入口 ACL 执行
- 分组/隧道规则管理页

### 8.1 已确认待实现的统一证书 / CA 资产与隧道级 CA 池

当前“证书管理”已经进一步收口出一套统一设计，但还没有进入真实代码链路。

当前已确认的范围如下：

- `frps` 后续统一管理证书资产和 CA 资产
- 底层使用同一张资产表，展示时按资产类型区分
- 当前只支持 `pem` 格式
- 证书 / CA 原文直接存数据库
- 管理员通过 WebUI 管理证书和 CA
- 导入现有资产时，同时支持“上传文件”和“手动粘贴 PEM”
- 管理面还支持生成 CA，以及用现有 CA 或新生成 CA 签发证书

当前已确认的最小资产字段如下：

- `id`
- `name`
- `remark`
- `source`
- `asset_type`
- `format_type`
- `crt`
- `crt_hash`
- `key`
- `created_at`
- `updated_at`

当前还需要一张树状关系表 `certificate_asset_relations`：

- `id`
- `child_asset_id`
- `parent_asset_id`
- `relation_type`
- `created_at`
- `updated_at`

字段口径固定为：

- `source` 当前只区分 `upload` 和 `generated`
- `asset_type` 当前只区分 `certificate` 和 `ca`
- `format_type` 当前固定为 `pem`
- `crt_hash` 由服务端在写库前计算，作为快速去重和快速比对字段
- `key` 可为空
- `source=generated` 时，`crt` 只保存当前节点这一张证书
- `source=upload` 时，`crt` 允许保留上传时的多证书 PEM 原结构
- `relation_type` 当前固定为 `issued_by`
- `child_asset_id` 在关系表中唯一，保证当前只支持单父树
- `parent_asset_id` 只允许引用 `source=generated` 且 `asset_type=ca` 的资产

当前多级 CA 树固定按“关系表里的单直接上游”表达：

- 叶子证书可以指向中间 CA
- 中间 CA 可以继续指向上游 CA
- 根 CA 没有上游

当前不支持：

- 多父上游
- 交叉签名

资产类型补充规则：

- `asset_type=certificate` 时，运行时如果要作为客户端证书使用，必须有 `key`
- `asset_type=ca` 时，`key` 可以为空
- 只有 `source=generated` 且 `asset_type=ca` 且 `key` 非空的资产，后续才能用于签发证书
- 文件上传和手动粘贴统一记为 `source=upload`
- 管理面生成的 CA 和证书统一记为 `source=generated`

上传与生成的边界当前固定为：

- 生成资产必须是原子节点；一个资产只存一张证书
- 上传资产允许保存管理员提供的完整 PEM 结构；可以是一张证书，也可以是单条证书链
- 上传资产继续沿用当前校验逻辑：必须能通过“资产内嵌链、数据库根 CA、系统 CA”三类来源完成有效链验证
- 上传资产不写入 `certificate_asset_relations`，不作为树节点参与后续链路重组
- 上传资产只支持按原结构下载

生成资产的下载语义当前固定为：

- 默认只下载当前节点
- 可以选择“当前节点到某个祖先”的一条单链，输出为一个多证书 PEM
- 可以选择某个 CA 节点的部分子树或整棵树，输出为多文件打包结果
- 所有 `crt` 聚合和归档打包都在 `frps` 内存完成，不落盘，不调用外部工具

当前同时已确认 CA 池回退到隧道级：

- 不做分组级 CA 池
- 每条 tunnel 可以引用自己的 CA 资产集合
- tunnel 只允许引用 `asset_type=ca` 的资产

系统 CA 的规则也已经固定：

- `frps` 和 `frpc` 都各自在启动时加载自己机器的系统 CA 环境
- 系统 CA 不写数据库
- `frps` 不把自己的系统 CA 下发给 `frpc`
- `frps -> frpc` 只下发当前 tunnel 实际引用到的 CA 资产

当前刻意不把这些派生信息持久化入库：

- `subject`
- `issuer`
- `dns_sans`
- `ip_sans`
- `not_before`
- `not_after`
- `fingerprint`

这类信息统一在读取资产后现场解析，用于管理面展示；`crt_hash` 只作为去重辅助字段，不作为展示型指纹字段直接对外承诺。

当前证书去重规则也已确认：

- 写库前先计算 `crt_hash`
- 先按 `crt_hash` 做快速候选过滤
- 命中相同 `crt_hash` 后，仍继续做精确内容比对，再决定是否拦截重复写入

当前同时明确不包含下面这些内容：

- 证书与端口的绑定
- 证书与 SNI 的绑定
- 证书与 listener / entrypoint 的绑定
- 自动把整条上游签发链都加入 tunnel 信任池

后续反向代理、WebUI HTTPS、监听器等模块只应复用这套统一资产模型；其中反向代理场景下，如果 `frps` 自己直接作为客户端连接上游目标服务器，也继续使用本机系统 CA 加显式引用 CA 资产的同一规则。

这些内容如果后续开始实现，必须先同步更新本文档。

## 9. 分组生效 IP 当前边界

下面这些规则已经确认，并已进入当前后端与 WebUI 链路：

- 如果数据库中配置的 `effective_ip` 后续不再存在于本机，后续运行态需要把分组标成异常；数据库值不自动改写，也不静默切换到其他地址。
- 管理接口现在会返回单一分组状态字段 `status`，取值固定为“启用 / 禁用 / 异常”；异常时可附带 `status_reason`。
- 这个分组状态字段是运行态派生值，不再额外拆出第二个“当前 IP 是否可用”的状态字段。
- 当前固定判定规则为：`enabled=false` 时显示“禁用”；`enabled=true` 且 `effective_ip` 位于当前可用 IP 列表时显示“启用”；其他情况显示“异常”。
- WebUI 当前已经消费 `status` / `status_reason`，在分组列表和编辑弹窗中展示三态与异常原因。
- WebUI 分组表单当前已经把“生效 IP”做成选项下拉，并在异常值失效时保留原值回显。
