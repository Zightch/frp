# 系统设置页面设计

更新时间：2026-04-25

## 1. 路由与入口

| 项目 | 值 | 说明 |
|------|-----|------|
| 父路由路径 | `/settings` | 系统设置容器页 |
| 父路由名 | `Settings` | 前端路由标识 |
| 入口位置 | `MainLayout` 侧边栏 | 文案为"系统设置" |
| 视图组件 | `src/views/SettingsView.vue` | 页面容器组件 |

当前页面已经接入：

- 登录态检查（由 MainLayout 继承）
- 侧边栏导航
- PC / 移动端适配

## 2. 页面结构

当前页面结构固定为两段：

1. 顶部 tabs 菜单栏
2. 子路由内容区

### 2.1 Tabs 菜单栏

当前 tabs 项：

| Tab 名称 | 路由 | 说明 |
|----------|------|------|
| `入口证书` | `/settings/entry-certificates` | 入口证书绑定配置 |
| 预留项 | - | 其他系统设置项预留 |

Tabs 行为：

- 使用 `el-tabs` 组件
- Tab 切换通过路由跳转实现
- 当前只有一个入口证书 tab

### 2.2 内容区

内容区由子路由组件填充，当前子路由：

| 子路由路径 | 子路由名 | 组件 |
|------------|----------|------|
| `/settings/entry-certificates` | `SettingsEntryCertificates` | `src/views/settings/EntryCertificates.vue` |

## 3. 入口证书设置

### 3.1 组件定位

| 项目 | 值 |
|------|-----|
| 组件路径 | `src/views/settings/EntryCertificates.vue` |
| 数据来源 | `certificateUsagesApi` |

### 3.2 数据接口

当前使用的接口：

- `GET /api/v1/settings/entry-certificates` - 获取入口证书绑定列表
- `PUT /api/v1/settings/entry-certificates/{usage_type}` - 绑定证书
- `DELETE /api/v1/settings/entry-certificates/{usage_type}` - 解绑证书

当前使用点：

| usage_type | 说明 |
|------------|------|
| `webui_https` | WebUI HTTPS 服务端证书 |
| `frpc_tls` | frpc 登录监听口 TLS 服务端证书 |

### 3.3 布局设计

入口证书项采用单行卡片布局，字段水平排列：

```
┌────────────────────────────────────────────────────────────────┐
│ WebUI HTTPS │ 已启用 │ 当前证书: server-cert │ [更换] [解绑] │
└────────────────────────────────────────────────────────────────┘
```

字段顺序：

| 位置 | 字段 | 说明 |
|------|------|------|
| 左侧 | 标题 | `WebUI HTTPS` / `frpc TLS` |
| 中左 | 状态标签 | `已启用` / `已禁用` / `未绑定` |
| 中右 | 当前证书 | 绑定的证书名称，未绑定时显示 `-` |
| 右侧 | 操作按钮 | `绑定` / `更换` / `解绑` |

### 3.4 状态与操作

| 状态 | 显示 | 操作按钮 |
|------|------|---------|
| `enabled` | 已启用（success 标签） | `更换` + `解绑` |
| `disabled` | 已禁用（info 标签） | `绑定` |
| `unbound` | 未绑定（info 标签） | `绑定` |
| `error` | 异常（danger 标签） | `绑定` |

### 3.5 绑定弹窗

点击绑定/更换按钮时弹出选择器：

- 使用 `el-select` 选择证书
- 只显示 `asset_type=certificate` 且 `key_present=true` 的证书
- 弹窗标题：`绑定入口证书`
- 弹窗内容：
  - 使用点（只读显示）
  - 证书选择器

### 3.6 WebUI HTTPS 特殊行为

绑定 `webui_https` 成功后：

- 当前页面协议为 `http://` 时，提示"WebUI 将切换到 HTTPS"
- 400ms 后自动跳转到同地址的 `https://`

解绑 `webui_https` 成功后：

- 当前页面协议为 `https://` 时，提示"WebUI 将切回 HTTP"
- 400ms 后自动跳转到同地址的 `http://`

### 3.7 移动端适配

- 入口证书项保持单行布局，按钮紧凑排列
- 绑定弹窗：使用 `el-drawer direction="btt"`（底部弹出）

## 4. 组件结构

```
src/views/
├── SettingsView.vue              # 系统设置容器页面
│   - 顶部 tabs 菜单栏
│   - 子路由出口
│
└── settings/
    └── EntryCertificates.vue     # 入口证书设置组件
        - 入口证书项列表
        - 绑定弹窗
```

## 5. 侧边栏导航变化

新增"系统设置"导航项：

```
分组配置
证书资产
系统设置    ← 新增
```

## 6. 证书资产页变化

从 `CertificateAssetsView.vue` 移除：

- 入口证书绑定卡片区域（`usageCards`）
- 绑定弹窗（`usageDialog`）
- 相关方法和状态

证书资产页保留：

- 证书资产列表
- 导入/生成/编辑/删除功能
- 详情抽屉
- 下载功能
