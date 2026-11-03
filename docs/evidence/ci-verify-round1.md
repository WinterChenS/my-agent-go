# CI verify 首轮全红：根因与修复（2026-11-03）

- 运行：`41608689011`（commit `9c9cf66`，"CI: 六目标真实 runner 验证"）
- 现象：6 个 go job 中 5 个失败；`go / mac-x64` 停在 `queued` 长达 1h37m，整个 run 无法收敛。
- 原始日志：`docs/evidence/raw/ci-round1/*.log`（`gh api .../actions/jobs/<id>/logs` 原样落盘）

## 结论

本地全绿、CI 全红，**不是产品代码在别的平台上行为不同**，而是三个测试把宿主环境当成既定前提。
四条根因，均属测试可移植性缺陷，无一是平台实现差异：

| # | 位置 | 根因 | 失败证据 |
|---|---|---|---|
| A | `probe_integration_test.go` `TestProbePs1NotDirectlyExecutable` | 只把 `PSBypassPolicy` 置 false，真正的受限前提依赖宿主机 ExecutionPolicy。本机全 scope `Undefined`（Windows 客户端默认 Restricted）→ 绿；runner 策略放开 → `.ps1` 跑通 → `Ready` | `probe_integration_test.go:142: 执行策略禁止时 .ps1 不应标 Ready；state=Ready`（win-x64 / win-arm64） |
| B | `probe_integration_test.go` `makeVersionShim` | fixture 固定生成 `pi.cmd`；Unix 上 `BuildLaunch` 对 `.cmd` 推导 `cmd.exe /c`，而 Unix 无 `cmd.exe` | `probe_integration_test.go:89: 认证版本 1.5.0 fixture 应 Ready，实际 state=DependencyMissing diag=[依赖缺失或无法启动: exec: "cmd.exe": executable file not found in $PATH]`（linux-x64 / linux-arm64 / mac-arm64） |
| C | `gate_test.go` `TestCommandFailureReturnsAssertionCode` | 硬编码 `cmd /c exit 1` 构造"命令退出 1"；Unix 上 `cmd` 不存在，落进"二进制缺失 → 2"分支 | `gate_test.go:221: 命令失败应返回 1，实际 2`（linux-x64 / linux-arm64 / mac-arm64） |
| D | `.github/workflows/verify.yml` | matrix 用 `macos-13` 承载 `mac-x64`，该镜像已退役，job 永远排队 | job 125824846031 状态长期 `queued`；连带 `gh run view --log` 以 `run ... is still in progress` 拒绝输出日志，这也是排查一开始拿不到日志的直接原因 |

补充：`macos-14` 也已不在 GitHub 当前支持列表（arm64 现为 `macos-15` / `macos-26`），一并替换。

## 修复

- A：用例自建前提。`t.Setenv("PSExecutionPolicyPreference", "Restricted")` —— Process 作用域经该环境变量传给子 `powershell.exe`，优先级高于 `CurrentUser`/`LocalMachine`（仅被组策略覆盖）。并加**前置自检**：若注入后脚本仍可执行，直接 `t.Fatalf` 报"无法构造受限场景"，绝不允许静默变成"恰好通过"。非 Windows 平台 `t.Skip`（执行策略概念只存在于 Windows）。
- B：`makeVersionShim` 按 `runtime.GOOS` 生成 `.cmd`（Windows）或可执行 `sh` 脚本（其它平台），保证 fixture 是被测平台**真能直接运行**的形态。
- C：按平台选 shell：Windows `cmd /c exit 1`，其它 `sh -c "exit 1"`。
- D：`macos-13 → macos-15-intel`、`macos-14 → macos-15`；同时去掉 `continue-on-error`（`experimental`），改由矩阵自身判红——真实 runner 已可用，用 `experimental` 掩盖失败与"PASS 由证据确定"的口径冲突。`race` 步骤加编译器前置检查，缺 `CC` 时 `exit 2` 明确判红，不再静默跳过。

## 验证

1. 区分力（对抗性）验证：临时把 `BuildLaunch` 的 `if PSBypassPolicy` 改成恒真（即"永远加 Bypass"，假装可用），重跑用例 →
   `FAIL: TestProbePs1NotDirectlyExecutable ... 执行策略禁止时 .ps1 不应标 Ready；state=Ready`。改回后 PASS。说明该断言确实在测产品行为，而不是恒真。
2. 复现 CI 宿主条件：临时把 `HKCU:\Software\Microsoft\PowerShell\1\ShellIds\Microsoft.PowerShell` 的 `ExecutionPolicy` 设为 `Bypass`（该值原本不存在），`Get-ExecutionPolicy -Scope CurrentUser` 确认为 `Bypass`，重跑用例 → PASS。证明注入的 `Restricted` 覆盖了宿主放开的策略。实验后已删回原值，`CurrentUser` 恢复 `Undefined`。
3. 本地全量：`go test ./... -count=1 -timeout 600s` 全绿；`gofmt -l` 无输出。
4. 交叉编译：`GOOS/GOARCH` = linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/arm64 逐一 `go vet ./...`（含测试包）全部 OK。
5. Windows runner 与 Unix runner 上的真实结果以 push 后的新 run 为准，本轮不预判。

## 遗留

- Unix 侧测试的**执行**结果只能由 CI runner 或 Linux/macOS 机器给出；本地仅完成编译级验证（`go vet`）。
- `TestProbePs1NotDirectlyExecutable` 在组策略强制放开的组织环境里会以 `t.Fatalf` 显式失败而非跳过——这是有意的：那种环境下该断言不可信，必须暴露出来。
