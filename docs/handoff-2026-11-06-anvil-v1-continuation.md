# Anvil V1 实施交接（2026-11-06 · 续）

本文件接在 `docs/handoff-2026-11-06-anvil-v1-implementation.md` 之后，记录 **F05-A…E、F01-GOV05、F08-A/B
落地之后** 的真实状态。它不替代规格、分模块计划、验收矩阵或原始证据；冲突时按 §2 的权威顺序处理。

## 1. 交接坐标

| 项 | 当前值 |
|---|---|
| 仓库 | `D:\project\my-agent-go` |
| 分支 | `feat/v1-foundation` |
| 本轮起点 | `485bd43` |
| **当前 HEAD** | **`e3988e7`**（接手时以 `git log -1` 为准） |
| 工作树 | 干净（`git status --short` 无输出） |
| 正式产品名 | Anvil |
| case 数 | 198（`tests/acceptance/cases.json`） |
| 证据根 | `docs/evidence/runs/<revision>/<gate>/<target>[/<display>]/<case>/`（gitignore，不入库） |
| 本机台账 | `C:\Users\Administrator\AppData\Local\Temp\anvil-sdd\progress.md`（**机器本地**，换机需重建） |

本轮 9 个提交：

```
e3988e7 F08: add Pi contract probe and compatibility report
72f070f F08: require certified version, registered hash and contract probe for ready_verified
147df2e F08: add Pi discovery, candidate origins and selection
9368147 F01: add evidence environment identity and target OS minimum gate
8a2bc52 F05: release pending approvals when a run settles while waiting
1f91fd1 F05: add history index, transcript lookup and single-writer guarantee
4ecea66 F05: add RUN05 crash recovery test and external-observation runner support
47fdbe6 F05: distinguish retry errors and defer settle during compaction/retry
7d502d9 F05: add stop escalation, per-run budget and usage reporting
```

接手第一条命令：

```powershell
git status --short
git log --oneline -5
go test ./... -count=1
```

工作树可能包含用户改动。不得 reset / checkout / 覆盖不属于当前 Task 的内容。

## 2. 权威顺序

1. 用户明确指令；
2. `docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md`；
3. `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md`（未被增量规格替换的部分）；
4. `docs/superpowers/plans/*.md`；
5. `docs/acceptance/v1-gates.md`、`tests/acceptance/cases.json`、`docs/acceptance/v1-plan-index.json`；
6. 历史方案、旧交接、调研文档。

现场规则见 `AGENTS.md`。旧交接里与本文件冲突的“当前状态 / 建议顺序”以本文件为准。

## 3. 本轮已完成（全部有提交 + 原始证据）

| 任务 | 提交 | 做了什么 | 证据 / 报告 |
|---|---|---|---|
| F05-A 停止与预算 | `7d502d9` | 停止升级（2s 可见 / 5s ForceKill / 10s 清理失败阻塞会话）；每回合预算 100 次工具调用 / 30 分钟；用量上报（未知 ≠ 0，价格未知不写 costUsd）；停止态拒工具调用；`policybridge` 在 Authorize 前按 `Options.Gate` 拒绝 | `stop_budget_usage_test.go`；报告 `F05-A-report.md` |
| F05-B 结算与断连 | `47fdbe6` | 重试/压缩期间不提前 `completed`（`settle.deferred`）；重试耗尽或 `stopReason:"error"` → `failed`+`PROVIDER_ERROR`；未知 `agent_settled` 负载 → `failed`+`COMPATIBILITY`；最后一个订阅者断开 → 拒批（`reason:"ui_disconnected"`） | `settle_disconnect_test.go`(375)；报告 `F05-B-report.md` |
| F05-C RUN05 + 外部观察 | `4ecea66` | fakeworker `-mode crashafterwrite`/`-wait`；`TestInterruptedRunDoesNotReplayUnknownWrite`；runner 支持 `observationSpec{Source,Artifacts}`，case 必须自己写 `observation.json`，缺 → FAIL，绝不回退测试进程 oracle；RUN01–RUN06 接线 | `docs/evidence/runs/4ecea66/G2/win-x64/RUN0*/`；报告 `F05-C-report.md` |
| F05-D 历史索引 | `1f91fd1` | 新包 `internal/history`：索引 + 只读转录定位；`run.HistorySink`；只用 001 表（无新迁移）；每会话单写者；**DB 只存定位与计数，不存消息正文**；无常驻 worker | `TestRunIndexSurvivesRestart` 等 6 个；RUN07 证据 `runs/1f91fd1/G2/win-x64/RUN07/`；报告 `F05-D-report.md` |
| F05-E 审批中结算 | `8a2bc52` | `agent_settled` 落在 `waiting_approval` 时，先 `abandonPendingApprovals(reason=run_settled)` 拒批、退回 running，再走终态；不再静默吞错误 | `settle_waiting_approval_test.go`；RUN01.service 证据 `runs/8a2bc52/G2/win-x64/RUN01.service/` |
| F01-GOV05 环境身份 | `9368147` | `contracts.Environment{os,osVersion,osBuild,arch,display}` 进 `GateEvidence` 与 schema；`build/platforms.json` 写死最低线（win 11/26100、mac 14、linux 24.04）；宿主真实探测（Windows `RtlGetVersion`、darwin `sw_vers`、linux `/etc/os-release`）；门禁拒绝环境不符/低于最低线/display 不一致 | `gate_test.go` +297；`hostenv_{windows,darwin,linux,other}.go`；报告 `F01-GOV05-report.md` |
| F08-A Pi 发现 | `147df2e`+`72f070f` | `pi.Discover/Select`：候选来源 preferred/saved/managed/path/common、去重与优先级、启动器与真实 runtime 分别取 SHA-256、微信 Node shim 拒绝（`unexpected-node-origin`）、只读（不改 PATH/文件）、状态机 discovering/ready_verified/ready_compatible/upgrade_available/incompatible/missing；`Desktop.DiscoverPi/SelectPi` | `lifecycle.go`(723)+`lifecycle_test.go`(577)；PI_DISC01/02 证据 `runs/147df2e/G0/win-x64/PI_DISC0{1,2}/`；报告 `F08-A-report.md` |
| F08-B 契约探测 | `e3988e7` | `ContractProbe` 六项检查 rpc/settled/mcp/extension/abort/cleanup，逐项具名失败、超时即失败；`ContractReport{version,state,capabilities,failedChecks,fixtureHash,environment,observed}`；接进 `Discover` 的生产路径；2.x 与 <1.5.0 短路不探测并在诊断里说明 | `contract.go`(489)+`contract_test.go`(466)；PI_COMP01–04 证据 `runs/e3988e7/G0/win-x64/PI_COMP0{1..4}/`；报告 `F08-B-report.md` |

### 本轮确立、后续不得回退的语义边界

1. **`ready_verified` 是三元条件**：版本在预认证清单 **且** 可执行体 SHA-256 在嵌入清单 **且** 契约探测通过。
   缺一即最多 `ready_compatible`；未登记哈希**永远**不能 `ready_verified`（`72f070f`）。
2. **探测失败 = `incompatible`**，不是“降级为 compatible”；只有“未执行探测”（无注入/版本范围短路）才停在 `ready_compatible` 并带诊断。
3. **结算必须落地**：重试/压缩/审批中都不允许停留在非终态或提前 `completed`。
4. **观察值必须来自外部**：集成 case 声明的 `observation.json` 与 artifacts 必须由 case 自己产出；runner 缺文件即 FAIL，不允许测试进程兜底。
5. **历史库只存定位**：转录正文不入库（已有原始 db 字节扫描测试）。
6. **证据必须带环境身份**，且达到目标最低 OS；低于最低线的证据只能算开发证据。

## 4. 当前验证状态（在本机实测，2026-11-06）

| 命令 | 退出码 | 结果 |
|---|---|---|
| `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static` | 0 | gofmt / `go vet ./...` / 前端 typecheck 三项 PASS |
| `go test ./... -count=1` | 0 | 全包 ok（含 `-race` 过的 run/policybridge/pi） |
| `scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G0` | **2** | 可跑 **30 / 31**，失败 0，阻塞 0；唯一缺 runner：`GOV05` |
| `scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2` | **2** | 可跑 **21 / 69**，失败 0，阻塞 0；48 条缺 runner（清单见输出） |
| `scripts/devtools.ps1 gate --id=G2 --evidence=…/runs/9368147/G2/win-x64` | 2 | 预期拒绝：`证据环境低于目标 win-x64 的最低要求: 宿主 OS build 19045 低于目标最低 build 26100` |

`go run ./cmd/devtools …` 会把退出码 2 压成 1（ADR-0001）。**判定一律走 `scripts/devtools.ps1` / `scripts/devtools.sh`**。

> 本机是 Windows 10 22H2 build 19045，低于 win-x64 最低验收系统 Windows 11 24H2 (build 26100)。
> 因此**本机产出的所有 win-x64 证据都被门禁拒绝**，这是 GOV05 的预期行为：证据可作开发证据，不能作验收结论。
> 不要为此降阈值、改 `build/platforms.json` 或绕过 `validateEnvironment`。

## 5. 未完成工作（建议顺序）

### 5.1 F08-C：把 F08 收干净（小，先做）

1. **PI_DISC02 目前有一个 Windows 专项 skip**：`internal/runtime/pi/lifecycle_test.go:460`
   `t.Skip("Windows 专项：微信开发者工具 Node shim")`。PI_DISC02 的 scope 是 ALL，需要在 macOS/Linux
   也能跑同一场景（POSIX 下用无扩展名 `pi` shell 启动器 + 假 `node` 落在 `.../微信web开发者工具/`），
   而不是靠 skip。补完后删除该 skip。
2. **预认证哈希清单没有落地载体**：`VersionPolicy.PreCertifiedHashes` 默认空，`ready_verified` 今天不可达。
   需要一个随发行包嵌入、带签名校验的清单（构建期生成 + 运行时验签），并写清“本地跑通 ≠ 认证”。
3. **真实 Pi 1.5.0 未验证**：本机 PATH 上是 1.4.0。`ContractProbe` 的 `get_commands` 握手、MCP/extension
   能力面判定只在 fakeworker 上验证过。**需要一台装了 Pi ≥1.5.0 的隔离机器**跑 PI_COMP01–04；没有就写 BLOCKED。

### 5.2 F01-oracle：38 条 test-process 欠账清零（G4 前置，机械但量大）

棘轮：`internal/devtools/index_test.go` 的 `legacyProcessOracleCeiling = 38`，
清单在 `tests/acceptance/oracle-allowlist.json` 的 `processAllowed`：

```
ENV01 ENV02 ENV03 GOV01 GOV02 GOV03 GOV04 PI01 PI02 PI03 PI04
RPC01 RPC02 RPC03 RPC04 RPC05 SEC01 SEC02 SEC03.runtime SEC04.runtime
SEC05 SEC06 SEC08 CFG03.isolation RES01 RES02 RES03 RES04
MEM01 MEM02 MEM03 MEM04 MEM05 BR00 BR01 BR04 BR05 BR06
```

做法（每清一条：改 case 的 runner → 从 allowlist 删除 → 把 `legacyProcessOracleCeiling` 减 1 →
重新生成该 case 证据 → 独立提交）：

- 观察源用 `observationSpec{Source, Artifacts}`：进程树/文件副作用类用 `filesystem` + case 写出的
  `observation.json`；数据库类用 `database` + `index-dump.json`（参考 RUN07）。
- 参照实现：`internal/run/observation_test.go`（`ANVIL_OBSERVATION_DIR`、首次截断、之后合并/追加、
  `tests` 数组），以及 `internal/devtools/integration.go` 的 `writeExternalObservationOracle`。
- 每一批完成后把 `legacyProcessOracleCeiling` 下调到新的真实值；**G4 前必须为 0**。
- 不要在 allowlist 里新增条目；`index_test.go` 会拒绝超过棘轮上限。

### 5.3 F06：托管安装 / 升级 / 回滚

case：`PI_INS01–03`、`PI_UPG01–03`（owner F06，phase G2）。要点见
`docs/superpowers/plans/2026-11-05-pi-lifecycle-updater.md` Task F06：只写托管根、校验和/签名、
失败回滚、不覆盖外部 Pi、`installing`/`rollback_available` 状态由 F06 产生（F08-A 已定义常量）。

### 5.4 F07：设置与“已保存的 Pi 路径”

F08-A 留下的明确缺口：
- `internal/app/pi_lifecycle_api.go` 里 `Saved` 恒为空（配置无该字段）；
- `Desktop.SelectPi` 不持久化选择。

F07 加配置字段后，把 `Saved` 接上、`SelectPi` 通过既有配置 API 持久化（**不新造文件格式**），
并补 `saved-path` 来源的集成证据。

### 5.5 其余

- C06/C07：`RUN04.ui`、`RUN06.ui`（浏览器 UI 用例，phase G3）。
- C08/C09、U00–U07、DB01–03、CFG01–04、MCP01–05、CONN01–08、SCH01–07、MOD01–04、BR08–17 等：
  按 `docs/superpowers/plans/*.md` 与 `docs/acceptance/v1-gates.md` 推进；G2 目前 48 条缺 runner。
- **B02/B03 仍 BLOCKED**：没有 macOS/Linux 真机，不得伪造证据；`sw_vers` / `/etc/os-release` 分支
  只有交叉编译证据。

## 6. 硬约束与已知坑（接手必读）

- **不伪造 PASS**：不写 `passed.json`、不用空 suite、不加 `t.Skip`（平台专项也尽量做成可移植用例）、
  不弱化既有断言、不改门禁阈值、不删目标。
- **不碰用户真实环境**：`~/.pi`、`.tools/pi/`、真实 appData、PATH 一律只读或隔离；测试全部用临时目录。
- **串行维护**：`internal/contracts`、`schemas/`、`internal/devtools` 的门禁判定同一时间只能一个写者。
- **迁移编号**：`internal/storage/migrations/` 003 保留给 C08、005 保留给 C06；F05-D 只用了 001。
- **`go run` 退出码失真**：门禁/CI 判定用包装脚本。
- **`storage.Open` 拒绝 `:memory:`**；DSN 固定 `busy_timeout(5000)/foreign_keys/WAL`、`SetMaxOpenConns(1)`。
- **F08 的生产探测 seam**：`Discover` 只有在 `VersionProbe == nil && ProbeFunc == nil` 时才接
  `defaultContractProbe`；注入 `VersionProbe` 视为离线发现（不启动进程）。这是为了让 F08-A 的纯文件
  fixture 测试保持可测，**不要**为了“统一”把它改成无条件探测——那会让所有离线测试去执行假文件。
- **契约探测握手用 `get_commands`**（`testdata/v1.5.0/*.jsonl` 里的真实握手），不是 `initialize`。
  若将来拿到含 `initialize` 的捕获，再按证据改，不要凭想象加。
- **cleanup 负例**用 `leakyContractWorker` 返回与真实 worker 相同的 `CLEANUP_FAILED`；真实孙进程回收由
  RPC05 `TestWorkerTerminatesGrandchild` 覆盖。别把这条负例当成“已证明回收失败路径”。
- **GOV05 之后**：任何新证据都必须带 `environment`；`internal/devtools/integration.go` / `native.go`
  已自动写入宿主真实值，测试 fixture 需自行补齐。

## 7. 环境事实（本机，2026-11-06 复核）

| 事实 | 影响 |
|---|---|
| Windows 10 22H2 build 19045 | 低于 win-x64 最低验收系统；本机 win-x64 证据只能作开发证据 |
| Go 1.27.3 | 基线版本 |
| MinGW-W64 gcc 16.2.0 可用 | `CGO_ENABLED=1` 的 Wails 构建、`go test -race` 可用（仍需先探测） |
| WebView2 156.0.4314.58 | Windows WebView 可用 |
| PATH 上 Pi 1.4.0 | 低于最低支持 1.5.0；不可用于 `ready_verified` 实测 |
| PATH 首个 node 是 v22.22.2（WorkBuddy），基线是 24.16.0 | 脚本必须校验实际路径 |
| `D:\Program Files\nodejs\npm.cmd` | `npm.ps1` 被执行策略禁止 |
| `wmic` 被拉黑 | 用 `Get-CimInstance Win32_Process` |
| `Start-Process -PassThru` 的 ExitCode 读不到 | GUI 退出码用 `cmd //c "程序 & echo %ERRORLEVEL%"` |

## 8. 单任务工作流（本轮实际用法）

每个 Task：**失败用例（保存红输出）→ 最小实现 → focused 验证 → 原始证据 → 带 Task ID 的独立提交**。

- 任务书：`C:\Users\Administrator\AppData\Local\Temp\anvil-sdd\<TASK>-brief.md`
- 报告：同目录 `<TASK>-report.md`（子代理写全量，父会话只收状态/commit/一行测试摘要/concerns）
- 台账：同目录 `progress.md`（每 Task 完成后更新：状态、提交、证据、遗留）
- 子代理约束：30k token 预算、只改任务书列出的文件、`git status --short` 后只提交自己的文件、
  不碰 `docs/`/`schemas/`/`tests/acceptance/cases.json`/`internal/devtools/gate.go`/`index_test.go`。
- 父会话必须复核：语义是否越界（本轮就抓到 `ready_verified` 被探测单独提升的越界）、
  测试是否被弱化、证据是否真为外部观察、提交是否夹带无关文件。

## 9. 接手完成定义

- 任一 gate 的 PASS 只能由 `scripts/devtools.ps1 gate …` 的证据判定给出；G0/G2 目前都是 exit 2，
  不得表述为“已通过”。
- 未过 G4 不称 Anvil V1 可交付；未过 G5 不称正式发行完成。
- 缺真机 / 凭证 / 签名 / 网络 → 写 BLOCKED 并保留修复步骤，不删目标、不改口径、不造假证据。
