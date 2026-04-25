# 分组配置页面 UI 规范

更新时间：2026-04-25

本文档描述当前已经落地的“分组配置”页面。页面旧称“接入管理”，但当前 UI、路由名和组件签名都已经切换到“分组配置”语义。

## 1. 路由与命名

| 项目 | 当前值 | 说明 |
|------|--------|------|
| 页面标题 | `分组配置` | 导航文案、页面标题统一使用该名称 |
| 路由名 | `GroupConfig` | 前端内部路由标识 |
| 路由路径 | `/proxy-groups` | 为兼容现有入口继续保留旧路径 |
| 视图组件 | `GroupConfigView.vue` | 当前主管理页组件 |

兼容边界：

- 后端 API 仍保持 `proxyGroupsApi`。
- 类型名仍保持 `ProxyGroup`。
- 文档中的“分组配置”指 UI 语义，不代表后端契约同步改名。

## 2. 页面结构

参考当前实际界面，页面结构如下：

```text
┌──────────────────────────────────────────────────────────────┐
│  分组配置                                           [刷新]   │
├──────────────────────────────────────────────────────────────┤
│  已选中: production | 状态: 启用 | Client ID: 9d9c...       │
│  生效 IP: 0.0.0.0 | frpc 登录传输: TLS 必需 [重置密钥][编辑][删除] │
├────────────────────────┬────────────────────────────────────┤
│  分组列表    [新建分组] │  隧道列表（production） [新建隧道] │
│  ┌───────────────────┐ │ ┌────────────────────────────────┐ │
│  │ 名称 Client ID 状态│ │ │ 名称 协议 远端 本地 状态 操作 │ │
│  │ production ...    │ │ │ web  tcp 8080 127.0.0.1:80   │ │
│  │ staging    ...    │ │ │ ssh  tcp 2222 127.0.0.1:22   │ │
│  └───────────────────┘ │ └────────────────────────────────┘ │
└────────────────────────┴────────────────────────────────────┘
```

结构说明：

- 页面头部只有标题和刷新按钮。
- 分组相关操作集中在“已选中分组”的操作栏中，不再放在分组表格行内。
- 内容区固定为左右双栏：
  - 左侧分组列表
  - 右侧当前分组的隧道列表
- 双栏内容区采用 `grid + flex` 撑满剩余高度，列表底边会随浏览器窗口高度自适应。

## 3. 分组管理

### 3.1 分组列表表格

| 列名 | 说明 |
|------|------|
| 名称 | 分组名称，点击整行即可选中 |
| Client ID | 分组稳定公开标识 |
| frpc 登录传输 | `明文 / TLS 必需` |
| 状态 | `启用 / 禁用 / 异常` Tag，异常时可悬停查看原因 |

当前实现中，分组表格没有单独“操作”列。

### 3.2 分组操作

- 新建分组：左侧面板头部按钮，使用 `el-dialog`
- 编辑分组：基于当前选中分组，在顶部操作栏中触发
- 删除分组：基于当前选中分组，在顶部操作栏中触发
- 重置密钥：基于当前选中分组，在顶部操作栏中触发

删除分组时：

- 若该分组下存在隧道，确认文案会明确提示“关联隧道会一并删除”。

重置密钥时：

- 成功后弹出一次性展示窗口。
- 新登录 `Key` 仅显示一次，支持复制到剪贴板。
- 确认文案会明确提示旧 `-key` 会立即失效。

## 4. 隧道管理

### 4.1 隧道列表表格

| 列名 | 说明 |
|------|------|
| 名称 | 隧道名称 |
| 协议 | `TCP` / `UDP` |
| 远端 | 单端口或端口范围 |
| 本地 | `local_host:local_port` 或范围 |
| 状态 | `启用 / 禁用 / 冲突 / 异常` Tag，冲突/异常时可悬停查看原因 |
| 操作 | 编辑、删除 |

### 4.2 隧道操作

- 新建隧道：右侧面板头部按钮，只有选中分组后才显示
- 编辑隧道：隧道表格行内操作
- 删除隧道：隧道表格行内操作

隧道新建和编辑都使用 `el-drawer`，不跳转页面。

## 5. 状态与交互

### 5.1 选中状态

- 点击分组表格行后，左侧表格行高亮。
- 右侧隧道列表立即切换为当前分组的数据。
- 顶部操作栏同步显示当前选中分组名称、状态、`client_id`、`effective_ip` 和 `frpc 登录传输`。

### 5.2 认证与跳转

- 页面挂载时先调用 `/auth/state`。
- 未初始化跳转 `/init`。
- 未认证跳转 `/login`。

### 5.3 空态与错误态

- 无分组时：显示“暂无分组，请新建”
- 未选中分组时：显示“请选择分组查看隧道”
- 已选中分组但无隧道时：显示“该分组暂无隧道”
- 加载失败时：显示错误文案和“重试”按钮

### 5.4 操作反馈

- 成功：`ElMessage.success`
- 失败：`ElMessage.error`
- 删除/重置等不可逆或影响较大的动作：`ElMessageBox.confirm`

## 6. 表单设计

### 6.1 新建/编辑分组

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 名称 | `el-input` | 是 | 分组名称 |
| 生效 IP | `el-select` | 是 | 该分组下全部代理统一绑定的本机监听 IP |
| frpc 登录传输 | `el-select` | 是 | `明文 / TLS 必需` |
| 启用 | `el-switch` | 否 | 是否启用 |

当前校验：

- 名称必填。
- `effective_ip` 必选。
- `control_transport_security` 必选。
- 下拉选项只来自服务端当前本机 IPv4、服务端当前本机 IPv6，以及特殊值 `0.0.0.0`、`::`。
- 编辑已有分组时，如果数据库中的 `effective_ip` 当前已失效，表单会以“已失效”选项回显原值。
- 当前若还没有绑定 `control_listener_tls`，表单会展示提示；真正保存校验以后端为准。

### 6.2 新建/编辑隧道

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 名称 | `el-input` | 是 | 隧道名称，前端按 `trim` 后判空 |
| 协议 | `el-select` | 是 | `TCP` / `UDP` |
| 远端类型 | `el-select` | 是 | 单端口 / 端口范围 |
| 远端端口 | `el-input-number` | 是 | 单端口或起始端口 |
| 远端结束 | `el-input-number` | 条件必填 | 端口范围时必填 |
| 本地地址 | `el-input` | 是 | IP 或域名，前端按 `trim` 后判空 |
| 本地端口 | `el-input-number` | 是 | 单端口或起始端口 |
| 本地结束 | `el-input-number` | 条件必填 | 端口范围时必填 |
| 启用 | `el-switch` | 是 | 前端要求提交时必须有明确布尔值 |

当前前端校验规则：

- 所有字段都在前端参与必填校验。
- 端口必须为 `1-65535` 的整数。
- 范围模式下，开始端口必须小于结束端口。
- 范围模式下，远端和本地的端口数量必须一致。
- 切换远端类型或修改端口值时，会立即联动重验相关字段。
- 提交前会对 `name` 和 `local_host` 做 `trim` 后再下发请求。

## 7. 登录 Key 展示弹窗

创建分组或重置密钥成功后，页面会弹出一次性 `Key` 展示窗口：

- 标题固定为“客户端登录 Key 已生成”
- 正文提示“请保存以下登录 Key，并通过 `-key` 提供给客户端”
- 只展示整串 `key`，不拆成 `client_id` / `client_secret` 两段复制
- 提供复制按钮
- 明确提示“此 Key 仅显示一次，关闭后将无法再次查看”

## 8. 组件选用

| 场景 | Element Plus 组件 |
|------|-------------------|
| 分组列表 / 隧道列表 | `el-table` |
| 分组操作弹窗 | `el-dialog` |
| 隧道操作抽屉 | `el-drawer` |
| 表单 | `el-form` + `el-form-item` |
| 输入框 | `el-input` |
| 数字端口输入 | `el-input-number` |
| 选择器 | `el-select` |
| 开关 | `el-switch` |
| 状态标签 | `el-tag` |
| 消息提示 | `ElMessage` |
| 确认弹窗 | `ElMessageBox.confirm` |

## 9. API 对齐

前端仍与后端 `frps/internal/api/management.go` 对齐：

### ProxyGroup

```typescript
interface ProxyGroup {
  id: number
  name: string
  client_id: string
  effective_ip: string
  enabled: boolean
  control_transport_security: 'plain' | 'tls_required'
  status: string
  status_reason?: string
  created_at: string
  updated_at: string
}
```

### LocalIP

```typescript
interface LocalIP {
  addr: string
  family: string
  standard: string
}
```

### Tunnel

```typescript
interface Tunnel {
  id: number
  group_id: number
  group_name: string
  name: string
  protocol: string // 'tcp' | 'udp'
  remote_type: string // 'single' | 'range'
  remote_start: number
  remote_end: number
  local_host: string
  local_start: number
  local_end: number
  enabled: boolean
  status: string
  status_reason?: string
  created_at: string
  updated_at: string
}
```

### 接口返回

| 接口 | 返回 |
|------|------|
| `/proxy-groups` GET | `{items: ProxyGroup[]}` |
| `/proxy-groups` POST | `{item: ProxyGroup, key: string}` |
| `/proxy-groups/{id}` PATCH | `{item: ProxyGroup}` |
| `/proxy-groups/{id}/key` POST | `{item: ProxyGroup, key: string}` |
| `/local-ips` GET | `{items: LocalIP[]}` |
| `/tunnels` GET | `{items: Tunnel[]}` |
| `/tunnels` POST | `{item: Tunnel}` |
| `/tunnels/{id}` PATCH | `{item: Tunnel}` |

## 10. 当前状态展示边界

- 后端接口当前已经返回一个分组状态字段，供页面直接展示“启用 / 禁用 / 异常”三态，不再额外拆出第二个“IP 可用性状态”字段。
- `enabled = false` 时显示“禁用”。
- `enabled = true` 且 `effective_ip` 当前有效并可绑定时显示“启用”。
- 其他情况显示“异常”；如果数据库中已有 `effective_ip` 当前已不在本机地址列表中，就属于这一类。
- 表单和列表在异常态下仍会保留并回显数据库中的原值，避免用户进入编辑态后丢失上下文。
- 隧道列表当前固定展示 `启用 / 禁用 / 冲突 / 异常` 四态，并在 `冲突` 或 `异常` 时显示 `status_reason`。
