# Native Browser Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在Wails右侧显示真实网页，并允许Pi在授权范围内观察、操作和交还控制权。

**Architecture:** BrowserService统一tab/权限/队列，NativeBrowserHost提供三个平台原生子视图；React只管理槽位和工具栏。

**Tech Stack:** Wails、WebView2、WKWebView、GTK/WebKitGTK、Go MCP，与执行基线一致。

**Spec:** V1执行基线；`docs/wails-browser-design.md`；master plan；验收矩阵。

## Global Constraints

基线R01–R10和浏览器固定值不可改变；截图流/浮窗不满足原生嵌入；不使用私有API。普通DOM交互是V1共同能力，trusted-only/canvas/复杂frame明确报告Unsupported，不能默认成功。

## Review Focus

缩放坐标错位→B01–B03；原生网页遮挡React审批→B01–B03/B07；导航后旧refs→B04；人工接管后旧队列写入→B05；跨session共享cookie不等于可共享tab→B05/B06。

## B00：浏览器契约和可重复测试站点

**依赖：** F00/F01。**Files：** `internal/browser/{types,backend,surface}.go`、`schemas/browser.schema.json`、`tests/fixtures/browser/{server.go,pages/}`、`cmd/browser-spike/main.go`；测试 `contracts_test.go`、`fixture_test.go`。

**Interfaces：** `BrowserBackend.Capabilities() CapabilitySet`、`CreateTab(ctx,TabOptions)(TabID,error)`、`Navigate(ctx,TabID,url string) error`、`Observe(ctx,TabID,ObserveOptions)(Snapshot,error)`、`Act(ctx,TabID,Action)(ActionResult,error)`、`Screenshot(ctx,TabID)(Image,error)`、`CloseTab(ctx,TabID) error`。

`BrowserSurface.Attach(ctx,NativeParent) error`、`Present(ctx,TabID,SlotGeometry) error`、`SetVisible(ctx,bool) error`、`Detach(ctx) error`。NativeParent仅native内部持有，不能导出到JSON。

Snapshot{ID,TabID,NavigationID,ControlGeneration,Viewport,Elements,Text,UnsupportedFrames}；Element{Ref,FrameID,Role,Name,Tag,InputType,Disabled,Bounds}；Action{Kind,SnapshotID,Ref,Text,Key,DeltaX,DeltaY,ExpectedCondition}；ActionResult{Delivered,Verified,NavigationID,Verification,Error}。ExpectedCondition仅允许url_equals/element_text/field_value/element_present等有界检查，不允许任意JS。Delivered表示输入已发送，不等于业务成功；没有后置条件时Verified=false/unknown，agent必须重新观察。Image{Bytes,MimeType,PixelWidth,PixelHeight,CSSWidth,CSSHeight,Scale,NavigationID}。SlotGeometry{SlotID,Revision,Left,Top,Width,Height,Visible,Zoom}。

- [x] 创建两个不同origin的本地站点，页面包括普通表单、SPA节点替换、同URL导航、cross-origin iframe、closed shadow、trusted-only button、登录cookie、下载、新窗口、慢加载。每个写操作通过服务端counter可核查。→ `tests/fixtures/browser`（内核分配端口；counter 是唯一 oracle；trusted-only 服务端闸门 + 页面 isTrusted）。
- [x] 写 `TestFixtureCountersAndOrigins`：提交前counter=0，提交一次=1；cross-origin确实不同；trusted-only忽略非trusted事件。不可仅创建同一页面的假标签。→ 实际为 `TestFixtureTwoDistinctOrigins` / `TestCounterSemantics` / `TestTrustedOnlyRejectsUntrustedInput` 等 10 条；实测 A=1/B=0、缺 `X-Event-Trusted` 返回 403 且计数 0。
- [x] 创建最小Wails spike，分别预留右侧slot和dialog；最初尚未挂native view要记录失败，不能先放iframe当成功。→ `cmd/browser-spike`：真实窗口 + 红框空 slot + `<dialog>`；14 条探针全部 NOT_IMPLEMENTED，verdict=AS_EXPECTED，截图见 `docs/evidence/raw/b00-browser/`。
- [x] 实现契约/schema与fixture，运行 `go test ./internal/browser ./tests/fixtures/browser -count=1`；锁定native suite观察步骤和证据格式后提交。→ 16 + 10 条通过；`verify --suite=native` 已落地并接入 BR00（`internal/devtools/native.go`）。证据 `docs/evidence/b00-browser-fixtures.md`。

**验收：** BR00；B01/B02/B03共用同一fixture和契约，不复制三套预期。

## B01：Windows 原生 WebView2 宿主

**依赖：** B00。**Files：** `internal/browser/native/{host_windows,webview2_windows,input_windows}.go`、`cmd/browser-spike/platform_windows.go`；测试 `host_windows_test.go`、`tests/native/browser_windows_test.go`。

**Interfaces：** 实现B00 Backend/Surface；`NewWindowsHost(parent NativeParent,opts HostOptions)(NativeHost,error)`；HostOptions{ProfilePath,OnEvent}，OnEvent接BrowserEvent{TabID,Type,Payload}。所有native方法在UI线程异步完成。

- [x] 写/记录 `NativeRightPaneWindows` 初始失败：同一主窗口右侧网页空缺。使用本机WebView2运行时，记录runtime、Wails和SDK版本。→ B00 spike 已记录（verdict=AS_EXPECTED）；WebView2=156.0.4314.58。
- [x] 创建独立child host/controller，页面不能调用主Wails bridge；不把主界面controller导航到第三方站点。UI线程创建/释放，late callback有generation保护。→ B01a：WS_CHILD + WebView2 controller，`GetParent(child)==main` 可查；自建 UI 线程派发器；独立 user data folder；未接入 Wails bridge（ADO 0004）。
- [ ] 实现导航、DOM观察、CDP点击/输入、截图；React slot转原生坐标，100/150/200%缩放下中心点误差≤2 CSS px。来回拖分栏20次，浏览器不得漂移/脱离主窗口。→ **部分**：100% 下导航/隔离世界观察/CDP 可信 click+fill/截图已真实通过（`TestNativeRightPaneWindows`、`TestNativeWindowGeometry100`）；150%/200% 与 20 轮分栏未做（B01b）。
- [ ] 展示应用dialog并隐藏相交native视图；中文输入、焦点切换、最小化恢复、关tab/关窗口验证。重复创建销毁50次，不崩溃、不遗留tab对象/句柄；记录RSS而非只断言对象计数。→ **部分**：`SetVisible` 隐藏原生视图后 dialog 可见已证；IME/focus/clipboard/最小化恢复与 50 轮+RSS 未做（B01b，当前只有 3 轮句柄不残留）。
- [ ] 运行 `go run ./cmd/devtools verify --suite=native --target=win-x64 --display=desktop`；ARM64目标也要有独立结果，编译x64不代替。提交native报告和录屏。→ **部分**：命令已可跑，BR00/BR01 PASS、可跑 2/7、缺 BR04/05/06/BR07.native/UI04.native → exit 2；win-arm64 无机器；无录屏（窗口截图替代）。证据 `docs/evidence/b01-native-webview2.md`。

**验收：** BR01、BR04–BR06的Windows结果；其他平台仍未通过。

## B02：macOS WKWebView 宿主

**依赖：** B00；接口与B01一致，可独立开发但不能交叉改contracts。**Files：** `internal/browser/native/{host_darwin.go,host_darwin.m,host_darwin.h}`、`tests/native/browser_darwin_test.go`。

**Interfaces：** `NewDarwinHost(parent NativeParent,opts HostOptions)(NativeHost,error)`，实现B00同一接口。不把Electron/CDP命令当WKWebView支持的接口。

- [ ] 记录 `NativeRightPaneMac` 初始失败与macOS/SDK/Wails版本；确认原生contentView挂载位置，不能用屏幕坐标跟随的独立NSWindow替代。
- [ ] 使用公开WKWebView API、独立data store、命名content world；页面脚本结果受大小限制，delegate返回经过校验的事件。Go/C/Objective-C对象释放及回调走主线程。
- [ ] 普通DOM form实现Observe/Act，fill读回验证；snapshot缩放到CSS元数据。trusted-only fixture必须明确Unsupported/需人工，不能谎称点击成功。
- [ ] 同Windows执行分栏、dialog、focus、中文IME、Retina/non-Retina、创建销毁50次；清除profile后cookie失效，关tab后应用仍可用。
- [ ] 在Intel和Apple Silicon分别执行native suite，保存证据；缺机器为BLOCKED。提交。

**验收：** BR02、BR04–BR06的macOS结果。

## B03：Linux GTK/WebKitGTK 宿主与挂载决策

**依赖：** B00。**Files：** `internal/browser/native/{host_linux.go,host_linux.c,host_linux.h}`、`tests/native/browser_linux_test.go`、必要时 `patches/wails-browserhost/` 和 `docs/adr/0001-native-host-hook.md`。

**Interfaces：** `NewLinuxHost(parent NativeParent,opts HostOptions)(NativeHost,error)`，实现B00接口。NativeParent为GTK对象而非任意X11窗口ID，不依赖X11 reparent去冒充Wayland支持。

- [ ] 核实Wails所选版本的GTK/WebKit版本，记录 `NativeRightPaneLinux` 初始失败；明确现有root widget、拟挂载container及所有权。不能直接销毁Wails根widget腾位置。
- [ ] 外部adapter足够则不改Wails；缺hook时写最小ADR/patch，限定attach/detach/UI dispatch，patch及版本hash纳入锁文件；不改Wails runtime权限或默认页面隔离。
- [ ] 实现widget挂载、布局allocation、命名脚本world、截图和普通DOM操作；GTK主循环处理异步回调，dispose后晚到结果丢弃。
- [ ] Ubuntu目标下X11/Wayland各执行分栏、dialog、IME/clipboard、缩放、fixture操作和创建销毁50次。Wayland不能通过时G1保持BLOCKED，不删display要求。
- [ ] x64/arm64分别运行native suite并提交证据。G1合并检查B01/B02/B03全矩阵，缺一不通过。

**验收：** BR03–BR06的Linux结果；G1阻断项。

## B04：DOM 快照、引用和工具语义

**依赖：** B01–B03、F04/F07。**Files：** `internal/browser/{observe,refs,actions,capabilities}.go`、`internal/browser/scripts/{observe,resolve,fill}.js`；测试 `snapshot_test.go`、`tests/integration/browser_dom_test.go`。

**Interfaces：** `BrowserService.Snapshot(ctx,ExecutionContext,TabID)(Snapshot,error)`、`Execute(ctx,ExecutionContext,TabID,Action)(ActionResult,error)`。后续MCP调用此服务，不直接调用native adapter。

- [ ] 写 `TestSameURLDocumentInvalidatesRefs`、`TestSPARemovedNodeRejected`：观察后替换页面/DOM，旧ref不得增加server counter，返回StaleSnapshot。
- [ ] 写 `TestSnapshotLimitsAndPasswordRedaction`：300元素、40000字符、256KiB边界；密码值不在snapshot/log/MCP result；超限显式标truncated，JSON保持完整。
- [ ] 实现ref映射、navigationId、命中/可见性核验、fill后读回；截图坐标和CSS viewport一致，4MiB超限返回明确错误或受控缩小并更新scale，不能默丢一半base64。
- [ ] cross-origin/closed-shadow未覆盖部分返回Unsupported；trusted-only页面在DOM输入未满足后置条件时返回VerificationFailed/NeedsHuman，不能假设能在执行前识别任意站点的事件策略。不盲目转坐标绕过缺失能力，未知效果不返回Verified=true。
- [ ] 运行browser unit/native integration，六目标与display按矩阵记录结果后提交。

**验收：** BR07–BR10。

## B05：控制权、串行队列、授权与 MCP 工具

**依赖：** B04/F04/F05。**Files：** `internal/browser/{service,queue,control,tools}.go`、`internal/harnessmcp/browser_tools.go`；测试 `control_test.go`、`tools_integration_test.go`。

**Interfaces：** `TakeOver(ctx,exec,tab)(ControlState,error)`、`Resume(ctx,exec,tab)(ControlState,error)`；ControlState{Mode,Generation,NavigationID}；MCP注册由server端绑定exec，工具不能自选另一个session。

- [ ] 写 `TestQueuedActionRevokedOnTakeover`：一个慢动作+2个queued写操作，接管后queued计数=0；当前无法撤销的动作显示Pausing，不能先显示Human再发生agent写入。
- [ ] 写 `TestResumeRequiresFreshSnapshot`、`TestCrossSessionTabDenied`、`TestTabLimit`：旧generation拒绝；sessionB不得操作A；第5/第13个tab返回TabLimitExceeded且无新native实例。
- [ ] 实现每tab队列、执行前双重授权检查、control generation、超时和MCP工具注册；不可用capability不注册。默认交互需要审批，可授会话/站点范围许可，不能让模型自行“确认”。
- [ ] 真Pi+fixture模型通过MCP完成snapshot→授权→fill→click→verify；再用UI接管同一tab。工具输出tabId/URL/navigationId与面板一致。已派发输入超时而结果未知时标OutcomeUnknown，不重放；无法确认静止则保持Pausing/ControlUncertain，不先显示Human。
- [ ] 执行unit/integration、记录输入计数和时间线，提交。

**验收：** BR11–BR14、SEC03/SEC04。

## B06：Profile、持久登录、弹窗与下载

**依赖：** B05/F07。**Files：** `internal/browser/{profiles,popup,downloads}.go`、`internal/storage/migrations/002_browser.sql`；测试 `profiles_test.go`、`downloads_integration_test.go`。

**Interfaces：** `ProfileForWorkspace(ctx,WorkspaceID)(BrowserProfile,error)`、`ClearProfile(ctx,WorkspaceID) error`、`SaveDownload(ctx,DownloadID,destination string) error`；BrowserProfile{ID,WorkspaceID,Path,Ephemeral}；DownloadJob{ID,TabID,URL,SuggestedName,State,Size,ArtifactID}。

- [ ] 写 `TestWorkspaceCookiesIsolatedAndPersisted`：A保存cookie重启仍有，B没有，清除A后消失；同workspace两个session可同cookie但不能互控tab。
- [ ] 写 `TestPopupInheritsSessionAndTabLimit`：window.open新tab属于原session且不突破限额；不打开不受控新窗口。
- [ ] 写 `TestDownloadRequiresDestination`：下载未选择路径不写任意workspace/用户目录；确认后文件hash匹配，取消无最终文件；文件名路径穿越被清理，产物记录可查。
- [ ] 实现数据store、new-window代理、download delegate和artifact登记。程序化上传本轮明确后置；保留用户原生文件选择器人工路径，不注入任意文件。
- [ ] target native integration运行并提交；真实OAuth/SSO未测试不得计为通过。

**验收：** BR15–BR17。

## B07：右侧面板与弹层集成

**依赖：** B05/B06/C01，G1。**Files：** `frontend/src/features/browser/{BrowserPanel,BrowserToolbar,BrowserTabs,BrowserSlot,useBrowserState}.tsx`、`frontend/src/app/BrowserOcclusionCoordinator.tsx`、`internal/app/browser_api.go`；测试 `BrowserPanel.test.tsx`、`tests/e2e/browser_workbench.md`。

**Interfaces：** Wails应用API `UpdateBrowserSlot(ctx,SlotGeometry) error`、`SelectBrowserTab(ctx,TabID) error`、TakeOver/Resume封装；React只传事件与几何，不能拿native handle。所有app overlay通过统一coordinator报告遮挡。

- [ ] 前端测试geometry revision、去重、resize每帧合并、tab状态不由React重建；先运行失败用例。
- [ ] 实现地址栏/回退/刷新、tab选择/关闭、接管/恢复、可见控制状态、失败/Unsupported操作提示；停止按钮始终位于native网页区域外。
- [ ] native窗口打开时依次弹设置dialog、命令面板、审批框，网页必须正确隐藏/恢复；Agent pause与面板隐藏不是同一概念，隐藏前不能静默终止run。
- [ ] 同tab由agent填入文本，用户直接看到并修改；恢复后agent观察到人工修改。截图预览页面不能替代此步骤。
- [ ] 执行frontend suite + target native E2E，归档录屏和fixture计数，提交。

**验收：** UI03、UI04、E2E03。
