//go:build windows

package nativetest

// B01b 的环境探针：进程 working set / 句柄数 / 本进程创建的 msedgewebview2 进程数。
//
// 都是“宿主进程之外”的客观数据，不依赖页面自报，所以可以拿来判 BR06 的
// “关闭后无遗留”。实现只用 Win32 + PowerShell（本机 wmic 被安全策略拦住，
// AGENTS.md 已记录，所以走 Get-CimInstance）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	tPsapi        = windows.NewLazySystemDLL("psapi.dll")
	tGetProcMem   = tPsapi.NewProc("GetProcessMemoryInfo")
	tGetHandleCnt = tKernel32.NewProc("GetProcessHandleCount")
)

// processMemoryCounters 是 PROCESS_MEMORY_COUNTERS（x64 布局）。
type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// ProcessMemory 是本进程的实测内存/句柄读数。
type ProcessMemory struct {
	WorkingSetBytes uint64
	PeakWorkingSet  uint64
	PagefileBytes   uint64
	HandleCount     uint32
}

// ReadProcessMemory 用 GetProcessMemoryInfo + GetProcessHandleCount 读本进程读数。
func ReadProcessMemory(t interface{ Logf(string, ...any) }) ProcessMemory {
	var out ProcessMemory
	proc, err := windows.GetCurrentProcess()
	if err != nil {
		t.Logf("GetCurrentProcess 失败: %v", err)
		return out
	}
	var c processMemoryCounters
	c.Cb = uint32(unsafe.Sizeof(c))
	ok, _, e := tGetProcMem.Call(uintptr(proc), uintptr(unsafe.Pointer(&c)), uintptr(c.Cb))
	if ok == 0 {
		t.Logf("GetProcessMemoryInfo 失败: %v", e)
		return out
	}
	out.WorkingSetBytes = uint64(c.WorkingSetSize)
	out.PeakWorkingSet = uint64(c.PeakWorkingSetSize)
	out.PagefileBytes = uint64(c.PagefileUsage)
	var handles uint32
	if ok, _, _ := tGetHandleCnt.Call(uintptr(proc), uintptr(unsafe.Pointer(&handles))); ok != 0 {
		out.HandleCount = handles
	}
	return out
}

// MB 把字节数换成 MB（保留一位小数由调用方格式化）。
func (p ProcessMemory) MB() float64 {
	return float64(p.WorkingSetBytes) / (1 << 20)
}

// WebView2ProcessStats 是按 profile 目录过滤出来的浏览器进程读数。
type WebView2ProcessStats struct {
	Count          int
	TotalWorkSetMB float64
	Lines          []string
}

// CountWebView2Processes 统计命令行里含 filter 的 msedgewebview2.exe。
//
// filter 用我们的 profile 目录前缀（`myagent-native-profile-` / `b01b-native`），
// 这样只会数到本用例创建的浏览器进程；机器上其它应用的 WebView2 不碰也不数。
func CountWebView2Processes(filter string) (WebView2ProcessStats, error) {
	script := fmt.Sprintf(
		`$p = @(Get-CimInstance Win32_Process -Filter "Name='msedgewebview2.exe'" | Where-Object { $_.CommandLine -like '*%s*' });`+
			`"count=$($p.Count)";`+
			`$sum=0; foreach ($x in $p) { $sum += $x.WorkingSetSize; "pid=$($x.ProcessId) ppid=$($x.ParentProcessId) wsMB=$([math]::Round($x.WorkingSetSize/1MB,1))" };`+
			`"totalMB=$([math]::Round($sum/1MB,1))"`, filter)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return WebView2ProcessStats{}, fmt.Errorf("powershell 查询 msedgewebview2 失败: %v (%s)", err, truncStr(string(raw), 200))
	}
	stats := WebView2ProcessStats{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		stats.Lines = append(stats.Lines, line)
		switch {
		case strings.HasPrefix(line, "count="):
			fmt.Sscanf(line, "count=%d", &stats.Count)
		case strings.HasPrefix(line, "totalMB="):
			fmt.Sscanf(line, "totalMB=%f", &stats.TotalWorkSetMB)
		}
	}
	return stats, nil
}

// CountWindowsOfClass 数**本进程**窗口树里类名为 name 的窗口（顶层 + 所有后代）。
//
// 用来判“子窗口真的被销毁了”：句柄表里留着 DestroyWindow 过的 HWND 不算数，
// 必须窗口类实例本身消失。用 GetWindow(GW_CHILD/GW_HWNDNEXT) 自己遍历，
// 避免 EnumWindows 只能看到顶层窗口的局限。
func CountWindowsOfClass(name string) int {
	count := 0
	for top := topWindow(0); top != 0; top = nextWindow(top) {
		if !windowInProcess(top) {
			continue
		}
		count += countClassInTree(top, name, 0)
	}
	return count
}

// countClassInTree 深度优先数类名匹配（depth=0 表示只数后代，不含 top 自身）。
func countClassInTree(root uintptr, name string, depth int) int {
	count := 0
	if depth > 0 && ClassNameOf(root) == name {
		count++
	}
	for child := childWindow(root); child != 0; child = nextWindow(child) {
		count += countClassInTree(child, name, depth+1)
		if depth > 64 { // 防御：窗口树不可能这么深
			break
		}
	}
	return count
}

func topWindow(after uintptr) uintptr {
	r, _, _ := tUser32.NewProc("GetTopWindow").Call(after)
	return r
}

func childWindow(parent uintptr) uintptr {
	r, _, _ := tUser32.NewProc("GetWindow").Call(parent, 5) // GW_CHILD
	return r
}

func nextWindow(hwnd uintptr) uintptr {
	r, _, _ := tUser32.NewProc("GetWindow").Call(hwnd, 2) // GW_HWNDNEXT
	return r
}

// windowInProcess 判断窗口属于本进程（GetWindowThreadProcessId 的 pid 出参）。
func windowInProcess(hwnd uintptr) bool {
	var pid uint32
	tUser32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid == uint32(windows.GetCurrentProcessId())
}

// truncStr 是本地的小截断（本文件不能依赖 _test.go 里的辅助函数：
// go vet/go build 会单独编译非测试文件）。
func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// moduleRoot 从当前工作目录向上找 go.mod，返回模块根目录。
//
// 为什么需要：`go test` 把测试二进制的 CWD 设成**包目录**（tests/native），
// 证据路径如果按相对路径直接写，会落到 tests/native/docs/... 里（B01b 踩过）。
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("从 %s 向上找不到 go.mod", dir)
		}
		dir = parent
	}
}

// ShowWindowNative 直接调 ShowWindow（最大化/最小化/恢复）。
func ShowWindowNative(hwnd uintptr, cmd int32) {
	tUser32.NewProc("ShowWindow").Call(hwnd, uintptr(cmd))
}

// WindowIconic 是 IsIconic 直读。
func WindowIconic(hwnd uintptr) bool {
	r, _, _ := tUser32.NewProc("IsIconic").Call(hwnd)
	return r != 0
}

// ProcessCount 数指定映像名的进程数（本机 wmic 被拦，走 Get-CimInstance）。
func ProcessCount(image string) (int, error) {
	script := fmt.Sprintf(`@(Get-CimInstance Win32_Process -Filter "Name='%s'").Count`, image)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("powershell 查询 %s 失败: %v (%s)", image, err, truncStr(string(out), 200))
	}
	clean := strings.TrimSpace(strings.ReplaceAll(string(out), "\r", ""))
	var n int
	if _, err := fmt.Sscanf(clean, "%d", &n); err != nil {
		return 0, fmt.Errorf("解析进程数失败: %q", truncStr(string(out), 120))
	}
	return n, nil
}

// PhysicalClick 是**OS 级**鼠标点击（SetCursorPos + mouse_event），
// 与 CDP 注入完全不同的路径：它会把点击交给屏幕坐标下的真实窗口。
//
// 用来验证“宿主算出来的物理坐标”真的落在那个按钮上——这是缩放用例里唯一
// 独立于 CDP/页面自报的物理测量。
func PhysicalClick(x, y int32) (restore func()) {
	var saved struct{ X, Y int32 }
	gotCursor, _, _ := tUser32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&saved)))
	tUser32.NewProc("SetCursorPos").Call(uintptr(x), uintptr(y))
	const leftDown, leftUp = 0x0002, 0x0004
	tUser32.NewProc("mouse_event").Call(leftDown, 0, 0, 0, 0)
	tUser32.NewProc("mouse_event").Call(leftUp, 0, 0, 0, 0)
	return func() {
		if gotCursor != 0 {
			tUser32.NewProc("SetCursorPos").Call(uintptr(saved.X), uintptr(saved.Y))
		}
	}
}

// RaiseToFront 把窗口抬到最前并尝试拿到前台（OS 点击的前提）。
//
// 返回是否真的成为前台窗口，以及一个“还原 z 序”的函数（调用方负责收尾，
// 不给用户留下一扇永远置顶的窗口）。
func RaiseToFront(hwnd uintptr) (bool, func()) {
	const (
		hwndTopmost    = ^uintptr(0) // -1
		hwndNotTopmost = ^uintptr(1) // -2
		swpNoSize      = 0x0001
		swpNoMove      = 0x0002
		swpShowWindow  = 0x0040
	)
	tUser32.NewProc("SetWindowPos").Call(hwnd, hwndTopmost, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpShowWindow)
	tUser32.NewProc("ShowWindow").Call(hwnd, 9) // SW_RESTORE
	tUser32.NewProc("SetForegroundWindow").Call(hwnd)
	time.Sleep(250 * time.Millisecond)
	fg, _, _ := tUser32.NewProc("GetForegroundWindow").Call()
	return fg == hwnd, func() {
		tUser32.NewProc("SetWindowPos").Call(hwnd, hwndNotTopmost, 0, 0, 0, 0,
			swpNoMove|swpNoSize|swpShowWindow)
	}
}

// WindowAt 返回屏幕坐标处的最上层窗口（WindowFromPoint）。
func WindowAt(x, y int32) uintptr {
	pt := struct{ X, Y int32 }{x, y}
	v := *(*uint64)(unsafe.Pointer(&pt))
	r, _, _ := tUser32.NewProc("WindowFromPoint").Call(uintptr(v))
	return r
}

// GetParentHWND 是 GetParent 的直读（销毁后的句柄应返回 0）。
func GetParentHWND(hwnd uintptr) uintptr {
	r, _, _ := tUser32.NewProc("GetParent").Call(hwnd)
	return r
}

// IsDescendantOf 判断 hwnd 是否是 ancestor 的后代（含自身）。
func IsDescendantOf(hwnd, ancestor uintptr) bool {
	for h := hwnd; h != 0; h = GetParentHWND(h) {
		if h == ancestor {
			return true
		}
	}
	return false
}

// SetFocusHWND 把键盘焦点交给指定窗口（模拟“用户点回主界面”）。
func SetFocusHWND(hwnd uintptr) bool {
	r, _, _ := tUser32.NewProc("SetFocus").Call(hwnd)
	return r != 0
}
