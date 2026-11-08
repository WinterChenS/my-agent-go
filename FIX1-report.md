# FIX1 执行报告 — 两个用户实测阻塞项

- 分支/worktree：`feat/fix1` @ `D:\project\anvil-wt\fix1`
- 基线：`bb5a6c9`（Merge MP1；= `feat/v1-foundation` = `my-agent-go` 当前 HEAD）
- 任务书：`FIX1-brief.md`（A：浏览器写动作总是「没有权限」；B：每次启动都等很久「Pi 连接」）
- 状态：**PASS（A 的主体在基线已存在，见 §1；B 本次实现）** + 一条必须跟进的缺口（§5）

---

## 1. Blocker A — 浏览器写动作审批

### 1.1 代码核对结论（与任务书前提不同，必须先说清）

任务书说「没有任何产品代码轮询 `PendingBrowserApprovals` / `ResolveBrowserApproval`」。
在 `feat/fix1` 的基线上这个前提**不成立**：M1（提交 `f527d2a`，已合入 `bb5a6c9`）已经把
审计通道接好了，位置在**应用装配层的默认 backend**，而不是 App.tsx 传 prop：

| 关注点 | 事实位置 |
|---|---|
| 轮询（默认 1s） | `frontend/src/features/chat/browserApproval.ts` → `useBrowserApprovals()` |
| in-flight 去重 / seq 防旧结果复活 | 同上：`inFlight`、`seq`、`reduceBrowserApprovals` |
| 失败不清空列表（读失败 ≠ 没有待审批） | 同上 `poll_failed` 分支 + `hint` |
| 产品装配（无需上层传 prop） | `ChatPage.tsx`：`useBrowserApprovals({ api: browserApprovals })`，`api` 为 `undefined` 时**回落到模块级** `browserApprovalApi`（真实 `Call.ByName`） |
| 渲染 | `MessageList` → `BrowserApprovalCard`（`browserItems` 为空时不渲染卡片） |
| 答复 | `decide()` → `ResolveBrowserApproval(approvalId, choice)`，成功后**重新拉取**（无乐观移除） |
| Go 权威 | `internal/app/browser_api.go`：`PendingBrowserApprovals` / `ResolveBrowserApproval` + `browserAuthorization`（默认 ask、60s 超时拒绝） |

也就是说：用户看到的「每次都等 60 秒然后没有权限」**不可能**是“前端没有轮询”导致的
（如果真没轮询，卡片不出现是必然，但 60 秒拒绝来自授权权威，两者都会得到同一句文案）。
本次不再重写一条平行的轮询链路（那会制造第二个真相来源），而是：

1. 补上任务书要求但基线缺失的一处行为：**页面在后台时暂停轮询**（隐藏窗口里没人看卡片，
   每秒一次跨进程调用只是白烧 CPU；回到前台立刻补一次）。
2. 用测试把“产品装配不传 prop 也轮询真实绑定”“一次只允许一个读取在飞”钉住，
   这两条正是任务书点名要证明、而基线没有覆盖的行为（现有用例都注入替身）。

### 1.2 本次改动

- `frontend/src/features/chat/browserApproval.ts`：轮询 effect 增加 `visibilitychange` 处理。
  - 挂载时的那一次读取**总是**执行（“这一页有没有待审批”得先知道）；
  - `document.hidden` 时停掉 `setInterval`，回到前台 `refresh()` + 重新起表；
  - 卸载/禁用时移除监听、清表。
- `frontend/src/features/chat/browserApproval.test.ts`：新增 2 条 hook 级用例（假时钟不必要，
  用可控 backend + `document.hidden` 覆写）：
  - 后台暂停、前台立刻补一次并继续；
  - 读取未落地时多次渲染 + 多个周期**不叠加请求**。
- `frontend/src/features/chat/ChatPage.test.tsx`：新增 2 条装配级用例：
  - **不传 `browserApprovals`**（模拟 App.tsx/shell.tsx 的现状），断言仍会调用真实
    `browserApprovalApi.pending()`，点「批准一次」调用 `resolve("bapr-1","once")`，
    并在答复后立刻重读（`pending` 调用次数 ≥ 2）——证明“答复后重新拉取、不做乐观移除”；
  - 断言“答复前卡片仍在”“失败显示结构化错误码”“没有 pending 不显示空卡片”由既有
    `browserApproval.test.ts` / ChatPage 用例覆盖（未改动、未弱化）。

### 1.3 未做（有意）

- 不把 `browserApprovals` 从 App.tsx 往上提：默认 backend 已经是真实绑定，再传一层只会
  多一个可能传错的参数；`ChatPage` 的默认值是产品路径，测试注入替身是测试路径。
- 不改 Go 侧授权/审批语义（`internal/app/browser_api.go` 不在 Touch 列表，且是对的）。

---

## 2. Blocker B — 启动等待

### 2.1 根因（已实测复现）

`ChatPage` 挂载 → `DiscoverPi()` → F08 `Discover` 对每个可用候选跑**六项隔离契约探测**
（spawn 真实 Pi + 回环 stub provider）。本机（PATH 上 `pi-node/current/pi.cmd`，v1.5.0）
实测：**单候选 10.37 s**；有 N 个候选就乘以 N。

### 2.2 快速发现路径（Go）

新增 `internal/runtime/pi/discover_fast.go`：`DiscoverFast(ctx, req)`。

- 与 `Discover` **同一套**来源/去重/状态判定，只在自己的请求副本上置
  `DiscoveryRequest.SkipContractProbe = true` 并清掉 `ProbeFunc`；
  既有 `Discover` 语义**未改**（默认仍跑完整契约探测）。
- 结果形状不变（同一个 `DiscoveryResult`），只多两个诚实声明：
  - `DiscoveryResult.verification`：`contract_probed` / `version_only`（新增类型
    `DiscoveryVerification`）；
  - `PiCandidate.contractVerified`：该候选**真的跑完并通过**六项探测。
- 绑定层：`internal/app/pi_lifecycle_api.go` 新增 `Desktop.DiscoverPiFast()`；
  `SelectPi` 也改用快速发现（点一下「选用」不该再等 10 s，选择不是验证动作）。
  `DiscoverPi` 保持完整探测（设置页显式「验证契约」用）。

**快速发现承诺什么**：候选的真实路径（启动器 + 真正被执行的运行时）、来源、
版本、发行形式、SHA-256 指纹、`missing/upgrade_available/ready_compatible/incompatible`
的可用性判定，以及“能启动”这件事。产物里明确 `verification=version_only`。

**不承诺什么**：这个二进制会不会说我们的协议（rpc/settled/mcp/extension/abort/cleanup
六项）。`ready_compatible + version_only` 只表示可启动、**未验证契约**；
`ready_verified` 仍需“版本已认证 + 哈希已登记 + 契约探测通过”三条同时成立，
未登记哈希永远不会因为本机跑通而升格。

### 2.3 前端

- `ChatPage` 的默认 readiness backend 改为 `settingsApi.discoverPiFast()`
  （接口 `ReadinessBackend.discoverPi` 形状不变，测试注入点不动）。
- **首次挂载不再阻塞输入**：`blockedHint` 只在**已得出否定结论**后才有值；
  检查在飞时只显示一条轻量 `data-composer-checking` 状态（`Composer` 新增
  `checkingHint` prop），`textarea` 不再因为“正在检查”被禁用。
  真正的异步兜底仍在：此时点发送会先等结论，`!ready` 就不发。
- `PiSettings`：
  - 挂载/列表用 `discoverPiFast`，并显示「列表来自快速发现：只探测版本与指纹…」；
  - 新增「验证契约」按钮 → `discoverPi`（完整探测），显示**耗时**与结论
    （`data-verification`、`data-elapsed-ms`）；
  - 每个候选多一个契约状态标记（`契约已验证 / 契约未验证`）。
- `settings/api.ts`：新增 `discoverPiFast`，`PiDiscoveryResult.verification`、
  `PiCandidate.contractVerified` 类型。

### 2.4 启动检查的前后耗时（本机实测，非验收证据）

测量方式：临时程序（已删除）直接调用 `pi` 包，请求形状与生产一致
（`PathEnv` = 真实 PATH、`CommonLocations()`、`ManagedRoot()`、`DefaultPolicy`）：

| 路径 | 耗时 | verification | 候选 |
|---|---|---|---|
| `Discover`（基线/完整） | **10.366 s** | `contract_probed` | 1（`pi-node/current/pi.cmd` 1.5.0） |
| `DiscoverFast`（本次） | **0.470 s / 0.478 s** | `version_only` | 同上 |
| `DiscoverFast`（独立包 1.5.0 作 preferred，单独复测） | 0.517 s / 0.529 s | `version_only` | 1 |
| `Discover`（同上独立包） | 10.331 s / 10.496 s | `contract_probed` | 1 |

→ 挂载检查 **≈ 10.4 s → ≈ 0.47 s（约 22×）**；有多个候选时基线上是“候选数 × 10 s”。
剩余 ~0.5 s 是 `--version` 版本探测（保留，快速路径的定义就是“只做版本探测”）。

---

## 3. 测试与证据

| 命令 | 结果 |
|---|---|
| `go test ./internal/runtime/pi/ ./internal/app/` | **ok**（`pi` 48.9 s，`app` 26.0 s） |
| `go test ./internal/runtime/pi/ -run 'TestDiscoverFast|TestDiscoverStillRunsContractProbe|TestDiscoverUsesContractProbe|TestUnknownPatchBecomesCompatibleOnlyAfterProbe|TestDiscoverReturnsAllCandidates' -v` | 6/6 PASS |
| `go test ./internal/app/ -run 'TestDiscoverPiFast\|TestSelectPi\|TestDiscoverPiListsCandidatesInPriorityOrder' -v` | 7/7 PASS |
| `go build ./internal/... ./cmd/devtools/` | exit 0（根包 `go build ./...` 需要 `frontend/dist`，见 §6 限制） |
| `gofmt -l internal/runtime/pi internal/app` | 无输出 |
| `go vet ./internal/runtime/pi/ ./internal/app/` | exit 0 |
| `npm run typecheck`（`D:/Program Files/nodejs/node.exe` v24.16.0） | exit 0 |
| `npm test` | **44 files / 392 tests passed**（基线 384 → +8） |
| `node scripts/check-ui-contract.mjs` | **[PASS]** 文案增量为 0，两种语言覆盖 40 个 Go 错误码 |
| `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static` | **[PASS] 退出码 0**：gofmt 无未格式化文件 / `go vet ./...` / 前端 typecheck |

新增/加强的用例清单：

- `internal/runtime/pi/discover_fast_test.go`（新）：
  - 快速路径**一次也不调用**注入的 `ProbeFunc`（计数器 = 0）；
  - 快速路径不会被一个 2 s 的慢探针拖住（探针根本没执行）；
  - 快速发现与完整发现的候选形状（id/版本/路径/指纹/来源/发行形式）一致；
  - 完整发现**仍然**跑契约探测（计数 = 1）、并把坏契约判成 `incompatible`、不选中。
- `internal/app/pi_lifecycle_api_test.go`（新增 1 条）：
  - `DiscoverPiFast` 探针计数 0、耗时 < 探针耗时、`verification=version_only`、
    `contractVerified=false`、最高 `ready_compatible`；
  - `SelectPi` 也不触发契约探测；
  - `DiscoverPi` 仍跑探测并给出 `contract_probed`。
- 前端：见 §1.2、§2.3（后台暂停、in-flight 去重、默认绑定装配、不阻塞输入、
  readiness 接受 `version_only`、PiSettings 快速发现与「验证契约」耗时）。

---

## 4. 改动文件

任务书 Touch 内：

```
internal/runtime/pi/lifecycle.go            (+SkipContractProbe/Verification/ContractVerified, classifyCompatibility 返回第三值)
internal/runtime/pi/discover_fast.go        (新)
internal/runtime/pi/discover_fast_test.go   (新)
internal/app/pi_lifecycle_api.go            (+DiscoverPiFast, SelectPi 改用快速发现)
internal/app/pi_lifecycle_api_test.go
frontend/src/features/chat/browserApproval.ts
frontend/src/features/chat/browserApproval.test.ts
frontend/src/features/chat/ChatPage.tsx
frontend/src/features/chat/ChatPage.test.tsx
frontend/src/features/chat/ChatReadiness.test.tsx
frontend/src/features/chat/Composer.tsx
frontend/src/features/settings/PiSettings.tsx
frontend/src/features/settings/api.ts
frontend/src/i18n/locales/zh-CN.json / en-US.json
```

Touch 列表**之外**（必须报告）：

```
frontend/src/features/settings/PiSettings.test.tsx   // 新增 discoverPiFast 替身 + 校验「验证契约」用例
frontend/src/features/settings/SettingsPage.test.tsx // 同上：fake 增加 discoverPiFast（委托给 discoverPi 替身）
```

理由：`PiSettings` 挂载从 `discoverPi` 换成 `discoverPiFast` 后，这两个测试文件里
`vi.mock("./api")` 的替身对象缺这个键会直接崩，属于修改同一行为必须同步的测试；
没有改动任何断言强度，也没有新增 skip。未触碰 `internal/devtools/**`、`tests/**`、
`docs/**`、`schemas/**`、`internal/run/**`、`internal/piinstall/**`。

---

## 5. 必须跟进的缺口（本次不能做，写清楚）

任务书说“完整契约探测保留给：**首次真正启动 run 前**（run 服务已有该逻辑）”。
核对后事实是：**run 启动前并没有契约探测**。

- `Desktop.Start`（`internal/app/desktop.go`）→ `resolveInstallation` 只做路径解析与
  托管指针哈希校验（`VERIFICATION_FAILED`），不跑 `ContractProbe`；
- 全仓库生产代码里没有第二处调用 `defaultContractProbe`/`ContractProbe`
  （`grep -rn "ContractProbe" internal --include=*.go | grep -v _test` 只命中
  `discover_fast` 之外的 `internal/runtime/pi/contract.go` 与 devtools 用例索引）。

因此本次改动后的真实语义是：**挂载/列表/选择都不验证契约；契约验证只剩下设置页的显式
「验证契约」**。后果：一个版本合法但协议坏掉的 Pi，会在 readiness 里显示为可用
（`ready_compatible`），到真正跑 run 时才失败。

没在本任务补 run-start 探测，因为那要改 `internal/app/desktop.go` 与 run 启动路径
（`internal/run/**` 明确禁止触碰，`desktop.go` 不在 Touch 列表），且会改变“启动一次 run
的时延/失败面”，属于需要 ADR 级别的决定。**建议下一任务（FIX2）**：在 `Start` 的
`resolveInstallation` 之后加一次“每个可执行体只做一次”的契约验证（按
`executableHash`+版本缓存结果），失败返回 `COMPATIBILITY` 结构化错误；这样快速发现
不背安全债。

---

## 6. 限制与口径

- 本机是 Windows 10 22H2（低于验收基线 Windows 11 24H2）：以上耗时与测试**只是开发证据**，
  不能作为 win-x64 验收结论；G0/验收需在符合要求的机器或 CI 上重跑。
- 根包构建/`go vet ./...` 需要先构建 `frontend/dist`（`frontend/embed.go` 的 `all:dist`），
  与本改动无关；本次先跑 `npm run build` 产出 `frontend/dist`（gitignore），
  静态门禁因此全绿。若在干净 worktree 上直接跑 static suite，第一条会因缺 dist 报
  `pattern all:dist: no matching files found`——那是环境前置，不是本改动的回归。
- 为了让两条既有的“真实 Pi”用例（`TestContractProbeAgainstRealPi`、
  `TestDiscoverPiAcceptsRealPiThroughProductionProbe`）在 worktree 里可执行，本地把主仓库的
  `.tools/pi/standalone/v1.5.0-win-x64`（gitignore 的本地工具链）复制到本 worktree；
  没有任何 `.tools/**` 或 `node_modules/**` 进入提交。
- 契约探测/`--version` 探测都在隔离 `agentDir`/HOME 下运行，未触碰用户真实 Pi 配置。
