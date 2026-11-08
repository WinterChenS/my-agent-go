# UX1 报告：点发送之前就把「为什么发不出去」摆到界面上

状态：**REVIEW**（前端实现与前端证据完成；真机/原生验收未取证，见 §6）

分支：`feat/ux1`（worktree `D:/project/anvil-wt/ux1`）
提交：`f763e20`（实现 + 用例 + 红证据）、本报告所在提交

验证命令与实际退出码（基线 Node `D:\Program Files\nodejs\npm.cmd`；PATH 首位是 v16 的微信 Node，运行前必须前置 `D:/Program Files/nodejs`，否则 vitest 起不来）：

| 命令 | 退出码 | 结果 |
|---|---|---|
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run typecheck` | 0 | `tsc --noEmit` 无输出（`scratch/ux1-typecheck.log`） |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test` | 0 | 43 files / **377 tests** passed（基线 355 + 新增 22）（`scratch/ux1-test.log`） |
| `node scripts/check-ui-contract.mjs` | 0 | PASS：catalog 之外文案增量 0，40 个 Go 错误码两种语言齐全（`scratch/ux1-copy.log`） |
| `powershell -File scripts/devtools.ps1 verify --suite=static` | 0 | gofmt / go vet / 前端 typecheck 全 PASS（`scratch/ux1-static.log`） |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run build` | 0 | `vite build` 1676 modules（本地验证，未作为证据提交） |

原始红证据：`scratch/red/ux1-initial-red.txt`（三个模块不存在 + 面板/徽标/推荐项断言失败）。

---

## 1. 判定层（新增 `frontend/src/features/chat/readiness.ts`）

`evaluateReadiness(input)` 是纯函数，输入全部是后端事实：`DiscoverPi` 的结果（或探测错误）、
当前工作区、当前模型、设置。输出 `{ ready, blockers[], fixture }`。

三项前提，顺序固定 **Pi → 工作区 → 模型**：

| 前提 | 判据（后端事实） | 阻塞码 |
|---|---|---|
| Pi 可用 | 有候选且 `compatibility ∈ {ready_verified, ready_compatible}`，并且版本 ≥ `1.5.0` | `PI_MISSING` / `PI_TOO_OLD` / `PI_UNUSABLE` / `PI_UNKNOWN` |
| 工作区 | 已登记（`currentWorkspace` 非空）且 `trusted` | `WORKSPACE_MISSING` / `WORKSPACE_UNTRUSTED` |
| 模型 | 非 `source=none` 且 provider/model 非空；不是内置 fixture；旧字段端点不是端口 0 | `MODEL_MISSING` / `MODEL_FIXTURE` / `MODEL_ENDPOINT` |

刻意的取舍：

- **不复制后端的兼容性规则**。`ready_verified/ready_compatible` 是后端结合版本、指纹与契约探测
  得出的结论；前端只额外守一条最低版本下限（`MIN_PI_VERSION = "1.5.0"`，与
  `internal/runtime/pi.DefaultPolicy.Minimum` 对齐并有注释），因为 1.4.0 这种候选即使被标成
  ready 也必然跑不起来。
- **版本比较按段做**（`compareVersions`），读不出数字返回 `NaN` 让调用方显式处理，不做
  `"" < "1.5.0"` 这类字符串比较。
- **fixture 的判据是 provider，不是端口**。默认设置就是 `provider=fixture`
  （`internal/app/desktop.go:defaultSettings`）；端口 0 只说明「端点写错了」，把它当成 fixture
  会把真实失败说成“回放”。两者各是一条阻塞原因（`MODEL_FIXTURE` / `MODEL_ENDPOINT`）。

## 2. 界面（`ReadinessPanel.tsx` + `ChatPage.tsx` + `Composer.tsx`）

- 对话页**挂载时**检查一次；**每次发送前**再检查一次（`sendWhenReady` 先 `await` 新结论，
  不 ready 就不调用 `onSendToSession`）。重新探测的理由：Pi 被删、工作区被撤销信任、模型被换掉
 都发生在页面还开着的时候，只信挂载时那份快照就会重演“点了才知道发不出去”。
- 阻塞面板（`[data-readiness-panel]`）列出**全部**未满足项，每条给：标题、当前值（mono 等宽，
  来自后端）、要做的事、以及 `去设置 · <分区>` 按钮（`/settings?section=pi|permissions|model`）。
  面板不是 toast：它不会自己消失，只会在全部满足后整块消失。
- 输入区：`blockedHint !== null` 时 textarea 与发送按钮禁用，并显示 `data-composer-blocked`
  的可读解释（`role="status"`）。检查中与有阻塞分开说（“还没检查完”≠“你缺东西”）。
- fixture 徽标（`[data-fixture-badge]`）用 `fixtureSignal()` 算，独立于探测结果：即使 Pi 探测
  失败，“现在用的是脚本化回放”也必须写在屏幕上。

## 3. 设置页定位（新增 `frontend/src/app/SettingsRoute.tsx`）

`app/**` 只做路由级接线：读 `route.query.get("section")`，找到
`[data-testid="settings-<section>"]` 后 `tabindex=-1` + `focus()` + `scrollIntoView()`，并显示
`settings.jumpNotice`（“已定位到<分区>”）。**不认识的 section 什么都不做**：猜错分区比停在页首更糟，
用户会以为那一项已经处理完。`app/pages/index.tsx` 因此改为转出 `SettingsRoute`。

## 4. Pi 分区（`PiSettings.tsx`，只加预选/展示）

- 候选本来就会逐条列出路径/版本/来源/分发形式/指纹/探测状态；本轮补：`推荐` 标记
  （`data-testid=pi-recommended`，取 `discovery.selected`，否则第一个可选用候选）+ 未选用时的
  `confirmHint`（“探测到候选但还没有选用：点「选用」才会确认”）。
- **仍然不自动选用**：挂载后不调用 `SelectPi`，低于最低版本的候选按钮保持禁用。用例断言
  `selectPi` 在挂载后未被调用。

## 5. 用例（TDD，先红后绿）

| 文件 | 用例 |
|---|---|
| `features/chat/readiness.test.ts` | 版本比较；全新状态三条原因与跳转目标；`upgrade_available` 带版本与最低版本；后端标 ready 但 1.4.0 仍拦；探测失败不假装可用；工作区未登记 vs 未信任；全满足；fixture 与端口 0 的区别；`source=none` 是 MODEL_MISSING |
| `features/chat/ChatReadiness.test.tsx` | 挂载即列三条 + 三条跳转真的导航 + 输入区禁用 + 点发送不打到后端；Pi 过旧带 1.4.0/1.5.0；未信任带目录；全满足后面板消失、发送前再检查一次并真的提交；fixture 徽标出现/消失；探测失败 |
| `features/settings/PiSettings.test.tsx` | 候选事实完整、推荐标记不是“已选用”、挂载不调用 `selectPi`、点选用才调用、过旧候选禁用 |
| `app/SettingsRoute.test.tsx` | `?section=pi|model` 定位并说明来意；未知 section 不猜 |

`ChatPage.test.tsx` 的既有 harness 增加了 `readiness` 替身（与 `browserApprovals` 同一模式），
**不放宽任何断言**：替身只固定“Pi 可用”，工作区/模型仍由各用例给的 shell 事实决定。

## 6. 限制与未取证

- 只在 Chrome 之外的 jsdom 单测里验证了行为；**没有**在 Windows 11 24H2 + WebView2 上跑
  native/E2E（本机是 Windows 10 22H2，按 AGENTS.md §4 不能作为 win-x64 验收证据）。
- 每次发送都会重新跑一次 `DiscoverPi`（会 spawn 探针）。这是“发送前真实检查”的代价；
  没有加缓存窗口，因为缓存会让“刚刚被删掉的 Pi”继续被放行。真机上如果探针明显变慢，
  需要单独评估（不在本 Task 范围内改 F08）。
- 发送前的检查与 `Composer` 的附件清理不是原子的：检查通过后、调用真正发出前若后端状态变化，
  附件列表已被清空。概率极低，但如实记录。
- 没有改任何 Go 代码（不需要新的只读 getter：`DiscoverPi`/`Status`/`CurrentWorkspace`/
  `CurrentModel` 已足够）。`CurrentModel` 在 `source=settings` 路径上不填 `item/revision`，
  前端类型里这两项却是必填（既有偏差），本轮只在测试里如实表达零值，未改 `types.ts`。
- 新增了 `frontend/src/features/settings/PiSettings.test.tsx` 与
  `frontend/src/app/SettingsRoute.test.tsx`（任务书 Touch 列表只点名了 `PiSettings.tsx`
  与 `app/**` 的实现文件；用例必须与被测代码同目录，故在此登记）。

Task: UX1
