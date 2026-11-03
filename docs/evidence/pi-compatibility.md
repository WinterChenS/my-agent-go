# F02 证据：Pi 定位、探测与版本策略

日期：2026-11-03。状态：**部分完成（DOING）**。定位器与探测已实现并有测试；真实 Pi 协议基线未捕获，PI03/PI04 仍 BLOCKED。

## 1. 已完成

| 文件 | 内容 |
|---|---|
| `internal/runtime/pi/compatibility.go` | 类型（PiInstallation/ProbeState/Origin/ProbeResult/Capability）、版本策略 `DefaultPolicy`（仅认证 1.5.0）、`BuildLaunch`（按扩展名推导启动方式，argv 形式，不拼 shell 字符串）、协议 fixture 目录哈希 |
| `internal/runtime/pi/locator.go` | `Locate`：用户指定 → PATH → 常见位置 → 托管目录；中文/空格路径；`pi.exe/.cmd/.bat` 优先、仅 `.ps1` 给诊断且标不可执行；按真实路径去重；失效的已保存路径返回 NotFound 但保留其它结果 |
| `internal/runtime/pi/probe.go` | `Probe`：隔离 `HOME/USERPROFILE/APPDATA/PI_CODING_AGENT_DIR` 后只跑 `--version`，不进入 agent loop、不请求模型、不启动用户 MCP |
| `internal/runtime/pi/locator_test.go` | PI01 用例：中文/空格 npm shim、多版本共存、仅 .ps1、无 Pi、已保存路径失效、重复路径去重、版本策略 |
| `internal/runtime/pi/probe_integration_test.go` | PI02 用例：隔离 agentDir 下用户 marker 哈希不变、用户 HOME 无新增文件；.ps1 执行策略禁止时报 ProbeFailed 而非假装可用 |
| `internal/runtime/pi/realhost_test.go` | 真实宿主集成测试（`MYAGENT_PI_REAL=1` 才跑） |

## 2. 真实宿主实测（PI01 真实证据）

命令与结果（原始日志：`docs/evidence/raw/f02-realhost.log`）：

```text
MYAGENT_PI_REAL=1 go test ./internal/runtime/pi -run TestRealHostPi -count=1 -v
exit=0

发现 Pi: version="1.4.0" origin=external source=path arch=any runnable=true
        path=C:\Users\Administrator\AppData\Local\pi-node\current\pi.cmd
探测 ...\pi.cmd => state=UnsupportedVersion version="1.4.0"
        diagnostics=[版本 1.4.0 未经认证（基线仅认证 [1.5.0]）；标 unverified，不自动运行]
```

结论：宿主真实 Pi 是 **1.4.0**（npm `.cmd` shim，`arch=any`），探测成功但**判为未认证**，
符合基线 §3「未知版本 unverified，不静默承诺可用」。没有把它标 Ready。

## 3. 未完成 / BLOCKED

| 用例 | 状态 | 缺什么 | 下一步 |
|---|---|---|---|
| PI03 真实 Pi 协议基线 | **BLOCKED** | 需隔离安装 Pi **1.5.0** 并真实捕获 RPC/extension 记录；本轮未安装（子任务被限流中断） | `npm --prefix .tools/pi/v1.5.0 install @earendil-works/pi-coding-agent@1.5.0`，接本地脚本化 fixture provider，捕获 accepted/delta/tool/compaction/error/idle 到 `internal/runtime/pi/testdata/v1.5.0/`；捕不到的事件如实标注 |
| PI04 无 Node 环境的独立包 | **BLOCKED** | 需无 Node 目标机 + 官方独立发行包 | 在干净目标机验证独立包能否加载本项目薄 extension 与 MCP |

`internal/runtime/pi/testdata/v1.5.0/` 目前**不存在**——这是刻意留空：没有真实捕获就不放合成数据，
`loadCapabilities` / `protocolFixtureHash` 在缺文件时返回空，不会编造能力清单。

## 4. 执行过程与纪律（如实记录）

- **先红后绿有真实证据**：子 Agent 先写了 `locator_test.go` / `probe_integration_test.go` 并运行，
  原始失败输出见 `docs/evidence/raw/f02-first-failure.log`：
  `TestLocateWindowsShimWithSpaces/PreferredTakesPriority`（指定路径未排首位）、
  `TestProbeHasNoUserConfigSideEffects`（state 为空，Probe 尚未实现）、
  `TestProbePs1NotDirectlyExecutable`（state 为空）。
- 子 Agent 在被限流中断后，我接管并**重写了 locator 与测试**：现有的
  `locator_test.go` 是复核版，覆盖中文/空格、仅 `.ps1`、无 Pi、失效已保存路径、去重、
  指定路径优先、版本策略；子 Agent 版本里的 `NpmCmdAndPs1Shims` / `BrokenSavedPathIgnoredWhenPathHasPi`
  两个子用例已被上述用例覆盖。
- 首次真实功能失败（复核轮）：仅 `.ps1` 目录的诊断记录被 `found=false` 过滤，
  `TestLocatePowerShellOnlyShimIsNotRunnable` 报"期望 1 条诊断记录，实际 0"；
  修正为"有 Executable 即收录为诊断项"后转绿。
- 编译期冲突：子 Agent 已留下 `compatibility.go`/`probe.go`，与我并行写的 `types.go`
  出现 `Origin`/`ProbeState`/`PiInstallation` 重复声明，最终统一到 `compatibility.go`
  作为唯一类型源，删除 `types.go`。

## 5. 复核要点

- `Locate` 全程 `exec.CommandContext(路径, 参数...)` / `os.Stat`，没有任何 `cmd /c` 或字符串拼接，
  中文与空格路径的用例真实跑通（Go 在 Windows 上可直接执行 `.cmd`，已单独实测）。
- `.ps1` 不给 "看起来能用" 的假象：`Runnable=false` 且 Note 说明执行策略约束。
- 去重按规范化真实路径（EvalSymlinks），避免同一 Pi 因软链重复展示。
