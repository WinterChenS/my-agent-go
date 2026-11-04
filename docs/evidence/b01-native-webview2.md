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
