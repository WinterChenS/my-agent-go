# R0-00 开工单 · 构建、D 盘预检与门禁

任务 `R0-00` · 目标 `win-x64` · 现场 Windows 10 22H2 build 19045

先读仓库根目录的 AGENTS.md、[重建技术方案](../specs/2026-11-09-anvil-prototype-rebuild-design.md)、[任务计划](2026-11-09-anvil-prototype-rebuild-plan.md)与[实施规范](../specs/2026-11-09-anvil-rebuild-execution-standard.md)。本单细化首个任务。后续按计划与已通过的门禁逐项推进。

## 1. 协调者派发前检查

- 在 `D:\project\anvil-wt\anvil-rebuild` 核对分支 `codex/anvil-rebuild`、干净工作树和 HEAD。记录作为本任务的基准。当前任务串行使用此工作树，协调者复核时由实施者先停止写入。
- 检查执行器能显式选择 `deepseek-2/deepseek-v4.1-flash`，并能返回实际运行时的模型 ID 和子任务标识。可用已配置的精确模型执行器。若使用 Paseo，先按其本机技能核对 provider 和模型清单、daemon 与子进程写入位置、目标工作区。核对通过后，以 `provider: "deepseek-2/deepseek-v4.1-flash"` 在现有 D 盘工作区创建子 Agent，复核执行器返回或运行元数据中的实际模型。Paseo 命令存在只是候选入口。任一前置无法确认时，记录 `BLOCKED`，保留验证步骤。
- 核对工具的工作目录、运行数据及缓存配置。D 盘目录方案如下。目录可按用途细分，记录最终绝对路径。先验证最近存在的父目录所在实际卷，再创建缺失子目录。跨卷链接、不可写目录、工具未提供写入位置的情况不得启动该工具。
- 确认 Go 1.27.3、Node 24.16.0 的实际路径和版本，使用仓库约定的 `D:\Program Files\nodejs\node.exe` 和 `npm.cmd`。核对当前 Windows build、WebView2 与交互式桌面。先确认 agent-desktop 输出路径后再运行 `agent-desktop status`。Pi 在本任务的空白桌面验收中无需启动。

| 写入用途 | 目标位置 |
| --- | --- |
| 工作树、前端依赖和构建产物 | 工作树内的 `frontend/node_modules`、`frontend/dist`、`dist` |
| `TEMP` / `TMP` / `TMPDIR`、Go build/mod、`GOTMPDIR`、`GOPATH`、npm 缓存 | 工作树内的 `.tmp\tooling\` 下各自的目录 |
| Playwright 浏览器与报告、Anvil 测试数据、WebView2 用户数据、agent-desktop 截图和跟踪 | 工作树内的 `.tmp\test\` 下各自的目录或独立证据目录 |
| 协调状态、执行器及子 Agent 的运行数据、日志 | 已确认的 D 盘执行器目录。协调状态可写在工作树内 `.tmp\coordination\` |
| 原始验收证据 | `D:\project\anvil-wt\anvil-rebuild\.tmp\evidence` |

这些是为重建工作设置的进程级路径。由现有本机 Pi 自身产生的文件按实施规范例外。

## 2. 只派发 R0-00

允许修改 `go.mod`、`go.sum`、必要的 Go embed 入口、`cmd/anvil/`、`frontend/`、`cmd/devtools/`、`scripts/devtools.ps1` 与 `tests/acceptance/rebuild.json`。提交前确认额外文件确为本任务编译或验收所需。

1. 先写并运行失败用例，覆盖合法 D 盘目录、其他盘、跨卷 junction、不可写 D 盘及禁止回退。路径检查使用解析后的实际卷。测试拒绝场景可使用路径解析器或文件系统接口的可控输入。实测现有路径时避免向其他盘创建测试夹具。
2. 先写并运行门禁失败用例，覆盖空矩阵、未知目标、缺证据、错误 SHA/目标、哈希不符、证据路径逃逸、有效的 FAIL 证据。另加一个完整合规的通过样例。用例夹具仅验证判定器。
3. 让上述用例通过，再搭建最小 Wails 窗口、React/Vite 构建和 PowerShell 入口。窗口只需可启动并识别为 Anvil。阶段 0 的页面与交互由后续任务实现。
4. 运行 `go test ./...`、锁定 Node 下的前端构建，以及 `scripts/devtools.ps1` 的 0/1/2 退出码用例。用 `agent-desktop` 验证真实窗口出现、可读取和可关闭。保存失败初跑、通过复跑、工具版本、截图和构建日志。证据文件须有真实来源。
5. 实施者用 Task ID 提交候选结果，交回 SHA、文件清单、命令、退出码、证据路径及已知限制。协调者核对改动范围，按该 SHA 重新构建并独立验收。复核失败时把具体失败动作和证据发回当前实施者。复核后的最终 SHA 与证据绑定。下一任务只能使用已复核的提交作为基准。

## 3. 证据和门禁的最小契约

矩阵至少声明 gate、target、case ID、必需证据类型。一个任务可含多个 case。矩阵还须声明每个阶段的必测 Task 清单，后续任务未登记 case 时仍保留待补条目，门禁返回 2，避免靠省略用例提前通过。R0-00 至少分出构建与门禁、D 盘路径预检、原生窗口三类用例。`verify --case=<id> --target=win-x64` 执行登记的用例并读取环境变量 `ANVIL_EVIDENCE_ROOT` 确定 D 盘证据根。真实桌面用例由协调者在交互式桌面使用 agent-desktop 执行并保存原始证据。自动化用例结果按当次实际执行生成。

每个 case 在 `<evidence>/<gitSHA>/<target>/<caseId>/result.json` 保存以下字段。日志和截图与结果同目录或位于其下级目录。

| 字段 | 要求 |
| --- | --- |
| `schemaVersion`、`caseId`、`gitSHA`、`target` | 初版版本号 `1`，完整 Git SHA 与门禁目标一致 |
| `status`、`evidenceKind` | 状态为 PASS、FAIL、BLOCKED、NOT_RUN。证据类型由矩阵声明，例如 automation、native-desktop、real-runtime |
| `os`、`toolVersions`、`runtimeVersion` | OS 版本、build 与架构。工具与运行时版本按用例适用性记录 |
| `commands` | 实际命令、工作目录、实际退出码。桌面记录包含 agent-desktop 命令及结果 |
| `artifacts` | 日志、截图、快照等的相对路径与 SHA-256。原生窗口用例包含窗口快照和截图 |
| `limitations` | 无限制用空数组。BLOCKED 写出原因和可复验条件 |

`gate --id=G0 --target=win-x64 --evidence=<D 盘绝对路径>` 先验证矩阵与证据完整性，再判定状态。路径必须留在本 case 的 D 盘目录。矩阵或证据无效、缺项、BLOCKED、NOT_RUN 返回 2。只有证据完整有效时，断言失败返回 1，全部必测项通过返回 0。PowerShell 包装脚本返回子进程原始退出码。阶段 0 其他任务未完成时，R0-00 只能得到自己的验收状态，G0 仍关闭。

## 4. 自动推进条件

协调者维护 D 盘的简短状态记录，包括当前任务、实施者 ID、候选 SHA、复核命令、门禁状态及下一任务。完成当前任务的独立复核后，自动派发同阶段下一个已满足依赖的任务。整个阶段的正式 gate 返回 0 后才能进入下一阶段。子 Agent 的完成通知仅触发复核。缺少必测证据、指定模型、合规写入路径或真实桌面条件时，停在原任务或阶段，报告解除阻塞所需条件。恢复时从最近一次已验证的 Git SHA 继续。
