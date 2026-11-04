# ADR 0004：Windows 原生 WebView2 宿主依赖 go-webview2

- 日期：2026-11-04
- 状态：已采纳（B01a 范围内）
- 影响任务：B01a（Windows 原生宿主），B01b、B02、B03 会沿用同一决策的边界
- 相关证据：`docs/evidence/b01-native-webview2.md`、`docs/evidence/raw/b01-native/`

## 背景

基线 R06 与设计文档 §4 要求「同一个主窗口右侧是真实网页」，明确排除 iframe、
浮窗、截图流。B00 实测得到的事实是：

- Wails v3.0.0 的 `WebviewWindow` 是**顶层窗口**，`win.NativeWindow()` 返回的是整个
  顶层窗口的 HWND（Windows 上窗口类 `WailsWebviewWindow`）；
- 因此右侧必须自建 **WS_CHILD 子 HWND + WebView2 controller**，挂在这个顶层窗口下。

于是需要一个能创建 controller、导航、求值、截图的 WebView2 绑定。

## 选项与取舍

| 选项 | 结论 | 原因 |
|---|---|---|
| 直接用 Wails 的 `internal/webview2` | 不可行 | 该包在 `internal/` 下，外部模块无法 import（Go 的 internal 规则）。能力上它够用，但拿不到就等于没有。 |
| 给 Wails 打 patch，导出 internal 包 | 不选（备选） | 需要维护 fork、跟随 Wails 升级、把 patch 纳入锁文件；B01a 的时间盒与「不改运行时隔离」约束下收益不足。若第三方绑定失效，这是第一备选，并且必须另写 ADR。 |
| 再开一个顶层 `WebviewWindow` 伪造成子视图 | 禁止 | R06 明确排除；顶层窗口不随主窗口裁剪、不能被主窗口覆盖，遮挡与坐标都会失真。 |
| 自写 WebView2 COM 绑定 | 不选 | 等价于重做 `CreateCoreWebView2EnvironmentWithOptions`、事件处理器、消息循环、Loader 发现，成本远高于收益。 |
| `github.com/jchv/go-webview2`（pkg/edge + go-winloader） | **采纳** | 纯 Go、无 cgo；`pkg/edge` 是导出包，提供 `Chromium{Embed,Navigate,Eval,Show,Hide,Resize,Focus,GetController,GetSettings}` 与 `ICoreWebView2*` COM 类型；`go-winloader` 内嵌 `WebView2Loader.dll`，不依赖 PATH 或安装目录。 |

已验证的事实（scratch 与仓库内 spike 都跑过）：

- `edge.Chromium.Embed(childHWND)` 在 WS_CHILD 子窗口里成功创建 controller
  （实测 ~300ms），WebView2 在其下再建 `Chrome_WidgetWin_0/1` 渲染窗口；
- `GetParent(child) == main`、`GetAncestor(child, GA_ROOT) == main` 成立；
- CDP `Page.captureScreenshot` 与 `Runtime.evaluate` 可用（通过本仓库自己的 vtable
  薄封装，见下）。

## 决策

1. `go.mod` **精确**固定 `github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808`，
   以及它带来的 `github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1`。
   不使用 `@latest`，不接受自动升级。
2. `build/versions.lock.json` 登记两条依赖（source/hash/license/purpose），并注明
   「非 semver 伪版本，升级需 ADR」。
3. WebView2 的 user data folder 必须与 Wails 主 WebView 分开，且**显式传入**
   （`HostOptions.ProfilePath`）。空值直接报错，不允许默认落到用户 AppData。
4. 只使用 `pkg/edge` 与 `webviewloader` 的导出面；对 edge 没有包装的 vtable 槽
   （`GetCoreWebView2` / `Close` / `CallDevToolsProtocolMethod`）在
   `internal/browser/native/edgewrap_windows.go` 里做**薄封装**，不 fork 上游。

## 非 semver 伪版本的升级策略

`v0.0.0-20260205173254-56598839c808` 是伪版本，语义是「某个 commit 的快照」，没有
兼容性承诺。规则：

1. 升级必须先写 ADR（或修订本 ADR），说明动机与验证范围；
2. 升级后必须重跑 `tests/native` 全部用例 + `cmd/browser-spike -native`，并把新
   证据追加到 `docs/evidence/b01-native-webview2.md`（不覆盖历史数据）；
3. 任何一次 `go get` 之后若 `go.sum` 里 `go-webview2` 或 `go-winloader` 的版本变化，
   都视为一次依赖决策，而不是例行更新。

## vtable shim 是 ABI 依赖（升级后哪条测试先红）

`edgewrap_windows.go` 里的 `controllerVtbl` / `webviewVtbl` / `navArgsVtbl` 顺序抄自
upstream 的 `ICoreWebView2Controller.go` / `corewebview2.go` / 事件参数类型，
等价于直接依赖 WebView2 的 COM ABI。谁先红：

| 变化 | 先红的用例/证据 |
|---|---|
| `ICoreWebView2Controller.GetCoreWebView2` 槽位错位 | `TestNativeRightPaneWindows`（CreateTab 阶段 `GetCoreWebView2` 直接失败，拿不到 `ICoreWebView2`） |
| `ICoreWebView2.CallDevToolsProtocolMethod` 槽位错位 | `TestNativeRightPaneWindows` 的 `Observe`/`Screenshot`（CDP 返回 HRESULT 或超时）；spike 里表现为 `B01|screenshot` 缺失 |
| `ICoreWebView2NavigationCompletedEventArgs` 槽位错位 | `TestNativeRightPaneWindows` 的 `waitReady`（navigationId 恒为 0 → `navigationCompleted` 事件不出现）；`TestNativeCreateDestroyRepeated` 的 `waitReady` |
| `ICoreWebView2Controller.Close` 槽位错位 | `TestNativeCreateDestroyRepeated`（关闭后子窗口/controller 状态不对，或进程退出时残留 `msedgewebview2.exe`） |
| WebView2 运行时升级导致 CDP 方法签名变化 | `TestNativeRightPaneWindows` 的截图断言（PNG 无法解码/尺寸不符）与 spike 的 `B01|screenshot` 行 |

另外两条静态防线：`edge.ComProc` 的类型是 `uintptr`，槽位错位不会在编译期报错，
所以**不能只依赖 `go build`**；`tests/native` 是必跑的。

## 已知限制（本轮确认，不修）

1. **失败路径会直接退出进程。** upstream `edge.Chromium.Embed` 内部在
   `EnvironmentCompleted` / `CreateCoreWebView2ControllerCompleted` 里用
   `log.Fatalf("Creating environment failed with %08x", res)`：controller 创建失败不是
   返回错误，而是 `os.Exit(1)`。后果：B01a 无法把「controller 创建失败的 HRESULT」
   作为错误值上报，只能在 stderr 看到一行 `Creating controller failed with 0x...`，
   退出码 1。我们的宿主在 `Embed` 返回 false 时给出明确错误，但**无法拦截那条
   Fatalf**。修法只有 fork 上游或改用别的绑定，属于 B01b/后续决策。
2. `edge.Chromium.Embed` 会向页面注入一段 `window.external={invoke:...}` 脚本
   （upstream 的 bridge 形状）。它位于**子视图**的页面世界，只把消息
   `postMessage` 给我们的宿主，**不通 Wails 主 WebView 的 bridge**；但这确实是一段
   页面可见的全局对象。B04 若要清理，可用 `Page.addScriptToEvaluateOnNewDocument`
   覆盖。
3. 该库只提供 Windows 实现。macOS/Linux 各自按 B02/B03 选自己的绑定，不在本 ADR
   范围内。

## 回退方案

若 go-webview2 在新版本上不可用（ABI 变化、运行时兼容问题、上游停更）：

1. 先尝试回退到本 ADR 固定的伪版本（`go get ...@v0.0.0-20260205173254-56598839c808`），
   验证 `tests/native` 是否恢复；
2. 若必须前进或前进后仍失败：改用「给 Wails 打最小 patch、导出 internal/webview2
   的 controller 能力」，patch 与其版本 hash 纳入 `build/versions.lock.json`，
   并新写 ADR；
3. 无论走哪条，`internal/browser` 的契约（`BrowserBackend` / `BrowserSurface`）不变，
   `cmd/browser-spike -native` 的三条硬判据不变——换的是宿主实现，不是验收口径。
