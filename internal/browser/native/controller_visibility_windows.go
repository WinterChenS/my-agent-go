//go:build windows

package native

// WebView2 controller 的可见性：用 vtable 直调 + 读回，不信依赖库的错误值。
//
// 为什么不能直接调用 chromium.Show()/Hide()：
// github.com/jchv/go-webview2（v0.0.0-20260516…）的 ICoreWebView2Controller.PutIsVisible
// 用 `_, _, err = SyscallN(...)` 拿第三个返回值当错误，然后 `if err != ERROR_SUCCESS`。
// 对 COM 方法来说第三个返回值是**线程 last-error**，不是 HRESULT：HRESULT 在第一个
// 返回值里。于是只要线程上残留了任意一个 last-error（实测 1400 ERROR_INVALID_WINDOW_HANDLE，
// 由前面的窗口操作留下），一个**成功**的 PutIsVisible 也会被报成失败。
//
// 实测（2026-11-06）：BR04 分栏第 9 轮 SetVisible(true) 报
// "ICoreWebView2Controller.PutIsVisible(true) 失败: Invalid window handle"，
// 但那一次 HRESULT 是 S_OK、页面照常渲染。依赖库修好之前，产品不能靠它的返回值判断
// 可见性，否则"隐藏后恢复"这条最普通的交互会随机失败。
//
// 这里是**读回校验**：直调 PutIsVisible 判 FAILED(hr)，再用 GetIsVisible 读回真实状态；
// 读回与期望不一致才报错。不写"忽略错误"的分支——错误还在，只是不再靠错的值判断。

import (
	"syscall"
	"unsafe"

	"my-agent-go/internal/contracts"

	"github.com/jchv/go-webview2/pkg/edge"
)

// ctrlVtblOffsets 是 ICoreWebView2Controller vtable 里我们用到的方法槽位。
//
// 顺序来自 Microsoft 的 ICoreWebView2Controller IDL（IUnknown 三槽之后）：
//
//	3 GetIsVisible, 4 PutIsVisible
//
// 只声明到需要的槽位：多声明一份布局就多一处会和依赖库漂移的地方。
const (
	ctrlSlotGetIsVisible = 3
	ctrlSlotPutIsVisible = 4
)

// ctrlMethod 取出 controller 的某个 vtable 槽位地址。
//
// ICoreWebView2Controller 在本项目使用的绑定里是 `struct{ vtbl *_ICoreWebView2ControllerVtbl }`，
// 即第一个机器字就是 vtable 指针。用 unsafe 读它，避免依赖未导出的字段名。
// 槽位顺序按 IDL 固定，依赖库升级若改了顺序，本文件的测试会立刻失败。
func ctrlMethod(c *edge.ICoreWebView2Controller, slot uintptr) uintptr {
	// 先读出 vtable 指针本身（第一个机器字），再用 unsafe.Add 做槽位偏移：
	// 不用 uintptr 算术转回 unsafe.Pointer，那正是 go vet 会报的危险模式。
	vtbl := *(*unsafe.Pointer)(unsafe.Pointer(c))
	entry := unsafe.Add(vtbl, slot*unsafe.Sizeof(uintptr(0)))
	return *(*uintptr)(entry)
}

// hresultFailed 判断 HRESULT 是否为失败码（最高位为 1）。
func hresultFailed(hr uintptr) bool {
	return int32(hr) < 0
}

// controllerIsVisible 读回 controller 真实的 IsVisible。
func controllerIsVisible(c *edge.ICoreWebView2Controller) (bool, uintptr) {
	var visible int32
	hr, _, _ := syscall.SyscallN(ctrlMethod(c, ctrlSlotGetIsVisible),
		uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&visible)))
	return visible != 0, hr
}

// setControllerVisible 设置 controller 可见性并读回确认。
//
// 返回 error 的含义是"没做到"，不是"依赖库说它失败了"：
//   - PutIsVisible 的 HRESULT 失败 → 报错（带 HRESULT）；
//   - HRESULT 成功但读回状态不一致 → 报错（带期望/实际）。
func setControllerVisible(c *edge.ICoreWebView2Controller, visible bool) error {
	if c == nil {
		return appErr(contracts.CodeInternal, "SetVisible 之前必须先 Attach")
	}
	var arg uintptr
	if visible {
		arg = 1
	}
	hr, _, _ := syscall.SyscallN(ctrlMethod(c, ctrlSlotPutIsVisible),
		uintptr(unsafe.Pointer(c)), arg)
	if hresultFailed(hr) {
		return appErr(contracts.CodeInternal,
			"ICoreWebView2Controller.PutIsVisible(%t) 失败: HRESULT=0x%08x", visible, uint32(hr))
	}
	got, getHR := controllerIsVisible(c)
	if hresultFailed(getHR) {
		return appErr(contracts.CodeInternal,
			"ICoreWebView2Controller.GetIsVisible 失败: HRESULT=0x%08x", uint32(getHR))
	}
	if got != visible {
		return appErr(contracts.CodeInternal,
			"controller 可见性读回不一致：期望 %t，实际 %t", visible, got)
	}
	return nil
}
