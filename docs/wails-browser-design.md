# Wails 右侧嵌入浏览器与 Agent 操作详细方案

日期：2026-11-03。状态：技术设计，未实现、未运行原型。

实现时以V1执行基线及原生浏览器模块计划为准，入口 `docs/README.md`；本文中的候选值和工期不覆盖新任务书的固定接口、范围和验收。

## 1. 结论与边界

可以保留 Wails，实现同窗口右侧真实网页，并让 Pi 通过 Go 浏览器工具操作该网页。但这不是安装一个 React 组件就能完成：需要开发一个 NativeBrowserHost，分别连接 Windows WebView2、macOS WKWebView、Linux WebKitGTK，并集成进 Wails 的原生视图树。

可行性分三层：原生引擎本身提供嵌入、脚本执行和截图能力；Wails 原生窗口集成需要本项目原型验证；三端一致的高保真自动化尚不能承诺，尤其是可信输入、复杂 iframe、canvas/拖拽和网站兼容性。不能把 DOM click/fill 当成与 CDP 输入等价的能力。

本方案推荐以系统原生引擎作为轻量首选，实现 DOM 驱动任务闭环。Wails、Go、React、宿主 Pi 的既有选型保持。若原型不能满足实际网站场景，启用独立 Chromium 后端的评估；未经用户选择，不将画面流或外部窗口冒充真正嵌入浏览器。

## 2. 已核实的基础与待验证项

- Wails v3.0.0 源码提供 WebviewWindow.NativeWindow()，返回平台原生窗口句柄；它不是 BrowserView API，也不等同于返回 Wails 主 WebView 的 controller。核对 tag commit：7b11001b92b712210b27d8cb53430e83f67ad8e4。
- WebView2 可以在父 HWND 下创建 controller，控制 bounds，执行脚本、截图以及调用 DevTools Protocol。
- WKWebView 可以加入原生视图层级，使用命名 content world 执行脚本并截图；公开脚本接口不等于 Chromium CDP 输入接口。
- WebKitGTK 提供网页 widget、脚本执行与截图接口；项目应锁定与所选 Wails Linux GTK 版本匹配的 WebKitGTK API，不能直接混用旧 GTK3 示例。
- 本次没有找到可以直接当成三端 BrowserView 使用的 Wails API。NativeWindow 是入口候选，容器挂载、UI 线程调度和生命周期需要原型验证；主窗口的 SetURL/ExecJS 不用于承载远程网页。

资料链接见文末。以下结构和接口均为项目设计，不是现有 Wails API 承诺。

## 3. 总体结构

```text
Wails 原生主窗口
├─ 应用 WebView：React/shadcn
│  ├─ 左侧工作区/会话
│  ├─ 中间聊天与工具结果
│  └─ 右侧栏：工具栏、标签栏、BrowserSlot 占位
└─ NativeBrowserHost：位于 BrowserSlot 对应原生坐标
   └─ 远程网页视图：WebView2 / WKWebView / WebKitGTK

宿主 Pi RPC worker
    → Harness MCP browser tools
    → Go BrowserService：授权、队列、标签、超时、状态
    → BrowserBackend / NativeBrowserHost
    → 右侧同一个网页实例
```

应用 WebView 和网页视图独立。远程网站没有应用 Wails bindings、凭证接口和文件系统接口；应用 UI 也不能简单通过 React DOM 读跨域网页。页面观察经原生 backend 执行受控脚本返回。

浏览器工具沿用已有 Harness MCP。无需像 GooeyPi 再维护一套相同的 extension HTTP tools；自有 Pi extension 保留权限/hooks 职责。MCP 请求上下文携带服务端绑定的 session 授权，模型不能通过自行填写 sessionId 越权。

## 4. 原生视图怎样放进右侧

推荐先实现占位覆盖模式：React 布局预留不包含远程网页的 BrowserSlot，上报其 bounding rect；原生网页视图作为同窗口的独立子视图，定位在同一位置。它不是新的顶层窗口，也不是在 React iframe 里打开网站。

BrowserSlot 上报：slotId、layoutRevision、left/top/width/height、visible、主界面 zoom 信息。使用 ResizeObserver、滚动/窗口变化和必要的补充检测；相同几何不重复上报，拖拽时合并到每帧一次。Go 丢弃旧 layoutRevision。

坐标在 native adapter 统一转换：CSS px → 应用 WebView 的逻辑单位 → 原生父容器单位。Windows 考虑 DPI 和缩放；macOS 处理坐标原点、flipped view 和 points；Linux 使用 widget allocation/scale factor。不能在三端直接将 CSS 数值当作物理像素。

网页视图保持存活，隐藏面板只隐藏，不销毁；关闭标签才释放。首版限制每会话 4 个、总计 12 个活跃标签，具体数值依据内存测量调整。保留登录态不意味着关掉标签后保留 JS 堆和 DOM。

### 原生层叠问题必须处理

独立原生网页通常盖在应用 WebView 上方，React 的 z-index 不能保证覆盖它。首版统一由 BrowserOcclusionCoordinator 管理：应用对话框/命令面板打开前隐藏相交的网页视图，关闭后根据当前 layoutRevision 恢复；导航和 agent 状态不随隐藏丢失。不要依赖只列举几个弹窗的临时处理，应给应用 overlay 统一的 show/hide 生命周期。

若未来需要网页上方持续显示可点击的原生工具/光标叠层，再做平台级 overlay。先将地址栏与标签栏放在网页原生区域之外，避免复杂层叠。

### 与 Wails 的集成策略

优先从 NativeWindow 获取父窗口，在 UI 线程创建自己的子容器；Windows 通常是父 HWND 下独立 child host，macOS 在 contentView 中挂独立子 NSView，Linux 在 GTK 视图树中需要明确的容器槽位。

原生句柄的存在不能证明可直接修改 Wails 的内部视图结构。先对固定 Wails 版本验证：如果公开句柄足够，则外部 browserhost 包实现；如果缺少必要的容器/lifecycle hook，允许设计一个最小、独立的 Wails 集成补丁，并优先向上游提供 hook。补丁范围只暴露 attach/detach/UI dispatch，不重写 Wails runtime，不靠强制类型转换读取未导出的 Go 对象。Linux 现有根 widget 的重新挂载尤其要做生命周期测试。

一个随主窗口移动的无边框顶层窗口不能作为真正嵌入的替代验收结果，它会产生焦点、遮挡和 Wayland 行为差异。

## 5. 三端 backend

| 平台 | 展示和导航 | 读取/截图 | 操作路线 | 验证重点 |
|---|---|---|---|---|
| Windows | WebView2 child controller；独立 user-data folder | ExecuteScript/所选运行时支持的隔离世界；CapturePreview | 首选 CallDevToolsProtocolMethod，发送 Input 事件 | STA/UI 线程、controller 回调、DPI、焦点、弹窗 |
| macOS | WKWebView 子 NSView；独立配置和 data store | WKContentWorld + evaluateJavaScript/callAsyncJavaScript；takeSnapshot | 先做 DOM ref click/fill；需要更高保真输入时单独验证公开机制 | 主线程、IME、cookie 持久化、frame、焦点与层级 |
| Linux | 与 Wails GTK 版本一致的 WebKitWebView 子 widget | 命名 script world、evaluate_javascript、get_snapshot 或匹配版本等价 API | DOM ref click/fill 为基础；原生输入方案另验 | GTK 生命周期、X11/Wayland、scale、portal、依赖版本 |

Windows 的 CDP 直接经 controller 调用，不必为此开放远程调试端口。macOS/Linux 不假设有同样的 CDP；不将私有 WebKit API 作为正式发行必需项。

所有 native 创建、操作和销毁都调度到相应 UI 主线程；Go goroutine 只排队和等待异步结果。不能在主线程等待一个仍需主线程完成的 Promise/Go channel。context 超时取消请求，晚到回调通过 generation 丢弃，关闭后不访问释放对象。

## 6. Go 抽象与目录

下面是建议的接口形状，不是已实现源码：

```go
type BrowserBackend interface {
    Capabilities() CapabilitySet
    CreateTab(ctx context.Context, opts TabOptions) (TabID, error)
    Navigate(ctx context.Context, tab TabID, url string) error
    Observe(ctx context.Context, tab TabID, opts ObserveOptions) (Snapshot, error)
    Act(ctx context.Context, tab TabID, action Action) (ActionResult, error)
    Screenshot(ctx context.Context, tab TabID) (Image, error)
    CloseTab(ctx context.Context, tab TabID) error
}

type BrowserSurface interface {
    Attach(ctx context.Context, parent NativeParent) error
    Present(ctx context.Context, tab TabID, slot SlotGeometry) error
    SetVisible(ctx context.Context, visible bool) error
    Detach(ctx context.Context) error
}
```

NativeParent 是平台专有句柄，封装于 browserhost，不能发送到前端或模型。截图以规范元数据描述 width、height、CSS viewport、scale 和 navigationId，避免坐标误差。

```text
internal/browser/
  service.go              标签、归属、事件、生命周期
  actions.go              操作队列与结果
  policy.go               模型和人工控制权
  snapshot.go             元素引用和文档版本
  tools.go                Harness MCP 工具注册
  scripts/                自有 DOM 观察与表单脚本
  native/
    host.go               BrowserSurface / BrowserBackend
    host_windows.go       Go + WebView2 COM binding
    host_darwin.go         Go 与 Objective-C/C bridge
    host_darwin.m          WKWebView 生命周期和 delegates
    host_linux.go         Go 与 GTK/WebKitGTK C bridge
    host_linux.c          widget 生命周期和 callbacks
frontend/src/features/browser/
  BrowserPanel.tsx
  BrowserToolbar.tsx
  BrowserTabs.tsx
  BrowserSlot.tsx
```

macOS/Linux 需要原生 glue，业务仍在 Go。按常规 cgo/native toolchain 做首版，不要求纯 Go 构建；Windows binding 的覆盖率和维护状况在 P0 选择，不预先承诺某个第三方包已支持所有接口。

## 7. 观察与操作协议

工具集合：browser_tabs、browser_navigate、browser_snapshot、browser_click、browser_fill、browser_key、browser_scroll、browser_screenshot，以及后续的 upload/download。新功能按 backend 能力开放，不给模型展示实际上不可用的工具。

每个 tab 有 tabId、ownerSession、profileId、navigationId、controlGeneration、selected、state。每次 observe 返回 snapshotId 和 element refs；动作携带 snapshotId + ref。服务检查 session 归属、页面文档是否变化、元素是否仍连接、目标是否可见、授权是否仍有效。

refs 不只是 document.querySelectorAll 的序号：每个 snapshot 建立独立映射，导航使映射失效；同 URL 的 document 替换也改变 navigationId；SPA 重排/节点断开使引用失效；开始执行前再次核实。错误返回 StaleSnapshot，让 agent 重新观察，不自动猜另一个位置继续点击。

DOM 脚本提取 role、name、tag、input type、disabled、可见区域和受限文本，屏蔽 password 值；结果有元素数量/字节上限。完整跨域 frame、closed shadow root 并非顶层脚本天然可读：设计 FrameAdapter 按 frameId 分别观察，有能力才开放；首版将未覆盖的页面区域明确返回 unsupported，而非声称整页可操作。

click 优先解析 ref、滚动到元素、做命中检查，再调用 backend。Windows 以 CDP input 为首选。DOM click/fill 路线使用页面表单 API 并正确分发事件，但事件不一定满足网站对 trusted input 的要求；富文本、拖拽、文件选择和 canvas 不能简单复用该方案。fill 完成后读回非敏感字段状态核验，不只返回脚本执行成功。

任意 evaluate 不作为默认 agent 工具。截图和坐标操作按 backend capability 开放，坐标输入和 DOM input 必须标明执行方式。截图的隐藏页/最小化支持需实测，不能看到 native screenshot API 就承诺一定能捕获后台页面。

每个 tab 一个串行队列；导航、关闭和控制权切换会使旧队列动作失效。observe/act/verify 构成一次操作闭环；DOM ready、URL、元素条件和网络信号按场景组合等待，不只 sleep 固定时长，也不把有长连接的页面永远等待 network idle。

## 8. 用户接管与授权

控制状态为 Agent / Pausing / Human / Resuming。点击“接管”时增加 controlGeneration、拒绝新 agent 动作并取消尚未开始的动作，等待当前可中断操作退出后再显示已接管。用户正在输入时 agent 不能同时填表。

恢复时重新取 URL、navigationId 和 snapshot，不使用接管前的元素引用；向 agent 返回控制权恢复事件。紧急停止入口位于网页区域外，即使网页卡死仍可触达。

浏览器服务限定 session/tab，远程页面不拿到应用 bridge。浏览器 profile 默认按工作区隔离，允许用户显式选择复用已有的产品内 profile；不读取个人系统 Chrome cookie。持久数据只写产品的 browser profile 目录，提供清除和临时模式。

用户登录在同一原生网页里进行；不能承诺所有第三方 OAuth、SSO、passkey 或禁止 embedded user-agent 的登录都能完成。需要外部浏览器授权的站点按站点支持处理，不能假定外部浏览器 cookie 会自动同步。

提交、发送、购买等策略检查在 BrowserService 的执行前进行，界面展示目标站点和动作。页面观察结果视为不可信数据。只批准抽象的“使用浏览器”不能等同于无限授权网页内的外部副作用；语义不明时不能仅凭点击坐标推断它是只读操作。

## 9. 窗口、上传下载和登录态

window.open/new-window delegates 统一交给 Go 创建受同一会话授权的新 tab，并设置上限。下载变成 DownloadJob，由用户选择保存位置，完成后登记 artifact；远程网站不能静默选择本机任意路径。

上传来自用户明确选择或 workspace 已授权文件，文件选择器走平台代理；不能用 JS 设置 file input.value 冒充文件上传实现。native backend 尚未支持 programmatic file chooser 时标记需要人工处理。

浏览器数据目录与应用配置、Pi 配置分离。保存账号状态与保存 tab 元数据分开：重启后重新导航并恢复 profile，不承诺恢复浏览器内存、未提交表单或运行中的下载。

## 10. 能力分级和降级

| 能力 | 原生首版目标 | 不能直接承诺的部分 |
|---|---|---|
| 真正右侧网页、用户手动交互 | 三端必须通过 | Wails 挂载/层级集成尚待原型 |
| 页面文本与 DOM 元素观察 | 三端常见网页 | 跨域 frame、closed shadow DOM |
| click/fill/scroll | 三端 DOM 任务基线 | 三端 trusted input 语义一致 |
| 截图、停止、接管 | 三端必须通过 | 隐藏页和最小化捕获一致 |
| 登录状态 | 产品内 profile 保存 | 任意站点 OAuth/SSO 可用 |
| Canvas、拖拽、复杂快捷键、全 frame 操作 | 单独场景验收 | 不能按 DOM 基线宣称支持 |

Native backend 返回能力集合，缺失工具不注册，运行中不能执行的动作返回明确错误并允许人工接管。若关键业务网站大量依赖未支持能力，评估 Chromium backend：可复用本机 Chrome/Edge，或按需安装专用运行时；如果使用画面流/独立窗口，明确与当前“真正嵌入网页”的交付差异，由用户选择后再变更目标。

CEF 属于另一个原生集成项目：Wails 壳仍可保留，但需处理 Chromium 生命周期、渲染、输入和打包，不当作简单 fallback。运行时按需下载只能减小基础安装包，不能减少使用后的磁盘和内存占用。

## 11. 原型与交付安排

先做三端可行性原型，不先堆完整 UI。允许按顺序开发各 adapter，但没有三端同级验收就不宣布跨平台完成。

| 阶段 | 内容 | 粗估，前提是熟悉原生开发且三端测试环境可用 |
|---|---|---|
| B0 原生嵌入验证 | 固定 Wails；三端挂载真实网页；拖动分栏、DPI、焦点、弹窗遮挡、截图、销毁 | 约 1–2 周 |
| B1 Agent 闭环 | DOM snapshot/ref；click/fill/scroll；Harness MCP；排队、过期引用、停止、接管 | 约 1–2 周 |
| B2 产品完善 | 持久 profile、tab 管理、窗口/下载代理、权限、目标站点与跨平台回归 | 约 1–2 周起 |

浏览器部分合计约 3–6 周起，属于新增工作估算，不是已验证工期；若需要 Wails 容器补丁或复杂 trusted input，重新估算。不将这段工作无条件塞进原有客户端 3–4 周 MVP 估算。

B0 通过条件：三端页面确实在同一窗口右侧；布局无漂移；网页不会盖住关键应用对话框；中文输入/焦点正常；点击和截图坐标可对齐；关闭后无崩溃或泄漏；无需关闭浏览器安全隔离。未通过时输出具体平台阻碍，不用外部浮窗或截图伪装通过。

B1 通过条件：Pi 能在真实表单完成观察→填写→点击→验证；人工接管期间不发生模型输入；切换导航后旧 refs 不可执行；另一 session 无法操作当前 tab；超时和停止不导致稍后补执行副作用。

B2 用例至少覆盖：普通表单、SPA 重渲染、子 frame、shadow DOM、弹窗、文件上传/下载、产品 profile 登录态、多个 DPI/显示器，以及 Linux X11/Wayland。超出 backend 能力时返回明确状态。

## 12. 选择建议

用户现有选择是 Wails + Go + 轻量客户端。与之最匹配的路线为 **Wails + 自研 NativeBrowserHost + 系统 WebView + Go Harness MCP**。先用三端原型确定实际可支持的自动化场景。

如果要求从首版起就在三端对任意网站提供接近统一 Chromium 的高保真操作，系统原生引擎路线存在明显限制，需要专用 Chromium 集成或重新比较 Electron。不能以“能显示网页”作为完整 GooeyPi 等效实现的证据。

## 13. 参考资料

- [Wails v3.0.0 原生窗口入口源码](https://github.com/wailsapp/wails/blob/v3.0.0/v3/pkg/application/webview_window.go)
- [Wails Window API](https://wails.io/reference/window/)
- [WebView2 Environment / 创建 controller](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2environment)
- [WebView2 脚本、截图和 DevTools API](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2)
- [Apple WKWebView](https://developer.apple.com/documentation/webkit/wkwebview)
- [WebKit WKWebView 公开头文件](https://github.com/WebKit/WebKit/blob/main/Source/WebKit/UIProcess/API/Cocoa/WKWebView.h)
- [WebKitGTK WebView API 概览（应按目标 GTK/API 版本选具体接口）](https://www.webkitgtk.org/reference/webkit2gtk/stable/class.WebView.html)
- [GooeyPi 浏览器源码分析](./gooeypi-browser-review.md)
