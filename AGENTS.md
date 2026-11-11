# AGENTS.md — 现场作业规则（Anvil V1）

> **Rust + Tauri 迁移**（2026-11-10）。重建分支的技术路线与派发顺序以 [ADR 0007](docs/adr/0007-rust-tauri-rebuild.md)、[Rust + Tauri 技术方案](docs/superpowers/specs/2026-11-10-anvil-rust-tauri-design.md)和[迁移实施计划](docs/superpowers/plans/2026-11-10-anvil-rust-tauri-migration-plan.md)为准。先结清旧宿主相关写入，再从 R0-RS01 开始。原型范围、指定模型、真实桌面、本机 Pi、D 盘和阶段门禁约定继续执行。

> **重建分支优先规则**（2026-11-09）。在 `codex/anvil-rebuild` 上，先读 [重建设计](docs/superpowers/specs/2026-11-09-anvil-prototype-rebuild-design.md)、[实施计划](docs/superpowers/plans/2026-11-09-anvil-prototype-rebuild-plan.md) 和 [ADR 0006](docs/adr/0006-prototype-rebuild-scope.md)。下文 Anvil V1 任务现状、Pi 单运行时范围、命令与门禁表是旧实现历史。新分支按 G0 界面、G1 多 Agent 发现和同步、G2 会话顺序开发，使用新的任务 ID 和证据。Windows 10+ 优先，先验当前 Windows 10 22H2，再按 macOS/Linux 真机结果报告。技术栈、隔离、任务取证、提交及现场执行规则继续适用。


> **重建实施与验收**。实施任务必须由 `deepseek-2/deepseek-v4.1-flash` subagent 执行，桌面交互用 `agent-desktop` 验收，Pi 真实用例使用本机已安装的 Pi 和现有登录。详见[实施与真实验收规范](docs/superpowers/specs/2026-11-09-anvil-rebuild-execution-standard.md)，其中 Pi 登录约定优先于下文旧规则。Windows 所有写入必须落在 D 盘，唯一例外是本机 Pi Agent 自身产生的文件。重建工作树使用 `D:\project\anvil-wt\anvil-rebuild`。

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

**Rust 1.95.0（`rust-toolchain.toml` 锁定）+ Tauri v2（桌面宿主）+ React/TS/Vite/Tailwind/shadcn（前端）**
+ SQLite（计划 `rusqlite` bundled，需 FTS5 trigram）+ 宿主 Pi（最低 1.5.0，候选范围 `>=1.5.0 <2.0.0`，必须经能力协商和隔离契约探测；agent loop 由 Pi 负责）。六目标：win-x64、win-arm64、mac-x64、mac-arm64、linux-x64、linux-arm64。

Go 与 Wails 已按 [ADR 0007](docs/adr/0007-rust-tauri-rebuild.md) 停用并在 R0-RS04 从工作树删除（`cmd/`、`internal/`、`go.mod`、`go.sum` 不再存在，需要时从 git 历史 `e376787` 之前取）。任何新代码、新脚本、新文档命令一律用 Rust 工具链——`cargo` 是 Rust 自带的构建与依赖管理工具（相当于 `go build` + `go mod`），不是独立技术栈。

正式产品名为 Anvil。用户可见可执行文件、安装包、数据目录、URI 和环境变量迁移到 `Anvil` / `anvil://` / `ANVIL_*`；仓库目录本轮不改名。`MYAGENT_*` 仅保留一个开发兼容周期，冲突时 `ANVIL_*` 优先。

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
| 本机是 Windows 10 22H2（build 19045） | **这就是本轮 win-x64 的验收平台**（[ADR 0005](docs/adr/0005-narrow-acceptance-to-win-x64-windows-10.md)：最低系统 `10` / build `19045`），本机证据就是验收证据 | 不再以"开发证据"降级；GOV05 移植到 Rust 门禁后按 build ≥ 19045 判定。其余五个目标按 ADR 0005 记为 deferred，不得据此宣称跨平台 |
| PATH 上 Pi 为 1.4.0，低于最低支持版本 1.5.0 | 不可进入兼容探测，状态应为 upgrade_available/incompatible | 不把 1.4.0 当通过；用应用托管安装/升级流程，不覆盖外部 Pi |
| PowerShell 标准输出有时不回显 | 误判"命令没跑" | 结果写文件再 `Read` 读回 |
| `wmic` 被安全策略拉黑 | 取进程信息失败 | 用 `Get-CimInstance Win32_Process` |
| `Start-Process -PassThru` 的 `ExitCode` 读不到（返回空） | 会误判 GUI 进程"没跑过" | GUI 退出码用 `cmd //c "程序 & echo %ERRORLEVEL%"` 包装采集 |

## 5. 统一命令契约（R0-RS03 起为 Rust）

`cargo` 是 Rust 自带的构建与依赖工具（相当于 `go build` + `go mod`），全部构建必须
`--locked`，目标目录由 `.cargo/config.toml` 锁在工作树内。Go 命令已下线，见 ADR 0007。

```text
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --case=<caseId> --target=win-x64 --evidence=<abs-evidence-root>
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G0 --target=win-x64 --evidence=<abs-evidence-root>
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 preflight --path=<path> [--create]
cargo build --locked -p anvil            # 桌面宿主
cargo test  --locked --workspace         # 全部 Rust 测试
npm --prefix frontend test               # 前端测试
npm --prefix frontend run build          # 前端产物
```

退出码：0 通过；1 有效证据判定 FAIL；2 输入/证据不完整、BLOCKED、NOT_RUN。
不允许 `|| true`、不允许写 `passed.json` 冒充执行、不允许空 suite 判成功。

**入口形式有坑**：`cargo run` / `go run` 这类包装会把子进程退出码压成 1，1 与 2 无法区分。
门禁/CI 判定一律走包装脚本（构建后原样透传退出码）：

```text
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 <同上参数>
```

包装脚本内部先 dot-source `scripts/env-rs.ps1`（`RUSTUP_AUTO_INSTALL=0`、D 盘
`RUSTUP_HOME`/`CARGO_HOME`/`TEMP`/`CARGO_TARGET_DIR`）。直接调用时必须先设进程级
`-ExecutionPolicy Bypass`，否则环境不生效、cargo 会往 C 盘装工具链。

依据见 `docs/adr/0001-devtools-exit-code-fidelity.md`。

## 6. 单任务纪律

每个任务：**失败用例 → 最小实现 → focused 验证 → 原始证据 → 可独立审查的提交**。
提交前在提交信息里写明 Task ID。状态只有 TODO/DOING/REVIEW/PASS/FAIL/BLOCKED；
PASS 由证据确定，不由"看起来能用"确定。缺机器/凭证/签名/网络就写 BLOCKED，
保留修复步骤，不删目标、不改口径、不伪造证据。

所有测试在隔离 appData / Pi agentDir / 临时 workspace 中跑，不碰用户真实配置与登录。

## 7. 目录所有权

- `crates/anvil-paths`、`schemas/`、`tools/devtools` 的门禁判定：**串行维护**，同一时间只能有一个写者。
- 其余包按模块计划表的 Task 归属，跨包改动先在对应用户计划任务里登记。
- 产品代码不得引用 `docs/` 里的示例当作已实现契约。

## 8. 报告口径

逐 Task/目标给 PASS/FAIL/BLOCKED/NOT_RUN、实际命令与退出码、产物路径/hash、限制。新增 F08/F09/C08/C09/U00–U07 必须使用自己的 case 和证据；旧任务 PASS 不能自动覆盖新增范围。缺 OAuth 客户端、连接器测试账户、目标机器、签名或网络时写 BLOCKED，不使用 mock 或静态 UI 冒充。未过 G4 不称 Anvil V1 可交付；未过 G5 不称正式发行完成。
