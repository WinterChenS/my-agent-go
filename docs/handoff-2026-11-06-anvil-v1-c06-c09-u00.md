# Anvil V1 交接（2026-11-06 · F01 欠账清零 + C06–C09 + U00 + 证据接线）

本文件接在 `docs/handoff-2026-11-06-anvil-v1-oracle-zero.md` 之后，记录 **C06a–f、C07、C08a/b、C09、U00、
GOV05 runner、E01 证据接线** 落地之后的真实状态。它不替代规格、分模块计划、验收矩阵或原始证据；
冲突时按 `AGENTS.md` §1 的权威顺序处理。

## 1. 交接坐标

| 项 | 当前值 |
|---|---|
| 仓库 | `D:\project\my-agent-go` |
| 分支 | `feat/v1-foundation` |
| **当前 HEAD** | 以 `git log -1` 为准（本文写成时 `0769d6a`） |
| 工作树 | 干净 |
| case 数 | 198（`tests/acceptance/cases.json`） |
| `oracle-allowlist.json` | 空（`legacyProcessOracleCeiling = 0`） |
| 必需原子 case（不含 9 条复合） | 189 条：**80 条有 canonical runner**，109 条没有 |
| 证据根 | `docs/evidence/runs/<revision>/<gate>/<target>[/<display>]/<case>/`（gitignore） |
| 本机台账 | `C:\Users\Administrator\AppData\Local\Temp\anvil-sdd\progress.md`（机器本地） |

本机四个套件的真实状态（`0769d6a`，Windows 10 22H2 build 19045）：

| 命令 | 退出码 | 说明 |
|---|---|---|
| `verify --suite=static` | 0 | gofmt / go vet / 前端 typecheck / 文案门禁 |
| `verify --suite=unit` | 0 | `go test ./internal/...` 全绿 + 前端 148 测试 |
| `verify --suite=integration --target=win-x64 --gate=G0` | 0 | 31/31 PASS（external-observation） |
| `verify --suite=native --target=win-x64 --display=desktop` | 2 | BR00/01/04/05/06 PASS；缺 runner：`UI04.native`、`BR07.native` |
| `gate --id=G0`（证据树 `runs/0769d6a`） | 2 | 142 必需证据位：0 个 case 缺 runner 证据位、100 个非 win-x64 目标缺位、42 个因宿主 build 19045 < 26100 被拒 |

**本机证据一律是开发证据**：`build/platforms.json` 要求 win-x64 最低 Windows 11 build 26100。在 Win11 24H2
或合规 CI 上重跑同一套命令，G0 的拒绝理由才会消失。不得把本机任何结果当作 win-x64 验收结论。

## 2. 本轮完成（2026-11-06 夜：验收口径收敛 + 并行推进）

### 2.1 验收口径：ADR 0005（先读它）

六目标矩阵 + Windows 11 24H2 最低线意味着本机能产出的**每一条**证据都被 GOV05 拒收，任何 gate 都无法评估。
用户明确要求本轮只需要 Windows 10，于是：

- **ADR 0005**：本轮唯一验收目标 = `win-x64`，最低系统 = Windows 10 22H2（build 19045）；其余五目标
  `active=false` 且必须写 `deferredBy`；它们的必需位在 gate 报告里**逐条打印为 deferred**
  （不计缺失、也不计通过）；某 gate 的必需位全部停用时判**非通过**。
- `build/platforms.json`、`activeTargets()`、`resolveRequiredEvidenceWithDeferred`、native runner 的
  `--gate` 选择都已落地。恢复其它目标必须撤 ADR 并重跑矩阵——不能只把 `active` 改回 true。

### 2.2 并行实现（各自独立 worktree，已合并）

| 分支 | 任务 | 内容 |
|---|---|---|
| `feat/u01` | U01 应用壳层 | 路由/导航（含 `anvil://`）、历史侧栏（分页/改名/置顶/归档/删除确认/重启持久化）、工作台摘要（缺数据 = `unavailable`）、全局搜索（`internal/search` + 迁移 006 + `Desktop.Search` + 前端搜索面板）、`not_implemented` 页 |
| `feat/brnative` | B04 原生浏览器 | observe 的**稳定 ref**（`data-anvil-uid` + per-tab nonce，陈旧 ref 一律 `STALE_SNAPSHOT` 且不发出输入）、元素/文本/JSON 限额与 `Truncated`、密码值永不回显、CDP 命中测试、cross-origin/closed-shadow 明确 `Unsupported`、截图 ≤4 MiB 受控缩放、BR07.native/BR08–BR10 runner |
| `feat/e02` | E02 证据接线 | MOD01–04、MCP01–05、SEC07.store、CONN03/04/05/08 共 14 条接 runner + 外部观察（8 条 PASS，6 条当次因 worktree 缺 `.tools/pi` 记 BLOCKED） |

### 2.3 父会话做的修复与合并

- `F01: let the native runner write evidence for any gate`（`3fa435e`）：原生 case 不止 G1（BR08–BR10 是 G2），
  证据路径按 gate 分开，runner 现在按 gate 选位与落盘；`verify --suite=native ... --gate=G2` 可用。
- `F01: give the real-Pi MOD/MCP steps the baseline Node on PATH`：那 6 条在合并后由 BLOCKED 变 PASS
  （worktree 缺 `.tools/pi`；主仓有，且必须用基线 Node，否则旧微信 node 报 `enableCompileCache`）。
- 移除分支误加进共享 `.git/info/exclude` 的 `scratch/` 行；把两条红证据留档在 `scratch/red/`。

### 2.4 当前 gate 状态（HEAD `72a5546`，本机 Windows 10 19045）

| gate | 结果 | 说明 |
|---|---|---|
| G0 | **passed=true exit 0** | 31 个必需位全 PASS；100 个 deferred（五目标）逐条打印 |
| G1 | exit 2 | 6/7 PASS（BR00/01/04/05/06 + BR07.native）；缺 **UI04.native** runner（B07/U02 右栏） |
| G2 | exit 2 | 集成 56/69 可跑全 PASS、原生 BR08–BR10 PASS；缺 13 条：`SEC03.browser`、`SEC04.browser`、`CFG01`、`BR11`–`BR17` |

四个套件：`static` 0、`unit` 0、`integration --gate=G0` 0、`native --gate=G1` 2（只缺 UI04.native）。

## 3. 仍未闭合（按影响排序）

1. **B05/B06（浏览器会话/接管/限额/策略/cookie/弹窗/下载）**：BR11–BR17 与 SEC03/04.browser，是 G2 的最后一块。
2. **B07/U02（右栏与弹层遮挡）**：UI04.native 是 G1 的最后一块。
3. **U02–U07 页面**：聊天、右栏、资源库、记忆、观测、主题/i18n 逐页迁移、品牌页；对应 UI01–UI13、E2E01–07。
4. **CFG01**：合并 provenance 目前不落盘（纯函数），需要一处可观察的落点才能取证。
5. **X03/X04 + F09**：打包（`devtools package` 仍是占位）、更新服务、性能与发布检查（PKG/UPD/PERF/REL/INS/PI_PKG）。
6. **BLOCKED（外部条件）**：签名预认证清单（无发行私钥）、托管包五目标 `sizeBytes`、真实 OAuth/连接器账号、
   macOS/Linux 真机（ADR 0005 下本轮不验收）。

## 4. 下一步（并行波次，均独立 worktree）

- 波次 2：`B05`（会话/接管/限额/策略 → BR11–BR14 + SEC03/04.browser）与 `U02`（聊天页 + 右栏 → UI01/UI02/UI04.native/RUN01.ui/RUN04.ui）并行。
- 波次 3：`B06`（cookie/弹窗/下载 → BR15–BR17）与 `U03/U04/U05`（Agent/资源库/记忆）并行。
- 波次 4：`U06/U07`（观测/主题/i18n/品牌）与 `F09`（更新服务）并行。
- 波次 5：`X03`（打包）→ `X04`（迁移/更新/性能）→ `E01` 收尾（CFG01 等）。

纪律不变：失败用例 → 最小实现 → focused 验证 → 原始证据 → 可独立审查的提交；**先提交、工作树完全干净
（含未跟踪文件）再取证**；每个 worktree 分支合并后由父会话重跑证据（revision 变了旧证据即失效）。

## 5. 2026-11-07：并行第二波（U02 合并、CFG01 取证、B05 半程）

- **U02 已合并**（`8286862`）：聊天页、输入区（项目/会话、模型、思考级别、权限摘要、附件）、工具卡片、
  审批卡、澄清卡、右栏四分区（浏览器/产物/引用/执行，缺数据 = `unavailable`）、窄窗抽屉不重建宿主节点。
  前端 222 测试 + typecheck + 文案门禁全绿；审批只由 `approval.resolved` 推进（替换了那条冲突用例）。
  后端缺口已写进报告：浏览器分区无绑定（B07）、`RunDetails` 缺 `Citations`、`StartSessionRun` 不收
  ProfileID/ThinkingLevel、无上下文超限稳定错误码、`AttachmentRequest` 无 Skill 身份。
- **CFG01 已取证**（`da75952`）：新增 `Desktop.EffectiveConfig()`（从磁盘层文档合并，返回 values/provenance/
  warnings/存在的层）+ `SaveConfigLayer`（CAS 写）；观察值来自"第二个 store 实例读回的层文档"与
  `effective-config.json`。G2 的缺 runner 从 13 条降到 12 条。
- **B05 半程**（分支 `feat/b05`，2 个提交，未合并）：控制权状态机 + 每 tab 串行队列 + 限额 + 派发前二次授权，
  浏览器动作已注册为 MCP 工具（经 policy bridge）。**缺**：BR11–BR14 / SEC03.browser / SEC04.browser 的
  runner 条目与观察 artifact，以及"接管后 queued 归零""OutcomeUnknown 不重放"的端到端用例。
  包内测试通过（`internal/browser`、`internal/harnessmcp`），但不足以取证。
- 当前 HEAD 的四个套件：`static` 0、`unit` 0、`go test ./...` 0；`gate --id=G0` 仍 passed；
  G1 只缺 `UI04.native`；G2 只缺 `SEC03.browser`、`SEC04.browser`、`BR11`–`BR17`。

### 下一步（接手即做）

1. 续做 B05：补 6 条 runner + 观察 artifact，跑 `verify --suite=integration --case=BR11..BR14,SEC03.browser,SEC04.browser`。
2. B06：cookie/profile 隔离、弹窗继承、下载确认与 hash（BR15–BR17）。
3. B07 + UI04.native：右栏原生 slot、遮挡协调、接管（G1 的最后一块）。
4. U03–U07：Agent/资源库/记忆/观测/主题/i18n 逐页迁移（文案基线只许缩小）。
5. X03/X04 + F09：打包（`devtools package` 仍是占位）、更新服务、性能与发布。
