# CI 第三轮：六目标全绿（run `41647730697`）

日期：2026-11-03。commit `9e31605`（merge `feat/v1-foundation`）。

这一轮的目的不是"跑一次绿"，而是把三处原先记 **BLOCKED / PARTIAL** 的东西
变成**有真实证据的结论**：SEC07（mac/linux 凭证）、RPC05（Unix 进程组回收）、
以及 environment.md 里那张过时的"五目标无运行环境"表。

## 1. 结果

| Job | Runner | build | vet | test | race | checkptr | 交叉编译 | 耗时 |
|---|---|---|---|---|---|---|---|---|
| win-x64 | windows-2025 | PASS | PASS | PASS | PASS | PASS | PASS | 2m47s |
| win-arm64 | windows-11-arm | PASS | PASS | PASS | **BLOCKED** | PASS | PASS | 4m04s |
| mac-x64 | macos-15-intel | PASS | PASS | PASS | PASS | PASS | PASS | 3m01s |
| mac-arm64 | macos-15 | PASS | PASS | PASS | PASS | PASS | PASS | 2m04s |
| linux-x64 | ubuntu-24.04 | PASS | PASS | PASS | PASS | PASS | PASS | 1m38s |
| linux-arm64 | ubuntu-24.04-arm | PASS | PASS | PASS | PASS | PASS | PASS | 1m45s |
| frontend | ubuntu-24.04 | typecheck + build PASS | — | — | — | — | — | 17s |

win-arm64 的 `-race` 是**目标级 BLOCKED**（Go 工具链不支持 `windows/arm64` 的 race detector），
不是通过。该目标以 `-d=checkptr=2` 作为补充证据，明确不替代竞态检测。
任何报告不得写成"六目标 race 全通过"。

## 2. 这一轮真正解决了什么

### SEC07：从"功能缺失"到"六目标实测"

原先 mac/linux 的 `NewNativeStore` 直接返回 `CREDENTIAL_STORE_UNAVAILABLE`——
那不是环境缺失，是**根本没实现**。补上两套后端后，六个目标各自走真实 OS 库：

| 平台 | 后端 | 证据 |
|---|---|---|
| Windows x64/arm64 | DPAPI（`CryptProtectData`） | win-x64 / win-arm64 job |
| macOS x64/arm64 | Keychain（`/usr/bin/security`） | mac-x64 / mac-arm64 job |
| Linux x64/arm64 | Secret Service（`secret-tool`） | linux-x64 / linux-arm64 job |

`internal/credentials` 在 linux-x64 上耗时 0.162s（真实调用了 `secret-tool store/lookup/clear`
并跑了 `ps`），不是 skip。判据是硬的：设了 `MYAGENT_REQUIRE_NATIVE_CREDSTORE=1`，
后端不可用会直接 `t.Fatalf`，不可能"安静地绿"。

原始日志：`raw/ci-round3/linux-x64.test-ok.log`。

### RPC05：补上被 skip 掉的 Unix 路径

`process_tree_test.go` 新增三条跨平台用例，直接对 `processTree` 断言：

- `TestProcessTreeKillsOwnedOnly`：强杀后本树孙进程停止、**树外同形态进程仍存活**
  （后半条是关键——只有前半条的话，"按名字杀所有 fakeworker"的实现也能过）；
- `TestProcessTreeActiveCountTracksOwned`：`activeCount` 从 ≥1 收敛到 0
  （它决定 `CLEANUP_FAILED` 会不会触发，恒 0 或恒非 0 都是 bug）；
- `TestProcessTreeRejectsUnattachedKill`：已关闭的进程树不得返回 kill 成功。

`TestWorkerTerminatesGrandchild` 同时去掉非 Windows 的 skip。

## 3. CI 上抓出来的三个真缺陷

这一轮不是"一次就绿"，红的每一轮都指向实现或编排问题，全部改的是代码/配置：

| # | 现象 | 根因 | 修法 |
|---|---|---|---|
| 1 | linux/mac 上 `TestProcessTreeActiveCountTracksOwned` 5s 仍非 0 | `kill(-pgid, 0)` 对**僵尸**也返回成功，已被 SIGKILL 只等 reap 的成员被永久算作"存活"，`Close` 会误报 `CLEANUP_FAILED` | kill 探针只作快路径；平台实现剔除僵尸（Linux 读 `/proc/<pid>/stat`，macOS 用 `ps -A -o pgid=,stat=`） |
| 2 | linux 上 "Delete 应幂等" 红 | `secret-tool clear` 对不存在的条目返回 **1**，原实现当故障 | 1 视为成功（Delete 契约是幂等） |
| 3 | linux-arm64 的 **race** 步骤红，失败的是凭证测试 | `MYAGENT_REQUIRE_NATIVE_CREDSTORE=1` 设在 job 级，但 keyring 只在 test 步骤的 `dbus-run-session` 里起过 | 抽 `scripts/ci/with-keyring.sh`，test/race/checkptr 三条命令统一经由它 |

另有第 4 条是**测试自身的调用顺序**错：mac 上仍未通过，是因为测试没 reap 自己的直接子进程
（主进程僵尸留在进程组里）。真实调用方 `Worker.supervise` 在 `Close` 前必然 `cmd.Wait()`，
测试改为按同样顺序执行，并给失败信息加了进程组快照（pid/pgid/stat/comm）。

## 4. 对抗性验证

| 对象 | 手法 | 结果 |
|---|---|---|
| `process_windows.go` 的 Job Object assign | 短路 `AssignProcessToJobObject` | `TestProcessTreeKillsOwnedOnly` 与 `ActiveCount` 双双红（“owned 孙进程在强杀进程树后仍存活（心跳 47 → 104 字节）”） |
| `policybridge` 的令牌与 Host 校验 | 两处判断改为恒假 | 5 个子用例红 |
| Pi 侧策略扩展 | 从 argv 里去掉 `-e harness-policy.ts` | 真实 Pi 用例红（待审批 0 次，命令真的被执行） |
| 配置 CAS 的 checksum | 判断短路 | `TestCASRejectsExternalEdit` 红 |
| DPAPI 加密 | `sealed = plain` | SEC07 用例红在扫描断言 |

## 5. 仍未解决的 BLOCKED

| 项 | 状态 | 为什么不能"解决" |
|---|---|---|
| win-arm64 的 `-race` | **目标级 BLOCKED** | Go 工具链不支持 `windows/arm64` 的 race detector。这是上游能力边界，不是本项目缺陷；无法通过改代码消除。已用 checkptr 补 unsafe 误用的覆盖。 |
| 六目标的 GUI 运行（WebView2 / WKWebView / WebKitGTK） | **BLOCKED** | CI runner 没有 display（Linux 无 X11/Wayland，mac/win 无交互桌面），本机 Win10 也低于基线 Win11 24H2。要等 C01 应用壳落地后单独立项。 |
| G0 门禁 | **BLOCKED** | 见 §6。 |

## 6. 距离 G0 还差什么

G0 不是"CI 绿了"就解锁。按验收矩阵，它还缺：

- **SEC02 / SEC03 / SEC08 仍是 PARTIAL**：bash 与 external-MCP 两条通道有真实双跑对照，
  但 `write`/`edit`、扩展工具、codemode 嵌套调用、项目 `.pi/mcp.json`、`.agents/skills`、
  祖先目录发现没有逐条真实验证。
- **`internal/harnessmcp` 未实现**：自有 MCP server（loopback + 随机令牌 + Host/Origin）
  还不存在，`TestAllToolPathsUsePolicy` 因此无法覆盖"自有 MCP"这条通道。
- **`policybridge` 尚未接入 RunService**（属 F05）。

## 7. 触发策略变更（成本）

从本轮起 CI 只在 **main 分支 push** 时触发；`pull_request` 触发已移除。
日常开发走 `feat/v1-foundation`，推送不消耗 CI 分钟；需要验证分支时手动
Run workflow（`workflow_dispatch` 可选分支），验证通过再合并。
