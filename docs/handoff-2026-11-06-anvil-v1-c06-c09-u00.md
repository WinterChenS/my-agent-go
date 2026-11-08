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

## 6. 2026-11-07 第三波：B05 已取证并合并

- **B05 合并**（`ec5dd1d`）：控制权（接管/恢复/`Pausing`/`ControlUncertain`）、每 tab 串行队列、4+12 tab 限额
  （拒绝时用 backend 计数证明**没有创建实例**）、派发前二次授权、浏览器动作作为 policy-gated MCP 工具、
  陈旧 ref/换 origin 拒绝、`OutcomeUnknown` 不重放。观察值来自 `action-timeline.jsonl`、`control-states.jsonl`、
  `counters-*.json`、`policy-decisions.jsonl`、`mcp-calls.jsonl`。
- **G2 的六条浏览器检查已取证**：BR11/BR12/BR13/BR14、SEC03.browser、SEC04.browser 全部 PASS。
- 两个 gate 缺陷在本轮被修掉（都有回归测试）：
  1. `gate` 以前会扫整个 revision 目录，同一 case 同时属于 G0 与 G2 时两份证据互相判成"重复/串 gate"——
     **跑得越全 gate 越红**。现在按目录里的 gate 段过滤（`ac7b8ed`）。
  2. 原生 runner 支持 `--gate`（BR08–BR10 属 G2）。
- `E02` 的 MOD02/03、MCP01/02/03/05 在合并后能跑通，但只产出 test-process oracle，按纪律**改记为 BLOCKED**
  并写明缺口（需要把 fixture provider 收到的请求与探针结论落盘）；不再让它们看起来在自证。

### 当前 gate 状态（HEAD `de62371`）

| gate | 结果 | 说明 |
|---|---|---|
| G0 | **passed=true exit 0** | 31/31 必需位 |
| G1 | exit 2 | 只缺 `UI04.native`（B07） |
| G2 | exit 2 | 63/69 集成 PASS + BR08–10 原生 PASS；仅剩 `BR15`–`BR17`（B06）与 6 条 BLOCKED（MOD02/03、MCP01/02/03/05 需要外部观察装置） |

### 下一波

`B06`（cookie/profile 隔离、弹窗继承、下载确认与 hash → BR15–BR17）+ `B07`（右栏原生 slot/遮挡 → UI04.native）
+ `U03/U04/U05`（Agent/资源库/记忆页面）；随后 `U06/U07`、`F09`、`X03/X04`。

## 7. 2026-11-07 第四波：B06/B07 合并，G0 与 G1 同时通过

- **B06 合并**（`2679910`）：按 workspace 的 profile（cookie 隔离/重启保留/清除生效/不碰个人浏览器目录）、
  `window.open` 受控 tab（同 session、不破限额、不创建不受控窗口）、目标先行下载（未选目标不写任何目录、
  确认后 hash 匹配、取消无残留、文件名穿越清理、保存后登记 artifact）。
  迁移用 **008_browser.sql**（计划写 002，但 002 已被 memory 占用；版本号是主键，硬凑会静默跳过——脚本头已记录）。
- **B07 合并**（`4d16fed`）：`internal/app/browser_api.go`（slot 几何/tab 选择/接管恢复/状态查询）、
  右栏 `BrowserPanel`/`BrowserToolbar`/`BrowserTabs`/`BrowserSlot`/`useBrowserState`、`BrowserOcclusionCoordinator`
  （设置/命令面板/审批框依次弹出时隐藏与恢复原生网页）、接管后人工修改可被 agent 观察到。
  顺带修：`host.SetVisible` 在 controller 尚未创建时把 nil 当成错误，导致首次 geometry 上报必失败。
- **gate 结果（HEAD `4d16fed`，干净工作区）**：
  - **G0 passed=true exit 0**（31/31）
  - **G1 passed=true exit 0**（7/7，含 `UI04.native`）
  - G2 exit 2：66/69 集成 PASS + 原生 PASS；**只剩 6 条 BLOCKED**（MOD02/03、MCP01/02/03/05 缺观察装置）
- 已知缺口（写进报告，不是通过）：
  - `BrowserPanel` 还没挂进产品 `ChatPage`，`cmd/myagent/main.go` 也没调用 `SetBrowserBridge` → 真实应用里仍显示
    `available=false`（UI04.workbench/E2E03 的前置，属 U02/main）
  - 原生宿主只呈现单 tab；back 是应用级历史（无 BFCache）；隐藏时无法 CDP 截图；OS 打字只覆盖小写 ASCII
  - 真实 WebView2 的 `NewWindowRequested`/`DownloadStarting` 未接（B06 用受控 backend 证明语义，limitations 已写）
- `E03` 已派发（模型 `deepseek-2/deepseek-v4.1-flash`）：给那 6 条补 observation 装置，是 G2 的最后一块。

## 8. 2026-11-07 第五波：E03 合并，**G0/G1/G2 三项全部通过**

- **E03 合并**（`e868f8d`）：MOD02/03、MCP01/02/03/05 六条补上外部观察装置（probe 收到的请求体 + 探针结论、
  MCP 侧工具调用/诊断结论落盘），6 条全部转为 `external-observation`。`Blocked` 字段改为
  `realPiBlockedReasonFor(...)` 的只读探测（装置缺失时如实 BLOCKED，不 skip 成 PASS）。
- **gate 结果（HEAD `9b2435f`，干净工作区，本机 win-x64 / Win10 19045）**：

| gate | 结果 |
|---|---|
| **G0** | **passed=true exit 0**（31/31） |
| **G1** | **passed=true exit 0**（7/7，含 UI04.native） |
| **G2** | **passed=true exit 0**（69/69；66 条集成 + 3 条原生） |

四套件：`static` 0、`unit` 0、`integration --gate=G0/G2` 0/0（G2 集成 66/66 通过，BR08–10 走原生）、
`native --gate=G1/G2` 0/0。
ADR 0005 下每个 gate 另有 deferred 位（五目标），gate 报告逐条打印，不计通过也不计缺失。

- 已派发 U03（Profile 编辑页，UI07）与 U04+U05（资源库/技能链接导入 RES05、记忆页 UI08/UI09），
  模型 `deepseek-2/deepseek-v4.1-flash`，各自独立 worktree。

### 剩余工作（G3 及之后）

1. **U03–U07**：Profile 编辑页、资源库（含 RES05 URL 导入与试运行）、记忆页、观测页、主题/i18n 逐页迁移、品牌页；
   对应 UI07–UI13、SRCH01、I18N/THEME/BRAND、RES05。
2. **产品接线**：`BrowserPanel` 挂进 `ChatPage`、`cmd/myagent/main.go` 调 `SetBrowserBridge`（UI04.workbench/E2E03 前置）。
3. **CONN07/SCH06**：连接器页与调度页（C08/C09 已有后端）。
4. **RUN01/04/06.ui**：聊天页真实 Wails + Pi 端到端。
5. **X01 E2E01–07**、**X03 打包**（`devtools package` 目前是占位）、**X04 迁移/更新/性能**、**F09 更新服务**、
   **X05 REL01–04**。
6. **仍 BLOCKED**：签名预认证清单（无发行私钥）、托管包五目标 `sizeBytes`、真实 OAuth/连接器账号。

## 9. 2026-11-07 第六波：U03 / U04+U05 合并；三项 gate 在新区间仍全绿

- **U03 合并**（`be26494`）：单个 Agent/Profile 产品编辑器（列表、CAS 保存、冲突呈现、来源查询、复制只带引用、
  删除按 revision 确认、上限越界结构化拒绝且不静默夹回）。**UI07 判 BLOCKED**（诚实）：`profile.Profile` 没有
  Description/SystemPrompt/ReasoningLevel/ToolIDs，本轮禁改 `internal/profile`，所以这些字段在 UI 上标 `unavailable`；
  且 UI07 是 NATIVE case，无真机证据。另一处已记录：旧 `Desktop.SaveProfile` 仍可写入越界值（随后被 Merge 静默收紧）。
- **U04+U05 合并**（`27c2c76`）：资源库页面（Skill/Agent/MCP，导入/新建/启停/删除，逐项显示拒绝原因）+
  **RES05 真实实现**（HTTPS only、大小/解压/文件数/重定向上限、public-destination-only 对每个 DNS answer/重定向/实际
  peer 生效、拒 IANA special-purpose 段、不执行脚本、试运行用临时 agentDir + 最小只读策略并清理）+ 记忆页
  （scope 过滤/开关不删除/导出/empty 与 unavailable 分开）。UI08/UI09 的 NATIVE 外部证据未产出（属 G3）。
  合并时 locale 冲突用深合并解决（两边各加了一个顶层命名空间）。
- **证据重跑**（HEAD `4f6af94`）：`G0`/`G1`/`G2` 三项 **passed=true exit 0**（31/31、7/7、69/69）。
- 期间修掉 B04/BR10 的两处计时竞态（fixture 计数异步记账 → 有界轮询；可满足后置条件 10s→12s，仍在 15s 动作超时内）。
- **环境敏感**（重要）：`tests/native` 是真实 WebView2 GUI 测试，**在整仓并行 `go test ./...` 时**会偶发
  "counter 一直为 0 / 键盘输入未到达"（同一提交下单独跑 native 套件 7/7、3/3 全过）。取证必须**单独**跑
  `verify --suite=native`，并确认没有残留 `msedgewebview2` 进程；不要把整仓并发跑的红当作产品回归。

### 剩余（G3 及之后）

1. **U06/U07**：观测页、主题/i18n 逐页迁移、品牌页（UI10–UI13、I18N/THEME/BRAND）。
2. **产品接线**：`BrowserPanel` 挂进 `ChatPage`、`cmd/myagent/main.go` 调 `SetBrowserBridge`；UI07 需要的
   `internal/profile` 字段扩展（Description/SystemPrompt/ReasoningLevel/ToolIDs）与 run 启动路径把 system prompt/思考级别
   送进 Pi 请求。
3. **CONN07/SCH06**（连接器页/调度页）、**RUN01/04/06.ui**（真实 Wails + Pi 端到端）。
4. **X01 E2E01–07** → **X03 打包** → **X04 迁移/更新/性能** → **F09 更新服务** → **X05 REL01–04**。
5. **BLOCKED**：签名预认证清单、托管包五目标 `sizeBytes`、真实 OAuth/连接器账号。

## 10. 2026-11-08 第七波：U06（设置/观测）与 W01（浏览器面板接线）合并

- **U06 合并**（`bd2fa35`）：设置页（常规/模型/权限/Pi/安全/更新/诊断）与观测页（usage/run details/诊断导出）。
  Pi 分区展示真实候选（路径/版本/来源/hash/探测态）；**更新分区渲染 unavailable**（F09 还不存在，不画假进度）；
  CAS 冲突呈现 `CONFIG_CONFLICT`。前端 309 测试 + typecheck + 文案门禁全绿。
  报告的后端缺口：缺 `ListRunUsage`（观测页跨 run 列表）、缺 `CredentialStoreStatus`、缺 `PiInstallManifest`、
  `SaveSettings` 无 revision（无法返回冲突）、导出缺 `HostTarget`。
- **W01 合并**（`53a0fef`）：浏览器面板挂进聊天页右栏、`cmd/myagent/main.go` 注册 browser bridge、
  遮挡协调器接到审批卡/澄清卡；修两处静默错误（面板 `h-full` 让原生 slot 量出 0×0；`updateSlot` 缺拒绝处理器）。
  启动装配默认拒绝授权（`unwiredBrowserAuthorizer`），BR07.tool 的授权接线仍未做。
  UI04.workbench / E2E03 = NOT_RUN（需真窗口手跑 + 录屏）。
- **gate 在 `53a0fef` 仍全绿**：G0 31/31、G1 7/7、G2 69/69。
- 再次确认：native 套件在负载下会偶发丢输入/计数（同提交第二次就全过）。取证单独跑并清理残留进程。

### 剩余

1. **U07**（唯一可写 `tests/acceptance/cases.json` 与 devtools UI runner 的任务）：可访问性、视觉回归、真机 UI 验收，
   把 UI01–UI13、I18N/THEME/BRAND 的 NATIVE 外部证据做出来 → 这是 G3 的主杠杆。
2. **BR07.tool**：浏览器写动作的授权接线（当前默认拒绝）。
3. 后端缺口补齐：`ListRunUsage`、`CredentialStoreStatus`、`PiInstallManifest`、`SaveSettings(revision)`、
   `HostTarget`、profile 的 Description/SystemPrompt/ReasoningLevel/ToolIDs + run 启动路径。
4. **CONN07/SCH06** 页面、**RUN01/04/06.ui** 端到端。
5. **X01 E2E** → **X03 打包** → **X04 迁移/更新/性能** → **F09** → **X05**。

## 11. 2026-11-08 第八波：B2（缺失绑定）与 T1（浏览器写授权）合并

- **B2 合并**（`0aa45a2`）：`ListRunUsage`（确定性 offset 分页，空 ≠ unavailable）、`PiInstallManifest`（`usable` 与
  `Entry()` 同源）、`SaveSettingsWithRevision`（内容派生 revision → `CONFIG_CONFLICT` 且文件不变，旧接口不变）、
  `CredentialStoreStatus`（稳定码、无秘密）、`HostTarget`（唯一映射）。19 条新用例。
  登记遗留：settings CAS 只支持相等比较；`persistPiSelection` 仍未持 `settingsMu`（既有并发丢更新风险）；
  `ListRunUsage` 深翻页会随新增记录位移（要严格快照需 keyset）；前端还没消费这些新绑定。
- **T1 合并**（`9bc98fa`）：浏览器写动作接上真实授权（policy bridge，交互默认 ask、session/site 授权、origin+
  navigationId 进指纹），**authorizer 缺失仍然 fail closed**，并有能抓住"默认放行"回归的用例；
  `BR07.tool` 产出 external-observation 证据（fixture 计数 + 策略账本 + 动作时间线）。
  仍未做：浏览器 MCP 工具没有注册进产品自有 MCP（`internal/app/mcp_api.go` 的 harnessTools 不在 T1 范围）；
  没有浏览器审批 UI（ask 会 60s 超时 → 拒绝）；应用层授权用专用 policy 服务（run 的 `effectivePermissions` 把 network 写死 false）。
- **gate（`eb75496`）**：G0 31/31、G1 7/7、G2 69/69 全 passed；前端 314 测试。
- 再次确认 native 套件负载敏感：本轮 G2 原生第一次 BR10 失败、同命令重跑 3/3 通过。

### 剩余（按优先级）

1. **U07**：可访问性/视觉回归/真机 UI 验收，唯一可写 `tests/acceptance/cases.json` 与 devtools UI runner 的任务；
   把 UI01–UI13、I18N/THEME/BRAND 的 NATIVE 证据做出来（G3 主杠杆）。
2. 前端消费 B2 的新绑定（观测页跨 run 列表、设置页真实版本列表/凭证状态、SaveSettings 冲突）；
   浏览器审批 UI + 把浏览器工具注册进产品 MCP。
3. CONN07/SCH06 页面；RUN01/04/06.ui 端到端。
4. X01 E2E01–07 → X03 打包 → X04 迁移/更新/性能 → F09 → X05。
