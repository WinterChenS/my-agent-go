//go:build windows

package native

// 本文件是对 github.com/jchv/go-webview2/pkg/edge 的**薄**补充封装。
//
// 为什么需要它：edge 把 controller 的 vtable 与 ICoreWebView2 的 vtable 都做成了
// 未导出字段，外部包只能调用它已经包好的少数方法（Navigate/Eval/Show/Hide/
// Resize/GetSettings/Focus）。B01a 需要而 edge 没包的只有三件事：
//
//	GetCoreWebView2（从 controller 取 webview 接口指针）
//	Close（销毁 controller 必须显式调用）
//	CallDevToolsProtocolMethod（截图与隔离世界求值都靠它）
//
// 这里用到的 vtable 槽顺序直接抄自 edge 的 ICoreWebView2Controller.go /
// corewebview2.go（它们与 Microsoft 的 ABI 一致）。这是**ABI 依赖**：
// 一旦 go-webview2 或 WebView2 运行时改变 vtable 顺序，测试会先红的那条写在
// docs/adr/0004-webview2-host-dependency.md。改动本文件必须同时更新 ADR。
//
// 注意：这里的 unsafe 用法与 edge 自己一致——把 Go 对象指针交给 COM 之后必须
// 让它一直活着。我们通过在 cdpClient.keep 里持有 handler 指针做到这一点，
// 宁可每个调用泄漏几十字节，也不让 native 侧持有已回收的指针。

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"my-agent-go/internal/contracts"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// controllerVtbl 是 ICoreWebView2Controller 的 vtable（顺序即 ABI）。
type controllerVtbl struct {
	QueryInterface, AddRef, Release                                                  edge.ComProc
	GetIsVisible, PutIsVisible                                                       edge.ComProc
	GetBounds, PutBounds, GetZoomFactor, PutZoomFactor                               edge.ComProc
	AddZoomFactorChanged, RemoveZoomFactorChanged, SetBoundsAndZoomFactor, MoveFocus edge.ComProc
	AddMoveFocusRequested, RemoveMoveFocusRequested                                  edge.ComProc
	AddGotFocus, RemoveGotFocus, AddLostFocus, RemoveLostFocus                       edge.ComProc
	AddAcceleratorKeyPressed, RemoveAcceleratorKeyPressed                            edge.ComProc
	GetParentWindow, PutParentWindow, NotifyParentWindowPositionChanged, Close       edge.ComProc
	GetCoreWebView2                                                                  edge.ComProc
}

// webviewVtbl 是 ICoreWebView2 的 vtable，声明到 CallDevToolsProtocolMethod 为止。
type webviewVtbl struct {
	QueryInterface, AddRef, Release                             edge.ComProc
	GetSettings, GetSource, Navigate, NavigateToString          edge.ComProc
	AddNavigationStarting, RemoveNavigationStarting             edge.ComProc
	AddContentLoading, RemoveContentLoading                     edge.ComProc
	AddSourceChanged, RemoveSourceChanged                       edge.ComProc
	AddHistoryChanged, RemoveHistoryChanged                     edge.ComProc
	AddNavigationCompleted, RemoveNavigationCompleted           edge.ComProc
	AddFrameNavigationStarting, RemoveFrameNavigationStarting   edge.ComProc
	AddFrameNavigationCompleted, RemoveFrameNavigationCompleted edge.ComProc
	AddScriptDialogOpening, RemoveScriptDialogOpening           edge.ComProc
	AddPermissionRequested, RemovePermissionRequested           edge.ComProc
	AddProcessFailed, RemoveProcessFailed                       edge.ComProc
	AddScriptToExecuteOnDocumentCreated                         edge.ComProc
	RemoveScriptToExecuteOnDocumentCreated                      edge.ComProc
	ExecuteScript, CapturePreview, Reload                       edge.ComProc
	PostWebMessageAsJSON, PostWebMessageAsString                edge.ComProc
	AddWebMessageReceived, RemoveWebMessageReceived             edge.ComProc
	CallDevToolsProtocolMethod                                  edge.ComProc
}

// navArgsVtbl 是 ICoreWebView2NavigationCompletedEventArgs 的 vtable。
type navArgsVtbl struct {
	QueryInterface, AddRef, Release edge.ComProc
	GetIsSuccess, GetWebErrorStatus edge.ComProc
	GetNavigationID                 edge.ComProc
}

// zoomToBits 把 float64 位模式塞进 uintptr：COM 的 double 参数按值传递。
func zoomToBits(z float64) uintptr {
	return uintptr(*(*uint64)(unsafe.Pointer(&z)))
}

func controllerVtblOf(c *edge.ICoreWebView2Controller) *controllerVtbl {
	return *(**controllerVtbl)(unsafe.Pointer(c))
}

func webviewVtblOf(w *edge.ICoreWebView2) *webviewVtbl {
	return *(**webviewVtbl)(unsafe.Pointer(w))
}

// getCoreWebView2 取 ICoreWebView2 接口指针（controller 的 GetCoreWebView2 槽）。
func getCoreWebView2(c *edge.ICoreWebView2Controller) (*edge.ICoreWebView2, error) {
	if c == nil {
		return nil, appErr(contracts.CodeInternal, "controller 为空")
	}
	var webview *edge.ICoreWebView2
	hr, _, _ := controllerVtblOf(c).GetCoreWebView2.Call(
		uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&webview)))
	if int32(uint32(hr)) < 0 {
		return nil, appErr(contracts.CodeInternal, "GetCoreWebView2 HRESULT=0x%08x", uint32(hr))
	}
	if webview == nil {
		return nil, appErr(contracts.CodeInternal, "GetCoreWebView2 返回空指针")
	}
	return webview, nil
}

// closeController 销毁 WebView2 controller。
//
// 必须显式调用：只 DestroyWindow 父窗口不会回收 controller，浏览器进程会一直挂着
// （B01a 的进程残留检查就是看这个）。
func closeController(c *edge.ICoreWebView2Controller) error {
	if c == nil {
		return nil
	}
	hr, _, _ := controllerVtblOf(c).Close.Call(uintptr(unsafe.Pointer(c)))
	if int32(uint32(hr)) < 0 {
		return appErr(contracts.CodeInternal, "ICoreWebView2Controller.Close HRESULT=0x%08x", uint32(hr))
	}
	return nil
}

// unknownVtbl 只声明 IUnknown 三槽：所有 COM 接口都从它们开始。
type unknownVtbl struct {
	QueryInterface, AddRef, Release edge.ComProc
}

// releaseCOM 调 IUnknown::Release，返回新引用计数。
func releaseCOM(p uintptr) uint32 {
	if p == 0 {
		return 0
	}
	vt := *(**unknownVtbl)(ptrFromUintptr(p))
	refs, _, _ := vt.Release.Call(p)
	return uint32(refs)
}

// releaseOwnedWebViewRef 只释放**我们自己**拿的那个 ICoreWebView2 引用。
//
// 引用账目（同一个 tab 周期内）：
//   - ICoreWebView2：edge 在 CreateCoreWebView2ControllerCompleted 里 AddRef 一次（它的，
//     不是我们的）；getCoreWebView2 的出参又给我们一份（我们的，必须还）；
//   - ICoreWebView2Controller / ICoreWebView2Environment：只有 edge 的 AddRef。
//
// 为什么不把 edge 欠的那几份也一并 Release：B01b 做过 A/B 实测（证据文档 §B01b 的
// 进程表），关掉 controller 后浏览器进程无论我们是否额外 Release 都会退出
// （`close` 之后 owned msedgewebview2.exe 都是 0），所以替 upstream 还债没有收益，
// 只有“晚回调拿到已释放对象”的风险。这里只还自己的，不碰别人的。
func releaseOwnedWebViewRef(cdp *cdpClient) (int, error) {
	if cdp == nil || cdp.webview == nil {
		return 0, nil
	}
	releaseCOM(uintptr(unsafe.Pointer(cdp.webview)))
	return 1, nil
}

// getZoomFactor 读取 controller 当前的页面缩放因子（ICoreWebView2Controller.GetZoomFactor）。
//
// 这是 WebView2 的**页面缩放**（等价于浏览器的 Ctrl+±），不是 Windows 的 DPI 缩放：
// DPI 缩放由 GetDpiForWindow 给出、只影响 CSS px → 物理 px 的换算；页面缩放只影响
// 页面内部布局（window.innerWidth 变小、devicePixelRatio 变大）。两条换算不能混用。
func getZoomFactor(c *edge.ICoreWebView2Controller) (float64, error) {
	if c == nil {
		return 0, appErr(contracts.CodeInternal, "controller 为空")
	}
	var zoom float64
	hr, _, _ := controllerVtblOf(c).GetZoomFactor.Call(
		uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&zoom)))
	if int32(uint32(hr)) < 0 {
		return 0, appErr(contracts.CodeInternal, "ICoreWebView2Controller.GetZoomFactor HRESULT=0x%08x", uint32(hr))
	}
	if zoom <= 0 {
		return 0, appErr(contracts.CodeInternal, "GetZoomFactor 返回非正值 %v", zoom)
	}
	return zoom, nil
}

// putZoomFactor 设置页面缩放因子（ICoreWebView2Controller.PutZoomFactor）。
//
// 只改页面缩放，不动 bounds、不改系统 DPI：系统 DPI 改了要注销/重启，测试里不能那么干。
func putZoomFactor(c *edge.ICoreWebView2Controller, zoom float64) error {
	if c == nil {
		return appErr(contracts.CodeInternal, "controller 为空")
	}
	if !(zoom > 0) {
		return appErr(contracts.CodeUnsupported, "缩放因子必须为正（收到 %v）", zoom)
	}
	hr, _, _ := controllerVtblOf(c).PutZoomFactor.Call(
		uintptr(unsafe.Pointer(c)), uintptr(zoomToBits(zoom)))
	if int32(uint32(hr)) < 0 {
		return appErr(contracts.CodeInternal, "ICoreWebView2Controller.PutZoomFactor(%.2f) HRESULT=0x%08x",
			zoom, uint32(hr))
	}
	return nil
}

// navigationResult 读取导航完成事件的关键字段（成功与否、错误码、导航 ID）。
func navigationResult(args *edge.ICoreWebView2NavigationCompletedEventArgs) (ok bool, navID uint64, webError int32) {
	if args == nil {
		return false, 0, -1
	}
	vt := *(**navArgsVtbl)(unsafe.Pointer(args))
	var success int32
	_, _, _ = vt.GetIsSuccess.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&success)))
	_, _, _ = vt.GetNavigationID.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&navID)))
	_, _, _ = vt.GetWebErrorStatus.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&webError)))
	return success != 0, navID, webError
}

// ---------- 完成回调的 COM 处理器 ----------

type handlerVtbl struct {
	QueryInterface, AddRef, Release, Invoke edge.ComProc
}

// comHandler 是一个最小 IUnknown + Invoke 实现，供 CDP 完成回调使用。
type comHandler struct {
	vtbl   *handlerVtbl
	invoke func(errorCode uintptr, payload uintptr)
}

var (
	handlerOnce sync.Once
	handlerVTB  handlerVtbl

	handlerMu    sync.Mutex
	handlerByPtr = map[uintptr]*comHandler{}
)

// sharedHandlerVtable 只构造一次：每次 NewComProc 都会占一个 Go→C 回调槽，
// 每个请求造一套会很快耗尽。
func sharedHandlerVtable() *handlerVtbl {
	handlerOnce.Do(func() {
		handlerVTB.QueryInterface = edge.NewComProc(func(this, riid, out uintptr) uintptr { return 0 })
		handlerVTB.AddRef = edge.NewComProc(func(this uintptr) uintptr { return 1 })
		handlerVTB.Release = edge.NewComProc(func(this uintptr) uintptr { return 1 })
		handlerVTB.Invoke = edge.NewComProc(func(this, errorCode, payload uintptr) uintptr {
			handlerMu.Lock()
			h := handlerByPtr[this]
			handlerMu.Unlock()
			if h != nil {
				h.invoke(errorCode, payload)
			}
			return 0
		})
	})
	return &handlerVTB
}

func newComHandler(fn func(errorCode uintptr, payload uintptr)) *comHandler {
	h := &comHandler{vtbl: sharedHandlerVtable(), invoke: fn}
	handlerMu.Lock()
	handlerByPtr[uintptr(unsafe.Pointer(h))] = h
	handlerMu.Unlock()
	return h
}

// cdpResultMaxUnits 是一条 CDP 结果最多读取的 UTF-16 单元数（32M 单元 ≈ 64MB）。
//
// 上限存在两个理由：坏指针不能把进程读崩；超出上限时必须**明确报告被截断**，
// 而不是把半截 JSON 当结果交出去（B04 的"不许静默截断"同样适用于传输层）。
// base64 截图会到这个量级，所以上限不能太小；真的更大时由截图路径改用受控缩小。
const cdpResultMaxUnits = 32 << 20

// utf16FromNativePtr 把 native 返回的 UTF-16 指针读成 Go 字符串。
//
// 第二个返回值表示"结果是否被上限截断"：截断的 JSON 一定不是合法结果，
// 调用方必须把它当错误处理，不能回退成"看起来空的结果"。
func utf16FromNativePtr(p uintptr, limit int) (string, bool) {
	if p == 0 {
		return "", false
	}
	if limit <= 0 || limit > cdpResultMaxUnits {
		limit = cdpResultMaxUnits
	}
	buf := make([]uint16, 0, 512)
	base := ptrFromUintptr(p)
	for i := 0; i < limit; i++ {
		c := *(*uint16)(unsafe.Add(base, i*2))
		if c == 0 {
			return windows.UTF16ToString(buf), false
		}
		buf = append(buf, c)
	}
	return windows.UTF16ToString(buf), true
}

// resultTooLargeError 表示一条 CDP 结果超出了可传输上限。
//
// 它不是业务失败，而是"这条结果太大了"：截图路径据此改用受控缩小，
// 其他路径必须把它当错误上报（绝不返回半截 JSON）。
type resultTooLargeError struct {
	method string
}

func (e *resultTooLargeError) Error() string {
	return "CDP " + e.method + " 的结果超过传输上限（会被截断）：拒绝返回不完整的 JSON"
}

// Code 让它对上层也表现得像一个可分类的错误。
func (e *resultTooLargeError) Code() string { return contracts.CodeUnsupported }

// ptrFromUintptr 把 native 交回来的整数地址还原成指针。
//
// 不能直接写 unsafe.Pointer(nativeUintptr)：go vet 的 unsafeptr 检查会拒绝
// 这种转换（它无法区分“native 交回的地址”和“算错的 uintptr”）。这里用
// 中间指针还原同一份位模式，语义相同，但不会被误报。
func ptrFromUintptr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

// wstr 把 Go 字符串转成 COM 需要的 UTF-16 指针。
func wstr(s string) (*uint16, error) {
	return windows.UTF16PtrFromString(s)
}

// jsonString 把一个字符串编码成 JSON 字面量（带引号与转义）。
// CDP 参数全部是 JSON，手拼字符串很容易在中文/引号上翻车。
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// ---------- CDP 客户端 ----------

// cdpResult 是一次 DevTools 协议调用的结果。
type cdpResult struct {
	resultJSON string
	err        error
}

// cdpCall 在 UI 线程发起、在任意线程等待。
//
// 这个两段式不是风格问题：CallDevToolsProtocolMethod 的完成回调由 UI 线程的
// 消息循环派发，如果在 UI 线程上阻塞等结果就会死锁（scratch 第一次就是这样超时的）。
type cdpCall struct {
	ch chan cdpResult
}

// wait 等结果。ctx 超时后如实返回错误，不重放调用。
func (c *cdpCall) wait(ctx context.Context, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-c.ch:
		return r.resultJSON, r.err
	case <-timer.C:
		return "", appErr(contracts.CodeOutcomeUnknown,
			"CDP 调用在 %s 内没有完成：结果未知，不自动重放", timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// cdpClient 绑定一个 webview，负责发起 CDP 调用。
//
// 所有方法都必须在 UI 线程上调用（initiate 部分），由 Host 通过 uiThread.invoke
// 保证。
type cdpClient struct {
	webview *edge.ICoreWebView2

	mu   sync.Mutex
	keep []*comHandler // 见文件头：交给 COM 的 Go 指针必须一直活着
}

func newCDPClient(w *edge.ICoreWebView2) *cdpClient {
	return &cdpClient{webview: w}
}

// start 发起一次 CallDevToolsProtocolMethod；结果通过返回的 cdpCall 等待。
func (c *cdpClient) start(method, params string) (*cdpCall, error) {
	if c == nil || c.webview == nil {
		return nil, appErr(contracts.CodeInternal, "CDP 客户端未初始化（controller 还没就绪）")
	}
	call := &cdpCall{ch: make(chan cdpResult, 1)}
	handler := newComHandler(func(errorCode uintptr, payload uintptr) {
		if int32(uint32(errorCode)) < 0 {
			call.ch <- cdpResult{err: appErr(contracts.CodeInternal,
				"CDP %s 失败 HRESULT=0x%08x", method, uint32(errorCode))}
			return
		}
		value, truncated := utf16FromNativePtr(payload, cdpResultMaxUnits)
		if truncated {
			call.ch <- cdpResult{err: &resultTooLargeError{method: method}}
			return
		}
		call.ch <- cdpResult{resultJSON: value}
	})
	c.mu.Lock()
	c.keep = append(c.keep, handler)
	c.mu.Unlock()

	var invokeFailed error
	mp, err := wstr(method)
	if err == nil {
		var pp *uint16
		pp, err = wstr(params)
		if err == nil {
			hr, _, _ := webviewVtblOf(c.webview).CallDevToolsProtocolMethod.Call(
				uintptr(unsafe.Pointer(c.webview)),
				uintptr(unsafe.Pointer(mp)),
				uintptr(unsafe.Pointer(pp)),
				uintptr(unsafe.Pointer(handler)),
			)
			if int32(uint32(hr)) < 0 {
				invokeFailed = appErr(contracts.CodeInternal,
					"CallDevToolsProtocolMethod(%s) HRESULT=0x%08x", method, uint32(hr))
			}
		}
	}
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "CDP 参数转 UTF-16 失败: %v", err)
	}
	if invokeFailed != nil {
		return nil, invokeFailed
	}
	return call, nil
}

// evalInContext 在指定执行上下文里求值并把返回值当字符串取出。
// contextID <= 0 表示默认上下文。
func (c *cdpClient) evalInContext(execContext int64, expression string) (*cdpCall, error) {
	params := fmt.Sprintf(`{"expression":%s,"returnByValue":true%s}`,
		jsonString(expression), contextParam(execContext))
	return c.start("Runtime.evaluate", params)
}

func contextParam(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf(`,"contextId":%d`, id)
}

// evalAwait 求值一个返回 Promise 的表达式并等它 resolve（awaitPromise）。
func (c *cdpClient) evalAwait(expression string) (*cdpCall, error) {
	params := fmt.Sprintf(`{"expression":%s,"returnByValue":true,"awaitPromise":true}`,
		jsonString(expression))
	return c.start("Runtime.evaluate", params)
}
