# 仓库级回归入口

更新时间：2026-04-29

本文档是当前仓库的统一回归入口，只收口已经存在且当前真实有效的 Go 定向测试、模块级 `go test` 和 Python e2e 命令矩阵。

边界固定如下：

- 不新增自动化脚本。
- 不替代 `docs/frps/development/testing-debugging.md` 与 `docs/frpc/testing-debugging.md` 的细节说明。
- 日常回归以本文为入口；子项目内部测试设计和稳定性补充层细节仍以对应开发文档为准。

## 1. 使用约定

- Go 命令在对应模块目录内执行。
- Python e2e 命令在仓库根目录执行。
- 每完成一个最小代码步骤，先按改动面跑对应定向 Go 测试；阶段收束或准备归档时，再跑模块级 `go test ./...` 和核心 Python e2e。
- 涉及 `frps/internal/control` 的状态机、故障注入、竞争窗口或稳定性改动时，必须跑 `go test -tags testhooks ./internal/control/...`。
- 如果设置了 `FRPS_TEST_MYSQL_DSN`，`frps` 的 Go 回归只能串行执行，统一显式加 `-p 1 -parallel 1`，避免多个测试包共享同一 MySQL 库时互相删表或污染数据。
- 如果当前改动只是 `docs/`、`todo` 或进度归档文档，可以不额外执行代码回归。

## 2. 当前核心 Go 定向回归

### 2.1 `frps`

常用定向回归入口：

```powershell
cd frps
go test ./internal/app
go test -tags testhooks ./internal/control/...
```

`frps` 模块级总回归：

```powershell
cd frps
go test ./...
go test -tags testhooks ./...
```

如果当前回归切到 MySQL：

```powershell
cd frps
$env:FRPS_TEST_MYSQL_DSN="frps:123456@tcp(127.0.0.1:3306)/frps"
go test -p 1 -parallel 1 ./...
go test -p 1 -parallel 1 -tags testhooks ./...
```

稳定性补充层入口：

```powershell
cd frps
go run ./cmd/stabilitymatrix -list
go run ./cmd/stabilitymatrix -profile platform
go run ./cmd/stabilitymatrix -profile all
```

控制面故障和竞争场景的直接必跑基线仍是 `go test -tags testhooks ./internal/control/...`；`stabilitymatrix` 用于补跑 `race`、多 `GOMAXPROCS`、配置抖动、soak、平台基线和资源压力 profile。

### 2.2 `frpc`

常用定向回归入口：

```powershell
cd frpc
go test ./internal/client
```

`frpc` 模块级总回归：

```powershell
cd frpc
go test ./...
```

## 3. 当前核心 Python e2e 矩阵

以下命令组成当前必须守住的最小端到端闭环：

```powershell
python test/e2e_tcp_single.py --scenario happy_path
python test/e2e_tcp_range.py
python test/e2e_udp_single.py --scenario happy_path
python test/e2e_udp_single.py --scenario idle_cleanup
python test/e2e_udp_range.py
python test/e2e_management_webui.py
python test/e2e_management_webui.py --webui-prefix /frps
```

对应覆盖边界如下：

- `e2e_tcp_single.py --scenario happy_path`：TCP 单端口最小正向链路。
- `e2e_tcp_range.py`：TCP range 偏移映射，并同轮确认 TCP single 不回退。
- `e2e_udp_single.py --scenario happy_path`：UDP 单端口最小正向链路。
- `e2e_udp_single.py --scenario idle_cleanup`：`frps` 侧空闲约 `30s` 后清理 UDP 会话，并向 `frpc` 下发 `udp.close`。
- `e2e_udp_range.py`：UDP range 偏移映射，并同轮确认 UDP single 不回退。
- `e2e_management_webui.py`：管理密钥初始化、challenge 登录、会话恢复、`auth.json` 删除重置、最小管理 CRUD 和 WebUI 子路径别名。

## 4. 扩展回归入口

以下命令不是每次最小步骤都必须执行，但在对应改动面上应作为补充回归入口：

```powershell
python test/e2e_tcp_single.py --scenario bad_token
python test/e2e_tcp_single.py --scenario disabled_group
python test/e2e_tcp_single.py --scenario disabled_tunnel
python test/e2e_tcp_single.py --scenario local_unavailable
python test/e2e_tcp_perf.py
python test/e2e_tcp_perf.py --stability-concurrency 128 --stability-duration 30
python test/e2e_tcp_perf.py --transfer-concurrency 8 --transfer-bytes-per-connection 67108864
```

当前口径固定如下：

- TCP 单端口负向场景只在改动登录、`proxy_group` 启停、隧道启停或本地目标失败路径时重点补跑。
- `e2e_tcp_perf.py` 属于代码健康压测基线，不属于每次日常最小回归的阻塞门槛。

## 5. 当前建议执行顺序

1. 先按改动面跑对应 Go 定向测试。
2. 控制面改动必须补跑 `go test -tags testhooks ./internal/control/...`。
3. 当前子步骤收束后，跑受影响模块的 `go test ./...`；发布前再跑 `go test -tags testhooks ./...`。
4. 准备归档当前阶段或关闭当前轮时，跑一遍核心 Python e2e 矩阵。
5. 涉及并发、恢复、资源或平台稳定性风险时，再跑 `go run ./cmd/stabilitymatrix -profile all`。
