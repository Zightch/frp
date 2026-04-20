# 接入管理页面 UI 规范

## 设计参考

参考阿里云、腾讯云控制台的管理页面风格：
- 左侧列表区 + 右侧详情区的双栏布局
- 表格行可选中，选中后右侧显示详情或关联数据
- 操作按钮收敛在表格行内或工具栏中
- 新增/编辑使用抽屉或弹窗，避免页面跳转

## 页面结构

```
┌─────────────────────────────────────────────────────────────┐
│  接入管理                                      [刷新] [新建] │
├───────────────────────┬─────────────────────────────────────┤
│  分组列表             │  隧道列表                           │
│  ┌─────────────────┐  │  ┌─────────────────────────────────┐│
│  │ ▸ default       │  │  │ 名称 │ 协议 │ 远端 │ 本地 │ 操作││
│  │   production    │  │  │ web  │ tcp  │ 8080 │ 80   │ ··· ││
│  │   test          │  │  │ ssh  │ tcp  │ 2222 │ 22   │ ··· ││
│  └─────────────────┘  │  └─────────────────────────────────┘│
│                       │  [新建隧道]                         │
├───────────────────────┴─────────────────────────────────────┤
│  已选中: default  │  Token: abc123  │  [重置Token] [编辑] [删除] │
└─────────────────────────────────────────────────────────────┘
```

## 功能模块

### 1. 分组管理

#### 分组列表表格
| 列名 | 说明 |
|------|------|
| 名称 | 分组名称，可点击选中 |
| Token ID | 分组标识，用于客户端配置 |
| 状态 | 启用/禁用状态（Tag） |
| 操作 | 编辑、删除、重置 Token |

#### 分组操作
- **新建分组**：顶部工具栏按钮 → 弹窗输入名称
- **编辑分组**：行内按钮或选中后底部操作栏 → 弹窗修改名称、状态
- **删除分组**：行内按钮 → 确认弹窗（提示会同时删除关联隧道）
- **重置 Token**：行内按钮或底部操作栏 → 确认弹窗 → 成功后显示新 Token

### 2. 隧道管理

#### 隧道列表表格
| 列名 | 说明 |
|------|------|
| 名称 | 隧道名称 |
| 协议 | TCP/UDP |
| 远端 | 远端端口（单个或范围） |
| 本地 | 本地地址（单个或范围） |
| 状态 | 启用/禁用状态（Tag） |
| 操作 | 编辑、删除 |

#### 隧道操作
- **新建隧道**：选中分组后右侧区域按钮 → 抽窗输入参数
- **编辑隧道**：行内按钮 → 抽窗修改参数
- **删除隧道**：行内按钮 → 确认弹窗

### 3. 交互规范

#### 选中状态
- 点击分组行 → 高亮选中，右侧显示该分组的隧道列表
- 底部操作栏显示选中分组的快捷操作

#### 空状态
- 无分组时：显示"暂无分组，请新建"
- 选中分组无隧道时：显示"该分组暂无隧道"

#### 加载状态
- 表格区域显示骨架屏或 loading 提示

#### 操作反馈
- 成功：Toast 提示 + 自动刷新列表
- 失败：Toast 提示错误信息

## 表单设计

### 新建/编辑分组

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 名称 | Input | 是 | 分组名称 |
| 启用 | Switch | 否 | 是否启用 |

### 新建/编辑隧道

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 名称 | Input | 是 | 隧道名称 |
| 协议 | Select | 是 | TCP/UDP |
| 远端类型 | Select | 是 | 单端口/端口范围 |
| 远端端口 | Input | 是 | 单端口或起始端口 |
| 远端结束 | Input | 条件 | 端口范围时必填 |
| 本地地址 | Input | 是 | IP 或域名 |
| 本地端口 | Input | 是 | 单端口或起始端口 |
| 本地结束 | Input | 条件 | 端口范围时必填 |
| 启用 | Switch | 否 | 是否启用 |

## 组件选用

| 场景 | Element Plus 组件 |
|------|-------------------|
| 表格 | `el-table` |
| 工具栏按钮 | `el-button` |
| 分组操作弹窗 | `el-dialog` |
| 隧道操作抽窗 | `el-drawer` |
| 状态标签 | `el-tag` |
| 表单 | `el-form` + `el-form-item` |
| 输入框 | `el-input` |
| 选择器 | `el-select` |
| 开关 | `el-switch` |
| 确认弹窗 | `ElMessageBox.confirm` |
| 消息提示 | `ElMessage` |

## API 对齐

前端 API 需与后端 `frps/internal/api/management.go` 保持一致：

### ProxyGroup
```typescript
interface ProxyGroup {
  id: number
  name: string
  token_id: string
  enabled: boolean
  client_access_mode: string
  tunnel_access_mode: string
  created_at: string
  updated_at: string
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
  created_at: string
  updated_at: string
}
```

### API 调整

| 接口 | 调整 |
|------|------|
| `/proxy-groups` GET | 返回 `{items: ProxyGroup[]}` |
| `/proxy-groups` POST | 返回 `{item: ProxyGroup, token: string}` |
| `/proxy-groups/{id}` PATCH | 返回 `{item: ProxyGroup}` |
| `/tunnels` GET | 返回 `{items: Tunnel[]}` |
| `/tunnels` POST | 返回 `{item: Tunnel}` |
| `/tunnels/{id}` PATCH | 返回 `{item: Tunnel}` |