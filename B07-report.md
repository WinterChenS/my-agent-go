# B07 报告：右栏原生 slot、遮挡协调、接管 UI（UI04.native）

- Worktree：`D:\project\anvil-wt\b07`，分支 `feat/b07`
- 基线 revision：`14d4315`（本轮最后一个提交；证据目录名与 `result.json` 里的
  `sourceRevision` 一致）
- 结论：**UI04.native = PASS（win-x64/desktop）**；UI03 / UI04.workbench / E2E03 =
  **NOT_RUN（G3，且需要 U02/U03/U05 的页面与浮层落地）**。其它目标/平台仍缺宿主
  （B02/B03），不由本任务冒充。

## 1. 提交

| commit | 内容 |
|---|---|
| `662dc8c` | B07: app-layer browser bridge for the right-hand native slot |
| `78af83b` | B07: right-panel browser UI, slot geometry reporting and occlusion coordinator |
| `14d4315` | B07: UI04.native evidence for the slot, occlusion, stop button and hand-over |

每个提交都带 `Task: B07`，提交前 `git status --short` 为空。红证据在
`scratch/red/b07-app-api-red.txt`、`scratch/red/b07-frontend-red.txt`。

## 2. 交付物

### 2.1 应用层（`internal/app/browser_api.go` + `browser_api_test.go`）

`BrowserBridge` 是 React 与原生 surface/控制服务之间唯一的一层：

- `UpdateSlot`：revision 单调推进，**旧 revision 丢弃**，相同几何**不重复 Present**
  （计数 `slotDropped`/`slotDeduped`/`slotPresented` 都在 `BrowserState` 里可见）。
- **工具条保留区**：`BrowserChromeReservedTop = 96`（CSS px）。`slot.top` 落进保留区
  时返回 `UNSUPPORTED` 并**拒绝这次几何**（不夹、不摆）——停止按钮不可能被原生网页盖住。
- `SetOverlay(source, open)`：命名来源 + 引用集合，只调 `Surface.SetVisible`。
  **不接管、不关 tab、不改控制代数**；空来源直接拒绝。
- `SelectTab` / `CloseTab` / `Navigate` / `Back` / `Reload` / `TakeOver` / `Resume`：
  全部转发给 `browser.Service`，失败带结构化 `CallError`。
- `BrowserState()`：可 JSON 序列化，**不含 hwnd/handle**（用例断言序列化结果里没有
  `handle`/`hwnd`/`nativeparent`/`0x`）。没有宿主时 `available=false` +
  `unsupported=[{capability:"surface",reason:...}]`，不是空白面板。
- Desktop 绑定：`BrowserState`、`UpdateBrowserSlot`、`SetBrowserOverlay`、
  `SelectBrowserTab`、`CloseBrowserTab`、`NavigateBrowserTab`、`BackBrowserTab`、
  `ReloadBrowserTab`、`TakeOverBrowserTab`、`ResumeBrowserTab`。

### 2.2 前端（`frontend/src/features/browser/**`、`frontend/src/app/BrowserOcclusionCoordinator.tsx`）

- `slotGeometry.ts`：每帧合并、几何去重、revision 单调；`hide()/show()` 沿用最近几何。
- `useBrowserState.tsx`：状态**只**来自 `BrowserState()`（动作后重读 + 轮询）；
  slot 挂载时测量一次，之后 `ResizeObserver`/`resize` 驱动；卸载时 `hide()+flush()`。
- `BrowserSlot.tsx`：只标记位置，**没有 iframe、没有截图冒充**；`data-browser-slot`
  上的 `data-browser-slot-state`/`data-slot-visible`/`data-slot-revision` 来自后端事实。
- `BrowserToolbar.tsx`：地址栏（回车导航）、后退/刷新、控制状态徽标、接管/恢复按钮、
  **停止按钮（`data-browser-stop`）在工具条里**。主题全部走 `--anvil-*`。
- `BrowserTabs.tsx`：tab 列表只渲染后端给的 `tabs`，选中/关闭只转发事件。
- `BrowserPanel.tsx`：chrome 高度 = 后端 `chromeReservedTop`；slot 是 chrome 的兄弟节点，
  节点只创建一次。
- `BrowserOcclusionCoordinator.tsx`：`BrowserOcclusionProvider` +
  `useBrowserOcclusionLayer(source, open)` / `BrowserOcclusionLayer`，按来源引用计数，
  只调 `SetBrowserOverlay`。已装进 `App.tsx` 壳层最外层。
- i18n：`browser.*` 两种语言都补齐（`node scripts/check-ui-contract.mjs` PASS）。

### 2.3 原生用例（`tests/native/ui04_windows_test.go`、`internal/devtools/native.go`）

`UI04.native`（G1，win-x64/desktop）跑两条用例，**全部走应用层**
（`Desktop` 绑定 → `BrowserBridge` → 真实 WebView2），与 React 调的是同一条路径：

1. `TestUI04NativeSlotOcclusionAndStopButton`
   - `GetParent(child)==main`、`GetAncestor(child,GA_ROOT)==main`、controller ready；
   - slot revision 1 → 子窗口矩形与 `CSS×DPI/96` 期望值误差 ≤1px；
   - 覆盖工具条的 revision 2 → `UNSUPPORTED` 且子窗口**没有移动**；
   - `WindowFromPoint`：工具条保留区那一点**不在**原生子窗口树里，槽位中心**在**；
   - 设置 dialog → 命令面板 → 审批框各开各关：每次打开 `IsWindowVisible(child)=false`，
     每次关闭恢复 `true`；期间 `CreateTab/CloseTab/Attach` 计数不变、子窗口句柄唯一、
     tab 集合不变、页面 URL/标题不变；
   - 恢复后可信点击让 fixture 服务端 counter 恰好 +1（页面真的还能用）。
2. `TestUI04NativeHandOver`
   - agent（经服务写路径）填入 `agent-b07-写入` 并 `field_value` 读回 verified；
   - 页面里那个 input 的真实值 = agent 写入的内容（用户直接看得到）；
   - 接管 → `human`，agent 写动作 → `NEEDS_HUMAN`；
   - 用户手改：**OS 物理点击** + **OS 键盘 `keybd_event`**（本次实测走的就是
     `os-keyboard`，页面读回 `agent-b07-写入-human-edit`）；
   - 没有新快照就恢复 → `STALE_SNAPSHOT`；新快照后恢复 → `resuming`，再 observe → `agent`；
   - 恢复后 agent 用 `field_value` 后置条件读到**人工文本**（verified）；
   - 提交后 fixture 服务端 counter=1，服务端收到的 `name` 就是人工改过的值。

外部观察（`oracle.json` = `kind: external-observation`, `source: filesystem`），
artifacts：`window-facts.json`、`slot-geometry.jsonl`、`occlusion-steps.jsonl`、
`stop-button.json`、`handover.jsonl`、`screenshots/ui04-restored.png`。
事实全部从落盘文件读回再断言（例如 `occlusionHiddenTransitions=3`、
`occlusionDistinctChildHWND=1`、`occlusionCreatesDelta=0`、
`stopPointInsideNativeChildTree=false`、`slotRevisionsAccepted=1`/`Refused=1`、
`postRestoreCounterDelta=1`、`agentObservedHumanEdit=true`、
`submittedPayloadEqualsHuman=true`）。

## 3. 命令与结果

| 命令 | 退出码 | 结果 |
|---|---|---|
| `go test ./internal/app -run 'TestBrowser\|TestDesktopBrowser' -count=1` | 0 | 10 条 PASS |
| `go test ./internal/browser -count=1` | 0 | PASS |
| `go test ./internal/devtools ./internal/browser -count=1` | 0 | PASS |
| `go test ./internal/app -count=1` | 1 | 只有既有的环境依赖失败 `TestDiscoverPiAcceptsRealPiThroughProductionProbe`（本 worktree 没有 `.tools/pi/standalone/v1.5.0-win-x64`，`.tools/` 是 gitignore 的按 worktree 目录；主仓库有、本 worktree 没有，与 B07 无关） |
| `npm run typecheck`（`D:\Program Files\nodejs\npm.cmd`） | 0 | PASS |
| `npm test` | 0 | 30 文件 / **237 条** PASS（新增 15 条：slotGeometry 6、BrowserPanel 6、Coordinator 3） |
| `node scripts/check-ui-contract.mjs` | 0 | PASS（catalog 之外文案增量 0，40 个错误码两种语言齐全） |
| `scripts/devtools.ps1 verify --suite=static` | 0 | PASS（gofmt / `go vet ./...` / 前端 typecheck） |
| `scripts/devtools.ps1 verify --suite=native --target=win-x64 --display=desktop --gate=G1` | 2→**0** | 7/7 G1 必需 case PASS：**UI04.native**、BR00、BR01、BR04、BR05、BR06、BR07.native |
| `scripts/devtools.ps1 gate --id=G1 --evidence=docs/evidence/runs/14d4315/G1` | 0 | `passed=true`，通过 7 / 不通过 0；30 条其它目标必需位按 ADR 0005 标 `deferred`（未评估，不是通过） |

证据根：`docs/evidence/runs/14d4315/G1/win-x64/desktop/UI04.native/`（gitignore）。

注意：本机是 Windows 10 22H2（build 19045），低于基线最低验收系统 Win11 24H2，
所以这份 win-x64 证据是**开发证据**；正式验收要在符合要求的机器/CI 上重跑（GOV05 会拒）。

## 4. 边界与遗留（不冒充）

1. **`internal/browser/native/host_windows.go` 改了一处行为（已在此报告）**：
   `SetVisible` 在 controller 尚未创建时（几何/可见性先到、tab 后建——正是面板的真实
   顺序）会把 nil controller 当错误，导致面板第一次几何上报永远失败。现在 controller
   未就绪时只切子窗口可见性。这不是新访问器，是 B07 路径首次触发的顺序缺陷；
   已跑过的 BR04/BR05/BR06（都用到 SetVisible）仍全绿。
2. **`BrowserPanel` 还没有挂进产品页面**：`features/chat/ChatPage.tsx` 的右栏
   `browserSlot` 仍写死 `<BrowserUnavailable />`。要接上只需把它换成 `<BrowserPanel />`，
   但 `frontend/src/features/chat/**` 不在 B07 的 Touch 列表（U02 所有）。同理
   `cmd/myagent/main.go` 还没有把原生宿主挂到 Desktop（没有 `SetBrowserBridge` 调用），
   所以成品里 `BrowserState().available=false`——如实显示 Unavailable 而不是空白。
   这两处接线是 UI04.workbench（G3）的前提。
3. **设置 dialog / 命令面板 / 审批框还不存在**（设置是路由页，审批是消息内联卡片）：
   协调器的 API 已就位并被壳层安装，U03/U05 落地这些浮层时各加一行
   `useBrowserOcclusionLayer("<source>", open)` 即可。原生侧的隐藏/恢复与
   "隐藏 ≠ 暂停" 已用真实 WebView2 证明。
4. **回退是应用层地址历史**，不是引擎 BFCache（B00 契约没有 history API，改
   `edgewrap_windows.go` 的 vtable 必须同时更新 ADR，不在本任务范围）。**刷新 = 整页
   重新导航**（会换文档身份），注释与 UI 文案都写明。页面内 SPA 历史不参与回退。
5. **多 tab 的原生呈现**：`internal/browser/native` 目前只挂一个 tab（B01a 的已知限制），
   所以面板的 tab 列表实际上只有一条；选择/关闭的代码路径与用例是真的，但"两个 tab
   各自有自己的原生视图"要等宿主的 tab 视图池（B03/B06）。
6. **隐藏态没有页内截图**：controller 隐藏后 CDP `Page.captureScreenshot` 会超时（实测
   8s 未返回），所以隐藏只用 `IsWindowVisible` + `SetVisible` 计数 + 恢复后页面身份
   未变来证明，不用一张拍不到的图充数。
7. **中文手输仍需人工步骤**：本用例的人工修改是 OS 物理点击 + OS 键盘小写 ASCII；
   IME/中文是 B01b 已如实标注的同一限制。
8. `tests/e2e/browser_workbench.md`（计划里 B07 的 Files 之一）属 E2E03/G3，且
   `tests/e2e/**` 不在本任务 Touch 列表，未创建。
