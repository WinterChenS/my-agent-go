# AGENTS.md — 现场作业规则（Anvil V1）

本文件是执行 Agent 的现场契约。动手前必读，与本文件冲突的临时便利一律让步。

## 1. 权威顺序

用户明确指令 > `docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md`（用户已确认的增量规格）
> `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md`（未被增量规格替换的执行基线）
> 分模块计划 `docs/superpowers/plans/*.md` > 验收矩阵 `docs/acceptance/v1-gates.md`
> 历史方案（`docs/agent-client-proposal.md`、`docs/technical-decisions.md`、
> `docs/wails-browser-design.md`、`docs/gooeypi-browser-review.md`）。

历史方案里的时间估算、候选方案、"后续可选"不是任务。调研结论不是实现完成证据。

## 2. 仓库现状（2026-11-05 增量基线）

- Git 仓库已有 F00–F05/F07、C01–C05、B00/B01 等实现、提交与证据；不得再按空仓库初始化、覆盖或重写历史。
- 新增 F08/F09/C08/C09/U00–U07 均须独立实现和取证；旧 PASS 不自动覆盖新范围。
- 不存在 `.codegraph`。若后续出现，按 CodeGraph 方式索引后再改代码。
- 工作树可能包含用户的 B01b、UI 原型或其它未提交工作；每次任务前先查 `git status --short`，只纳入本 Task 文件。

## 3. 技术选型（不可自行更换）

Go 1.27.3（Harness 主体）+ Wails v3.0.0（桌面宿主）+ React/TS/Vite/Tailwind/shadcn（前端）
+ SQLite（`modernc.org/sqlite`，需 FTS5 trigram）+ 宿主 Pi（最低 1.5.0，候选范围 `>=1.5.0 <2.0.0`，必须经能力协商和隔离契约探测；agent loop 由 Pi 负责）。六目标：win-x64、win-arm64、mac-x64、mac-arm64、linux-x64、linux-arm64。

正式产品名为 Anvil。用户可见可执行文件、安装包、数据目录、URI 和环境变量迁移到 `Anvil` / `anvil://` / `ANVIL_*`；仓库目录与 Go module 本轮不改名。`MYAGENT_*` 仅保留一个开发兼容周期，冲突时 `ANVIL_*` 优先。

换桌面壳、换浏览器承载方式、把 Pi/Node/Chromium 塞进基础包、删平台、迁移用户旧凭证、
放宽门禁阈值、增加后置功能 —— 一律先写 ADR，不静默执行。

## 4. 本机环境事实（踩过的坑，别再踩）

| 事实 | 后果 | 做法 |
|---|---|---|
| Bash 工具链残缺（`dirname`/`head` 缺失） | `Bash` 工具执行即报错 | 一律用 `PowerShell` 工具。**2026-11-04 复核**：当前 harness 的 bash 可用（`head`/`grep`/`sed` 均在）；但 MSYS 会把 `cmd /c` 改写成 `C:/`，需要时用 `cmd //c`。旧记录仍保留，因为换机器/换 harness 可能复发 |
| PATH 首个 node 是 v22.22.2（WorkBuddy 托管运行时） | 不符合基线 Node 24.16.0 | 显式用 `D:\Program Files\nodejs\node.exe`（v24.16.0） |
| `微信web开发者工具\node.exe` 也在 PATH | 抢解析，正是 `ToolchainRejectsWechatNode` 要拦的场景 | 脚本必须校验实际路径，不能只 `node -v` |
| `npm.ps1` 被执行策略禁止 | `npm -v` 报脚本无法运行 | 用 `D:\Program Files\nodejs\npm.cmd` |
| 无 gcc/cc/clang/mingw | cgo 相关构建可能不可用 | 需要 cgo 时先探测再决定，不假设。**2026-11-04 复核**：本机存在 MinGW-W64 gcc 16.2.0（`mingw64in\gcc.exe`），`CGO_ENABLED=1` 的 Wails 构建与 `go test -race` 均成功；仍是"先探测"，不要假设每台机器都有 |
| WebView2 运行时 156.0.4314.58 已装 | Windows WebView 可用 | 记录进环境证据 |
| 本机是 Windows 10 22H2（build 19045），低于基线最低验收系统 Windows 11 24H2 | 本机产生的 win-x64 证据只是开发证据，不能作为 win-x64 验收结论 | 验收在 Windows 11 24H2 机器或符合要求的 CI 上重跑；GOV05 实现后由门禁自动拒绝 |
| PATH 上 Pi 为 1.4.0，低于最低支持版本 1.5.0 | 不可进入兼容探测，状态应为 upgrade_available/incompatible | 不把 1.4.0 当通过；用应用托管安装/升级流程，不覆盖外部 Pi |
| PowerShell 标准输出有时不回显 | 误判"命令没跑" | 结果写文件再 `Read` 读回 |
| `wmic` 被安全策略拉黑 | 取进程信息失败 | 用 `Get-CimInstance Win32_Process` |
| `Start-Process -PassThru` 的 `ExitCode` 读不到（返回空） | 会误判 GUI 进程"没跑过" | GUI 退出码用 `cmd //c "程序 & echo %ERRORLEVEL%"` 包装采集 |

## 5. 统一命令契约（F01 完成后才可用）

```text
go run ./cmd/devtools verify --suite=static
go run ./cmd/devtools verify --suite=unit
go run ./cmd/devtools verify --suite=integration --target=<targetId>
go run ./cmd/devtools verify --suite=native --target=<targetId> --display=<session>
go run ./cmd/devtools verify --suite=e2e --target=<targetId>
go run ./cmd/devtools package --target=<targetId> --channel=internal
go run ./cmd/devtools verify --suite=package --target=<targetId> --artifact=<path>
go run ./cmd/devtools gate --id=G0 --evidence=<abs-evidence-root>
```

退出码：0 通过；1 断言失败；2 配置/索引/目标/门禁缺失或未知。
不允许 `|| true`、不允许写 `passed.json` 冒充执行、不允许空 suite 判成功。

**入口形式有坑**：`go run` 会把程序退出码压成 1，1 与 2 无法区分。
门禁/CI 判定一律走包装脚本（退出码原样保真）：

```text
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 <同上参数>
./scripts/devtools.sh <同上参数>
```

依据见 `docs/adr/0001-devtools-exit-code-fidelity.md`。

## 6. 单任务纪律

每个任务：**失败用例 → 最小实现 → focused 验证 → 原始证据 → 可独立审查的提交**。
提交前在提交信息里写明 Task ID。状态只有 TODO/DOING/REVIEW/PASS/FAIL/BLOCKED；
PASS 由证据确定，不由"看起来能用"确定。缺机器/凭证/签名/网络就写 BLOCKED，
保留修复步骤，不删目标、不改口径、不伪造证据。

所有测试在隔离 appData / Pi agentDir / 临时 workspace 中跑，不碰用户真实配置与登录。

## 7. 目录所有权

- `internal/contracts`、`schemas/`、`cmd/devtools` 的门禁判定：**串行维护**，同一时间只能有一个写者。
- 其余包按模块计划表的 Task 归属，跨包改动先在对应用户计划任务里登记。
- 产品代码不得引用 `docs/` 里的示例当作已实现契约。

## 8. 报告口径

逐 Task/目标给 PASS/FAIL/BLOCKED/NOT_RUN、实际命令与退出码、产物路径/hash、限制。新增 F08/F09/C08/C09/U00–U07 必须使用自己的 case 和证据；旧任务 PASS 不能自动覆盖新增范围。缺 OAuth 客户端、连接器测试账户、目标机器、签名或网络时写 BLOCKED，不使用 mock 或静态 UI 冒充。未过 G4 不称 Anvil V1 可交付；未过 G5 不称正式发行完成。
