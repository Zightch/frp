# frps 文档索引

`docs/frps/` 按“设计 / 功能 / 技术 / 开发”拆分。

## 目录

- `design/`
  - 设计边界、实现约束和演进说明。
- `features/`
  - 当前已经进入真实代码和测试链路的行为说明。
- `technical/`
  - 当前代码结构、数据模型、接口和控制面实现边界。
- `development/`
  - 本地运行、WebUI 开发、测试调试与稳定性入口。

## 推荐阅读顺序

1. [功能总览](./features/overview.md)
2. [管理面与 WebUI](./features/management-surface.md)
3. [`proxy_group` 与隧道管理](./features/group-and-tunnel-management.md)
4. [控制面与数据面](./features/control-and-data-plane.md)
5. [证书资产](./features/certificate-assets.md)
6. [架构与模块](./technical/architecture.md)
7. [数据模型](./technical/data-model.md)
8. [控制面技术设计](./technical/control-plane.md)
9. [开发入口](./development/getting-started.md)

## 当前重点设计项

- [证书绑定设计：入口证书与 Tunnel TLS](./design/certificate-binding.md)
- [限速策略设计](./design/rate-policy.md)

根目录不再保留旧版平铺文档；请直接从本索引进入。
