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
// B01a 明确不做（留给 B01b）：第二个 tab、key/scroll 动作、select/checkbox 语义、
// 150%/200% 缩放、IME、50 轮创建销毁、遮挡协调器。
package native

import (
	"context"

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
