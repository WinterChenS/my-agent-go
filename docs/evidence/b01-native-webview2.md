# B01a：Windows 原生 WebView2 子视图（核心链路）

- 日期：2026-11-04
- 分支：`feat/v1-foundation`，基线提交 `c49a93b`（B00）
- 范围：**只证明「同一主窗口右侧挂真实 WebView2 子视图」这条核心链路可行**，
  并留下可复核证据。DOM 全量语义、150%/200% 缩放、IME、50 轮创建销毁属于 B01b，
  本轮不做完，也不写成已支持。
- 结论：**PASS（B01a 范围内）**。三条硬判据全部成立（父子句柄关系、导航完成、
  CDP 页面像素非纯色），另有窗口级截图为辅。
- ADR：`docs/adr/0004-webview2-host-dependency.md`

## 1. 依赖决策（T1）

Wails v3.0.0 的 `internal/webview2` 不可 import（`internal/` 规则），所以右侧子视图
需要一个独立的 WebView2 绑定。选择 `github.com/jchv/go-webview2`，精确固定到：

```text
github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808   (MIT)
github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1  (ISC, 传递依赖)
```

`pkg/edge` 是导出包，提供 `Chromium{Embed,Navigate,Eval,Show,Hide,Resize,Focus,
GetController,GetSettings}` 与 `ICoreWebView2*` COM 类型；`go-winloader` 内嵌
`WebView2Loader.dll`（纯 Go，不需要 cgo 或 PATH 上的 Loader）。

### scratch 验证（先在 `.tmp/scratch-wv2` 独立模块里做，后改仓库）

最小 scratch：自建顶层窗口 + `WS_CHILD` 子窗口 → `edge.NewChromium()` +
`DataPath=独立目录` → `Embed(child)` → `Navigate(fixture)`。原文
`raw/b01-native/scratch-embed-cdp.log`：

```text
SCRATCH| main_hwnd=0x60906 class="scratchWv2Main"
SCRATCH| child_hwnd=0xd10a24 client=884x661 GetParent(child)=0x60906 parentIsMain=true
SCRATCH| Embed(child) returned true after 289ms
SCRATCH| GetController()=0x7ce800114308 inited
SCRATCH| enumChild(main): hwnd=0xd10a24 class="scratchWv2Child" parent=0x60906
SCRATCH| enumChild(main): hwnd=0x8e0a86 class="Chrome_WidgetWin_0" parent=0xd10a24
SCRATCH| enumChild(main): hwnd=0x45086c class="Chrome_WidgetWin_1" parent=0x8e0a86
SCRATCH| enumChild(main): hwnd=0x350a50 class="Chrome_RenderWidgetHostHWND" parent=0x45086c
SCRATCH| enumChild(main): hwnd=0x4904a8 class="Intermediate D3D Window" parent=0x45086c
SCRATCH| ICoreWebView2=0x7ce800124000（vtable 薄封装可用）
SCRATCH| CDP Runtime.evaluate => {"result":{"type":"string","value":"{\"ready\":\"complete\",\"title\":\"Browser fixture\",...
SCRATCH| CDP Page.captureScreenshot OK bytes(json)=42215 head={"data":"iVBORw0KGgoAAAANSUhEUgAAA8EAAAQQCAIAAABN785T...
```

结论：`Embed(childHWND)` 能把真实页面渲染进子 HWND；`WebView2` 会在我们建的子窗口
下再建 `Chrome_WidgetWin_0/1`、`Chrome_RenderWidgetHostHWND` 渲染窗口。

scratch 第一次跑时 CDP 调用超时的原因也记下来了：`CallDevToolsProtocolMethod` 的
完成回调由 **UI 线程的消息循环**派发，如果在同一线程上阻塞等结果就会死等。正确形状是
「UI 线程发起 → 调用方线程等结果」，B01a 的 `cdpCall` 就是这个两段式。

scratch 里的 `DataPath` 打印成 `...\.tmp\.tmp\scratch-wv2\profile`，是因为它在
`.tmp/scratch-wv2` 目录里又 join 了一次 `.tmp`，属于一次性的 scratch 路径，不进仓库。

## 2. 实现结构

| 文件 | 作用 |
|---|---|
| `internal/browser/native/host.go` | 平台无关类型：`HostOptions{ProfilePath,OnEvent}`、`BrowserEvent`、`WindowInfo`、`PageState`、`NativeHost` 接口 |
| `internal/browser/native/host_windows.go` | `NewWindowsHost` + `BrowserBackend`/`BrowserSurface` 全部方法 |
| `internal/browser/native/win32_windows.go` | UI 线程派发器（message-only 窗口 + `PostMessage`）、子窗口创建、DPI/坐标换算、句柄校验 |
| `internal/browser/native/edgewrap_windows.go` | edge 的**薄**补充封装：`GetCoreWebView2`、`Close`、`CallDevToolsProtocolMethod`、导航事件参数 |
| `internal/browser/native/observe_windows.go` | 隔离世界观察、click/fill、后置条件核验 |
| `internal/browser/native/host_other.go` | 非 Windows 明确返回 `UNSUPPORTED`（B02/B03 各做各的） |
| `cmd/browser-spike/native_windows.go` | `-native` 模式的探针与证据落盘 |
| `cmd/browser-spike/capture_windows.go` | 窗口级截图（屏幕 BitBlt / PrintWindow）与像素统计 |
| `tests/native/*` | 真实 Win32 窗口 + 真实 WebView2 的 4 条用例 |

线程模型：`Attach` 必须在拥有 parent HWND 的线程上调用（用
`GetWindowThreadProcessId(parent) == GetCurrentThreadId()` 校验，不匹配返回明确错误）。
之后所有 native 操作都经自建派发器投回该线程，`internal/browser/native` **不 import
Wails**，测试因此可以用自己的 Win32 窗口跑同一条代码路径；spike 侧用
`application.InvokeSync` 把 `Attach` 放回 UI 线程。

## 3. 实测命令与退出码

| 命令 | 退出码 | 日志/说明 |
|---|---|---|
| `go get github.com/jchv/go-webview2@v0.0.0-20260205173254-56598839c808` | 0 | 未执行 `go mod tidy`（工作区有 C04 的未提交改动） |
| `go build ./...` | 0 | 含 C04 工作区改动一起编译 |
| `go vet ./internal/browser/... ./tests/native ./cmd/browser-spike` | 0 | 无输出 |
| `gofmt -l internal/browser/native tests/native cmd/browser-spike` | 0 | 无输出 |
| `go test ./internal/browser ./tests/fixtures/browser -count=1` | 0 | 契约与 fixture 无回归（`raw/b01-native/go-test-regression.log`） |
| `go test ./tests/native -count=1 -v` | 0 | 4 条用例全 PASS，8.8s（`raw/b01-native/go-test-native.log`） |
| `.tmp\browser-spike.exe -native`（`MYAGENT_SPIKE_AUTOQUIT_SECONDS=75`） | 0 | verdict=`NATIVE_VIEW_PRESENT`（`raw/b01-native/spike-native.stdout.log`） |
| `.tmp\browser-spike.exe`（B00 模式，不带 `-native`） | 0 | verdict=`AS_EXPECTED`，B00 行为未被破坏（`raw/b01-native/spike-b00-mode.stdout.log`） |

GUI 退出码用 `cmd /c "... & echo %ERRORLEVEL%"` 包装脚本采集（
`Start-Process -PassThru` 的 `ExitCode` 在这台机器上读不到）。三项静态检查 + 两条
test 的最后一次合并结果见 `raw/b01-native/final-verify.log`（`build/vet/gofmt/两个
test` 全部 exit 0）。

环境：`go1.27.3 windows/amd64`、`Wails=v3.0.0`、`Platform=Windows 10 Pro 2009
(Build 19045)`、`WebView2=156.0.4314.58`、`dpi=96 scale=1.00`。

## 4. 硬判据一：HWND 父子关系（原文）

`spike-native.stdout.log`：

```text
B01|hwnd|main=0x4609dc child=0xb60acc GetParent(child)=0x4609dc parentMatches=true dpi=96 scale=1.00
B01|present|childRect=1100,178-1532,912 webviewChild=0x760b06
B01|zorder|0|0xb60acc class="MyAgentNativeHostChild" title=""
B01|zorder|1|0x2a0aec class="Chrome_WidgetWin_0" title=""
```

- `GetParent(child) == main`：同一个主窗口的子窗口，不是第二个顶层窗口。
- `tests/native` 另外断言 `GetAncestor(child, GA_ROOT) == main` 与类名
  `MyAgentNativeHostChild`（`TestNativeRightPaneWindows`）。
- z 序证据：主窗口的直接子窗口从上到下是「我们的子窗口 → Wails 主 WebView」。
  **这是本轮踩到的真坑**：一开始用 `SWP_NOZORDER` 摆位置，子窗口被 Wails 主 WebView
  盖住——CDP 截图正常、窗口截图却是占位 UI。改成 `HWND_TOP` 之后两种窗口截图都能拍到
  网页。`Present` 现在每次都会把子窗口抬到同级最上层（见 `moveWindow` 注释）。

几何：右栏 slot 是壳页面 `getBoundingClientRect()` 实测的 CSS px
`left=722 top=37 432×734`，`devicePixelRatio=1`；`Present` 按
`CSS px × (DPI/96)` 换算到父窗口客户区，子窗口客户区就是页面 viewport
（`B01|observe ... viewport=432x734 scale=1.00`），CDP 截图正好 `432×734`。

## 5. 硬判据二：导航完成与页面自报状态

```text
B01|page|readyState="complete" title="Browser fixture" url=http://127.0.0.1:60376/
B01|page|fixtureCountersInPage={}
B01|observe|snapshot=snap-1 nav=nav-2 elements=10 textChars=303 viewport=432x734 scale=1.00 unsupportedFrames=0
```

- `readyState=complete` + 非空标题，是页面自己在子视图里报的（`PageState` 走 CDP
  `Runtime.evaluate`），不是宿主猜的。
- `fixtureCountersInPage={}` 是页面内 `fetch('/__counters')` 的**初始值**：此时还没有
  任何写操作，所以是空对象。这条链路证明子视图与 fixture 在同一个网络栈里跑页面。
- `nav=nav-2` 是 WebView2 自己的 NavigationId（首次 `about:blank` 占掉 1）。

## 6. 硬判据三：页面像素（CDP 截图）

```text
B01|screenshot|path=...\b01-native-right-pane.png bytes=25207 pixel=432x734 css=432x734 scale=1.00 nav=nav-2
              stats={w=432 h=734 samples=4824 uniqueColors=83 dominant=#ffffff dominantRatio=0.923}
```

截图来自 `Page.captureScreenshot`（页面像素），**不是整屏截屏**：
`PixelWidth/Height = 432×734` 与 `CSSWidth/Height`、slot 宽度一致，`Scale=1.00`，
`NavigationID=nav-2`。PNG 解码后采样 4824 点、83 种颜色，不是纯色。

`Screenshot` 的实现细节：先 `Page.bringToFront`，然后 `Page.captureScreenshot`
（surface 模式）8s 超时后自动退回 `fromSurface:false`（renderer 模式）——
本机窗口被遮住时 surface 模式**实测挂过一次**，这条退路是为此加的，退路使用时会发
`screenshot_fallback` 事件。

## 7. 动作实测（click / fill，可信输入 + 后置条件）

```text
B01|act|click|delivered=true verified=true status=verified detail=观测到 url_equals="http://127.0.0.1:60376/form"
B01|act|fill|delivered=true verified=true  status=verified detail=观测到 field_value="B01a-fill"
```

- 元素定位用隔离世界里打的 `data-myagent-ref` 属性（元素身份），不是"按坐标重新解析"：
  `Act` 会先按 ref 找回元素、`scrollIntoView`、再取**当前** rect，标签/类型变了直接
  返回 `STALE_SNAPSHOT`。
- 点击/输入走 CDP `Input.dispatchMouseEvent` / `Input.insertText`（可信输入路径），
  不是 `element.click()`。
- `Verified=true` 只在后置条件被观测通过时出现；没有后置条件时返回
  `Delivered=true` + `Status=unknown`（`browser.DeliveredResult`），契约未改。

## 8. Capabilities（实测值）

```text
B01|capabilities|navigate=true observe=true act=true screenshot=true unsupported=8
```

口径是「**本实例真的成功过一次**」：`navigate` 在导航完成回调里置位，`observe` 在隔离
世界观察成功后置位，`act` 在可信输入派发成功后置位，`screenshot` 在 CDP 截图成功
后置位。构造后、使用前全是 `false` —— 这是刻意的（那时没有任何证据），B01b 会改成
创建时跑探针。

`Unsupported` 逐项给原因（原文见 `spike-native.stdout.log`）：

| 能力 | 原因 |
|---|---|
| `trustedInput` | 只实现 click/fill 的 CDP 可信输入路径，未做 trusted-only 站点的行为验证 |
| `act:key` / `act:scroll` | 未实现（B01b） |
| `act:select/checkbox` | 语义未实现（B01b） |
| `crossOriginFrames` | 只观察顶层文档；cross-origin iframe 进 `UnsupportedFrames` 并标原因 |
| `closedShadowRoots` | 读不到，不声称支持 |
| `downloads` / `popups` | 未实现（B06） |

## 9. 遮挡证据（应用 dialog vs 原生视图）

本轮只做到「**隐藏原生视图后 dialog 可见**」，并按任务要求给出可复核证据：

```text
B01|occlusion|nativeVisible=false childVisible=false
B01|dialog|open=true
B01|occlusion|appDialogOpen=true
```

`b01-native-dialog-occlusion.png` 里：右侧原生视图已隐藏（露出壳页面占位），
应用内 `<dialog>` 完整可见；`appDialogOpen=true` 是页面自己回报的。

**这条证据不能证明反向情况**——原生视图可见时，应用 dialog 会被它盖住（HTML 弹层在
主 WebView 里，原生子窗口在它上面）。那是 B07 的 `BrowserOcclusionCoordinator` 要
处理的，本轮只提供 `SetVisible` 这个原语并证明它有效。

## 10. 截图清单（`docs/evidence/raw/b01-native/`）

| 文件 | 内容 | 字节 | sha256 |
|---|---|---|---|
| `b01-native-right-pane.png` | CDP 页面像素（右栏内容本身，`432×734`，83 色） | 25207 | `9741140f55db9b72f81eb408638fbb94aa8e6706576ef7031792d0de827e7464` |
| `b01-native-window-slot.png` | 窗口级（屏幕 BitBlt）：主窗口 + 右栏真实网页（`/form`，含填入值） | 80428 | `dcdae8946f0ad86c27a54a0f6f63ece9ae846fbf59f53ad103e9135e5fd8b448` |
| `b01-native-window-printwindow.png` | 窗口级（`PrintWindow(PW_RENDERFULLCONTENT)`），抬到顶部后同样能拍到子窗口内容 | 77945 | `09187f418f4148c54d0b8d684a34c98f5f50790847c21206e9bda51551abcbc7` |
| `b01-native-dialog-occlusion.png` | 隐藏原生视图 + 应用 dialog 可见 | 115182 | `9e3f8594bb969ea9438084b3092a50a287346be9af73572e2da4c1c237205f2b` |
| `b01-native-dialog-occlusion-print.png` | 同上，PrintWindow 版本 | 112805 | `2b9f8961faa404448352c57e3089b0c3fe3510784b36666c47fe6108c2e3e583` |
| `b01-native-summary.json` | 探针摘要（含失败项列表，本轮为空） | 1767 | `f37c77d36b67d5748f830b1b6376cf2b20279635ff75137e48c538b7853951e1` |

窗口级截图的像素统计（`spike-native.stdout.log`）：

```text
B01|windowcapture|method=screen-bitblt ... uniqueColors=81 dominantRatio=0.555
B01|windowcapture|method=printwindow  ... uniqueColors=76 dominantRatio=0.555
```

**如实说明**：scratch 阶段（独立的顶层窗口，进程在终端后面跑）我用屏幕 BitBlt 采到过
纯色（`uniqueColors=1`），原因是窗口被其它窗口遮住 + 原生子窗口被主 WebView 盖住。
修好 z 序后，在 spike 里两种窗口抓取方式都能拍到网页；窗口级截图仍然只是**辅助**证据，
BR01 的硬证据是「父子句柄关系 + CDP 页面像素」。

## 11. 测试（`tests/native`）

```text
--- PASS: TestNativeRightPaneWindows (2.68s)         # BR01：子窗口/导航/页面像素/Observe/Act
--- PASS: TestNativeWindowGeometry100 (1.66s)         # BR04 子集：100% 缩放坐标、旧 revision 丢弃
--- PASS: TestNativeDialogOcclusionVisibility (1.73s)# BR05 子集：SetVisible 只切可见性，controller 存活
--- PASS: TestNativeCreateDestroyRepeated (2.65s)     # BR06 子集：3 轮创建/销毁 + Detach 幂等
```

用例跑的是真实 Win32 窗口 + 真实 WebView2，不 mock：截图来自 CDP 页面像素，父子关系
来自 `GetParent`/`GetAncestor`，位置来自 `GetWindowRect`。

## 12. 与 B00 契约的偏差

1. **`internal/browser/native` 额外导出了 `WindowInfo()` 与 `PageState()`。**
   `BrowserBackend`/`BrowserSurface` 的签名一字未改；这两个方法是原生宿主自己的
   观测面（证据采集、将来的地址栏/标题栏、fixture 计数链路）。`PageState` 的数据来自
   页面本身，注释里写明它是**不可信输入**，不参与授权或核验判定。
2. **`CloseTab` 的重复调用语义**：同一个 tab 重复关闭返回 `nil`（目标状态已达成）；
   关闭一个从未创建过的 tab 返回 `NOT_FOUND`（拼错的 id 不能静默通过）。
3. **`Act` 只实现 click/fill**：`key`/`scroll` 返回 `UNSUPPORTED` 且不发出任何输入，
   `Verification.Status=unsupported`；没有把「未实现」写成「支持」。
4. **`Capabilities` 采用「成功过才为 true」**（见 §8），B00 的占位实现是「全 false +
   原因」，两者口径一致，只是证据来源不同。
5. **观测/动作脚本注入 `data-myagent-ref` 属性到页面 DOM**（隔离世界与主世界共享 DOM）。
   这是 B01a 的元素身份方案；B04 会换成正式的引用注册表。

## 13. 已知限制（本轮确认）

1. **controller 创建失败会直接退出进程，拿不到错误值。**
   upstream `edge.Chromium.Embed` 在 `EnvironmentCompleted` /
   `CreateCoreWebView2ControllerCompleted` 里用
   `log.Fatalf("Creating environment failed with %08x", res)`；失败时进程以 1 退出，
   只在 stderr 留一行 HRESULT。我们的 `Embed` 返回 false 时会给出明确错误
   （"edge.Chromium.Embed(...) 返回 false ... HRESULT 只在 stderr"），但**拦不住**
   那条 `Fatalf`。本轮没有人为制造 controller 创建失败（那样只会得到一次进程崩溃），
   失败路径的完整原文因此只有 upstream 的代码位置，没有伪造的实验记录。
   修法：fork 上游或换绑定（见 ADR 0004 的回退方案）。
2. **`edge.Chromium.Embed` 会往子视图页面注入 `window.external={invoke:...}`**。
   它只把消息 `postMessage` 给我们的宿主，**不通 Wails 主 WebView 的 bridge**；
   B04 可以用 `Page.addScriptToEvaluateOnNewDocument` 覆盖掉。
3. **窗口被遮挡时 `Page.captureScreenshot`（surface 模式）可能挂住**，已加
   `fromSurface:false` 退路；两种模式的截图在尺寸/像素上等价，但退路使用时会发事件。
4. **WebView2 profile 目录在关闭 controller 后短暂被占用**：`t.TempDir()` 的自动清理
   会因此报 "directory is not empty"（实测），测试里改成自行重试删除。
5. 非 Windows 平台：`NewWindowsHost` 返回 `UNSUPPORTED`（B02/B03）。

## 14. 留给 B01b / 后续的清单

| 项 | 归属 | 现状 |
|---|---|---|
| 第二个及更多 tab（4/session、12/total） | B01b | 未做；`CreateTab` 第二次返回 `UNSUPPORTED` |
| 150% / 200% 缩放下的坐标误差 ≤2 CSS px | B01b（BR04） | 只验证 100%（`TestNativeWindowGeometry100` 在非 100% 时 Skip 并说明） |
| 分栏拖动 20 轮不漂移 | B01b（BR04） | 未做；`Present` 已有 revision 丢弃语义 |
| 中文 IME、focus、clipboard、最小化恢复 | B01b（BR05） | 未做；只验证 `SetVisible` |
| 创建销毁 50 轮 + RSS | B01b（BR06） | 只做 3 轮从测试里验证句柄不残留 |
| key / scroll / select / checkbox 语义 | B04 | 返回 `UNSUPPORTED` |
| 页面 dialog 遮挡协调器 | B07 | 未做；本轮只提供 `SetVisible` 原语 |
| trusted-only 站点的 `VerificationFailed`/`NeedsHuman` 行为 | B04（BR10） | 未做；`capabilities.trustedInput=false` |
| `verify --suite=native` 的 case 映射 | 复核者（devtools） | 建议见 §15 |

## 15. 给 `verify --suite=native` 的 case → `go test -run` 映射建议

| 验收项 | 测试 |
|---|---|
| BR01 | `go test ./tests/native -run TestNativeRightPaneWindows -count=1` |
| BR04 | `go test ./tests/native -run TestNativeWindowGeometry100 -count=1`（100% 子集；150/200% 尚未实现，建议单独记 NOT_RUN，不要判 PASS） |
| BR05 | `go test ./tests/native -run TestNativeDialogOcclusionVisibility -count=1`（仅可见性/遮挡子集） |
| BR06 | `go test ./tests/native -run TestNativeCreateDestroyRepeated -count=1`（3 轮子集） |
| B01a 集成（Wails 窗口） | `.tmp\browser-spike.exe -native`（退出码 0 且输出 `verdict=NATIVE_VIEW_PRESENT`） |
| B00 回归 | `.tmp\browser-spike.exe`（退出码 0 且 `verdict=AS_EXPECTED`） |

建议 devtools 里把「BR04 只覆盖 100%」「BR05 只覆盖可见性」「BR06 只覆盖 3 轮」写进
case 的 note，不要因为 run 通过就把整条 BR 标 PASS。

## 16. 进程残留检查

跑完 spike 与测试后检查：

```text
> tasklist /FI "IMAGENAME eq browser-spike.exe"
信息: 没有运行的任务匹配指定标准。
```

按命令行过滤 `msedgewebview2.exe`（`--user-data-dir` 含 `b01-native`、`myagent-native-profile`
或 `scratch`）无匹配：我们创建的 WebView2 浏览器进程随宿主进程退出。
机器上其它 `msedgewebview2.exe`（例如 `MangoDisk.exe` 的）与本轮无关，未动。

每次 spike 退出时 stderr 会有一行
`Failed to unregister class Chrome_WidgetWin_0. Error = 1412`：这是 WebView2 进程被
宿主带退时的已知噪音（窗口类已随进程回收），不影响退出码与句柄清理。

---

# B01b（2026-11-05）：缩放 / 分栏 20 轮 / 焦点 / 最小化 / 剪贴板 / IME / 50 轮

- 分支 `feat/v1-foundation`，基线提交 `62d3f98`（B01a 为 `73d6f01`）
- 范围：**只补 B01a §14 清单里的 BR04/BR05/BR06 子集**，不改 B00 契约签名
  （`BrowserBackend`/`BrowserSurface` 一字未改），不重做 B01a 已通过的链路。
- 以上 B01a 的数字与结论全部保留，未改一字。本节是追加。
- 结论：**BR04/BR05/BR06 的 Windows 子集有实测证据；三项都不是整条 BR PASS**，
  每项覆盖到哪一步、缺口在哪，见 §27 与 §28。

## 17. 本轮代码改动（`internal/browser/native/**`、`tests/native/**`）

| 改动 | 位置 | 说明 |
|---|---|---|
| 页面缩放接入 `SlotGeometry.Zoom` | `host_windows.go` `Present`/`CreateTab` | `ICoreWebView2Controller.PutZoomFactor`；`Present` 只用 `Zoom` 改**页面**缩放，子窗口物理尺寸仍按 `slot CSS × DPI/96`，两条换算分开 |
| `GetZoomFactor`/`PutZoomFactor` 薄封装 | `edgewrap_windows.go` | vtable 槽原本已在 `controllerVtbl` 声明，这里只加调用与错误处理；`GetZoomFactor` 回读用于证据 |
| `Focus(ctx, tab)` | `host.go` + `host_windows.go` | `ICoreWebView2Controller.MoveFocus(PROGRAMMATIC)`；BR05 的焦点子集需要它 |
| `CallCDP(ctx, tab, method, params, timeout)` | 同上 | **证据通道**：直接发一条 DevTools 命令，不经过隔离世界/后置条件核验。只给 tests/native 与 spike 采集“页面自己怎么说”的证据，不是产品路径（用它得出的断言在证据里都标成页面自报值） |
| `WindowInfo.ZoomFactor` / `WindowInfo.Focused` | `host.go` | 跨线程只读事实（`atomic` 存 float64 位模式），供证据与测试断言 |
| 归还自己持有的 `ICoreWebView2` 引用 | `edgewrap_windows.go` `releaseOwnedWebViewRef` | `getCoreWebView2` 出参是一次 AddRef，关 tab 时还回去；edge 自己 AddRef 的 controller/webview/environment **不动**（A/B 实测见 §25） |

契约面不变的部分：`browser.BrowserBackend`/`BrowserSurface` 的签名、`SlotGeometry`
的字段语义、`Capabilities` 的“成功过才为 true”口径都没改。新增的 `Focus`/`CallCDP`
是 native 宿主自己的观测/操作面（与 B01a 的 `WindowInfo`/`PageState` 同类）。

## 18. 命令与退出码

| 命令 | 退出码 | 日志 |
|---|---|---|
| `go test ./tests/native -count=1 -v -timeout 900s`（`MYAGENT_B01B_OUT=docs/evidence/raw/b01b-native`） | 0 | `raw/b01b-native/go-test-native-b01b.log`：11 个用例（7 个新增 + B01a 的 4 个，含 3 个缩放子测试）全 PASS，`ok 88.182s` |
| `go test ./tests/native -run TestNativeCreateDestroy50Rounds -count=1 -v`（第二次独立运行） | 0 | `raw/b01b-native/go-test-native-50rounds-run3.log`，50 轮全 PASS，53.985s |
| `.tmp\browser-spike.exe -native`（`MYAGENT_SPIKE_AUTOQUIT_SECONDS=75`） | 0 | `raw/b01b-native/spike-native.stdout.log`，`verdict=NATIVE_VIEW_PRESENT` |
| `.tmp\browser-spike.exe`（B00 模式，不带 `-native`） | 0 | `raw/b01b-native/spike-b00.stdout.log`，`verdict=AS_EXPECTED` |
| `go vet ./internal/browser/native ./tests/native ./cmd/browser-spike` | 0 | 无输出 |
| `gofmt -l internal/browser/native tests/native cmd/browser-spike` | 0 | 无输出 |

GUI 退出码仍用 `cmd //c "... & echo %ERRORLEVEL%"` 采集。环境：`go1.27.3
windows/amd64`、`Wails=v3.0.0`、`WebView2=156.0.4314.58`、Windows 10 Pro 19045、
测试窗口 DPI=96（scale=1.00）。

`go build ./...` 在本轮**不可用**：工作区有 C05（`internal/app/memory_api.go` 等）
的未完成改动，与 B01b 无关。B01b 改动的包单独构建/vet 通过：
`go build ./internal/browser/... ./cmd/browser-spike ./tests/native` → 0。

## 19. BR04 子集一：100% / 150% / 200% 页面缩放

`TestNativeZoomFactor`（子测试 100/150/200）。缩放用
`ICoreWebView2Controller.PutZoomFactor` 模拟，**没有**改 Windows 显示缩放
（那要注销/重启）。每个缩放断言四件事，原文（`go-test-native-b01b.log`）：

```text
zoom=1.00 zoomFactor(controller)=1.00 dpi=1.00 child=480x600@(728,151) viewportCSS=480x600 dpr=1.00 rectCSS=(24.00,146.33 95.98x23.00) centerCSS=(71.99,157.83) centerPhys=(799.99,308.83) physDiff=0.000px backErr=0.000px hit=trusted-target
zoom=1.00 OS物理点击 (800,309) 命中 WebView 子窗口 0xdf043c → counter +1（按钮物理矩形 96.0x23.0，落点分辨力 ≤ 半宽）
zoom=1.00 click counter 1 -> 2 (exactly +1)
zoom=1.00 screenshot=480x600 css=480x600 scale=1.00 uniqueColors=30
zoom=1.50 zoomFactor(controller)=1.50 dpi=1.00 child=480x600@(728,151) viewportCSS=320x400 dpr=1.50 rectCSS=(24.00,191.12 96.00x23.33) centerCSS=(72.00,202.79) centerPhys=(836.00,455.19) physDiff=0.000px backErr=0.000px hit=trusted-target
zoom=1.50 OS物理点击 (836,455) 命中 WebView 子窗口 0xdf043c → counter +1（按钮物理矩形 144.0x35.0，落点分辨力 ≤ 半宽）
zoom=1.50 click counter 3 -> 4 (exactly +1)
zoom=1.50 screenshot=480x600 css=320x400 scale=1.50 uniqueColors=35
zoom=2.00 zoomFactor(controller)=2.00 dpi=1.00 child=480x600@(728,151) viewportCSS=240x300 dpr=2.00 rectCSS=(24.00,157.55 95.98x23.50) centerCSS=(71.99,169.30) centerPhys=(871.98,489.59) physDiff=0.000px backErr=0.000px hit=trusted-target
zoom=2.00 OS物理点击 (872,490) 命中 WebView 子窗口 0xdf043c → counter +1（按钮物理矩形 192.0x47.0，落点分辨力 ≤ 半宽）
zoom=2.00 click counter 5 -> 6 (exactly +1)
zoom=2.00 screenshot=480x600 css=240x300 scale=2.00 uniqueColors=113
```

整理成表（同一 slot：CSS `640,40 480×600`，窗口 DPI scale=1.00）：

| 页面缩放 | controller `GetZoomFactor` | 子窗口物理 | 页面 `innerWidth×innerHeight` | `devicePixelRatio` | CDP 截图 | `physDiff` | `backErr` | OS 物理点击 |
|---|---|---|---|---|---|---|---|---|
| 1.00 | 1.00 | 480×600 | 480×600 | 1.00 | 480×600 | 0.000 px | 0.000 px | (800,309) → counter +1 |
| 1.50 | 1.50 | 480×600 | 320×400 | 1.50 | 480×600 | 0.000 px | 0.000 px | (836,455) → counter +1 |
| 2.00 | 2.00 | 480×600 | 240×300 | 2.00 | 480×600 | 0.000 px | 0.000 px | (872,490) → counter +1 |

两条换算（刻意分开，不混用）：

- **壳 → 原生**：子窗口物理 px = `slot CSS px × DPI/96`（与页面缩放**无关**）。
  1.00/1.50/2.00 三组子窗口都是 `480×600@(728,151)`，slot 不变它就不变。
- **页面内**：CSS viewport = 子窗口物理 px ÷ 页面缩放（容差 1px）；
  `devicePixelRatio = (DPI/96) × zoom`（实测 1.00 / 1.50 / 2.00）。

三个误差口径：

1. `physDiff`：元素中心物理坐标的两种独立算法之差——页面自报 `devicePixelRatio`
   算一次，宿主账目 `zoom(controller 实测) × DPI` 算一次。本机 DPI=96（scale=1）时
   两者在代数上恒等，所以三组都是 `0.000px`；**这不是“浏览器布局误差为 0”的证据**，
   只说明两套账一致。DPI≠1 的机器上它才会真正测量两套账的偏差（该场景本机未验证）。
2. `backErr`：把物理坐标按 `zoom×DPI` 换算回 CSS，与 `getBoundingClientRect()` 中心
   比较，三组都 `0.000px`（阈值 2 CSS px）。同一坐标上 `document.elementFromPoint`
   命中的是 `trusted-target` 本身。
3. **OS 级物理点击**才是独立测量：`SetCursorPos` + `mouse_event` 把算出来的**物理**
   坐标交给屏幕。`WindowFromPoint` 落在我们自己的 WebView 子窗口上，fixture 服务端
   `trusted-only` 计数器恰好 +1（三组各 +1）。之后再用 CDP 可信点击一次，计数器再 +1。
   按钮物理矩形 96×23 / 144×35 / 192×47，即落点分辨力就是按钮尺寸（物理坐标能点在
   按钮内任意位置都成功，误差上界 ≤ 半宽：48 / 72 / 96 px）。

**如实说明两条限制**：

- 150%/200% 是**页面缩放**（WebView2 `PutZoomFactor`），不是 Windows 系统 DPI 缩放
  （改系统显示缩放要注销/重启）。BR04 的文字是“缩放100/150/200%或平台等价分辨率”，
  这里覆盖的是前者；系统 DPI 150%/200%（`GetDpiForWindow` 返回 144/192）**未做**，
  属环境限制。
- 这里的截图是页面像素（CDP），不是窗口级截图。

## 20. BR04 子集二：分栏 20 轮不漂移

`TestNativeSplitter20Rounds`：20 次改变右栏 slot 宽度（320–720 来回，第 9 轮宽度 0
并叠加一次隐藏/恢复），每次 `Present` 后断言子窗口 rect、`GetParent`、controller、
CDP 截图尺寸。原文（节选，完整 20 轮见日志）：

```text
round  1: slot w=320 -> child (488,151) 320x600 err=0.00px zoom=1.00
round  1: screenshot=320x600（跟随 slot）
round  2: slot w=720 -> child (488,151) 720x600 err=0.00px zoom=1.00
...
round  9: slot w=0 -> child (488,151) 0x600 err=0.00px zoom=1.00
round  9: 0 宽轮，跳过一次截图（viewport 为 0）
round 10: slot w=620 -> child (488,151) 620x600 err=0.00px zoom=1.00
...
round 20: slot w=520 -> child (488,151) 520x600 err=0.00px zoom=1.00
round 20: screenshot=520x600（跟随 slot）
splitter: 20 轮完成 maxRectErr=0.00px zeroWidthRound=9 收尾 click counter 0->1
```

- **20 轮 rect 最大误差 0.00px**（阈值 1px）。位置恒为 `(488,151)`，宽度严格等于
  slot 宽度 ×DPI（本机 scale=1）。
- 每轮 `GetParent(child)==main`、`GetAncestor(child,GA_ROOT)==main`、
  `ControllerReady=true` 都成立；第 9 轮 `SetVisible(false)` 后 `IsWindowVisible=false`
  且 controller 仍存活，恢复后可见。
- 每轮 CDP 截图尺寸都等于当轮子窗口物理尺寸（日志里逐轮有 `screenshot=WxH（跟随 slot）`），
  说明视图跟着 slot 重排，不是一张定住的图。
- 收尾再点一次可信按钮，服务端 counter `0->1`，页面仍可交互。

## 21. BR05 子集一：焦点与键盘

`TestNativeFocusAndKeyboard`：

```text
focus: hasFocus=true active="" (Focus 之后)
keyboard: input.value="abc" active="f-name" (CDP Input.dispatchKeyEvent)
focus: 主窗口拿回焦点后 hasFocus=false active="f-name"
```

- `Focus()`（`MoveFocus(PROGRAMMATIC)`）之前 `document.hasFocus()===false`，之后
  `===true`（用例先断言“之前是 false”，否则这条没有意义）。
- 键盘：先 CDP 可信点击 `#f-name`，再 `Input.dispatchKeyEvent` 三组 keyDown+keyUp
  带 `text=a/b/c`，**读回 `input.value === "abc"`**（不是只看事件或 DOM 属性）。
- 切回主界面：`RaiseToFront` 后**在 UI 线程** `SetFocus(主窗口)`。直接跨线程
  `SetFocus` 实测返回 0（不算产品问题），物理点击主窗口客户区实测也没让 WebView2
  失焦，所以最终用 UI 线程 SetFocus。结果：页面 `document.hasFocus()===false`，
  `activeElement` 仍停在 `f-name`（浏览器保留“最后焦点元素”，但文档不再有焦点）。

这是 **OS 级窗口焦点**；键盘输入本身走 CDP（不是 OS 键盘钩子），证据里分开标注。

## 22. BR05 子集二：最小化 / 恢复

`TestNativeMinimizeRestore`：

```text
minimize: main=(80,80 1180x820) -> (-32000,-32000 160x28) child=(728,151)-(1208,751) -> (-31360,-31960)-(-30880,-31360) iconic=true
restore: main=(80,80 1180x820) rectEqual=true screenshot=480x600 uniqueColors=54->54
```

- `ShowWindow(SW_MINIMIZE)` → 等 1s → `IsIconic=true`；主窗口 rect 变成
  `(-32000,-32000 160x28)`，**子窗口跟着父窗口一起被移到 -32000 区**（这就是它确实是
  子窗口、不是浮在外面的独立窗口的直接证据）。
- `SW_RESTORE` 后主窗口 rect 与最小化前**完全相等**（`rectEqual=true`），controller
  仍就绪；CDP 截图仍成功、尺寸不变、`uniqueColors=54->54`（非纯色，恢复前后同值）。

## 23. BR05 子集三：剪贴板（sentinel 往返 + 清理）

`TestNativeClipboardSentinel`（`go-test-native-b01b.log`）：

```text
clipboard: 授权前 permissions.query = write=granted read=prompt
clipboard: 授权后 permissions.query = write=granted read=granted
clipboard: writeText/readText 往返一致（sentinel 长度 43，值不落盘）
clipboard: 撤销授权后 permissions.query = write=granted read=prompt
clipboard: 撤销授权后 writeText 结果 = ok（clipboard-write 默认 granted）
clipboard: 收尾清空并读回确认（长度 0）
```

- 页内 `navigator.clipboard.writeText(sentinel)` → `readText()` 读回，**逐字符相等**；
  sentinel 形如 `b01b-clipboard-sentinel-<纳秒>`，证据里只记录长度不记录值。全程
  **没有先读用户剪贴板**（不把用户内容带进证据），用完写空串并读回确认长度为 0。
- 权限：用 CDP `Browser.grantPermissions`
  （`clipboardReadWrite` + `clipboardSanitizedWrite`，**origin 只限当前 fixture**）——
  不是 `edge.SetGlobalPermission`（那会对所有 permission kind 放开）。
  实测：授权前 `read=prompt`，授权后 `read=granted`；`write` 在 Chromium 里本来就是
  `granted`，所以“未授权写”并不是被拦（撤销授权后 writeText 仍 `ok`），这一点如实记录，
  不把自动授予包装成“权限校验通过”。
- 文档必须在**有焦点**时才能读写剪贴板（实测到 `Document is not focused`），所以先
  `Focus()`；撤销授权/写剪贴板之后页面可能丢焦点，收尾清空时若失败会重新 `Focus` 再清，
  清不掉就直接 fail（不能让探针字符串留在用户剪贴板里）。
- 早前一次手工探针里 `writeText` 的 Promise 挂起超过 15s（同一时刻报
  `Document is not focused`），最终用例里**没有复现**（未授权写也是 `ok`）。用例仍保留
  JS 侧 `Promise.race` 5s 超时作为保护——那是防御，不是证据。

## 24. BR05 子集四：中文 IME（CDP 模拟）

`TestNativeIMEComposition`：

```text
ime: 事件序列=[compositionstart compositionupdate input compositionupdate input compositionend]
ime: 组合中 value="nihao" isComposing=true；提交后 value="你好"
ime: 这是 CDP 模拟（Input.imeSetComposition + Input.insertText），不是 OS 级 IME；真机手输仍需人工验证
```

- 组合中：`Input.imeSetComposition{text:"nihao"}` → 页面记录 `compositionstart` →
  `compositionupdate` → `input`，此时 `isComposing=true`，value 里是拼音 `nihao`。
- 提交：`Input.insertText{text:"你好"}` → `compositionend` 收尾，最终
  `value === "你好"`，拼音被替换掉（不是拼在中文后面）。
- 观察器是测试用 `Runtime.evaluate` 运行时注入的（`tests/fixtures/browser/**` 不在
  本轮可改范围，所以没改 fixture 页面源码）。
- **边界**：这验证的是宿主 → CDP → 页面的事件顺序与焦点/提交链路，**不是** TSF/输入法
  进程；真实手输中文 IME 仍是人工步骤（见 §27）。

## 25. BR06 子集：50 轮创建/销毁 + RSS / 句柄 / 浏览器进程

`TestNativeCreateDestroy50Rounds`：50 轮
`Attach → CreateTab → Navigate → Screenshot(320×240) → Detach`。每轮断言子窗口
`IsWindow=false`、`GetParent=0`、本进程 `MyAgentNativeHostChild` 窗口类实例数回到 0、
`WindowInfo.ChildHWND=0` 且 `ControllerReady=false`。50 轮全部通过。逐轮采样（主日志 `go-test-native-b01b.log`）：

```text
round  1: wsMB=30.2 peakMB=30.2 pagefileMB=55.5 handles=440 wv2During(6 procs, 308.4MB) wv2AfterDetach(3 procs, exitLatency=1.013s) roundTime=5.188s
round 10: wsMB=30.8 peakMB=30.8 pagefileMB=55.8 handles=442 wv2During(6 procs, 317.3MB) wv2AfterDetach(0 procs, exitLatency=0s) roundTime=3.116s
round 25: wsMB=30.5 peakMB=31.3 pagefileMB=55.3 handles=442 wv2During(6 procs, 326.2MB) wv2AfterDetach(0 procs, exitLatency=0s) roundTime=2.12s
round 50: wsMB=30.3 peakMB=31.7 pagefileMB=55.1 handles=442 wv2During(6 procs, 319.6MB) wv2AfterDetach(5 procs, exitLatency=1.521s) roundTime=6.249s
50 轮完成 total=50.114s spike.exe before=0 after=0 childClassInstances=0 wv2Procs=0
```

| 轮 | working set | 峰值 | pagefile | 句柄 | 浏览器进程（存活时） | Detach 后立即采样 | 归零延迟 |
|---|---|---|---|---|---|---|---|
| 1 | 30.2 MB | 30.2 MB | 55.5 MB | 440 | 6 个 / 308.4 MB | 3 | 1.013s |
| 10 | 30.8 MB | 30.8 MB | 55.8 MB | 442 | 6 个 / 317.3 MB | 0 | 0s |
| 25 | 30.5 MB | 31.3 MB | 55.3 MB | 442 | 6 个 / 326.2 MB | 0 | 0s |
| 50 | 30.3 MB | 31.7 MB | 55.1 MB | 442 | 6 个 / 319.6 MB | 5 | 1.521s |

独立第二次运行（`go-test-native-50rounds-run3.log`）：

```text
round  1: wsMB=23.8 peakMB=23.8 pagefileMB=51.3 handles=344 wv2During(6 procs, 311.7MB) wv2AfterDetach(0 procs, exitLatency=0s) roundTime=3.435s
round 10: wsMB=27.6 peakMB=27.8 pagefileMB=54.3 handles=347 wv2During(6 procs, 322.4MB) wv2AfterDetach(3 procs, exitLatency=6.373s) roundTime=9.55s
round 25: wsMB=28.5 peakMB=28.5 pagefileMB=54.8 handles=357 wv2During(6 procs, 324.9MB) wv2AfterDetach(0 procs, exitLatency=0s) roundTime=2.575s
round 50: wsMB=28.8 peakMB=29.2 pagefileMB=55.1 handles=358 wv2During(6 procs, 318.6MB) wv2AfterDetach(4 procs, exitLatency=836ms) roundTime=4.698s
50 轮完成 total=53.985s spike.exe before=0 after=0 childClassInstances=0 wv2Procs=0
```

| 轮 | working set | 峰值 | pagefile | 句柄 | 浏览器进程（存活时） | Detach 后立即采样 | 归零延迟 |
|---|---|---|---|---|---|---|---|
| 1 | 23.8 MB | 23.8 MB | 51.3 MB | 344 | 6 个 / 311.7 MB | 0 | 0s |
| 10 | 27.6 MB | 27.8 MB | 54.3 MB | 347 | 6 个 / 322.4 MB | 3 | 6.373s |
| 25 | 28.5 MB | 28.5 MB | 54.8 MB | 357 | 6 个 / 324.9 MB | 0 | 0s |
| 50 | 28.8 MB | 29.2 MB | 55.1 MB | 358 | 6 个 / 318.6 MB | 4 | 836ms |

读法与限制（**不写成“零泄漏”**）：

- working set 主日志 30.2 → 30.3MB、第二次 23.8 → 28.8MB（两次起点差异来自 WebView2
  运行时/COM 初始化落在第一次采样之前还是之后，不是每轮累积）；第 10 轮到第 50 轮
  基本平（30.8→30.3、27.6→28.8）。句柄 440→442、344→358，10 轮后**不再增长**；
  pagefile 基本平。这类平台期与一次性初始化一致，但**不能**说成“绝对没有泄漏”：
  只跑了 50 轮，也没有分配级追踪（UMDH/分配采样）。
- controller 存活期间固定 6 个 `msedgewebview2.exe`（约 308–335MB，属于浏览器进程组
  自己的内存，**不在**宿主 working set 里）。
- **Detach 之后浏览器进程是异步退出的**：主日志第 1/50 轮 Detach 返回时还有 3/5 个
  进程在退出，分别用了 1013ms / 1521ms 才归零；第二次运行的两次采样是 3/4 个，
  用了 6373ms / 836ms（`exitLatency`）。所以断言写的是“轮次之间 0 遗留 + 收尾 2s 后
  owned 进程数 = 0”，**不是**“Detach 一返回就是 0”；用固定 sleep 假装干净会掩盖
  这个秒级尾延迟。
- `browser-spike.exe` 跑前跑后都是 0；测试结束时本进程 `MyAgentNativeHostChild`
  窗口类实例数为 0。
- COM 引用：`getCoreWebView2` 出参的 AddRef 由我们在关 tab 时归还
  （事件 `com_released`）；edge 自己 AddRef 的 controller/webview/environment
  **没有**替它释放。做过的 A/B：把归还调用临时关掉再跑 5 轮 + 关闭 tab，
  `after close` 的 owned `msedgewebview2.exe` 同样是 0——说明浏览器进程退出不依赖
  我们额外 Release，所以只还自己的那份，避免“晚回调拿到已释放对象”的风险。

## 26. 证据文件（`docs/evidence/raw/b01b-native/`）

| 文件 | 内容 | 字节 | sha256 |
|---|---|---|---|
| `go-test-native-b01b.log` | 全部 11 个 native 用例（含 3 个缩放子测试）的 `-v` 原文，exit 0 | 11096 | `086c43fdf97eea789f31c241f97ebb5d06a215f4f8f1e16d9e04ceae8e9917e3` |
| `go-test-native-50rounds-run3.log` | 50 轮用例的第二次独立运行（含 `exitLatency`） | 1483 | `9550250e8dab62bceaa6c125130563661d15fc1484b3d9222c6e77692eb171fd` |
| `spike-native.stdout.log` | `-native` 集成回归，`verdict=NATIVE_VIEW_PRESENT` | 4145 | `eb304c64cc9484e7fa5bc922aa84660b05e4289aac904b9d717ac5f3aec9f35e` |
| `spike-native.stderr.log` | Wails/WebView2 版本行与环境噪音 | 718 | `a4aa48d454915394585475ab4a4221f00c9cf31ad852bb3ce22ed40d9aabf8e1` |
| `spike-b00.stdout.log` | B00 模式回归，`verdict=AS_EXPECTED` | 4029 | `8c2ff676e66c5d25cd47f886271a304033abbee70af0e51cf88a56dcb6e8bd44` |
| `spike-b00.stderr.log` | 同上，stderr | 718 | `222a2c96550fbab4a67b28dca7ceb454bcf8fe195e763278390bdf4beb815f2a` |
| `b01b-zoom-100.png` | 100% 时 CDP 页面像素（480×600） | 14366 | `d67a294b43b8e20289abdfbdada6d9f4acfba78a0415de54a7fb96d238944329` |
| `b01b-zoom-150.png` | 150%（480×600 物理，320×400 CSS） | 21503 | `02124b0414767e2c8adae5fb2f29d93061e033acb8d809d625fc1da75bd391c8` |
| `b01b-zoom-200.png` | 200%（480×600 物理，240×300 CSS） | 41657 | `23f52b001a1a49ffae3095e59d83aa1b7e3d35dac5c90ee8d3f7c526c9ca6e43` |
| `b01b-after-restore.png` | 最小化恢复后的 CDP 页面像素 | 26003 | `ab9f0062be20b77814b03e0a1b1a9975c0453a60e348ca1c40b5ae43f0368435` |

截图全部是 **CDP 页面像素**；本节没有新增窗口级截图（B01a §10 已说明窗口级截图在有
遮挡时会拍到纯色，只作辅助）。

## 27. 仍未做的项（不许当成已通过）

| 项 | 归属 | 现状 |
|---|---|---|
| 第二个及更多 tab（4/session、12/total） | B05 | 未做；`CreateTab` 第二次仍返回 `UNSUPPORTED` |
| 系统 DPI 150%/200%（Windows 显示缩放） | B01b 环境缺口 | 未做：本机 96 DPI，改显示缩放要注销/重启。页面缩放 150/200% 已做 |
| 真机手输中文 IME | 人工步骤（BR05） | 未做；本节只有 CDP 模拟的组合/提交链路 |
| key / scroll / select / checkbox 动作语义 | B04 | 未实现，仍返回 `UNSUPPORTED` |
| trusted-only 站点的 `VerificationFailed`/`NeedsHuman` | B04（BR10） | 未实现；`capabilities.trustedInput=false` |
| 页面 dialog 遮挡协调器 | B07 | 未做；只有 `SetVisible` 原语 |
| 下载 / popup 接管 | B06 | 未做 |
| 长时间 soak（>50 轮）、分配级泄漏追踪 | 后续 | 未做；本节只有 50 轮 + working set/句柄趋势 |
| `verify --suite=native` 的 case 映射 | 复核者（devtools） | 建议见 §28；`internal/devtools/**` 本轮不动 |

## 28. 给 `verify --suite=native` 的 case → 命令映射建议（BR04/05/06）

| 验收项 | 建议命令 | 覆盖到哪一步（务必写进 case note） |
|---|---|---|
| BR04 缩放 | `go test ./tests/native -run TestNativeZoomFactor -count=1` | 100/150/200% **页面缩放**（无系统 DPI 变体）；坐标误差 ≤2 CSS px + OS 物理点击计数 |
| BR04 分栏 | `go test ./tests/native -run TestNativeSplitter20Rounds -count=1` | 20 轮含一次 0 宽 + 隐藏/恢复；rect 误差 ≤1px |
| BR05 焦点/最小化 | `go test ./tests/native -run 'TestNativeFocusAndKeyboard|TestNativeMinimizeRestore' -count=1` | focus + CDP 键盘 + 最小化恢复；**不含** dialog 层级协调（B07） |
| BR05 剪贴板 | `go test ./tests/native -run TestNativeClipboardSentinel -count=1` | sentinel 往返 + 清理；权限只对 fixture origin 授予 |
| BR05 IME | `go test ./tests/native -run TestNativeIMEComposition -count=1` | **CDP 模拟**；真机手输仍是人工步骤 |
| BR06 | `go test ./tests/native -run TestNativeCreateDestroy50Rounds -count=1 -timeout 600s` | 50 轮 + working set/句柄/浏览器进程采样；不含分配级泄漏证明 |

建议 devtools 把 BR04/05/06 的 status 记为**子集通过**（细节见上表 note），不要因为
命令 exit 0 就把整条 BR 标 PASS。

## 29. 进程残留检查（B01b 之后）

```text
> tasklist //FI "IMAGENAME eq browser-spike.exe"
信息: 没有运行的任务匹配指定标准。

> Get-CimInstance Win32_Process -Filter "Name='msedgewebview2.exe'" |
  Where-Object { $_.CommandLine -like '*b01b*' -or $_.CommandLine -like '*myagent-native-profile*' } |
  Measure-Object | Select-Object -ExpandProperty Count
0
```

按 `--user-data-dir` 只过滤我们自己的 profile（`b01b-native-profile-*`、
`myagent-native-profile-*`、spike 的 `.tmp\b01b\spike-*`）；机器上其它应用的
`msedgewebview2.exe` 未动。测试里创建的临时 profile 目录在关闭后由用例重试删除
（WebView2 关闭后短暂占文件，B01a §13.4 已记录）。
