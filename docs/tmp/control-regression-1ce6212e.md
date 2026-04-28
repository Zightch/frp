# control 重构对比记录：1ce6212e

## 背景

- 当前版本：`134de64 refactor: move frps control assembly into wiring` 之后。
- 对比基线：`1ce6212e 删除sum同步`。
- 目标：标记当前 `-tags testhooks` 隐藏场景失败，并检查相对 `1ce6212e` 是否存在功能损失或代码缺陷。

## 已标记问题：testhooks 隐藏场景

当前命令：

```powershell
go test -tags testhooks ./internal/control/wiring
```

现象：

- 当前版本可以编译并开始运行隐藏场景。
- 失败集中在 refresh / runtime scan / recovery / pending config 的竞争窗口：
  - 旧 pending session 在 refresh 后没有按隐藏测试预期主动退出。
  - 部分场景预期触发 `control.config_push.before_write`，但当前实现未发出对应 `config.push`。
  - 后续出现 `read pipe: i/o timeout` 或 observed state 等待超时，属于前置 close / push 未发生后的连锁结果。

基线对照：

```powershell
go test -mod=mod -tags testhooks ./internal/control
```

在 `1ce6212e` 上同样不是绿色基线，单测会失败，例如：

- `TestServerScenarioLateDelayedAckFromClosedSessionDoesNotPolluteReplacementSession`
- 失败点：`write frame: scripted conn raw io is unsupported; use FrameIO`

因此该问题不能直接判定为本轮 facade / wiring 迁移引入的功能回归；它更像是一组长期未纳入默认验收、且自身需要重新对齐当前 FrameIO / recovery 语义的隐藏竞争场景。

## 默认验收对比

`1ce6212e` 使用 `-mod=mod` 跑默认测试：

```powershell
go test -mod=mod ./internal/control/...
go test -mod=mod ./internal/app ./internal/api/...
go test -mod=mod ./...
```

结果：通过。

当前版本默认测试：

```powershell
go test ./internal/control/...
go test ./internal/app ./internal/api/...
go test ./...
```

结果：通过。

结论：默认产品测试链路没有发现功能损失。

## 对外入口对比

`internal/app` / `internal/api` 当前仍只依赖根包：

```text
github.com/zightch/frp/frps/internal/control
```

没有直接依赖 `control/wiring`。

从 `1ce6212e` 到当前版本，根包稳定入口仍保留：

- `Options`
- `Server`
- `NewServer`
- `ListenAndServe`
- `Shutdown`
- `EnsureInitialRuntimeScan`
- `RefreshGroup`
- `ObserveState`
- `ConfigureControlTLS`
- `ClearControlTLS`
- `TunnelRuntimeIssues`
- `Repository` / `SQLRepository` / `GroupRuntime` / `ConfigSnapshot`
- listener / bind 相关兼容 alias
- `NewRepository`
- `NewNetListenerFactory`
- `NewScriptedListenerFactory`

本次对比发现并修复一个兼容缺口：

- `1ce6212e` 根包导出了 `control.Logger`。
- facade 收口后遗漏了该 alias。
- 当前已补回：`type Logger = wiring.Logger`。

## 功能损失判断

当前没有发现 app/api 可见功能损失：

- 控制面启动、shutdown、首轮 runtime scan gate 仍由 `control.Server` 暴露。
- 在线 `RefreshGroup` 入口仍稳定。
- 观测 `ObserveState` 入口仍稳定。
- control TLS runtime 入口仍稳定。
- repository / listener factory 测试入口仍稳定。
- 默认全量测试通过，说明 app/api 编译链路和默认控制面行为没有断。

## 代码缺陷 / 风险点

- `-tags testhooks` 隐藏场景不是可用的重构前绿色基线，但当前已经进入可编译运行状态，暴露的 refresh / scan / recovery 竞争语义仍值得单独收口。
- 这些隐藏场景需要单独决策：是更新测试预期以匹配当前 runtime recovery 策略，还是恢复测试期望的“pending refresh 强制断开旧 session / scan recovery 必发 push”语义。
- `1ce6212e` 删除了部分 `go.sum` 条目，干净 worktree 下需要 `-mod=mod` 才能完成默认测试对照；当前版本默认 `go test ./...` 可直接通过。

## 建议后续动作

- 单独开一阶段处理 `testhooks` 场景，不和 facade / wiring 迁移混在同一个提交里。
- 先按场景分类：
  - scripted FrameIO 测试写法修正。
  - pending refresh 是否应关闭旧 session。
  - scan recovery 与 refresh recovery 去重策略。
  - config push hook 触发条件。
- 决策后再补或改测试，避免把隐藏测试旧预期当作未经确认的产品语义。
