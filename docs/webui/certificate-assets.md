# 证书资产管理页面 UI 规范

更新时间：2026-04-24

本文档描述证书资产管理页面的设计规范。后端 API 已完成实现，前端页面待开发。

## 1. 路由与命名

| 项目 | 值 | 说明 |
|------|-----|------|
| 页面标题 | `证书资产` | 导航文案、页面标题统一使用该名称 |
| 路由名 | `CertificateAssets` | 前端内部路由标识 |
| 路由路径 | `/certificate-assets` | 新增独立路径 |
| 视图组件 | `CertificateAssetsView.vue` | 页面主组件 |

## 2. 页面结构

参考现有分组配置页面的布局模式，页面结构如下：

```text
┌──────────────────────────────────────────────────────────────┐
│  证书资产                                           [刷新]   │
├──────────────────────────────────────────────────────────────┤
│  ┌────────────────────────────────────────────────────────┐ │
│  │ 资产类型: [全部 ▼]  来源: [全部 ▼]       [导入][生成]  │ │
│  └────────────────────────────────────────────────────────┘ │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ 名称 │ 类型 │ 来源 │ 颁发者 │ 有效期 │ 状态 │ 操作    │ │
│  │ ────────────────────────────────────────────────────── │ │
│  │ web-cert │ 证书 │ 上传 │ Let's Encrypt │ 2026-05-24 │ 有效 │ 查看/删除 │
│  │ root-ca │ CA │ 生成 │ (自签) │ 2036-04-24 │ 有效 │ 查看/删除 │
│  │ expired-cert │ 证书 │ 上传 │ Old CA │ 2025-01-01 │ 已过期 │ 查看/删除 │
│  └────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

结构说明：

- 页面头部：标题和刷新按钮
- 筛选栏：资产类型下拉、来源下拉、操作按钮组
- 内容区：单列表格，撑满剩余高度

## 3. 筛选与操作

### 3.1 筛选条件

| 筛选项 | 选项 | 说明 |
|--------|------|------|
| 资产类型 | 全部 / 证书 / CA | 按 `asset_type` 筛选 |
| 来源 | 全部 / 上传 / 生成 | 按 `source` 筛选 |

筛选为前端本地过滤，不请求后端。

### 3.2 顶部操作按钮

| 按钮 | 说明 |
|------|------|
| 导入 | 打开"导入证书/CA"弹窗，支持上传和粘贴两种方式 |
| 生成 | 打开"生成证书/CA"弹窗 |

## 4. 资产列表表格

### 4.1 表格列定义

| 列名 | 字段来源 | 说明 |
|------|----------|------|
| 名称 | `name` | 资产名称 |
| 类型 | `asset_type` | `证书` 或 `CA`，使用 Tag 展示 |
| 来源 | `source` | `上传` 或 `生成`，使用 Tag 展示 |
| 颁发者 | 现场解析 | 证书的 `issuer CN`；CA 自签时显示"(自签)" |
| 有效期 | 现场解析 | `not_after` 日期，格式 `YYYY-MM-DD` |
| 状态 | 现场解析 | 基于当前时间和 `not_before`/`not_after` 计算 |
| 操作 | - | 查看、删除按钮 |

### 4.2 状态展示规则

| 状态 | 条件 | Tag 类型 |
|------|------|----------|
| 有效 | 当前时间在有效期内 | `success` |
| 即将过期 | 距离过期不足 7 天 | `warning` |
| 已过期 | 当前时间超过 `not_after` | `danger` |
| 未生效 | 当前时间早于 `not_before` | `info` |

### 4.3 派生信息获取

后端 API 返回的资产数据中包含原始 `crt` PEM 内容，前端需要：

- 使用 Web Crypto API 或纯 JS x509 解析库提取展示字段
- 解析内容：`subject CN`、`issuer CN`、`not_before`、`not_after`、`SAN`、`fingerprint`
- 解析逻辑统一封装在 `src/utils/certParser.ts`

## 5. 导入证书/CA

### 5.1 弹窗结构

使用 `el-dialog`，宽度 500px，标题"导入证书/CA"。

弹窗内使用 `el-tabs` 切换两种导入方式：

| Tab | 说明 |
|-----|------|
| 上传文件 | 通过文件选择器上传 `crt` 和 `key` 文件 |
| 粘贴 PEM | 手动粘贴 PEM 文本 |

### 5.2 上传文件 Tab

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 资产类型 | `el-select` | 是 | `证书` / `CA` |
| 名称 | `el-input` | 是 | 资产名称 |
| 备注 | `el-input` | 否 | 可选备注 |
| 证书文件 | `el-upload` | 是 | `.crt` / `.pem` 文件 |
| 私钥文件 | `el-upload` | 条件 | `.key` / `.pem` 文件；`证书` 类型时必填，`CA` 类型时可选 |

文件上传后，前端读取文件内容作为 PEM 文本，提交时发送 JSON 请求。

### 5.3 粘贴 PEM Tab

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 资产类型 | `el-select` | 是 | `证书` / `CA` |
| 名称 | `el-input` | 是 | 资产名称 |
| 备注 | `el-input` | 否 | 可选备注 |
| 证书内容 | `el-input type="textarea"` | 是 | PEM 格式证书文本 |
| 私钥内容 | `el-input type="textarea"` | 条件 | PEM 格式私钥文本；`证书` 类型时必填，`CA` 类型时可选 |

### 5.4 API 调用

- 上传和粘贴统一调用对应 API：
  - 上传文件：`POST /api/v1/certificate-assets/upload`
  - 粘贴 PEM：`POST /api/v1/certificate-assets/paste`
- 请求格式为 `multipart/form-data`（上传）或 `application/json`（粘贴）

### 5.5 错误处理

后端返回结构化错误，前端需要解析并展示：

| error_code | 说明 | 前端展示 |
|------------|------|----------|
| `validation_error` | 字段校验失败 | 展示 `details` 中的字段级错误 |
| `name_conflict` | 名称已存在 | 提示"该名称已存在，请更换" |
| `duplicate_content` | 证书内容重复 | 提示"该证书已存在，无需重复导入" |

## 6. 生成证书/CA

### 6.1 弹窗结构

使用 `el-dialog`，宽度 500px，标题"生成证书/CA"。

弹窗内使用 `el-tabs` 切换两种生成类型：

| Tab | 说明 |
|-----|------|
| 生成 CA | 生成新的 CA 证书 |
| 生成证书 | 使用现有 CA 签发证书 |

### 6.2 生成 CA Tab

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 名称 | `el-input` | 是 | 资产名称 |
| 备注 | `el-input` | 否 | 可选备注 |
| Common Name | `el-input` | 是 | CA 的 CN 字段 |
| 有效期(天) | `el-input-number` | 是 | 默认 3650（10年） |
| 是否可签发 | `el-switch` | 否 | 生成带私钥的 CA，可用于签发子证书 |

### 6.3 生成证书 Tab

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| 名称 | `el-input` | 是 | 资产名称 |
| 备注 | `el-input` | 否 | 可选备注 |
| 签发 CA | `el-select` | 是 | 选择现有可签发 CA（`asset_type=ca` 且有私钥） |
| Common Name | `el-input` | 是 | 证书的 CN 字段 |
| SAN 域名 | `el-input` | 否 | 多个域名用逗号分隔 |
| SAN IP | `el-input` | 否 | 多个 IP 用逗号分隔 |
| 有效期(天) | `el-input-number` | 是 | 默认 365（1年） |

### 6.4 API 调用

- 统一调用：`POST /api/v1/certificate-assets/generate`
- 请求体中包含 `generate_type: 'ca' | 'certificate'`

## 7. 查看资产详情

### 7.1 弹窗结构

使用 `el-drawer`，宽度 500px，标题"证书详情"或"CA 详情"。

### 7.2 详情内容

| 字段 | 说明 |
|------|------|
| 名称 | `name` |
| 类型 | `asset_type`，Tag 展示 |
| 来源 | `source`，Tag 展示 |
| 备注 | `remark`，无备注时显示"-" |
| 创建时间 | `created_at` |
| 更新时间 | `updated_at` |
| 上游 CA | `issuer_asset_id` 对应的资产名称，无则显示"(无)" |

**证书信息**（现场解析）：

| 字段 | 说明 |
|------|------|
| 主题 Subject | 完整 DN |
| 颁发者 Issuer | 完整 DN |
| 序列号 | 十六进制 |
| 指纹 SHA-256 | 十六进制，支持复制 |
| 有效期起始 | `not_before` |
| 有效期结束 | `not_after` |
| SAN 域名 | DNS SAN 列表 |
| SAN IP | IP SAN 列表 |

**私钥信息**：

- 仅显示"已存储"或"未存储"
- 不展示私钥内容

### 7.3 详情页操作

| 按钮 | 说明 |
|------|------|
| 复制证书 | 复制 `crt` PEM 内容到剪贴板 |
| 复制私钥 | 复制 `key` PEM 内容到剪贴板（无私钥时禁用） |
| 删除 | 删除该资产 |

## 8. 删除资产

### 8.1 普通删除

点击删除按钮时：

1. 先调用 `GET /api/v1/certificate-assets/{id}/delete-impact` 获取影响分析
2. 根据返回结果展示确认弹窗

### 8.2 删除影响分析

后端返回格式：

```typescript
interface DeleteImpactResponse {
  has_children: boolean
  children: Array<{ id: number; name: string; asset_type: string }>
}
```

### 8.3 确认弹窗

**无子证书时**：

```
确定删除证书资产"{name}"吗？此操作不可恢复。
```

**有子证书时**：

```
该证书/CA 被以下证书引用：
- child-cert-1 (证书)
- child-ca-1 (CA)

删除将同时删除以上子证书，此操作不可恢复。
确定继续删除吗？
```

弹窗增加"级联删除"复选框，默认勾选。

### 8.4 API 调用

- 普通删除：`DELETE /api/v1/certificate-assets/{id}`
- 级联删除：`DELETE /api/v1/certificate-assets/{id}?cascade=true`

## 9. API 类型定义

```typescript
// 证书资产
interface CertificateAsset {
  id: number
  name: string
  remark: string
  source: 'upload' | 'generated'
  asset_type: 'certificate' | 'ca'
  format_type: 'pem'
  crt: string           // PEM 内容
  crt_hash: string
  key: string           // PEM 内容，可为空
  issuer_asset_id: number | null
  created_at: string
  updated_at: string
}

// 证书派生展示信息（前端解析）
interface ParsedCertInfo {
  subject_cn: string
  subject_dn: string
  issuer_cn: string
  issuer_dn: string
  serial: string
  not_before: Date
  not_after: Date
  dns_sans: string[]
  ip_sans: string[]
  fingerprint: string
  is_ca: boolean
}

// 删除影响响应
interface DeleteImpactResponse {
  has_children: boolean
  children: Array<{ id: number; name: string; asset_type: string }>
}

// 生成请求
interface GenerateCARequest {
  name: string
  remark?: string
  common_name: string
  validity_days: number
  can_issue: boolean
}

interface GenerateCertRequest {
  name: string
  remark?: string
  issuer_asset_id: number
  common_name: string
  dns_sans?: string[]
  ip_sans?: string[]
  validity_days: number
}
```

## 10. 组件选用

| 场景 | Element Plus 组件 |
|------|-------------------|
| 资产列表 | `el-table` |
| 筛选下拉 | `el-select` |
| 导入弹窗 | `el-dialog` + `el-tabs` |
| 生成弹窗 | `el-dialog` + `el-tabs` |
| 详情抽屉 | `el-drawer` |
| 表单 | `el-form` + `el-form-item` |
| 输入框 | `el-input` |
| 数字输入 | `el-input-number` |
| 选择器 | `el-select` |
| 开关 | `el-switch` |
| 文件上传 | `el-upload` |
| 文本域 | `el-input type="textarea"` |
| 状态标签 | `el-tag` |
| 消息提示 | `ElMessage` |
| 确认弹窗 | `ElMessageBox.confirm` |
| 描述列表 | `el-descriptions` |

## 11. 证书解析工具

### 11.1 技术方案

使用纯 JavaScript 实现的 ASN.1/PEM 解析库，不依赖 Web Crypto API（兼容性考虑）。

推荐库：
- `asn1.js` 或 `@peculiar/asn1` - ASN.1 解析
- 或自行实现最小化 PEM/x509 解析

### 11.2 解析接口

```typescript
// src/utils/certParser.ts

export interface ParsedCert {
  subjectCN: string
  subjectDN: string
  issuerCN: string
  issuerDN: string
  serial: string
  notBefore: Date
  notAfter: Date
  dnsSANs: string[]
  ipSANs: string[]
  fingerprint: string
  isCA: boolean
}

export function parseCertificatePEM(pem: string): ParsedCert | null
export function calculateCertificateStatus(parsed: ParsedCert): 'valid' | 'expiring' | 'expired' | 'not_yet_valid'
export function formatDate(date: Date): string  // YYYY-MM-DD
```

### 11.3 错误处理

- PEM 解析失败时返回 `null`
- 调用方捕获 `null` 后展示"解析失败"

## 12. 路由集成

### 12.1 路由配置

在 `src/router/index.ts` 中新增：

```typescript
{
  path: '/',
  component: () => import('@/layouts/MainLayout.vue'),
  children: [
    // 现有路由...
    {
      path: 'certificate-assets',
      name: 'CertificateAssets',
      component: () => import('@/views/CertificateAssetsView.vue')
    }
  ]
}
```

### 12.2 侧边栏导航

在 `MainLayout.vue` 的菜单中新增：

```vue
<el-menu-item :index="{ name: 'CertificateAssets' }">
  <el-icon><Key /></el-icon>
  <span>证书资产</span>
</el-menu-item>
```

图标使用 Element Plus 内置的 `Key` 或 `Document` 图标。

## 13. 边界与限制

- 不实现证书内容的在线编辑（只读展示）
- 不实现私钥内容的展示（只显示是否存储）
- 不实现证书与隧道/分组的绑定关系展示
- 不实现证书格式转换（只支持 PEM）
- 前端解析证书仅用于展示，不参与业务逻辑

## 14. 错误状态

| 场景 | 处理 |
|------|------|
| 列表加载失败 | 显示错误文案和"重试"按钮 |
| 无资产 | 显示"暂无证书资产，请导入或生成" |
| 导入/生成失败 | `ElMessage.error` 展示后端返回的错误信息 |
| 解析证书失败 | 详情页展示"证书解析失败"提示 |
| 无可签发 CA | 生成证书时提示"请先导入或生成可签发 CA" |
