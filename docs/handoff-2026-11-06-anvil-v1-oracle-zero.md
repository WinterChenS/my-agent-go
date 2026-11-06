# Anvil V1 交接（2026-11-06 · 证据欠账清零 + Pi 生命周期闭环）

本文件接在 `docs/handoff-2026-11-06-anvil-v1-continuation.md` 之后，记录 **F08-C/D/E、F01-oracle 38→0、
F06/F06b、F07-saved、B01b 可见性修复** 落地之后的真实状态。它不替代规格、分模块计划、验收矩阵或原始证据；
冲突时按 `AGENTS.md` §1 的权威顺序处理。

## 1. 交接坐标

| 项 | 当前值 |
|---|---|
| 仓库 | `D:\project\my-agent-go` |
| 分支 | `feat/v1-foundation` |
| 上一份交接的 HEAD | `e3988e7` |
| **当前 HEAD** | **以 `git log -1` 为准**（本文写成时 `0b1ad49`） |
| 工作树 | 干净 |
| case 数 | 198（`tests/acceptance/cases.json`） |
| `oracle-allowlist.json` | **空**（`legacyProcessOracleCeiling = 0`） |
| 证据根 | `docs/evidence/runs/<revision>/<gate>/<target>[/<display>]/<case>/`（gitignore，不入库） |
| 本机台账 | `C:\Users\Administrator\AppData\Local\Temp\anvil-sdd\progress.md`（**机器本地**，换机需重建） |

接手第一条命令：

```powershell
git status --short
git log --oneline -5
go test ./... -count=1
```

## 2. 本轮完成（每个任务都有提交 + 原始证据）

### 2.1 F01：test-process 欠账 38 → 0（G4 前置条件已满足）

`tests/acceptance/oracle-allowlist.json` 现在为空，`internal/devtools/index_test.go` 的
`legacyProcessOracleCeiling` 已降到 0。**从此新 case 必须自带 `external-observation` 证据，不允许自证。**

| 批次 | case | 观察装置（都在被测断言路径之外） | 构建提交 |
|---|---|---|---|
| 1 | MEM01–05、RES01–04、CFG03.isolation | 第二个 `storage.DB` 句柄从库文件读真实行；磁盘树 walk；fixture provider 自己收到的请求体；子进程自记 argv/env | `bfc8ad2`（记账） |
| 2 | ENV01–03、GOV01–04 | `check-toolchain.ps1` 自己落盘的机器报告（resolved path/version/lock hash）；真实 `devtools` 子进程的退出码与原样输出 | `3ded087` |
| 3 | PI01–04、RPC01–05 | 候选 shim 子进程自述（真实路径/argv0/cwd）、fake-home 前后快照、fixture provider 请求体、fakeworker 自记 frames/io、OS 进程表 | `89a5247` |
| 4 | SEC01/05/04.runtime/06/02/03.runtime/08、BR00 | 独立执行进程 `internal/policy/testdata/executor` 追加 `executions.jsonl`；client wire 日志；七条通道各自 sentinel + 沙箱树；fixture server 请求日志与计数器 | `292303d` |
| 5 | BR01/04/05/06 | Win32 窗口事实（HWND/父子/DPI/句柄）、CDP 像素、截图 PNG、独立 PowerShell 进程表、独立 `Get-Clipboard` 进程、CDP IME 结果 | `0cb01b2` |

开发期取证加速器：`verify --suite=integration --target=<t> --gate=<G> --case=A,B`（只缩小本次产出哪些 case 的
证据，**不构成 gate 结论**）。

### 2.2 F08：Pi 发现/兼容/契约闭环

| 任务 | 提交 | 要点 |
|---|---|---|
| F08-C | `8ba98f0` | 微信 Node shim 用例改为 **内容判据**（`#!` 嗅探），Windows 上不再 skip；新增 Ed25519 分离签名预认证清单载体 `internal/runtime/pi/precert.go` + `precertgen`，嵌入清单**故意缺席**（fail closed），`ready_verified` 生产不可达 |
| F08-D | `114ee10` | 契约探测以前不带 `--mode rpc` → 真实 Pi 进 text 模式，六项全超时，好用的 Pi 被判 `incompatible`。现在 `launchArgs` = `--mode rpc` → `IsolationSpec.Args()` → `--session-dir` → provider/model，形状与 `internal/run` 一致；真实 Pi 1.5.0 六项全过 |
| F08-E | `0b1ad49` | 生产探测不传 provider/model、隔离 agentDir 无 `models.json` → 真实 Pi 回 "No API key found…" → 仍被判 `incompatible`。现在探测自带**只绑回环**的 stub provider 并写隔离 `models.json`，结束（含失败/取消）即关闭；只给一半 Provider/Model 判配置错误 |

真实 Pi 证据：`docs/evidence/runs/0b1ad49/G0/win-x64/PI_COMP01/contract-report-real-pi.json`
（`state=ready_compatible`，`rpc/settled/mcp/extension/abort/cleanup` 全过）。
`ready_verified` 仍不可达：**没有签名预认证清单**（需要有发行私钥，见 §4）。

### 2.3 F06：托管安装/升级/回滚 + 应用真的用上它

| 任务 | 提交 | 要点 |
|---|---|---|
| F06 | `27d78ae` | `internal/piinstall`：嵌入受信清单（`sizeBytes<=0` 显式拒绝）、下载（HTTPS + 主机白名单 + 每跳复核 + 流式哈希 + fail-closed 签名）、解包（越界/链接/配额/架构）、`active.json` 原子激活、安装事务、回滚；绑定层 `InstallPi/CancelPiInstall/RollbackPi`（UI 只能给版本与 target） |
| F06b | `a53a1ff` | 复核发现的真实缺口：`<managed>/versions/<v>/` 对 F08 的一层扫描**不可见**，`active.json` 也从没被消费。`resolveInstallation` 现在是 `settings.piPath`（用户显式，失效不回退）→ `<managed>/active.json`（校验可执行体 sha256）→ `CONFIG_MISSING`；发现请求改传 `<managed>/versions` |

真实包证据：PI_INS02 用官方 `pi-windows-x64-1.5.0.zip`（61,890,147 字节，`sha256:12d58a70…`）装出
128,877,056 字节 `pi.exe`。开发机安全快照（`internal/piinstall/safety_test.go`）：`~/.pi`、`.tools/pi`、
真实 `%LOCALAPPDATA%\MyAgent` 三处**零变化**（`~/.pi/agent/{sessions,logs}` 因宿主 harness 自身写入而排除）。

### 2.4 F07：选中的 Pi 跨重启保留

| 提交 | 要点 |
|---|---|
| `5491088` | `Settings.PiSavedPath`（既有 `settings.json`，不新造文件格式）；`SelectPi` 成功后落盘、失败或落盘失败不写；发现请求 `Saved` 接上 |
| `118b242` | 修正：原实现写整份内存快照，会把 `MYAGENT_*` 环境覆盖永久钉进用户配置；变量消失后 `resolveInstallation` 不静默回退 → 用户突然启动不了任何 Pi。现在以磁盘文档为基线只改 `piSavedPath`（回归测试 `TestSelectPiDoesNotFreezeEnvOverrides`） |

### 2.5 B01b：一个真实的产品缺陷（不是测试问题）

`da1dafb`：`github.com/jchv/go-webview2` 的 `ICoreWebView2Controller.PutIsVisible` 把
`syscall.SyscallN` 的**第三个返回值（线程 last-error，实测 1400 ERROR_INVALID_WINDOW_HANDLE）**当成 HRESULT
比较，于是**成功**的调用被报成失败。BR04 分栏第 9 轮 `SetVisible(true)` 因此报 "Invalid window handle"。

修复：`internal/browser/native/controller_visibility_windows.go` 直调 vtable，判 `FAILED(hr)` 并用
`GetIsVisible` 读回确认；单测钉住 HRESULT 的 MSB 语义与 vtable 槽位（依赖升级改布局会立刻失败，而不是踩错
函数指针）。BR06 的 `after` 进程表同时改为轮询等 WebView2 子进程退出（最多 10s），严格"零残留"断言保留。

## 3. 当前 gate 状态（本机 Windows 10 22H2 build 19045）

**本机证据一律是开发证据**：`build/platforms.json` 要求 win-x64 最低 Windows 11 build 26100，GOV05 会拒绝。
不得把下列任何结果当作 win-x64 验收结论。

| 命令 | 退出码 | 说明 |
|---|---|---|
| `verify --suite=static` | 0 | gofmt / go vet / 前端 typecheck |
| `verify --suite=integration --target=win-x64 --gate=G0 --case=…` | 0 | 本轮涉及的全部 G0 case PASS（external-observation） |
| `verify --suite=native --target=win-x64 --display=desktop` | 2 | BR00/01/04/05/06 PASS；**缺 runner**：`UI04.native`、`BR07.native`（未实现，属配置缺失） |
| `gate --id=G0` | 2 | 132 个必需位没有 runner（GOV05 等）；宿主低于最低 build |
| `gate --id=G2` | 2 | 同上（大量未实现 case） |

## 4. 仍未闭合（按影响排序）

1. **签名预认证清单不存在**：`internal/runtime/pi/precert/` 只有 README；没有发行私钥就没有
   `ready_verified`。需要 X05 的签名流水线（BLOCKED：缺密钥与发行环境）。
2. **托管包的五个目标没有精确 `sizeBytes`**：win-arm64/mac-x64/mac-arm64/linux-x64/linux-arm64 的官方包大小
   离线测不到，清单写 0 且 loader 拒绝；需要发行流程回填后重新签名。
3. **缺失 runner 的 132 个 case**：GOV05、CFG01–04/DB01–03、MOD01–04、RES05、MCP01–05、CONN01–08、
   SCH01–07、UI01–13、SES01–03、SRCH01、I18N01–04、THEME01–05、BRAND01–03、ART01–03、BR02/03/07–17、
   E2E01–07、PKG01–07、UPD01–14、PERF01–04、REL01–04。这是 G1–G5 的全部剩余工作量。
4. **UI 仍不展示"当前托管版本"**：`Status.PiPath` 只回显 `settings.piPath`；`DiscoverPi` 的托管候选仍按目录名
   排序（F08 冻结），列表里的"当前"标记需要 UI 侧对照 `active.json`。
5. **`settings.piPath` 失效时不回退托管版本**（有意为之）：UI 必须给出"清空自定义路径"的修复指引。
6. **macOS/Linux 无真机证据**：tar.gz 解包、执行位、ELF/Mach-O 校验只有合成归档单测；POSIX `#!` 判据、真实 Pi
   契约探测、`Get-Clipboard` 等等价物都要在对应目标机重跑。

## 5. 建议的下一步顺序

`C06a`（全局 RunService：per-run binding + 单一 admission queue，当前正在做）→ `C06b`（会话目录 + 迁移 005 +
`session_api`）→ `C06c`（附件 blob store/broker）→ `C06d`（澄清扩展）→ `C06e`（导出）→ `C08`（连接器）→
`C09`（调度）→ `U00`（Anvil 更名/i18n/主题，`desktop.go` 串行写点）→ `U01`–`U07` → `GOV05` runner →
其余 runner → X03/X04（打包/发行）→ `G4`。

纪律不变：失败用例 → 最小实现 → focused 验证 → 原始证据 → 可独立审查的提交；证据先提交、工作树干净再取证；
缺机器/凭证/签名就写 BLOCKED，不改口径、不伪造证据。
