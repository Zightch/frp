# 当前 Todo

## 当前状态

- “限速策略”最小闭环已经完成。
- 当前闭环已覆盖：
  - `frps` 首次登录后的限速执行
  - 在线更新策略后的 reload 生效
  - 单端口 TCP / UDP
  - `independent` / `shared`
  - 共享桶竞争
  - Go 回归和 Python e2e
- 当前归档见：`docs/progress/2026-05-01.md`

## 当前没有继续排期的事项

- 本轮先不继续扩到：
  - TCP range / UDP range 限速
  - burst 对外配置
  - 限速统计和观测页
  - 多策略叠加
  - 一个隧道绑定多个策略
  - 旧版本兼容层

## 当前已知残留

- `cd frps && go test ./... -count=1` 仍会受 `internal/app` 两条历史启动时序测试波动影响：
  - `TestAppRunDelaysManagementAPIUntilInitialRuntimeScanCompletes`
  - `TestAppRunDelaysInitialRuntimeScanUntilCertificatePreparationCompletes`
- 这两条当前仍按历史噪音记录，未归因到限速功能。

## 下一轮入口

- 如果下一轮继续做限速，优先再决定是否需要：
  - 正式文档同步到回归入口和测试文档
  - range tunnel 限速
  - 限速观测和统计能力

## 进度归档入口

- 当前轮待办只看：`docs/tmp/todo.md`
- 已完成事项归档到：`docs/progress/YYYY-MM-DD.md`
- 轮换规则见：`docs/workflow.md`
- 归档说明见：`docs/progress/README.md`
