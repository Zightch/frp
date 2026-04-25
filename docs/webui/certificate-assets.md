# 证书资产管理页面当前实现

更新时间：2026-04-25

本文档描述 `CertificateAssetsView.vue` 的当前已落地行为，不是前瞻式设计稿。

## 1. 路由与入口

| 项目 | 值 | 说明 |
|------|-----|------|
| 路由路径 | `/certificate-assets` | 相对 WebUI 基址生效 |
| 路由名 | `CertificateAssets` | 前端路由标识 |
| 入口位置 | `MainLayout` 侧边栏 | 文案为“证书资产” |
| 视图组件 | `src/views/CertificateAssetsView.vue` | 页面主组件 |

当前页面已经接入：

- 登录态检查
- 侧边栏导航
- PC / 移动端弹层适配

## 2. 数据来源

页面不再自行解析 PEM 证书内容，列表和详情直接消费后端返回的派生字段。

当前使用的接口：

- `GET /api/v1/certificate-assets`
- `PATCH /api/v1/certificate-assets/{id}`
- `POST /api/v1/certificate-assets/upload`
- `POST /api/v1/certificate-assets/paste`
- `POST /api/v1/certificate-assets/generate`
- `GET /api/v1/certificate-assets/{id}/delete-impact`
- `DELETE /api/v1/certificate-assets/{id}`
- `DELETE /api/v1/certificate-assets/{id}?cascade=true`
- `GET /api/v1/certificate-assets/{id}/download-options`
- `GET /api/v1/certificate-assets/{id}/download`

当前列表项字段以 `src/api/index.ts` 中的 `CertificateAsset` 为准，包含：

- 基础字段：`id`、`name`、`remark`、`source`、`asset_type`、`format_type`
- 关系字段：`issuer_asset_id`、`issuer_name`
- 证书派生字段：`common_name`、`subject`、`issuer`、`serial_number`、`not_before`、`not_after`、`dns_names`、`ip_addresses`
- 状态字段：`key_present`、`can_issue`、`is_self_signed`、`chain_length`

## 3. 页面结构

当前页面结构固定为三段：

1. 页面标题和刷新按钮
2. 筛选栏
3. 资产列表表格

筛选栏包含：

- 资产类型：`all / certificate / ca`
- 来源：`all / upload / generated`
- 顶部操作：`导入`、`生成`

筛选为前端本地过滤，不额外请求后端。

移动端约定：

- 导入弹窗、生成弹窗、下载弹窗在移动端切换为底部 `el-drawer`
- 详情使用 `el-drawer`，移动端从底部弹出，桌面端从右侧弹出

## 4. 列表与详情

### 4.1 列表列

| 列名 | 来源 | 说明 |
|------|------|------|
| 名称 | `name` | 资产名称 |
| 备注 | `remark` | 无备注时显示 `-` |
| 类型 | `asset_type` | `CA` / `证书` |
| 来源 | `source` | `上传` / `生成` |
| 颁发者 | `issuer_name` / `issuer` / `is_self_signed` | 自签显示“(自签)” |
| 有效期 | `not_after` | 页面按 `YYYY-MM-DD` 显示 |
| 状态 | `not_before` + `not_after` | `有效 / 即将过期 / 已过期 / 未生效 / 未知` |
| 操作 | - | `查看`、`编辑`、`删除` |

### 4.2 详情抽屉

详情当前展示三块信息：

- 基础信息：名称、类型、来源、备注、上游 CA、创建时间、更新时间
- 证书信息：CN、Subject、Issuer、序列号、有效期、状态、SAN、自签标记、链长度
- 私钥信息：是否存储、是否可签发

当前详情操作有三个：

- `编辑`
- `下载`
- `删除资产`

当前未实现：

- 复制证书
- 复制私钥
- 在线编辑 PEM

### 4.3 编辑资产

当前只允许编辑两个字段：

- `name`
- `remark`

不允许编辑：

- 证书 / 私钥内容
- 资产类型
- 来源
- 上游关系
- 生成参数

编辑入口当前有两处：

- 列表行操作中的 `编辑`
- 详情抽屉底部的 `编辑`

## 5. 导入

导入弹层包含两个 Tab：

- `上传文件`
- `粘贴 PEM`

### 5.1 上传文件

字段如下：

- `name`
- `remark`
- `crt` 文件
- `key` 文件，可选

当前上传行为：

- 前端直接提交 `multipart/form-data`
- 不再让管理员手选资产类型
- 资产类型由后端根据首张证书自动识别

### 5.2 粘贴 PEM

字段如下：

- `name`
- `remark`
- `crt`
- `key`，可选

当前粘贴行为：

- 前端直接提交 JSON
- 资产类型仍由后端自动识别

### 5.3 当前错误处理

页面统一用 `ElMessage.error` 直接展示后端错误文案。

当前常见错误码包括：

- `certificate_asset_validation_failed`
- `certificate_asset_name_conflict`
- `certificate_asset_duplicate_content`

## 6. 生成

生成弹层包含两个 Tab：

- `生成 CA`
- `生成证书`

### 6.1 生成 CA

当前字段：

- `name`
- `remark`
- `issuer_asset_id`
- `common_name`
- `key_algorithm`
- `key_bits`
- `validity_days`

行为约定：

- 上游 CA 留空时生成自签根 CA
- 选择上游 CA 时生成中间 CA
- 只允许选择 `can_issue=true` 的 CA

### 6.2 生成证书

当前字段：

- `name`
- `remark`
- `issuer_asset_id`
- `common_name`
- `key_algorithm`
- `key_bits`
- `dns_names`
- `ip_addresses`
- `validity_days`

行为约定：

- `issuer_asset_id` 必填
- `dns_names`、`ip_addresses` 在前端以逗号分隔输入，提交时拆成数组

### 6.3 当前算法与长度

当前支持三类算法：

- `ecdsa`
- `rsa`
- `ed25519`

长度规则：

- `ecdsa`：固定选项 `256 / 384 / 521`
- `ed25519`：固定 `256`
- `rsa`：管理员输入，必须 `>= 2048` 且为 `8` 的倍数

当前默认值：

- 算法默认 `ecdsa`
- 长度默认 `256`

## 7. 下载

下载前，页面先调用 `download-options` 拉取当前资产允许的下载模式。

### 7.1 当前下载模式

| 模式 | 适用资产 | 说明 |
|------|----------|------|
| `original` | 上传资产 | 原样下载当前资产保存的 PEM 结构 |
| `single` | 生成资产 | 下载当前节点 |
| `chain` | 生成资产 | 下载“当前节点到某个祖先”的单条证书链 |
| `tree` | 生成 CA | 下载该 CA 节点的部分子树或整棵树 |

### 7.2 当前页面交互

- `chain` 模式下，页面展示祖先选择器
- `tree` 模式下，页面展示树形勾选器
- 未显式选择祖先或树节点时，后端按默认行为处理

### 7.3 当前下载产物

当前所有下载模式统一返回 ZIP。

具体约定：

- `original`：ZIP 内包含原始 `.crt`，有私钥时额外包含 `.key`
- `single`：ZIP 内包含当前节点 `.crt`，有私钥时额外包含 `.key`
- `chain`：ZIP 内包含聚合后的 `*-chain.crt`，有私钥时额外包含当前节点 `.key`
- `tree`：ZIP 内按节点拆分出多个 `.crt` / `.key`

当前前端只负责触发浏览器下载，不做 ZIP 解包。

## 8. 删除

删除前，页面固定先调用：

- `GET /api/v1/certificate-assets/{id}/delete-impact`

返回结构以 `CertificateAssetDeleteImpact` 为准：

- `target`
- `affected_items`
- `requires_confirmation`
- `warning_message`

### 8.1 当前删除确认语义

无影响项时：

- 直接提示“确定删除”

有影响项时：

- 页面展示 `affected_items`
- 确认后自动以 `cascade=true` 发起删除
- 当前没有额外的“级联删除”复选框

### 8.2 当前影响范围

`affected_items` 不只包含生成树下游，还包括删除后会导致校验失效的其他资产，例如：

- 生成资产的直接 / 间接子节点
- 依赖数据库 CA 补链的上传资产

## 9. 当前 API 类型摘要

```ts
type CertificateAssetGenerateKeyAlgorithm = 'ecdsa' | 'rsa' | 'ed25519'

type CertificateAssetDownloadMode = 'original' | 'single' | 'chain' | 'tree'

interface CertificateAssetGeneratePayload {
  name: string
  remark?: string
  asset_type: 'certificate' | 'ca'
  issuer_asset_id?: number
  common_name: string
  validity_days: number
  dns_names?: string[]
  ip_addresses?: string[]
  key_algorithm?: CertificateAssetGenerateKeyAlgorithm
  key_bits?: number
}

interface CertificateAssetDeleteImpact {
  target: CertificateAsset
  affected_items: Array<{
    item: CertificateAsset
    depth: number
  }>
  requires_confirmation: boolean
  warning_message?: string
}

interface CertificateAssetDownloadOptions {
  target: CertificateAsset
  modes: Array<{
    mode: CertificateAssetDownloadMode
    default: boolean
  }>
  chain_items?: Array<{
    item: CertificateAsset
    depth: number
  }>
  tree_items?: Array<{
    item: CertificateAsset
    parent_asset_id?: number
    depth: number
  }>
}
```

## 10. 当前限制

- 不支持在页面手工指定导入资产类型，统一由后端识别
- 不支持复制证书 / 私钥内容
- 不支持在线编辑已存在资产
- 不支持在页面展示 tunnel 对证书资产的引用关系
- 不支持下载时修改上传资产结构；上传资产只允许 `original`
