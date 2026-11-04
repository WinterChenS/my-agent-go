//go:build windows

// Package nativetest 用真实 Win32 窗口 + 消息循环驱动 internal/browser/native。
//
// 为什么不用 Wails：B01a 的宿主只依赖“拥有 parent HWND 的 UI 线程 + 消息循环”，
// 用自建窗口跑同一条代码路径比在测试里拉起一个 Wails 应用更轻，也更能证明
// “宿主不 import Wails”。Wails 环境的集成证据在 cmd/browser-spike -native。
package nativetest

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	tUser32   = windows.NewLazySystemDLL("user32.dll")
	tKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	tRegisterClassExW = tUser32.NewProc("RegisterClassExW")
	tCreateWindowExW  = tUser32.NewProc("CreateWindowExW")
	tDestroyWindow    = tUser32.NewProc("DestroyWindow")
	tDefWindowProcW   = tUser32.NewProc("DefWindowProcW")
	tPeekMessageW     = tUser32.NewProc("PeekMessageW")
	tTranslateMessage = tUser32.NewProc("TranslateMessage")
	tDispatchMessageW = tUser32.NewProc("DispatchMessageW")
	tPostMessageW     = tUser32.NewProc("PostMessageW")
	tGetClientRect    = tUser32.NewProc("GetClientRect")
	tClientToScreen   = tUser32.NewProc("ClientToScreen")
	tGetWindowRect    = tUser32.NewProc("GetWindowRect")
	tIsWindowVisible  = tUser32.NewProc("IsWindowVisible")
	tIsWindow         = tUser32.NewProc("IsWindow")
	tGetModuleHandle  = tKernel32.NewProc("GetModuleHandleW")
)

const (
	tWMAppRun    = 0x8000 + 0x501
	tWSOverlappd = 0x00CF0000
	tWSVisible   = 0x10000000
	tPMRemove    = 0x0001
	tSWShow      = 5
)

type tRect struct{ Left, Top, Right, Bottom int32 }
type tPoint struct{ X, Y int32 }

type tMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      tPoint
}

type tWndClassEx struct {
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

var (
	classOnce sync.Once
	classErr  error

	taskMu   sync.Mutex
	taskNext uintptr
	taskByID = map[uintptr]func(){}
	taskDone = map[uintptr]chan struct{}{}
)

// TestHostWindow 是一个带消息循环的顶层窗口，等价于 Wails 主窗口的角色。
type TestHostWindow struct {
	hwnd     uintptr
	threadID uint32
	quit     atomic.Bool
	done     chan struct{}
}

// NewTestHostWindow 在专用 OS 线程上创建窗口并跑消息循环，直到 Close。
func NewTestHostWindow(title string) (*TestHostWindow, error) {
	type result struct {
		w   *TestHostWindow
		err error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := registerTestClass(); err != nil {
			ch <- result{err: err}
			return
		}
		hinst, _, _ := tGetModuleHandle.Call(0)
		hwnd, _, e := tCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(mustUTF16("MyAgentNativeTestHost"))),
			uintptr(unsafe.Pointer(mustUTF16(title))),
			tWSOverlappd|tWSVisible,
			80, 80, 1180, 820, 0, 0, hinst, 0)
		if hwnd == 0 {
			ch <- result{err: fmt.Errorf("CreateWindowExW 失败: %v", e)}
			return
		}
		w := &TestHostWindow{
			hwnd:     hwnd,
			threadID: mustThreadID(),
			done:     make(chan struct{}),
		}
		tUser32.NewProc("ShowWindow").Call(hwnd, tSWShow)
		ch <- result{w: w}
		w.loop()
	}()
	r := <-ch
	if r.err != nil {
		return nil, r.err
	}
	return r.w, nil
}

// HWND 返回顶层窗口句柄。
func (w *TestHostWindow) HWND() uintptr { return w.hwnd }

// Close 销毁窗口并结束消息循环。
func (w *TestHostWindow) Close() {
	if w == nil {
		return
	}
	w.quit.Store(true)
	// 投递一条消息唤醒 PeekMessage 循环。
	tPostMessageW.Call(w.hwnd, tWMAppRun, 0, 0)
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
	}
}

func (w *TestHostWindow) loop() {
	var m tMsg
	for !w.quit.Load() {
		ok, _, _ := tPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, tPMRemove)
		if ok == 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		tTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		tDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	close(w.done)
	tDestroyWindow.Call(w.hwnd)
}

// CallOnUI 把闭包送到窗口线程执行并等它完成（等价于 production 的 uiThread.invoke）。
func (w *TestHostWindow) CallOnUI(fn func()) error {
	done := make(chan struct{})
	taskMu.Lock()
	taskNext++
	id := taskNext
	taskByID[id] = func() {
		defer close(done)
		fn()
	}
	taskMu.Unlock()

	ok, _, e := tPostMessageW.Call(w.hwnd, tWMAppRun, id, 0)
	if ok == 0 {
		taskMu.Lock()
		delete(taskByID, id)
		taskMu.Unlock()
		return fmt.Errorf("PostMessage 失败: %v", e)
	}
	select {
	case <-done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("UI 任务 15s 未完成")
	}
}

// ClientOrigin 返回客户区左上角的屏幕坐标（Present 的坐标基准）。
func (w *TestHostWindow) ClientOrigin() (int32, int32) {
	p := tPoint{}
	tClientToScreen.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	return p.X, p.Y
}

func testWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if uint32(msg) == tWMAppRun {
		if wp != 0 {
			taskMu.Lock()
			fn := taskByID[wp]
			delete(taskByID, wp)
			taskMu.Unlock()
			if fn != nil {
				fn()
			}
		}
		return 0
	}
	r, _, _ := tDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func registerTestClass() error {
	classOnce.Do(func() {
		hinst, _, _ := tGetModuleHandle.Call(0)
		cls := tWndClassEx{
			lpfnWndProc:   windows.NewCallback(testWndProc),
			hInstance:     windows.Handle(hinst),
			lpszClassName: mustUTF16("MyAgentNativeTestHost"),
		}
		cls.cbSize = uint32(unsafe.Sizeof(cls))
		atom, _, e := tRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))
		if atom == 0 {
			if errno, ok := e.(windows.Errno); !ok || errno != 1410 {
				classErr = fmt.Errorf("RegisterClassExW 失败: %v", e)
			}
		}
	})
	return classErr
}

func mustUTF16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func mustThreadID() uint32 {
	id, _, _ := tKernel32.NewProc("GetCurrentThreadId").Call()
	return uint32(id)
}

// WindowRectOf 返回屏幕坐标矩形（测试断言用）。
func WindowRectOf(hwnd uintptr) tRect {
	var r tRect
	tGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// WindowVisible 是 IsWindowVisible 的直读。
func WindowVisible(hwnd uintptr) bool {
	v, _, _ := tIsWindowVisible.Call(hwnd)
	return v != 0
}

// WindowExists 是 IsWindow 的直读。
func WindowExists(hwnd uintptr) bool {
	v, _, _ := tIsWindow.Call(hwnd)
	return v != 0
}

// ClientSize 返回客户区宽高（物理像素）。
func ClientSize(hwnd uintptr) (int32, int32) {
	var r tRect
	tGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r.Right - r.Left, r.Bottom - r.Top
}

// RootAncestor 返回 GA_ROOT（顶层祖先）——用来证明原生视图不是独立顶层窗口。
func RootAncestor(hwnd uintptr) uintptr {
	const gaRoot = 2
	r, _, _ := tUser32.NewProc("GetAncestor").Call(hwnd, gaRoot)
	return r
}

// ClassNameOf 返回窗口类名。
func ClassNameOf(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := tUser32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}
