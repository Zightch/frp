# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成 tunnel TLS 功能，只覆盖 `TCP tunnel`，包含两侧：
  - `frps` 监听侧 TLS / mTLS
  - `frpc` 后端侧 TLS / mTLS

当前前提：

- 不做旧版本兼容，可以直接重构旧模型、旧协议和旧接口。
- `certificate_asset_usages` 扩展为通用证书绑定层，不再只服务入口证书。
- `7000` 控制链路若仍为 `plain`，只返回 warning 和记录日志，不强制拦截 tunnel TLS 保存。

子步骤：

- 边界与规则收口：
  - 固定只做 `TCP tunnel TLS`
  - 明确 `listen_tls` 和 `backend_tls` 两侧职责
  - 固定系统 CA 默认行为与 mTLS 必填条件
- 通用证书绑定层重构：
  - 重构 `certificate_asset_usages`，支持 `target_type + target_id + usage_type + asset_id`
  - 支持 tunnel 级单证书绑定与多 CA 池绑定
  - 抽出统一 `certbindings` 服务，负责绑定查询、替换、解析和资产类型校验
- 数据模型与 schema：
  - 为 `tunnels` 增加 TLS 标量字段
  - 调整 `certificate_asset_usages` 的索引和唯一约束
  - 删除旧的“入口证书专用绑定模型”假设
- API 重构：
  - 更新 `groupconfig` 的 tunnel create / update / list / view
  - 将 tunnel TLS 配置和 warnings 纳入管理接口
  - 重做 settings 全局证书绑定接口，使其走新的通用绑定层
- 领域模型重构：
  - 拆开数据库 tunnel 模型、`frps` runtime 模型、`frpc` 下发模型
  - 不再让旧 `protocol.TunnelEntry` 同时承载三层语义
- 控制面配置下发协议：
  - 重构 `ConfigPush` 或等价配置载荷
  - 只向 `frpc` 下发 `backend_tls` 所需配置与 TLS materials
  - 支持 CA bundle、client cert、client key 的独立载荷组织
- `frps` 监听侧 TLS：
  - 在 TCP listener 启动链路按 tunnel 配置套 TLS
  - 支持 `NoClientCert` 和 `RequireAndVerifyClientCert`
  - 支持“是否加载系统 CA”，默认不加载
  - 支持追加多选 CA 池
- `frpc` 后端侧 TLS：
  - 在本地目标拨号链路切换明文 / TLS / mTLS
  - 默认加载系统 CA 池，并允许追加多选 CA
  - 支持客户端证书、私钥和 `InsecureSkipVerify`
- warning 机制：
  - 当 `backend_tls` 需要向 `frpc` 下发 CA 或客户端证书，而 group 的 `7000` 控制链路仍为 `plain` 时
  - API 返回 warnings
  - 服务端记录 warning 日志
- 校验与错误处理：
  - 限制 `UDP tunnel` 不可启用 TLS
  - 校验 mode 与绑定组合、mTLS 必填项、CA 池非空条件
  - 校验证书和 CA 的资产类型
  - 统一错误码、状态和日志语义
- 自动化测试：
  - 补齐 schema、通用绑定服务、API、runtime 投影、协议编解码单元测试
  - 补齐 `frps` 监听侧 TLS / mTLS 测试
  - 补齐 `frpc` 后端侧 TLS / mTLS 测试
  - 覆盖 warning 返回与日志行为
- 集成 / E2E：
  - 验证仅监听侧 TLS
  - 验证仅后端侧 TLS
  - 验证双侧 TLS
  - 验证监听侧 mTLS
  - 验证多 CA 池与系统 CA 开关
  - 验证后端测试模式与 plain tunnel 不回退
- 收尾重构：
  - 删除旧入口证书专用路径和过时代码
  - 统一命名、结构和边界
  - 跑完整测试并收口进度记录

当前轮边界：

- 当前只承接 tunnel 级 TLS，不扩到 UDP、SNI 多证书选择、HTTP 层证书自动管理或额外的应用层协议适配。
- `frps` 监听侧 TLS 与 `frpc` 后端侧 TLS 是两段独立 TLS，不追求端到端单次握手语义。
- `frps` 监听侧 CA 池支持“是否加载系统 CA”，默认不加载系统 CA。
- `frpc` 后端侧启用 TLS 时默认加载系统 CA 池，允许追加自定义 CA。
- `frpc` 后端侧启用 mTLS 时必须有客户端证书和私钥。
- `backend_tls` 所需证书材料由 `frps` 下发给 `frpc`。
- 当前这一步只更新 `todo` 文档，不动代码。

当前状态：

- 本轮总目标已定义，尚未开始代码实现。
- 下一步从通用证书绑定层与 schema 重构开始。
