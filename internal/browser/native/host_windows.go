//go:build windows

package native

// B01a 的 Windows 宿主实现：一个主窗口、一个 WS_CHILD 子窗口、一个 WebView2 controller。
//
// 状态访问规则（重要，别破坏）：
//   - chromium/ctrl/cdp/tab/快照表等"原生对象"只在 UI 线程上读写，
//     所有入口都经过 uiThread.invoke，所以不需要额外加锁；
//   - 需要跨线程读的事实（句柄、DPI、能力）用 atomic 字段，WindowInfo/
//     Capabilities 只读这些。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"

	"github.com/jchv/go-webview2/pkg/edge"
)

// host 是 Windows 原生宿主。
type host struct {
	opts HostOptions

	// 跨线程可读的事实。
	parentHWND      atomicUintptr
	childHWND       atomicUintptr
	webviewChild    atomicUintptr
	controllerReady atomic.Bool
	dpi             atomic.Uint32
	childRect       atomic.Pointer[Rect]
	// zoomBits 存页面缩放因子的 float64 位模式（WindowInfo 跨线程读）。
	zoomBits atomic.Uint64
	focused  atomic.Bool

	caps struct {
		navigate   atomic.Bool
		observe    atomic.Bool
		act        atomic.Bool
		screenshot atomic.Bool
	}

	// 以下字段只在 UI 线程访问。
	ui         *uiThread
	attached   bool
	comRelease func()
	chromium   *edge.Chromium
	ctrl       *edge.ICoreWebView2Controller
	cdp        *cdpClient
	tab        browser.TabID
	tabOpen    bool
	navSeq     uint64
	renderNav  atomic.Uint64
	lastSlot   browser.SlotGeometry
	snaps      map[string]*snapshotRefs
	snapSeq    uint64
}

// snapshotRefs 让 Act 能在"同一个文档、同一个元素"上动作。
//
// 刻意按元素本体定位而不是按坐标：坐标会因滚动/重排而指向别的元素，那正是
// BR08 要挡的"按位置重新解析"。B01a 只在元素上打一个 data-myagent-ref 属性
// 做身份标记；B04 会把这个注册表搬进隔离世界的正式实现。
type snapshotRefs struct {
	id         string
	navigation string
	execCtx    int64
	refs       map[string]browser.Element
}

// NewWindowsHost 构造 Windows 宿主。构造本身不碰任何 native 资源：
// 真正的窗口与 controller 在 Attach/CreateTab 里创建（见 host 注释）。
func NewWindowsHost(parent browser.NativeParent, opts HostOptions) (NativeHost, error) {
	if !parent.Valid() {
		return nil, appErr(contracts.CodeInternal,
			"NewWindowsHost 需要有效的 NativeParent（parent=%s）", parent.Label())
	}
	if _, err := hwndFromParent(parent.Handle()); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.ProfilePath) == "" {
		return nil, appErr(contracts.CodeUnsupported,
			"HostOptions.ProfilePath 不能为空：WebView2 的 user data folder 必须显式指定，"+
				"不能与 Wails 主 WebView 共用，也不能默认落到用户 AppData")
	}
	abs, err := filepath.Abs(opts.ProfilePath)
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "解析 ProfilePath 失败: %v", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, appErr(contracts.CodeInternal, "创建 ProfilePath(%s) 失败: %v", abs, err)
	}
	opts.ProfilePath = abs
	return &host{opts: opts, snaps: map[string]*snapshotRefs{}}, nil
}

// ---------- BrowserSurface ----------

// Attach 在主窗口下创建原生子窗口。
//
// 必须在拥有 parent HWND 的 UI 线程上调用：跨线程创建子窗口虽然 Win32 不禁止，
// 但消息投递、销毁顺序与 WebView2 的线程亲和性都会变成隐性坑，所以这里直接
// 拒绝并说明原因。
func (h *host) Attach(_ context.Context, parent browser.NativeParent) error {
	if h.attached {
		return appErr(contracts.CodeInternal,
			"Attach 重复调用：右侧已经挂过原生子窗口，不允许再叠一层")
	}
	hwnd, err := hwndFromParent(parent.Handle())
	if err != nil {
		return err
	}
	if owner := windowThread(hwnd); owner != currentThread() {
		return appErr(contracts.CodeInternal,
			"Attach 必须在拥有主窗口 HWND=0x%x 的 UI 线程上调用（窗口线程=%d 当前线程=%d）；"+
				"spike 里用 application.InvokeSync 把 Attach 放回 UI 线程",
			hwnd, owner, currentThread())
	}
	if err := registerWindowClasses(); err != nil {
		return err
	}
	release, err := ensureCOM()
	if err != nil {
		return err
	}
	ui, err := newUIThread()
	if err != nil {
		release()
		return err
	}
	client, err := clientRect(hwnd)
	if err != nil {
		ui.close()
		release()
		return err
	}
	child, err := createChildWindow(hwnd, 0, 0, client.Right-client.Left, client.Bottom-client.Top)
	if err != nil {
		ui.close()
		release()
		return err
	}
	if got := parentOf(child); got != hwnd {
		destroyWindow(child)
		ui.close()
		release()
		return appErr(contracts.CodeInternal,
			"子窗口父子关系不成立：GetParent(0x%x)=0x%x，期望 0x%x", child, got, hwnd)
	}

	h.ui = ui
	h.comRelease = release
	h.attached = true
	h.parentHWND.Store(hwnd)
	h.childHWND.Store(child)
	h.dpi.Store(dpiForWindow(hwnd))
	h.lastSlot = browser.SlotGeometry{Visible: true, Zoom: 1}
	h.emit(BrowserEvent{Type: EventAttached, Payload: map[string]any{
		"parentHwnd": fmt.Sprintf("0x%x", hwnd),
		"childHwnd":  fmt.Sprintf("0x%x", child),
		"parentOf":   fmt.Sprintf("0x%x", parentOf(child)),
	}})
	return nil
}

// Present 把右侧槽位的 CSS px 几何换算成物理像素并摆好子窗口。
//
// 换算链：slot（应用 WebView 内的 CSS px） → ×DPI/96 → 子窗口相对父客户区 px。
// Wails 主 WebView 填满主窗口客户区（B00 实测 1180×820 窗口 → 1164×781 viewport），
// 所以这里不需要再减边框；若以后主窗口有自绘边框，要在这一步补偏移而
// 不是在调用方减。
func (h *host) Present(ctx context.Context, tab browser.TabID, slot browser.SlotGeometry) error {
	if err := slot.Validate(); err != nil {
		return err
	}
	return h.runOnUI(ctx, func() error {
		if !h.attached || h.childHWND.Load() == 0 {
			return appErr(contracts.CodeInternal, "Present 之前必须先 Attach")
		}
		if slot.Revision != 0 && h.lastSlot.Revision != 0 && slot.Revision < h.lastSlot.Revision {
			// 迟到的旧 revision：丢弃，不把视图拽回旧位置（B07 的语义）。
			h.emit(BrowserEvent{TabID: tab, Type: EventSlotDropped, Payload: map[string]any{
				"arrivedRevision": slot.Revision,
				"lastRevision":    h.lastSlot.Revision,
			}})
			return nil
		}
		scale := h.scale()
		x := int32(math.Round(slot.Left * scale))
		y := int32(math.Round(slot.Top * scale))
		w := int32(math.Round(slot.Width * scale))
		hgt := int32(math.Round(slot.Height * scale))
		if w < 0 || hgt < 0 {
			return appErr(contracts.CodeUnsupported, "槽位宽高为负（%.2fx%.2f）", slot.Width, slot.Height)
		}
		child := h.childHWND.Load()
		if err := moveWindow(child, x, y, w, hgt); err != nil {
			return err
		}
		if h.chromium != nil {
			h.chromium.Resize() // PutBounds = 子窗口客户区
			h.webviewChild.Store(firstChildOf(child))
			if err := h.applyZoom(slot.Zoom); err != nil {
				return err
			}
		}
		h.lastSlot = slot
		if r, err := windowRect(child); err == nil {
			h.childRect.Store(&Rect{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Bottom})
		}
		h.emit(BrowserEvent{TabID: tab, Type: "slot_presented", Payload: map[string]any{
			"revision":   slot.Revision,
			"visible":    slot.Visible,
			"cssRect":    []float64{slot.Left, slot.Top, slot.Width, slot.Height},
			"pxRect":     []int32{x, y, w, hgt},
			"scale":      scale,
			"zoomFactor": h.zoom(),
		}})
		return nil
	})
}

// SetVisible 隐藏/显示原生视图，不销毁 controller（隐藏面板不能丢 tab 状态）。
//
// 同时也切 controller 的 IsVisible：只隐藏父级 HWND 会留下一个"看不见但在
// 合成"的 WebView2，遮挡判断和截图都会受影响。
func (h *host) SetVisible(ctx context.Context, visible bool) error {
	return h.runOnUI(ctx, func() error {
		if !h.attached || h.childHWND.Load() == 0 {
			return appErr(contracts.CodeInternal, "SetVisible 之前必须先 Attach")
		}
		child := h.childHWND.Load()
		if visible {
			showWindow(child, swShow)
			if h.chromium != nil {
				if err := h.chromium.Show(); err != nil {
					return appErr(contracts.CodeInternal, "ICoreWebView2Controller.PutIsVisible(true) 失败: %v", err)
				}
			}
		} else {
			if h.chromium != nil {
				if err := h.chromium.Hide(); err != nil {
					return appErr(contracts.CodeInternal, "ICoreWebView2Controller.PutIsVisible(false) 失败: %v", err)
				}
			}
			showWindow(child, swHide)
		}
		h.emit(BrowserEvent{Type: "visibility_changed", Payload: map[string]any{
			"visible":      visible,
			"childVisible": isWindowVisible(child),
		}})
		return nil
	})
}

// Detach 销毁 controller 与子窗口，并关闭 UI 派发器。
//
// 重复调用安全：第二次直接返回 nil（已经拆干净了，没有可拆的东西）。
func (h *host) Detach(ctx context.Context) error {
	if h.ui == nil {
		return nil
	}
	ui := h.ui
	if err := ui.invoke(ctx, func() { h.closeTabOnUI() }); err != nil {
		return err
	}
	ui.close()
	h.ui = nil
	h.attached = false
	if h.comRelease != nil {
		h.comRelease()
		h.comRelease = nil
	}
	h.parentHWND.Store(0)
	h.childHWND.Store(0)
	h.webviewChild.Store(0)
	h.controllerReady.Store(false)
	h.childRect.Store(nil)
	h.emit(BrowserEvent{Type: EventDetached})
	return nil
}

// ---------- BrowserBackend ----------

// Capabilities 只报告"本实例真的成功过"的能力。
//
// 这是刻意的：构造完成但还没用过任何一个方法时全是 false，因为那时确实没有
// 证据证明它们能用。B01b 会在创建时跑一组探针把 initial 值提前点亮。
func (h *host) Capabilities() browser.CapabilitySet {
	set := browser.CapabilitySet{
		Platform:   "windows",
		Engine:     "WebView2 (Microsoft Edge WebView2 Runtime)",
		Navigate:   h.caps.navigate.Load(),
		Observe:    h.caps.observe.Load(),
		Act:        h.caps.act.Load(),
		Screenshot: h.caps.screenshot.Load(),
	}
	reason := func(name, why string) {
		set.Unsupported = append(set.Unsupported, browser.UnsupportedCapability{Capability: name, Reason: why})
	}
	if !set.Navigate {
		reason("navigate", "本实例还没有成功完成过一次导航（B01a 用'成功过'作为证据口径，B01b 改成创建时探针）")
	}
	if !set.Observe {
		reason("observe", "本实例还没有成功完成过一次隔离世界的 DOM 观察")
	}
	if !set.Act {
		reason("act", "本实例还没有成功派发过一次可信输入")
	}
	if !set.Screenshot {
		reason("screenshot", "本实例还没有成功完成过一次 CDP 页面截图")
	}
	reason("trustedInput", "B01a 只实现 click/fill 的 CDP 可信输入路径，未做 trusted-only 站点的行为验证")
	reason("act:key", "key 动作未实现（B01b）")
	reason("act:scroll", "scroll 动作未实现（B01b）")
	reason("act:select/checkbox", "select/checkbox 语义未实现（B01b）")
	reason("crossOriginFrames", "B01a 只观察顶层文档；cross-origin iframe 进 UnsupportedFrames 并标注原因")
	reason("closedShadowRoots", "闭合 shadow root 读不到，B01a 不声称支持")
	reason("downloads", "下载委托未实现（B06）")
	reason("popups", "window.open 未代理（B06）")
	return set
}

// CreateTab 创建唯一的 WebView2 controller 并导航到 opts.URL。
//
// B01a 只允许一个 tab：第二个 CreateTab 返回明确错误，不静默复用第一个。
func (h *host) CreateTab(ctx context.Context, opts browser.TabOptions) (browser.TabID, error) {
	if err := opts.Validate(); err != nil {
		return "", err
	}
	var tab browser.TabID
	err := h.runOnUI(ctx, func() error {
		if !h.attached {
			return appErr(contracts.CodeInternal, "CreateTab 之前必须先 Attach")
		}
		if h.tabOpen {
			return appErr(contracts.CodeUnsupported,
				"B01a 只支持一个 tab（当前 tab=%s）；多 tab 属于 B01b", h.tab)
		}
		child := h.childHWND.Load()
		if child == 0 || !isWindow(child) {
			client, err := clientRect(h.parentHWND.Load())
			if err != nil {
				return err
			}
			child, err = createChildWindow(h.parentHWND.Load(), 0, 0,
				client.Right-client.Left, client.Bottom-client.Top)
			if err != nil {
				return err
			}
			h.childHWND.Store(child)
		}
		chromium := edge.NewChromium()
		chromium.DataPath = h.opts.ProfilePath
		chromium.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, args *edge.ICoreWebView2NavigationCompletedEventArgs) {
			ok, navID, webErr := navigationResult(args)
			h.navSeq++
			h.renderNav.Store(navID)
			if ok {
				h.caps.navigate.Store(true)
				h.emit(BrowserEvent{TabID: h.tab, Type: EventNavigationCompleted, Payload: map[string]any{
					"navigationId": navID,
					"seq":          h.navSeq,
				}})
				return
			}
			h.emit(BrowserEvent{TabID: h.tab, Type: EventNavigationFailed, Payload: map[string]any{
				"navigationId":   navID,
				"webErrorStatus": webErr,
			}})
		}
		// Embed 内部会跑一个嵌套消息循环直到 controller 就绪（实测 ~300ms）。
		// 期间派发器仍可能被重入，所以下面每个入口都先检查 cdp != nil。
		if !chromium.Embed(child) {
			return appErr(contracts.CodeInternal,
				"edge.Chromium.Embed(0x%x) 返回 false：WebView2 环境或 controller 创建失败。"+
					"已知限制：go-webview2 在失败路径用 log.Fatalf 直接退出进程，HRESULT 只在 stderr",
				child)
		}
		ctrl := chromium.GetController()
		if ctrl == nil {
			return appErr(contracts.CodeInternal, "Embed 成功但 GetController() 为 nil")
		}
		webview, err := getCoreWebView2(ctrl)
		if err != nil {
			return err
		}
		h.chromium = chromium
		h.ctrl = ctrl
		h.cdp = newCDPClient(webview)
		h.webviewChild.Store(firstChildOf(child))
		h.controllerReady.Store(true)
		// 新建的 controller 默认 bounds 不是子窗口尺寸（实测 961×1040 这种奇怪值），
		// 不调这一次 Resize 的话，第一次 Present 之前页面 viewport 都是错的。
		chromium.Resize()
		// Zoom 也要补一次：Present 可能发生在 controller 之前（那时只能记下
		// lastSlot.Zoom），controller 就绪后必须把页面缩放对上。
		if err := h.applyZoom(h.lastSlot.Zoom); err != nil {
			return err
		}
		h.tab = browser.TabID("tab-1")
		h.tabOpen = true
		tab = h.tab
		h.emit(BrowserEvent{TabID: tab, Type: EventTabCreated, Payload: map[string]any{
			"profilePath": h.opts.ProfilePath,
			"hwnd":        fmt.Sprintf("0x%x", child),
		}})
		chromium.Navigate(opts.URL)
		h.emit(BrowserEvent{TabID: tab, Type: EventNavigationStarted, Payload: map[string]any{"url": opts.URL}})
		return nil
	})
	if err != nil {
		return "", err
	}
	return tab, nil
}

// Navigate 发起导航；返回时只保证已经发起（契约要求）。
func (h *host) Navigate(ctx context.Context, tab browser.TabID, url string) error {
	if err := (browser.TabOptions{URL: url}).Validate(); err != nil {
		return err
	}
	return h.runOnUI(ctx, func() error {
		if !h.tabOpen || h.tab != tab {
			return appErr(contracts.CodeNotFound, "没有活跃 tab %q（当前 %q）", string(tab), string(h.tab))
		}
		if h.chromium == nil {
			return appErr(contracts.CodeInternal, "controller 尚未就绪")
		}
		h.chromium.Navigate(url)
		h.navSeq++
		h.emit(BrowserEvent{TabID: tab, Type: EventNavigationStarted, Payload: map[string]any{"url": url}})
		return nil
	})
}

// Observe 在隔离世界里取有界元素列表与受限文本。
//
// 走 Page.createIsolatedWorld 而不是主世界求值：页面脚本改不到隔离世界里的
// 辅助函数，观察结果不能被页面伪造。
func (h *host) Observe(ctx context.Context, tab browser.TabID, opts browser.ObserveOptions) (browser.Snapshot, error) {
	snap, err := h.observe(ctx, tab, opts)
	if err != nil {
		return browser.Snapshot{}, err
	}
	if err := snap.CheckLimits(); err != nil {
		return browser.Snapshot{}, err
	}
	if err := snap.ValidateRefs(); err != nil {
		return browser.Snapshot{}, err
	}
	h.caps.observe.Store(true)
	return snap, nil
}

// Act 执行一次动作。Delivered 只表示输入已交给引擎；只有后置条件被观测通过
// 才可能 Verified=true（BR10）。
func (h *host) Act(ctx context.Context, tab browser.TabID, action browser.Action) (browser.ActionResult, error) {
	return h.act(ctx, tab, action)
}

// Screenshot 用 CDP Page.captureScreenshot 取页面像素。
//
// 为什么不用整屏截屏：那会把 Wails 主窗口 UI 一起截进来，拿它冒充"页面截图"
// 是 BR01 明确禁止的。CapturePreview（ICoreWebView2 的 API）作为退路保留，
// B01a 只用 CDP 这一条并如实标注。
func (h *host) Screenshot(ctx context.Context, tab browser.TabID) (browser.Image, error) {
	if err := h.requireTab(ctx, tab); err != nil {
		return browser.Image{}, err
	}
	// 先把页面抬到前台：窗口被遮住时 captureScreenshot 默认走 compositor surface，
	// 可能一直不完成（本机实测过一次 15s 超时）。截图是只读操作，重试不违反
	// “不自动重放”的规则；重试的参数会写到结果的错误/事件里。
	if _, err := h.cdpRoundTrip(ctx, "Page.bringToFront", `{}`, 3*time.Second); err != nil {
		h.emit(BrowserEvent{TabID: tab, Type: "bring_to_front_failed", Payload: map[string]any{"error": err.Error()}})
	}
	variants := []struct {
		params string
		nom    string
	}{
		{`{"format":"png"}`, "surface"},
		{`{"format":"png","fromSurface":false}`, "renderer"},
	}
	var res string
	var lastErr error
	for _, v := range variants {
		got, err := h.cdpRoundTrip(ctx, "Page.captureScreenshot", v.params, 8*time.Second)
		if err == nil {
			res = got
			lastErr = nil
			if v.nom != "surface" {
				h.emit(BrowserEvent{TabID: tab, Type: "screenshot_fallback", Payload: map[string]any{"mode": v.nom}})
			}
			break
		}
		lastErr = err
		h.emit(BrowserEvent{TabID: tab, Type: "screenshot_attempt_failed", Payload: map[string]any{
			"mode":  v.nom,
			"error": err.Error(),
		}})
	}
	if lastErr != nil {
		return browser.Image{}, lastErr
	}
	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		return browser.Image{}, appErr(contracts.CodeInternal, "解析 captureScreenshot 结果失败: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return browser.Image{}, appErr(contracts.CodeInternal, "截图 base64 解码失败: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return browser.Image{}, appErr(contracts.CodeInternal, "截图不是合法 PNG: %v", err)
	}
	cssW, cssH, scale, err := h.viewportMetrics(ctx)
	if err != nil {
		return browser.Image{}, err
	}
	img := browser.Image{
		Bytes:        raw,
		MimeType:     "image/png",
		PixelWidth:   cfg.Width,
		PixelHeight:  cfg.Height,
		CSSWidth:     cssW,
		CSSHeight:    cssH,
		Scale:        scale,
		NavigationID: h.navigationID(),
	}
	if err := img.Validate(); err != nil {
		return browser.Image{}, err
	}
	h.caps.screenshot.Store(true)
	return img, nil
}

// CloseTab 关闭当前 tab：销毁 controller 与子窗口。
//
// 重复关闭同一个 tab 返回 nil（目标状态已达成）；关闭一个从未创建过的 tab
// 返回 NOT_FOUND，不让拼错的 id 静默通过。
func (h *host) CloseTab(ctx context.Context, tab browser.TabID) error {
	var notFound bool
	err := h.runOnUI(ctx, func() error {
		if h.tab == tab {
			// 已经关过同一個 tab：目标状态已达成，不重复发事件，也不报错。
			h.closeTabOnUI()
			return nil
		}
		notFound = true
		return appErr(contracts.CodeNotFound, "没有 tab %q（当前 %q）", string(tab), string(h.tab))
	})
	if notFound {
		return err
	}
	return err
}

// PageState 返回页面自报的显示态（标题/URL/readyState）与 fixture 计数。
func (h *host) PageState(ctx context.Context, tab browser.TabID) (PageState, error) {
	if err := h.requireTab(ctx, tab); err != nil {
		return PageState{}, err
	}
	const expr = `(async () => {
  let counters = null;
  try { const r = await fetch('/__counters'); counters = await r.json(); }
  catch (e) { counters = { error: String(e) }; }
  return JSON.stringify({ readyState: document.readyState, title: document.title, url: location.href, counters: counters });
})()`
	var call *cdpCall
	err := h.runOnUI(ctx, func() error {
		if h.cdp == nil {
			return appErr(contracts.CodeInternal, "controller 尚未就绪")
		}
		var err error
		call, err = h.cdp.evalAwait(expr)
		return err
	})
	if err != nil {
		return PageState{}, err
	}
	res, err := call.wait(ctx, 10*time.Second)
	if err != nil {
		return PageState{}, err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return PageState{}, err
	}
	var payload struct {
		ReadyState string         `json:"readyState"`
		Title      string         `json:"title"`
		URL        string         `json:"url"`
		Counters   map[string]any `json:"counters"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return PageState{}, appErr(contracts.CodeInternal, "解析 PageState 失败: %v", err)
	}
	return PageState{ReadyState: payload.ReadyState, Title: payload.Title, URL: payload.URL, Counters: payload.Counters}, nil
}

// Focus 把键盘焦点交给原生视图。
//
// 必须在 UI 线程调用 MoveFocus：它会把焦点下发给 WebView2 内部的渲染窗口，
// 页面里 document.hasFocus() 才会变 true（否则 CDP 的 key 事件只能改 DOM 而不能
// 真正触发焦点相关行为，IME 也没地方提交）。
func (h *host) Focus(ctx context.Context, tab browser.TabID) error {
	return h.runOnUI(ctx, func() error {
		if !h.tabOpen || h.tab != tab {
			return appErr(contracts.CodeNotFound, "没有活跃 tab %q（当前 %q）", string(tab), string(h.tab))
		}
		if h.ctrl == nil {
			return appErr(contracts.CodeInternal, "controller 尚未就绪")
		}
		if err := h.ctrl.MoveFocus(uintptr(edge.COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC)); err != nil {
			return appErr(contracts.CodeInternal, "ICoreWebView2Controller.MoveFocus 失败: %v", err)
		}
		h.focused.Store(true)
		h.emit(BrowserEvent{TabID: tab, Type: "focus_moved", Payload: map[string]any{"reason": "programmatic"}})
		return nil
	})
}

// CallCDP 是证据通道：直接发一条 DevTools 协议命令并返回原始结果 JSON。
//
// 不是产品路径（不经过隔离世界与后置条件核验），只给 tests/native 与 spike 采集
// “页面自己怎么说”的证据。超时后返回 OUTCOME_UNKNOWN 并且不重放调用。
func (h *host) CallCDP(ctx context.Context, tab browser.TabID, method, params string, timeout time.Duration) (string, error) {
	if err := h.requireTab(ctx, tab); err != nil {
		return "", err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return h.cdpRoundTrip(ctx, method, params, timeout)
}

// WindowInfo 返回原生窗口事实（证据与测试用）。
func (h *host) WindowInfo() WindowInfo {
	parent := h.parentHWND.Load()
	child := h.childHWND.Load()
	info := WindowInfo{
		ParentHWND:       parent,
		ChildHWND:        child,
		ControllerReady:  h.controllerReady.Load(),
		DPI:              h.dpi.Load(),
		Scale:            h.scale(),
		WebViewChildHWND: h.webviewChild.Load(),
		ZoomFactor:       h.zoom(),
		Focused:          h.focused.Load(),
	}
	if child != 0 {
		info.ParentOfChild = parentOf(child)
		info.ParentMatches = info.ParentOfChild == parent && parent != 0
		if r, err := windowRect(child); err == nil {
			info.ChildRect = Rect{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Bottom}
		}
	}
	return info
}

// ---------- 内部 ----------

// closeTabOnUI 必须在 UI 线程调用。
func (h *host) closeTabOnUI() {
	if h.ctrl != nil {
		// Close 失败也不阻止销毁父窗口，但要留下事件证据。
		if err := closeController(h.ctrl); err != nil {
			h.emit(BrowserEvent{TabID: h.tab, Type: "controller_close_failed", Payload: map[string]any{
				"error": err.Error(),
			}})
		}
		// 只还我们自己的那一份 ICoreWebView2 引用（getCoreWebView2 出参），
		// 不动 edge 自己 AddRef 的对象：见 releaseOwnedWebViewRef 的说明。
		if released, err := releaseOwnedWebViewRef(h.cdp); err != nil {
			h.emit(BrowserEvent{TabID: h.tab, Type: "com_release_failed", Payload: map[string]any{
				"error": err.Error(),
			}})
		} else if released > 0 {
			h.emit(BrowserEvent{TabID: h.tab, Type: "com_released", Payload: map[string]any{"count": released}})
		}
	}
	h.ctrl = nil
	h.chromium = nil
	h.cdp = nil
	h.controllerReady.Store(false)
	h.focused.Store(false)
	// 页面缩放跟着 controller 一起消失：回到 1 表示“没有活着的 controller”，
	// 下一次 CreateTab 会按 lastSlot.Zoom 重新设置。
	h.zoomBits.Store(math.Float64bits(1))
	h.snaps = map[string]*snapshotRefs{}
	if h.tabOpen {
		h.emit(BrowserEvent{TabID: h.tab, Type: EventTabClosed})
	}
	h.tabOpen = false
	h.webviewChild.Store(0)
	if child := h.childHWND.Load(); child != 0 {
		destroyWindow(child)
		h.childHWND.Store(0)
	}
}

func (h *host) runOnUI(ctx context.Context, fn func() error) error {
	if h.ui == nil {
		return appErr(contracts.CodeInternal, "宿主没有 UI 派发器：Attach 尚未成功")
	}
	var out error
	if err := h.ui.invoke(ctx, func() { out = fn() }); err != nil {
		return err
	}
	return out
}

func (h *host) scale() float64 {
	dpi := h.dpi.Load()
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// zoom 返回页面缩放因子（1 = 100%）。
func (h *host) zoom() float64 {
	bits := h.zoomBits.Load()
	if bits == 0 {
		return 1
	}
	z := math.Float64frombits(bits)
	if !(z > 0) {
		return 1
	}
	return z
}

// applyZoom 把页面缩放设成 zoom 并记下实测值（必须在 UI 线程调用）。
//
// 只改页面缩放：子窗口的物理尺寸仍然是 slot CSS 尺寸 × DPI/96，两者互不影响。
func (h *host) applyZoom(zoom float64) error {
	if h.ctrl == nil {
		// controller 还没就绪：记下期望值，CreateTab 就绪后会补设。
		if zoom > 0 {
			h.zoomBits.Store(math.Float64bits(zoom))
		}
		return nil
	}
	if !(zoom > 0) {
		return appErr(contracts.CodeUnsupported, "缩放因子必须为正（收到 %v）", zoom)
	}
	if err := putZoomFactor(h.ctrl, zoom); err != nil {
		return err
	}
	got, err := getZoomFactor(h.ctrl)
	if err != nil {
		return err
	}
	h.zoomBits.Store(math.Float64bits(got))
	return nil
}

func (h *host) requireTab(ctx context.Context, tab browser.TabID) error {
	return h.runOnUI(ctx, func() error {
		if !h.tabOpen || h.tab != tab {
			return appErr(contracts.CodeNotFound, "没有活跃 tab %q（当前 %q）", string(tab), string(h.tab))
		}
		if h.cdp == nil {
			return appErr(contracts.CodeInternal, "controller 尚未就绪")
		}
		return nil
	})
}

func (h *host) emit(ev BrowserEvent) {
	if h.opts.OnEvent == nil {
		return
	}
	// OnEvent 在 UI 线程上调用：实现方必须不阻塞（典型做法是写 channel）。
	h.opts.OnEvent(ev)
}

// navigationID 返回当前文档的稳定标识（每次导航完成变化）。
func (h *host) navigationID() string {
	return fmt.Sprintf("nav-%d", h.renderNav.Load())
}

// cdpRoundTrip 在 UI 线程发起一次 CDP 调用，再在调用方线程等结果。
func (h *host) cdpRoundTrip(ctx context.Context, method, params string, timeout time.Duration) (string, error) {
	var call *cdpCall
	err := h.runOnUI(ctx, func() error {
		if h.cdp == nil {
			return appErr(contracts.CodeInternal, "controller 尚未就绪")
		}
		var err error
		call, err = h.cdp.start(method, params)
		return err
	})
	if err != nil {
		return "", err
	}
	return call.wait(ctx, timeout)
}

func (h *host) cdpEvaluate(ctx context.Context, execContext int64, expression string, timeout time.Duration) (string, error) {
	var call *cdpCall
	err := h.runOnUI(ctx, func() error {
		if h.cdp == nil {
			return appErr(contracts.CodeInternal, "controller 尚未就绪")
		}
		var err error
		call, err = h.cdp.evalInContext(execContext, expression)
		return err
	})
	if err != nil {
		return "", err
	}
	return call.wait(ctx, timeout)
}

// viewportMetrics 返回页面 CSS viewport 与设备像素比。
func (h *host) viewportMetrics(ctx context.Context) (w, hgt, scale float64, err error) {
	res, err := h.cdpEvaluate(ctx, 0,
		`JSON.stringify([window.innerWidth, window.innerHeight, window.devicePixelRatio])`, 5*time.Second)
	if err != nil {
		return 0, 0, 0, err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return 0, 0, 0, err
	}
	var vals []float64
	if err := json.Unmarshal([]byte(value), &vals); err != nil || len(vals) != 3 {
		return 0, 0, 0, appErr(contracts.CodeInternal, "解析 viewport 失败: %q", value)
	}
	if vals[2] <= 0 {
		vals[2] = 1
	}
	return vals[0], vals[1], vals[2], nil
}

// runtimeValue 从 Runtime.evaluate 的返回里取出字符串结果。
func runtimeValue(resultJSON string) (string, error) {
	var payload struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &payload); err != nil {
		return "", appErr(contracts.CodeInternal, "解析 Runtime.evaluate 结果失败: %v", err)
	}
	if payload.ExceptionDetails != nil {
		return "", appErr(contracts.CodeInternal,
			"页面求值抛异常: %s", payload.ExceptionDetails.Text)
	}
	return payload.Result.Value, nil
}
