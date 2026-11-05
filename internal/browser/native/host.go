// Package native 是 internal/browser 契约的原生宿主实现。
//
// B01a 只有 Windows/WebView2：在 Wails 主窗口（顶层 HWND）下挂一个自建的
// WS_CHILD 子窗口，里面放真实的 WebView2 controller。不是第二个顶层窗口、
// 不是 iframe、不是截图流——R06 与基线 §7 只接受这一种形态。
//
// 线程模型（B00 实测得到的硬约束）：
//   - controller 的创建、bounds 更新、销毁都必须在拥有 parent HWND 的 UI 线程上；
//   - 本包不 import Wails，自己建一个 message-only 窗口 + PostMessage 做派发，
//     这样测试可以用自己的 Win32 窗口和消息循环跑同一条代码路径；
//   - Attach 必须在 UI 线程调用（用 GetWindowThreadProcessId 校验），
//     不匹配就返回明确错误，而不是跨线程创建窗口。
//
// B01a 当时明确不做：第二个 tab、key/scroll 动作、select/checkbox 语义、
// 150%/200% 缩放、IME、50 轮创建销毁、遮挡协调器。
//
// B01b 已补上（仍不含多 tab / select / checkbox）：
//   - 页面缩放（SlotGeometry.Zoom → ICoreWebView2Controller.PutZoomFactor），
//     100/150/200% 下的坐标换算与可信点击；
//   - Focus（MoveFocus）与关闭 controller 后释放 COM 引用（BR06 的浏览器进程回收）；
//   - 分栏 20 轮 / 创建销毁 50 轮 / 最小化恢复 / 剪贴板 / IME 的可复核证据不在这里，
//     在 tests/native 与 docs/evidence/b01-native-webview2.md 的 B01b 段。
package native

import (
	"context"
	"time"

	"my-agent-go/internal/browser"
)

// BrowserEvent 是 native 层向调用方上报的事件。
//
// Payload 必须是有界、可序列化的小对象：它会进日志和事件通道，不放句柄、
// 不放整页 DOM。
type BrowserEvent struct {
	TabID   browser.TabID  `json:"tabId"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// 事件类型。B01a 只发这几类，够 spike 与测试等待"导航完成"。
const (
	EventAttached            = "attached"
	EventDetached            = "detached"
	EventTabCreated          = "tab_created"
	EventTabClosed           = "tab_closed"
	EventNavigationStarted   = "navigation_started"
	EventNavigationCompleted = "navigation_completed"
	EventNavigationFailed    = "navigation_failed"
	EventSlotDropped         = "slot_dropped"
)

// HostOptions 是原生宿主的构造参数。
//
// ProfilePath 是 WebView2 的 user data folder，必须与 Wails 主 WebView 的目录
// 分开——同一个 folder 被两个环境占用会让 CreateCoreWebView2EnvironmentWithOptions
// 直接失败（B00 §7 实测）。空值表示"不允许"，构造时即报错：悄悄落到用户
// AppData 里会污染真实配置。
type HostOptions struct {
	ProfilePath string
	OnEvent     func(BrowserEvent)
}

// WindowInfo 是宿主的原生窗口事实，专供证据采集与测试断言。
//
// 它不是产品 UI 的数据来源：句柄只在 native 侧流转，不进 IPC。
type WindowInfo struct {
	// ParentHWND 是 Wails 主窗口（顶层窗口）句柄。
	ParentHWND uintptr `json:"parentHwnd"`
	// ChildHWND 是自建的 WS_CHILD 子窗口句柄；0 表示还没有创建。
	ChildHWND uintptr `json:"childHwnd"`
	// ParentOfChild 是 GetParent(ChildHWND) 的实际返回值。
	ParentOfChild uintptr `json:"parentOfChild"`
	// ParentMatches 是 ParentOfChild == ParentHWND 的实测结果。
	ParentMatches bool `json:"parentMatches"`
	// WebViewChildHWND 是 WebView2 自己建在 ChildHWND 下的渲染窗口。
	WebViewChildHWND uintptr `json:"webviewChildHwnd"`
	// ControllerReady 表示 controller 与 ICoreWebView2 都已取到。
	ControllerReady bool `json:"controllerReady"`
	// DPI 是 parent HWND 所在显示器的 DPI（96 = 100%）。
	DPI uint32 `json:"dpi"`
	// Scale 是 DPI/96，即 CSS px → 物理 px 的换算系数。
	Scale float64 `json:"scale"`
	// ChildRect 是最近一次 Present 之后子窗口的物理像素矩形。
	ChildRect Rect `json:"childRect"`
	// ZoomFactor 是 controller 当前的**页面**缩放因子（1 = 100%）。
	//
	// 它与 Scale 是两条独立的换算：Scale 是 DPI/96（CSS px → 物理 px），
	// ZoomFactor 是页面内部缩放（物理 px = CSS px × Scale × ZoomFactor）。
	ZoomFactor float64 `json:"zoomFactor"`
	// Focused 表示最近一次 Focus 的 MoveFocus 调用成功（页面侧是否真的有焦点要读
	// document.hasFocus()，那是页面自报值）。
	Focused bool `json:"focused"`
}

// Rect 是物理像素矩形（子窗口坐标系无关，用屏幕坐标记录）。
type Rect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
}

// NativeHost 是平台原生宿主的完整接口。
//
// 它同时实现 B00 的 BrowserBackend 与 BrowserSurface：内容与位置分开是契约层的
// 划分，在 native 里它们本来就由同一个 controller 承担。
type NativeHost interface {
	browser.BrowserBackend
	browser.BrowserSurface
	// WindowInfo 返回原生窗口事实，供证据/测试断言；不参与产品数据流。
	WindowInfo() WindowInfo
	// Focus 把键盘焦点交给原生视图（CDP 输入与 IME 之前必须做这一步）。
	//
	// 返回后焦点在 controller 上：OS 层窗口焦点仍由调用方决定（切回主界面要
	// 自己 SetFocus 主窗口），这里只保证 WebView2 内部把焦点给了页面。
	Focus(ctx context.Context, tab browser.TabID) error
	// CallCDP 是**证据通道**：直接发一条 DevTools 协议命令，返回原始结果 JSON。
	//
	// 它不经过 isolate/后置条件核验，所以不是产品路径：产品代码用 Observe/Act/
	// Screenshot，它们才是经过核验的。它只给 tests/native 与 spike 用来采集“页面
	// 自己怎么说”的证据（viewport、hasFocus、clipboard、IME 事件序列）。用它得出的
	// 断言必须在证据里标明是页面自报值，不能当作授权依据。
	CallCDP(ctx context.Context, tab browser.TabID, method, params string, timeout time.Duration) (string, error)
	// PageState 返回页面自己报告的 readyState/title/URL 与 fixture 计数。
	//
	// 这些值来自被观察的页面，是不可信输入：只用于 UI 标题栏、地址栏和证据
	// 采集，绝不参与授权或核验判定（核验走隔离世界的 expectedCondition）。
	PageState(ctx context.Context, tab browser.TabID) (PageState, error)
}

// PageState 是页面自报的显示态。
//
// Counters 是 fixture 站点的 /__counters 快照（页面内 fetch 得到），用来证明
// “右侧那个视图真的在同一张网络栈里跑页面”，而不是一张图。
type PageState struct {
	ReadyState string         `json:"readyState"`
	Title      string         `json:"title"`
	URL        string         `json:"url"`
	Counters   map[string]any `json:"counters,omitempty"`
}
