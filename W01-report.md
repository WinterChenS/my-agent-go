# W01 报告：把浏览器面板接进产品（UI04.workbench / E2E03 的前置）

- Worktree：`D:\project\anvil-wt\w01`，分支 `feat/w01`
- 基线 revision：`0bc621b`
- 结论：**W01 = PASS**（面板挂载、桥接注册、遮挡接线、四条守门用例）。
  **UI04.workbench / E2E03 = NOT_RUN**：它们要的是真机上的成品集成（Windows 11 24H2 +
  Pi 操作真实表单 + 录屏），本轮没有产生原生证据，不用开发机冒充。

## 1. 提交

| commit | 内容 |
|---|---|
| `0970d1c` | W01: mount the browser panel in the chat page's browser section |
| `ef41ade` | W01: register the browser bridge in the desktop entrypoint |
| `d4382e5` | W01: red evidence for the panel mount and the exec provider |

每个提交都带 `Task: W01`，提交前 `git status --short` 为空。红证据是真跑的失败输出：
`scratch/red/w01-frontend-red.txt`（4 条新用例全红）、`scratch/red/w01-app-red.txt`（面板看不到
agent 的 tab）。

## 2. 交付物

### 2.1 面板接入（`frontend/src/features/chat/ChatPage.tsx`）

- 右栏浏览器分区从硬编码 `<BrowserUnavailable />` 换成 `<BrowserPanel>`：
  tab 列表、控制态（agent/human/pausing）、可见性、遮挡全部来自 `BrowserState()`；
  几何上报走 `UpdateBrowserSlot`。
- 停止按钮只在「这一页确实有一轮在跑」时出现（`runBelongsToPage && shell.busy`）：
  别的会话的 run 不能从这个页面被停掉。
- 宽窄切换仍只改 `aside` 的 class：面板与 slot 节点位置不变，不会被卸载/重建。

### 2.2 布局契约（`frontend/src/features/browser/BrowserPanel.tsx`）

面板原本是 `h-full`，而右栏是滚动容器：正常流里只剩内容高度，绝对定位的 slot
会量出 **0×0**——原生网页被摆成零尺寸（"看不见"，不是"没挂上"）。现在面板自己给出
一块可测量的高度（`h-[360px]`），理由写在组件头部注释里。

### 2.3 桥接注册（`cmd/myagent/main.go`）

`registerBrowserPanel` 按硬性顺序装配，任何一步失败都**不**注册"看起来可用"的空桥：
1. 等主窗口句柄（`NativeWindow()` 在 `a.Run()` 之前是 nil，最多等 30s）；
2. `application.InvokeSyncWithError` 在 UI 线程上 `Attach` 原生宿主
   （跨线程创建子窗口会被 `host_windows.go` 直接拒绝）；
3. 立刻 `SetVisible(false)`：`Attach` 建出的子窗口是 `WS_VISIBLE` 且覆盖整个客户区，
   面板还没上报几何就显示会盖住整个应用；
4. 构造 `browser.Service`（Authorizer = 默认拒绝，见 §4）→ `SetBrowserBridge`；
5. `a.OnShutdown` 里 `svc.Close()` + `host.Detach()`。
profile 目录固定为 `<appRoot>/browser-profile`（不与主 WebView 共用）。

### 2.4 补的绑定（`internal/app/browser_api.go`，按任务书"缺绑定要报告"补的）

- `BrowserBridgeOptions.ExecProvider` + `BrowserBridge.execContext()`：tab 的归属是**会话**
  （`browser.Service.Tabs` 按 `exec.SessionID` 过滤），而产品只有一个 Desktop。身份在构造时
  焊死，agent 后续在自己会话里开的 tab 就永远不出现——面板显示"没有 tab"，右侧却真有页面。
- `Desktop.BrowserExecutionContext()`：读 `runtimeSessionID`（与 `RunService.Start` 同一个值），
  在 `d.mu` 下取。身份在**持 b.mu 之前**解析，锁序保持单向。

### 2.5 遮挡接线（`features/chat/ApprovalCard.tsx`、`ClarificationCard.tsx`）

两张卡各加一行：
`useBrowserOcclusionLayer("approval-card" | "clarification-card", true)`。
协调器头部注释列出已接入来源，并写明以后新增 dialog 的接入方式（一行，别自己调
`setOverlay`，别顺手 `stop run`）。隐藏面板 ≠ 暂停 agent 这条没动。

### 2.6 测试

| 用例 | 位置 | 守什么 |
|---|---|---|
| 浏览器分区挂的是真实面板，tabs/控制态来自 BrowserState 绑定 | `features/chat/ChatPage.test.tsx` | 挂载路径 `[data-context-section='browser'] [data-browser-panel]`；旧占位消失；tab 文本、`data-browser-control` 与「恢复」按钮可用性来自 stub 绑定；slot 报 `available` 且真的调了 `updateSlot` |
| 没有原生宿主时如实显示 unavailable 与原因 | 同上 | `data-browser-slot-state='unavailable'` + 后端给的 `surface` 原因，不画假标签页 |
| 窄窗抽屉开关只改布局 | 同上 | 抽屉默认收起（隐藏不是卸载）；展开/收起后 `[data-browser-slot]`、`[data-browser-panel]` 是**同一个 DOM 节点**，tab 还在 |
| 审批卡/澄清卡登记遮挡层，消失后释放 | 同上 | `setOverlay("approval-card"/"clarification-card", true→false)`；卡片消失后遮挡必须解除 |
| 装的是真实面板时抽屉开关同样不重挂载 | `features/chat/ContextPanel.test.tsx` | 用真实 `BrowserPanel`，`onSlotMount` 计数恒为 1，宽→窄与关闭都不变节点 |
| `TestBrowserBridgeExecProviderFollowsCurrentRun` | `internal/app/browser_api_test.go` | 固定身份看不到别的会话的 tab；身份切到当前 run 后看得到，且 `SelectTab`/`CloseTab` 走同一身份 |
| `TestBrowserExecutionContextFollowsRuntimeSession` | 同上 | 未运行时会话为空（"真的没有 tab"）、工作区身份为 `defaultWorkspaceID`、跑过之后跟随 run 会话 |

顺带修掉一处会静默吞错的地方：`useBrowserState` 的 `updateSlot(...).then(onFulfilled)` 没有
拒绝处理器，"没有 Wails runtime / 绑定拒绝"会变成未处理的 rejection（面板看上去没反应）。
现在两种失败都落到面板的 hint 上。

没有 `it.skip`，没有弱化断言。测试 fixture 收在 `features/browser/testFixtures.ts`（只被用例
import，产品代码不引用；文案门禁不受影响）。

## 3. 验证（本轮实跑，含退出码）

```text
"D:/Program Files/nodejs/node.exe" node_modules/typescript/bin/tsc --noEmit   → 0
"D:/Program Files/nodejs/node.exe" node_modules/vitest/vitest.mjs run
  → Test Files 34 passed (34) / Tests 280 passed (280)，无 Unhandled Errors
"D:/Program Files/nodejs/node.exe" scripts/check-ui-contract.mjs              → 0（PASS，catalog 之外文案增量为 0）
go build ./...                                                                → 0
go test ./internal/app -count=1                                               → ok my-agent-go/internal/app 33.4s
./scripts/devtools.sh verify --suite=static                                   → 0（gofmt / go vet / 前端 typecheck）
```

- 前端用例必须用基线 Node（`D:/Program Files/nodejs/node.exe`，v24.16.0）。用 `npm.cmd`
  会解析到 PATH 首个 node v22（WorkBuddy），vitest 直接启动失败（`EventEmitterAsyncResource`）。
- 本 worktree 起初缺 `internal/app` 用例需要的本机工具链
  `<repo>/.tools/pi/standalone/v1.5.0-win-x64`（gitignore），
  `TestDiscoverPiAcceptsRealPiThroughProductionProbe` 因环境缺失失败；从主检出只读复制该目录后
  `go test ./internal/app` 全绿。该目录不进版本库。

## 4. 限制 / 未做（明确写在前面）

1. **UI04.workbench / E2E03 = NOT_RUN**。要求是 Windows 11 24H2 上的成品集成（Pi 真实操作右侧
   表单、用户接管/恢复、server 提交次数准确）＋录屏；本机是 Windows 10 22H2，且本轮没有启动
   GUI 采集原生证据。W01 只把成品路径接通，不等于门禁通过。
2. **浏览器写动作默认拒绝**：产品还没有装配 `harnessmcp.BrowserPolicyAuthorizer`（BR07.tool 的
   审批接线）。`cmd/myagent` 因此传入 `unwiredBrowserAuthorizer`（Authorize/Recheck 都返回
   `UNSUPPORTED`，并写明"本次动作没有发出"）。这是有意的安全默认，不是遗漏；面板的
   tab/接管/导航/刷新不走 Authorizer，可用。**BR07.tool 接线时要替换它**，不能改成"总是同意"。
3. **启动装配未在真窗口上执行过**：`registerBrowserPanel` 只有 `go build` / `go vet` 覆盖，
   本轮没有启动应用观察 Attach → SetVisible(false) → 面板上报几何 → 原生视图就位这条链路。
   风险点已尽量写进代码注释（初始 `WS_VISIBLE`、必须在 UI 线程 Attach、失败即保持不可用）。
   建议在 UI04.workbench 之前先做一次开发机手跑（本机 win-x64 只能算开发证据）。
4. 面板在窄窗抽屉里关闭时，slot 会被量成 0×0（原生视图随之不可见），这是"隐藏"而不是
   卸载；等 UI04.workbench 在真机上核对抽屉开合的原生可见性表现。
5. 触摸清单的偏离（均因任务书条目本身要求）：`ApprovalCard.tsx` / `ClarificationCard.tsx`
   （条目 3 点名的两个浮层）、`ChatPage.test.tsx` / `ContextPanel.test.tsx`（条目 4 的用例）、
   `BrowserPanel.tsx` / `useBrowserState.tsx`（`features/browser/**` 允许的 additive 修正）。
   `internal/app/browser_api.go` 按"缺绑定"补了 ExecProvider，已在此报告。
6. 未触碰：`internal/devtools/**`、`tests/acceptance/**`、`docs/**`、`schemas/**`、
   `internal/run/**`、`internal/runtime/pi/**`。
