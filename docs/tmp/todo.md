# 当前轮 Todo

- 进度归档索引：`docs/progress/README.md`

当前总目标：

- 完成“客户端凭据模型切换 + `frps/internal/control` 结构收束”的当前轮基线设计，并按不兼容切换口径推进后续实现，为监控管理面、抓包、令牌桶限速和连接总览预留稳定边界。

子步骤：

- 1. 统一领域命名与不兼容边界
  - `frpc` 组登录凭据不再使用 `token` 术语。
  - 统一改为：
    - `client_id`
    - `client_secret`
    - `client_secret_hash`
    - `management_secret`
    - `session_token`
    - `bucket_tokens`
  - `client_id` 作为稳定公开标识，不随密钥轮转变化。
  - 凭据轮转默认只更新 `client_secret` / `client_secret_hash`，不更换 `client_id`。
  - 不保留旧字段名，不做双写，不承诺新旧版本混跑。
- 2. 协议、配置、存储与管理面口径一次切换
  - `frps/pkg/protocol`
    - `AuthBegin.TokenID -> ClientID`
    - `ChallengeResponse(tokenHash, nonce) -> ChallengeResponse(secretHash, nonce)`
  - `frpc/internal/config`
    - 配置由单一 `token` 改为显式 `client_id + client_secret`
  - `frps/internal/control/repository`
    - `LoadGroupRuntime(ctx, tokenID) -> LoadGroupRuntimeByClientID(ctx, clientID)`
    - `GroupRuntime.TokenHash -> GroupRuntime.ClientSecretHash`
  - `frps/internal/app/schema`
    - `proxy_groups.token_id -> client_id`
    - `proxy_groups.token_hash -> client_secret_hash`
  - `frps/internal/api` / `frps/webui`
    - `token_id` 展示与 JSON 字段统一改为 `client_id`
    - 创建组 / 轮转凭据返回 `credential` 对象，不再返回 `token`
- 3. `frps/internal/control` 第一阶段重构
  - 拆分 `sessionState`
    - 配置态：`current/pending/acked/recoveryMode`
    - 运行态：`done/frozen/generation/listeners/streams/udp`
  - 抽离版本化 `runtime issue store`
  - 引入统一 `runtime registry`
    - 记录 control session、listener、TCP stream、UDP session、public connection
  - 收束 `runtime reconciler`
    - 统一 `initial scan / config ack / refresh / scan recovery` 四类触发
  - 拆开 `ensureTunnelListeners()` 内部阶段
    - `plan`
    - `start`
    - `attach`
    - `serve`
- 4. 为未来能力预埋稳定边界
  - `RuntimeRegistry`
    - 作为管理面“查看所有连接 / 查看运行时状态”的统一读面
  - `RuntimeHooks`
    - 统一承接 control frame、TCP stream、UDP session 的抓包 / 审计 / 限速 hook
  - `RatePolicy`
    - 第一版限速策略使用令牌桶
    - key 设计预留 `group_id / tunnel_id / session_id / conn_id / client_id`
  - `CaptureTap`
    - 默认异步、非阻塞、限量，不允许直接拖慢数据面
- 5. 管理登录密钥处理原则
  - 管理面认证机制保持独立，不与 `frpc` 客户端凭据合并。
  - 当前只建议做命名清理：
    - `key_hash -> secret_hash`
    - 局部 `token -> sessionToken`
  - 不在本轮改变 `auth.json + challenge + session cookie` 的认证模型。
- 6. 后续执行顺序
  - 先切凭据命名与 schema
  - 再拆 `sessionState`
  - 再引入 `runtime issue store`
  - 再引入 `runtime registry`
  - 再收束 `runtime reconciler`
  - 最后接管理面连接视图、抓包、限速

当前轮边界：

- 本轮先做结构与命名收束，不在 `todo` 中混入实现细节和临时调试结论。
- 本轮不拆 `control` 子包，不引入事件总线、插件系统、状态机 DSL 或 DI 容器。
- 本轮不为旧协议、旧配置、旧库表、旧 API 字段提供兼容层。
- 当前上一轮跨平台稳定性修复结果仍以 `docs/progress/2026-04-23.md` 为准，本 `todo` 只定义下一轮执行目标。

当前唯一下一步：

- 先完成“客户端凭据命名与不兼容切换”的实现切入点收敛。
