//go:build windows

package native

// 本文件是 B01a 的 Win32 基础件：线程派发器、子窗口创建、坐标与 DPI 换算。
//
// 之所以自己写派发器而不是用 application.InvokeSync：
//   - internal/browser/native 不应该 import Wails，否则 tests/native 没法用普通
//     Win32 窗口跑同一条代码路径；
//   - 派发语义很简单：把闭包 PostMessage 到 UI 线程，UI 线程的消息循环
//     DispatchMessage 到我们自己的 message-only 窗口时执行它。
//
// 这样 Attach 只要在拥有 parent HWND 的线程上调用，后续所有 native 操作都会落回
// 同一个线程，和 Wails 的 UI 线程是同一个东西。

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"my-agent-go/internal/contracts"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")

	procRegisterClassExW       = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW        = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow          = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW         = modUser32.NewProc("DefWindowProcW")
	procGetMessageW            = modUser32.NewProc("GetMessageW")
	procTranslateMessage       = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW       = modUser32.NewProc("DispatchMessageW")
	procPostMessageW           = modUser32.NewProc("PostMessageW")
	procPostQuitMessage        = modUser32.NewProc("PostQuitMessage")
	procGetWindowThreadProcess = modUser32.NewProc("GetWindowThreadProcessId")
	procGetParent              = modUser32.NewProc("GetParent")
	procGetClientRect          = modUser32.NewProc("GetClientRect")
	procGetWindowRect          = modUser32.NewProc("GetWindowRect")
	procSetWindowPos           = modUser32.NewProc("SetWindowPos")
	procShowWindow             = modUser32.NewProc("ShowWindow")
	procIsWindow               = modUser32.NewProc("IsWindow")
	procIsWindowVisible        = modUser32.NewProc("IsWindowVisible")
	procEnumChildWindows       = modUser32.NewProc("EnumChildWindows")
	procGetDpiForWindow        = modUser32.NewProc("GetDpiForWindow")
	procCoInitializeEx         = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize         = modOle32.NewProc("CoUninitialize")
)

// windows.HWND_MESSAGE 是 message-only 窗口的父子句柄（-3）。
const hwndMessage = ^uintptr(2)

const (
	wmAppDispatch = 0x8000 + 0x2A1 // WM_APP+673：宿主的 UI 线程任务派发

	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsClipChildren = 0x02000000

	swHide = 0
	swShow = 5

	swpNoActivate = 0x0010
	swpNoOwner    = 0x0200

	coinitApartmentThreaded = 0x2
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type winRect struct{ Left, Top, Right, Bottom int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

func appErr(code, format string, args ...any) *contracts.AppError {
	return contracts.NewAppError(code, fmt.Sprintf(format, args...), false)
}

// hwndFromParent 校验 NativeParent 里装的确实是 Windows 句柄。
//
// Wails v3 的 WebviewWindow.NativeWindow() 返回 unsafe.Pointer（unsafe.Pointer(hwnd)），
// 测试里为了方便会用 uintptr / windows.HWND。三种都接受，其余一律明确报错：
// 把任意 any 当句柄用等于随手解引用野指针。
func hwndFromParent(handle any) (uintptr, error) {
	switch v := handle.(type) {
	case unsafe.Pointer:
		if v == nil {
			return 0, appErr(contracts.CodeInternal, "NativeParent 句柄为 nil unsafe.Pointer")
		}
		return uintptr(v), nil
	case uintptr:
		if v == 0 {
			return 0, appErr(contracts.CodeInternal, "NativeParent 句柄为 0")
		}
		return v, nil
	case windows.HWND:
		if v == 0 {
			return 0, appErr(contracts.CodeInternal, "NativeParent 句柄为 0")
		}
		return uintptr(v), nil
	case nil:
		return 0, appErr(contracts.CodeInternal, "NativeParent 没有句柄：窗口还没创建或已销毁")
	default:
		return 0, appErr(contracts.CodeInternal,
			"NativeParent 句柄类型不支持：%T（Windows 期望 unsafe.Pointer/uintptr/HWND）", handle)
	}
}

// windowThread 返回拥有该窗口消息队列的线程 ID。
func windowThread(hwnd uintptr) uint32 {
	var pid uint32
	tid, _, _ := procGetWindowThreadProcess.Call(
		hwnd, uintptr(unsafe.Pointer(&pid)))
	return uint32(tid)
}

// currentThread 返回当前 OS 线程 ID。
func currentThread() uint32 {
	tid, _, _ := modKernel32.NewProc("GetCurrentThreadId").Call()
	return uint32(tid)
}

// ensureCOM 在 UI 线程上初始化 STA。
//
// WebView2 的 controller 必须在已初始化 COM 的线程上创建。Wails 的 UI 线程
// 通常已经初始化过；测试自建窗口的线程没有，所以这里自己保证一次。
// 返回的 release 只在本线程确实做了初始化时才需要调用。
func ensureCOM() (release func(), err error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	switch uint32(hr) {
	case 0: // S_OK：本次初始化成功
		return func() { _, _, _ = procCoUninitialize.Call() }, nil
	case 1: // S_FALSE：本线程已经初始化过（引用计数 +1），同样要配对 CoUninitialize
		return func() { _, _, _ = procCoUninitialize.Call() }, nil
	case 0x80010106: // RPC_E_CHANGED_MODE：线程已是 MTA，不能改。WebView2 会自己处理。
		return func() {}, nil
	default:
		return nil, appErr(contracts.CodeInternal, "CoInitializeEx(STA) 失败 HRESULT=0x%08x", uint32(hr))
	}
}

// ---------- UI 线程派发器 ----------

// uiTask 是一次排队到 UI 线程的闭包。
type uiTask struct {
	fn        func()
	done      chan struct{}
	abandoned bool
}

// uiThread 把闭包派发到创建它的那个 OS 线程。
type uiThread struct {
	threadID uint32
	hwnd     uintptr

	mu     sync.Mutex
	tasks  map[uintptr]*uiTask
	next   uintptr
	closed bool
}

var (
	classOnce sync.Once
	classErr  error

	dispMu     sync.Mutex
	dispByHWND = map[uintptr]*uiThread{}
)

// registerWindowClasses 注册宿主要用的两个窗口类（只注册一次）。
func registerWindowClasses() error {
	classOnce.Do(func() {
		hinst, _, _ := modKernel32.NewProc("GetModuleHandleW").Call(0)
		wndProc := windows.NewCallback(hostWndProc)
		for _, name := range []string{messageWindowClass, childWindowClass} {
			cls := utf16Ptr(name)
			wc := wndClassExW{
				lpfnWndProc:   wndProc,
				hInstance:     windows.Handle(hinst),
				lpszClassName: cls,
			}
			wc.cbSize = uint32(unsafe.Sizeof(wc))
			atom, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
			if atom == 0 {
				// ERROR_CLASS_ALREADY_EXISTS（1410）说明同一个进程里已经注册过，
				// 这在测试里会出现在多个用例之间，不算失败。
				if errno, ok := e.(windows.Errno); ok && errno == 1410 {
					continue
				}
				classErr = appErr(contracts.CodeInternal, "RegisterClassExW(%s) 失败: %v", name, e)
				return
			}
		}
	})
	return classErr
}

const (
	messageWindowClass = "MyAgentNativeHostDispatch"
	childWindowClass   = "MyAgentNativeHostChild"
)

// hostWndProc 处理派发窗口与子窗口的消息。
func hostWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if uint32(msg) == wmAppDispatch {
		dispMu.Lock()
		disp := dispByHWND[hwnd]
		dispMu.Unlock()
		if disp != nil {
			disp.runTask(wparam)
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// runTask 在 UI 线程上执行一个已排队的闭包。
func (t *uiThread) runTask(id uintptr) {
	t.mu.Lock()
	task, ok := t.tasks[id]
	if ok {
		delete(t.tasks, id)
		if task.abandoned {
			ok = false
		}
	}
	t.mu.Unlock()
	if !ok {
		return
	}
	defer close(task.done)
	task.fn()
}

// newUIThread 在当前线程上建 message-only 窗口，之后所有 invoke 都会回到这里。
//
// 必须在目标 UI 线程上调用（Attach 里已经先校验过线程）。
func newUIThread() (*uiThread, error) {
	if err := registerWindowClasses(); err != nil {
		return nil, err
	}
	hinst, _, _ := modKernel32.NewProc("GetModuleHandleW").Call(0)
	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(messageWindowClass))), 0, 0,
		0, 0, 0, 0, hwndMessage, 0, hinst, 0)
	if hwnd == 0 {
		return nil, appErr(contracts.CodeInternal, "创建派发窗口失败: %v", e)
	}
	disp := &uiThread{threadID: currentThread(), hwnd: hwnd, tasks: map[uintptr]*uiTask{}}
	dispMu.Lock()
	dispByHWND[hwnd] = disp
	dispMu.Unlock()
	return disp, nil
}

// invoke 把 fn 送到 UI 线程执行；已经在 UI 线程时直接执行（Wails 的派发器
// 也是这个语义，避免自己等自己）。
func (t *uiThread) invoke(ctx context.Context, fn func()) error {
	if t == nil {
		return appErr(contracts.CodeInternal, "宿主没有 UI 派发器：Attach 尚未成功")
	}
	if currentThread() == t.threadID {
		fn()
		return nil
	}
	task := &uiTask{fn: fn, done: make(chan struct{})}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return appErr(contracts.CodeInternal, "UI 派发器已关闭（宿主已 Detach）")
	}
	id := t.next
	t.next++
	t.tasks[id] = task
	t.mu.Unlock()

	ok, _, e := procPostMessageW.Call(t.hwnd, wmAppDispatch, id, 0)
	if ok == 0 {
		t.mu.Lock()
		delete(t.tasks, id)
		t.mu.Unlock()
		return appErr(contracts.CodeInternal, "PostMessage(UI 派发) 失败: %v", e)
	}

	select {
	case <-task.done:
		return nil
	case <-ctx.Done():
		t.mu.Lock()
		if t.tasks[id] == task {
			delete(t.tasks, id)
			task.abandoned = true
			t.mu.Unlock()
			return ctx.Err()
		}
		t.mu.Unlock()
		// 已经在执行：等它跑完，避免"调用方已经超时但闭包还在改状态"。
		select {
		case <-task.done:
		case <-time.After(2 * time.Second):
		}
		return ctx.Err()
	}
}

// close 销毁派发窗口；之后 invoke 一律返回明确错误。
func (t *uiThread) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	tasks := t.tasks
	t.tasks = map[uintptr]*uiTask{}
	hwnd := t.hwnd
	t.mu.Unlock()

	for _, task := range tasks {
		task.abandoned = true
	}
	dispMu.Lock()
	delete(dispByHWND, hwnd)
	dispMu.Unlock()
	_, _, _ = procDestroyWindow.Call(hwnd)
}

// ---------- 子窗口与几何 ----------

// createChildWindow 在 parent 下建一个 WS_CHILD 子窗口。
func createChildWindow(parent uintptr, x, y, w, h int32) (uintptr, error) {
	if err := registerWindowClasses(); err != nil {
		return 0, err
	}
	hinst, _, _ := modKernel32.NewProc("GetModuleHandleW").Call(0)
	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(childWindowClass))), 0,
		wsChild|wsVisible|wsClipChildren,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, 0, hinst, 0)
	if hwnd == 0 {
		return 0, appErr(contracts.CodeInternal, "CreateWindowExW(child) 失败: %v", e)
	}
	return hwnd, nil
}

func parentOf(hwnd uintptr) uintptr {
	p, _, _ := procGetParent.Call(hwnd)
	return p
}

func clientRect(hwnd uintptr) (winRect, error) {
	var r winRect
	ok, _, e := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return r, appErr(contracts.CodeInternal, "GetClientRect(0x%x) 失败: %v", hwnd, e)
	}
	return r, nil
}

func windowRect(hwnd uintptr) (winRect, error) {
	var r winRect
	ok, _, e := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return r, appErr(contracts.CodeInternal, "GetWindowRect(0x%x) 失败: %v", hwnd, e)
	}
	return r, nil
}

// moveWindow 把子窗口摆到指定位置，并把它抬到同级窗口的最上面。
//
// 必须抬到顶部：Wails 主 WebView 也是一个子 HWND，它会因为 resize/paint 把
// 自己 SetWindowPos 到最上层。如果不抬，我们的原生视图会被主 WebView 盖住——
// CDP 截图仍然正常（那是另个合成器），但用户和窗口截图什么都看不到。
// 这是 B01a 实测出来的坑，不是理论风险。
func moveWindow(hwnd uintptr, x, y, w, h int32) error {
	ok, _, e := procSetWindowPos.Call(hwnd, 0, // 0 = HWND_TOP
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		swpNoActivate|swpNoOwner)
	if ok == 0 {
		return appErr(contracts.CodeInternal, "SetWindowPos(0x%x) 失败: %v", hwnd, e)
	}
	return nil
}

func showWindow(hwnd uintptr, cmd int32) {
	_, _, _ = procShowWindow.Call(hwnd, uintptr(cmd))
}

func isWindow(hwnd uintptr) bool {
	ok, _, _ := procIsWindow.Call(hwnd)
	return ok != 0
}

func isWindowVisible(hwnd uintptr) bool {
	ok, _, _ := procIsWindowVisible.Call(hwnd)
	return ok != 0
}

func destroyWindow(hwnd uintptr) {
	if hwnd != 0 {
		_, _, _ = procDestroyWindow.Call(hwnd)
	}
}

// dpiForWindow 返回窗口所在显示器的 DPI；取不到时按 96 处理（即 100%）。
func dpiForWindow(hwnd uintptr) uint32 {
	// GetDpiForWindow 需要 Win10 1607+；这台机器是 19045，一定存在。
	// 老系统上 proc.Find 会失败，这里退回 96 而不是猜一个非 1 的缩放。
	if err := procGetDpiForWindow.Find(); err != nil {
		return 96
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		return 96
	}
	return uint32(dpi)
}

// firstChildOf 返回 hwnd 的第一个子窗口（WebView2 的渲染窗口），没有则 0。
func firstChildOf(hwnd uintptr) uintptr {
	var found uintptr
	cb := windows.NewCallback(func(child, _ uintptr) uintptr {
		found = child
		return 0 // 找到一个就停
	})
	_, _, _ = procEnumChildWindows.Call(hwnd, cb, 0)
	return found
}

func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err) // 常量字符串不应失败
	}
	return p
}

// atomicUintptr 是 WindowInfo 用的无锁快照字段。
type atomicUintptr struct{ v atomic.Uintptr }

func (a *atomicUintptr) Store(v uintptr) { a.v.Store(v) }
func (a *atomicUintptr) Load() uintptr   { return a.v.Load() }
