# 证书资产

当前 `frps` 已经具备统一证书 / CA 资产层，但还没有把证书真正绑定到监听入口。

## 当前已实现范围

- 统一管理 `certificate` 和 `ca` 两类资产
- 支持 `upload`、`paste`、`generate`
- 支持查看资产详情和链信息
- 支持编辑资产 `name`、`remark`
- 支持删除影响分析和级联删除
- 支持下载选项查询和下载接口

当前尚未实现：

- WebUI HTTPS 证书绑定
- `7000` 登录口 TLS 证书绑定

对应设计见 [../design/certificate-binding.md](../design/certificate-binding.md)。

## 资产来源

- `source=upload`
  - 文件上传
  - 手动粘贴 PEM
- `source=generated`
  - 生成 CA
  - 使用现有 CA 或新 CA 签发证书

## 当前输入方式

已实现接口：

- `GET /api/v1/certificate-assets`
- `POST /api/v1/certificate-assets/upload`
- `POST /api/v1/certificate-assets/paste`
- `POST /api/v1/certificate-assets/generate`
- `PATCH /api/v1/certificate-assets/{id}`
- `GET /api/v1/certificate-assets/{id}/delete-impact`
- `DELETE /api/v1/certificate-assets/{id}`
- `GET /api/v1/certificate-assets/{id}/download-options`
- `GET /api/v1/certificate-assets/{id}/download`

`PATCH` 当前只允许修改：

- `name`
- `remark`

## 当前模型边界

- 资产表只保存证书原文、私钥和最小元数据
- 生成资产进入树状关系表
- 上传资产保留原始 PEM 结构，不进入生成树下载重组
- 当前设计只支持树状单父关系，不支持多父、交叉签名或 AIA 自动补链入库

## 当前上传与生成规则

- 生成资产必须是原子节点，一条资产只存一张证书
- 上传资产允许保留原始多证书 PEM 结构
- 上传资产仍必须能通过“资产内嵌链、数据库根 CA、系统 CA”完成有效链校验
- 只有 `source=generated` 且 `asset_type=ca` 且 `key` 非空的资产，当前才能用来签发证书

## 当前下载语义

生成资产：

- 默认只下载当前节点
- 支持从当前节点向上选祖先，下载单条证书链
- 支持选择某个 CA 节点的部分子树或整棵树
- HTTP 响应统一为 ZIP

上传资产：

- 只允许按原始结构下载
- 不支持补数据库上游
- 不支持裁剪链或树状选择

打包规则：

- `crt` 聚合、ZIP 组装都在 `frps` 内存完成
- 不落盘
- 不调用外部工具

## 当前生成算法

当前已支持：

- `ecdsa`
- `rsa`
- `ed25519`

当前规则：

- `ecdsa` 允许 `256 / 384 / 521`
- `ed25519` 固定为 `256`
- `rsa` 允许管理员输入任意 `>= 2048` 且为 `8` 的倍数的长度
