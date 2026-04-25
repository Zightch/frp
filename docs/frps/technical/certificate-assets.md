# frps 证书资产技术设计

## 当前职责

`internal/certassets` 当前负责：

- 资产仓储
- 上传 / 粘贴 / 生成服务
- PEM 解析与校验
- 系统 CA 与数据库资产准备
- 下载产物组装

## 当前输入模型

后端统一支持三条入口：

- upload
- paste
- generate

无论入口如何，最终都统一进证书资产服务，由后端负责：

- 解析 PEM
- 判断 `asset_type`
- 计算 `crt_hash`
- 进行链校验和重复内容校验

## 当前校验规则

- `crt` 必须是有效 PEM 证书内容
- `key` 如果存在，也必须是有效 PEM 私钥
- `crt` 至少要解析出一张证书
- `asset_type=certificate` 且 `key` 非空时，`key` 必须和叶子证书匹配
- `asset_type=ca` 时，首张证书必须是 CA
- `source=generated` 时，`crt` 必须且只能包含一张证书
- `source=upload` 时，`crt` 可以包含多张证书，但必须保留原顺序
- 上传资产必须能通过“资产自带链、数据库根 CA、系统 CA”完成有效链验证

## 当前 API / View 层补充

当前管理 API 在资产详情和列表里会现场解析并返回：

- `issuer_name`
- `common_name`
- `subject`
- `issuer`
- `serial_number`
- `not_before`
- `not_after`
- `dns_names`
- `ip_addresses`
- `key_present`
- `can_issue`
- `is_self_signed`
- `chain_length`

这些展示字段不持久化入库。

当前返回模型里还保留了 `issuer_asset_id`，它本质上是“当前直接上游”的派生视图，而不是回到旧的内联字段模型。

## 当前下载实现

下载接口支持：

- `download-options`
- `download`

打包边界当前已经固定：

- `crt` 聚合在内存完成
- ZIP 组装在内存完成
- 不写磁盘
- 不调用外部工具

生成资产：

- 可下载当前节点
- 可下载单条上游链
- 可下载部分树或整棵树

上传资产：

- 只按原始结构下载

## 与下一阶段绑定能力的关系

当前证书资产层只负责“资产本身”。

下一阶段新增的“证书绑定”层只负责：

- 某个入口正在使用哪张叶子证书
- 运行时如何把叶子和上游链组装成 TLS 对象

该层见 [../design/certificate-binding.md](../design/certificate-binding.md)。
