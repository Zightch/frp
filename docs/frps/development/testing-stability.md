# frps 稳定性补充层执行说明

本文档只定义“稳定性补充层”的统一执行入口，不再重复可测性基建本身；hook、fake world、manual clock、场景 runner 和不变量断言仍以 [testing-infrastructure.md](./testing-infrastructure.md) 为准。

## 1. 目标

在确定性场景已经收口之后，再用同一套场景脚本补跑下面 6 类回归：

- `-race`
- 多 `GOMAXPROCS`
- 高频配置抖动
- 长稳 soak
- 双平台基线
- 资源压力

这些入口都必须继续复用正式测试中的 `ObservedState`、可注入 fake 边界和断言，不允许退回“shell 脚本 + 日志 grep”。

## 2. 统一入口

稳定性补充层统一通过 `frps/cmd/stabilitymatrix` 执行：

```powershell
cd frps
go run ./cmd/stabilitymatrix -list
go run ./cmd/stabilitymatrix -profile platform
go run ./cmd/stabilitymatrix -profile config-churn
go run ./cmd/stabilitymatrix -profile all
```

工具默认行为：

- 串行执行 `go test` 命令，不把多个依赖本机端口或真实 TCP 可见性的包并发跑起来。
- 每个 profile 都直接调用现有正式测试；成败判定仍由测试内的状态断言和不变量断言给出。
- `platform` 不是单独维护一套脚本，而是把同一批场景作为 Windows / Linux / WSL 共同基线；在不同平台重复跑同一命令即可比较业务结论。

控制面故障和竞争场景的递归必跑入口仍是：

```powershell
cd frps
go test -tags testhooks ./internal/control/...
```

当测试包层级调整时，必须同步 `stabilitymatrix` 中对应 profile 的 package target，避免补充层只跑到空包。

常用参数：

- `-list`：列出全部 profile。
- `-profile <name[,name...]>`：执行指定 profile；`all` 会按固定顺序跑完整层。
- `-dry-run`：只打印要执行的命令，不实际运行。
- `-go <path>`：切换 `go` 可执行文件。

## 3. Profile 清单

- `race`
  - 对启动门闩、端口冲突 / 热更新主线场景和稳定性场景加 `-race`。
- `gomaxprocs`
  - 对同一批场景加 `-cpu 1,2,4`，确认不同调度档位下结论一致。
- `config-churn`
  - 聚焦高频 `RefreshGroup()`、空/满配置切换和高 churn listener 回滚。
- `soak`
  - 使用 fake time 在单进程内推进长周期恢复/失效循环，并检查资源是否回落。
- `platform`
  - 当前平台的基线执行入口；同命令应在 Windows 和 Linux/WSL 上复用。
- `resource-pressure`
  - 放大 tunnel 数量和 refresh 周期，验证 listener / pending / runtime issue 仍可收敛。

## 4. 环境旋钮

个别 profile 会通过环境变量放大负载；默认值已经内置在命令工具里，必要时也可以手动覆写：

```powershell
$env:FRPS_STABILITY_CHURN_VERSIONS = "24"
$env:FRPS_STABILITY_SOAK_CYCLES = "64"
$env:FRPS_STABILITY_RESOURCE_TUNNELS = "48"
$env:FRPS_STABILITY_RESOURCE_CYCLES = "6"
go run ./cmd/stabilitymatrix -profile config-churn,soak
```

这些旋钮只改变场景规模，不改变断言标准。

## 5. 当前落地场景

稳定性补充层当前直接覆盖以下正式测试：

- `frps/internal/app/startup_scenario_test.go`
  - 首轮扫描门闩、控制端口开放、管理 API 首次可见
- `frps/internal/control/wiring/*_scenario_test.go`
  - 端口冲突检查 / 热更新交互的确定性、异常输入、恢复故障和竞争窗口
- `frps/internal/control/wiring/stability_scenario_test.go`
  - `TestServerStabilityScenarioConfigChurnConverges`
  - `TestServerStabilityScenarioFakeTimeSoakKeepsResourcesBounded`
  - `TestServerStabilityScenarioResourcePressureRefreshAcrossManyTunnelsConverges`

后续新增稳定性场景时，优先把它们挂进 `cmd/stabilitymatrix` 的现有 profile，而不是另起新的零散脚本入口。
