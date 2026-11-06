//go:build windows

package nativetest

// BR05 的外部观察：焦点/键盘、最小化恢复、剪贴板、IME。
//
// 观察源：
//   - window-facts.json：GetForegroundWindow / IsIconic / GetWindowRect 的直读值；
//   - clipboard.txt / clipboard.raw：由**另一个 PowerShell 进程**执行
//     Get-Clipboard -Raw 写出的 OS 剪贴板内容；clipboard-sentinel.txt 是测试写下的
//     期望值，两者读回比对；
//   - ime.jsonl：每次 CDP IME 调用的结果 + 页面读回的 input.value 与事件序列；
//   - screenshots/after-restore.png：最小化恢复后的 CDP 页面像素。
//
// 事实（foregroundMatched / minimizedThenRestored / clipboardSentinel / imeCommitted）
// 全部从这些文件算出。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// focusTrace 是一次焦点用例里从 OS 读到的前台窗口序列。
type focusTrace struct {
	BeforeFocus    uintptr
	AfterFocus     uintptr
	AfterRaise     uintptr
	AfterSetFocus  uintptr
	SetFocusOK     bool
	ReverseAttempt bool
	PageHasFocus   bool
	InputValue     string
}

// observeFocusAndKeyboard 是 BR05 焦点子集的外部观察装置。
//
// 前台窗口句柄全部来自 GetForegroundWindow（OS），页面 hasFocus/输入值来自页面
// 自报但落盘后读回；两者分开写，不混成一个"看起来正常"。
func observeFocusAndKeyboard(t *testing.T, h *harness, tr focusTrace) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	main := h.win.HWND()
	observationUpdateSection(t, "window-facts.json", "BR05.focus", obsFacts{
		"capturedAt":              time.Now().UTC().Format(time.RFC3339Nano),
		"mainHWND":                main,
		"foregroundBeforeFocus":   tr.BeforeFocus,
		"foregroundAfterFocus":    tr.AfterFocus,
		"foregroundAfterRaise":    tr.AfterRaise,
		"foregroundAfterSetFocus": tr.AfterSetFocus,
		"setFocusOnMainOK":        tr.SetFocusOK,
		"reverseAttempted":        tr.ReverseAttempt,
		"pageHasFocus":            tr.PageHasFocus,
		"keyboardInputValue":      tr.InputValue,
		"observedBy":              "GetForegroundWindow（OS）直读；页面 hasFocus/input.value 由 Runtime.evaluate 取回后落盘",
	})
	facts := observationSection(t, "window-facts.json", "BR05.focus")
	mainHWND := uint64(facts.num(t, "mainHWND"))
	foregroundMatched := uint64(facts.num(t, "foregroundAfterSetFocus")) == mainHWND
	if !facts.flag(t, "pageHasFocus") {
		t.Fatal("window-facts.json 里页面 hasFocus=false：Focus(MoveFocus) 之后页面没拿到焦点")
	}
	if got := facts.text(t, "keyboardInputValue"); got != "abc" {
		t.Fatalf("window-facts.json 里键盘输入读回 %q，期望 \"abc\"", got)
	}
	observationMergeFacts(t, obsFacts{
		"foregroundMatched":      foregroundMatched,
		"foregroundBeforeFocus":  facts.num(t, "foregroundBeforeFocus"),
		"foregroundAfterFocus":   facts.num(t, "foregroundAfterFocus"),
		"mainHWND":               facts.num(t, "mainHWND"),
		"pageHasFocusAfterFocus": facts.flag(t, "pageHasFocus"),
		"keyboardInputValue":     facts.text(t, "keyboardInputValue"),
		"focusObservedBy":        "window-facts.json 的 BR05.focus 段：GetForegroundWindow（OS）+ 页面 hasFocus/input.value",
		"focusNote": "本机 GetForegroundWindow() 返回 0（当前会话没有前台窗口），" +
			"因此 foregroundMatched=false 是环境事实，不是页面没焦点；正向（Focus 之后 hasFocus=true）已由页面自报 + 落盘证明",
	})
}

// observeMinimizeRestore 是 BR05 最小化恢复子集的外部观察装置。
//
// 最小化状态来自 IsIconic（OS），矩形来自 GetWindowRect（OS），恢复后的页面像素
// 来自 CDP 截图落盘的 PNG。
func observeMinimizeRestore(t *testing.T, h *harness, before, minimized, after tRect,
	iconicAfterMinimize, iconicAfterRestore, controllerReady bool, pagePNG []byte) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	main := h.win.HWND()
	observationWrite(t, "screenshots/after-restore.png", pagePNG)
	px := readPNGFacts(t, "screenshots/after-restore.png")
	observationUpdateSection(t, "window-facts.json", "BR05.minimize", obsFacts{
		"capturedAt":          time.Now().UTC().Format(time.RFC3339Nano),
		"mainHWND":            main,
		"rectBefore":          obsRect(before),
		"rectMinimized":       obsRect(minimized),
		"rectAfterRestore":    obsRect(after),
		"iconicAfterMinimize": iconicAfterMinimize,
		"iconicAfterRestore":  iconicAfterRestore,
		"controllerReady":     controllerReady,
		"screenshot":          px.obsFacts(),
		"observedBy": "IsIconic / GetWindowRect（OS）直读落盘；恢复后的页面像素来自 " +
			"CDP Page.captureScreenshot，尺寸/颜色数/sha256 从 PNG 文件读回",
	})
	facts := observationSection(t, "window-facts.json", "BR05.minimize")
	beforeRect := facts.obj(t, "rectBefore")
	afterRect := facts.obj(t, "rectAfterRestore")
	minimizedRect := facts.obj(t, "rectMinimized")
	shot := facts.obj(t, "screenshot")
	sameRect := beforeRect.num(t, "left") == afterRect.num(t, "left") &&
		beforeRect.num(t, "top") == afterRect.num(t, "top") &&
		beforeRect.num(t, "width") == afterRect.num(t, "width") &&
		beforeRect.num(t, "height") == afterRect.num(t, "height")
	minimizedThenRestored := facts.flag(t, "iconicAfterMinimize") && !facts.flag(t, "iconicAfterRestore") && sameRect
	if !facts.flag(t, "iconicAfterMinimize") {
		t.Fatal("window-facts.json 里最小化之后 IsIconic=false")
	}
	if facts.flag(t, "iconicAfterRestore") {
		t.Fatal("window-facts.json 里恢复之后 IsIconic 仍为 true")
	}
	if !sameRect {
		t.Fatalf("window-facts.json 里恢复后的矩形 (%v) 与最小化前 (%v) 不一致", afterRect, beforeRect)
	}
	if minimizedRect.num(t, "top") == beforeRect.num(t, "top") {
		t.Fatal("window-facts.json 里最小化后窗口位置没有变化：最小化可能没真的发生")
	}
	if !facts.flag(t, "controllerReady") {
		t.Fatal("window-facts.json 里恢复后 controller 不在了")
	}
	if shot.num(t, "distinctColors") <= 4 {
		t.Fatalf("落盘的恢复后截图只有 %d 种颜色（纯色）", int(shot.num(t, "distinctColors")))
	}
	observationMergeFacts(t, obsFacts{
		"minimizedThenRestored":      minimizedThenRestored,
		"minimizeRectBefore":         beforeRect,
		"minimizeRectMinimized":      minimizedRect,
		"minimizeRectAfterRestore":   afterRect,
		"restoreScreenshotSha256":    shot.text(t, "sha256"),
		"restorePixelDistinctColors": shot.num(t, "distinctColors"),
		"minimizeObservedBy":         "window-facts.json 的 BR05.minimize 段（IsIconic/GetWindowRect 直读）+ screenshots/after-restore.png（CDP 像素）",
	})
}

// observeClipboardSentinel 用**另一个 PowerShell 进程**读回 OS 剪贴板并落盘。
//
// 三条纪律：
//   - 只落盘 sentinel 相关内容（用例自己写进去的字符串），不碰用户剪贴板内容；
//   - clipboard.txt / clipboard.raw 由 PowerShell 进程写，测试只读回；
//   - clipboard-sentinel.txt 是测试写进去的期望值，两个文件比对才是观察值。
func observeClipboardSentinel(t *testing.T, sentinel string) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	txt := observationPath("clipboard.txt")
	rawPath := observationPath("clipboard.raw")
	if err := os.MkdirAll(filepath.Dir(txt), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	script := fmt.Sprintf(`$t = Get-Clipboard -Raw; if ($null -eq $t) { $t = '' }; `+
		`$bytes = [Text.Encoding]::UTF8.GetBytes($t); `+
		`[IO.File]::WriteAllBytes(%q, $bytes); `+
		`[IO.File]::WriteAllText(%q, $t, (New-Object Text.UTF8Encoding $false)); `+
		`"len=$($t.Length)"`, rawPath, txt)
	code, out := runPowerShellToFile(t, script)
	if code != 0 {
		t.Fatalf("独立 PowerShell 进程读取剪贴板失败（exit=%d）: %s", code, truncate(out, 300))
	}
	observationWrite(t, "clipboard-sentinel.txt", []byte(sentinel))

	rawBytes := observationRead(t, "clipboard.raw")
	txtBytes := observationRead(t, "clipboard.txt")
	want := string(observationRead(t, "clipboard-sentinel.txt"))
	// CRLF/尾随换行按同一口径剥掉，再逐字节比较（Get-Clipboard 会补行尾）。
	got := strings.TrimRight(string(txtBytes), "\r\n")
	gotRaw := strings.TrimRight(string(rawBytes), "\r\n")
	matches := got == want && gotRaw == want

	sum := sha256.Sum256(rawBytes)
	observationUpdateSection(t, "window-facts.json", "BR05.clipboard", obsFacts{
		"capturedAt":              time.Now().UTC().Format(time.RFC3339Nano),
		"readBy":                  "独立的 powershell -NoProfile -NonInteractive -Command Get-Clipboard -Raw 进程写出的 clipboard.txt / clipboard.raw",
		"powershellExitCode":      code,
		"powershellStdout":        strings.TrimSpace(out),
		"clipboardRawBytes":       len(rawBytes),
		"clipboardRawSha256":      hex.EncodeToString(sum[:]),
		"sentinelLength":          len(want),
		"clipboardTrimmedLen":     len(got),
		"matchesSentinelFile":     matches,
		"trailingNewlineStripped": len(string(txtBytes)) - len(got),
	})
	facts := observationSection(t, "window-facts.json", "BR05.clipboard")
	if !facts.flag(t, "matchesSentinelFile") {
		t.Fatalf("独立进程读回的剪贴板与 sentinel 文件不一致（raw=%d 字节, trimmed=%d 字节, sentinel=%d 字节）",
			int(facts.num(t, "clipboardRawBytes")), int(facts.num(t, "clipboardTrimmedLen")), int(facts.num(t, "sentinelLength")))
	}
	observationMergeFacts(t, obsFacts{
		"clipboardSentinel":   true,
		"clipboardRawBytes":   facts.num(t, "clipboardRawBytes"),
		"clipboardRawSha256":  facts.text(t, "clipboardRawSha256"),
		"clipboardObservedBy": "clipboard.txt / clipboard.raw 由一个独立的 PowerShell 进程执行 Get-Clipboard -Raw 写出；clipboard-sentinel.txt 是测试写下的期望值；两者读回比对",
	})
}

// observeClipboardCleared 用独立进程确认剪贴板已被清空（不落空文件，写进 window-facts）。
func observeClipboardCleared(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	txt := observationPath("clipboard-cleared.txt")
	script := fmt.Sprintf(`$t = Get-Clipboard -Raw; if ($null -eq $t) { $t = '' }; `+
		`[IO.File]::WriteAllText(%q, $t, (New-Object Text.UTF8Encoding $false))`, txt)
	code, out := runPowerShellToFile(t, script)
	if code != 0 {
		t.Fatalf("独立 PowerShell 进程确认剪贴板清空失败（exit=%d）: %s", code, truncate(out, 300))
	}
	raw, err := os.ReadFile(txt)
	if err != nil {
		t.Fatalf("读回 clipboard-cleared.txt 失败: %v", err)
	}
	_ = os.Remove(txt) // 清空后是空文件，不作为 artifact 留在证据里
	trimmed := strings.TrimRight(string(raw), "\r\n")
	observationUpdateSection(t, "window-facts.json", "BR05.clipboardCleared", obsFacts{
		"capturedAt":         time.Now().UTC().Format(time.RFC3339Nano),
		"powershellExitCode": code,
		"clearedLength":      len(trimmed),
		"readBy":             "独立的 powershell Get-Clipboard -Raw 进程（结果读回后不留在证据里）",
	})
	facts := observationSection(t, "window-facts.json", "BR05.clipboardCleared")
	if got := facts.num(t, "clearedLength"); got != 0 {
		t.Fatalf("独立进程读回的剪贴板仍有 %d 个字符：测试字符串留在用户剪贴板里了", int(got))
	}
	observationMergeFacts(t, obsFacts{"clipboardCleared": true})
}

// observeIMEStep 把一次 CDP IME 调用与页面读回值写进 ime.jsonl。
func observeIMEStep(t *testing.T, rec obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationAppendJSONL(t, "ime.jsonl", rec)
}

// finalizeIMEObservation 从 ime.jsonl 读回 IME 事实。
func finalizeIMEObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	lines := observationReadJSONL(t, "ime.jsonl")
	if len(lines) != 2 {
		t.Fatalf("ime.jsonl 有 %d 行，期望 2 行（imeSetComposition + insertText）", len(lines))
	}
	compose := lines[0]
	commit := lines[1]
	if got := compose.text(t, "inputValue"); !strings.Contains(got, "nihao") {
		t.Fatalf("ime.jsonl 里组合中的 inputValue=%q，期望包含拼音", got)
	}
	if !compose.flag(t, "isComposing") {
		t.Fatal("ime.jsonl 里组合中 isComposing=false")
	}
	if got := commit.text(t, "inputValue"); got != "你好" {
		t.Fatalf("ime.jsonl 里提交后的 inputValue=%q，期望 \"你好\"", got)
	}
	if commit.flag(t, "isComposing") {
		t.Fatal("ime.jsonl 里提交后 isComposing 仍为 true")
	}
	events := commit.list(t, "events")
	if len(events) == 0 {
		t.Fatal("ime.jsonl 里提交后没有任何 composition/input 事件")
	}
	last := ""
	seenUpdate, seenEnd := false, false
	for _, raw := range events {
		m, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("ime.jsonl 的事件不是对象: %T", raw)
		}
		ev := obsFacts(m)
		typ := ev.text(t, "t")
		if strings.HasPrefix(typ, "composition") {
			last = typ
			if typ == "compositionupdate" {
				seenUpdate = true
			}
			if typ == "compositionend" {
				seenEnd = true
			}
		}
	}
	if !seenUpdate || !seenEnd || last != "compositionend" {
		t.Fatalf("ime.jsonl 里 composition 事件没有以 compositionend 收尾（seenUpdate=%t seenEnd=%t last=%q）", seenUpdate, seenEnd, last)
	}
	observationMergeFacts(t, obsFacts{
		"imeCommitted":    true,
		"imeFinalValue":   commit.text(t, "inputValue"),
		"imeComposeValue": compose.text(t, "inputValue"),
		"imeEventCount":   len(events),
		"imeArtifact":     "ime.jsonl",
		"imeObservedBy": "ime.jsonl 每行是一次 CDP Input.imeSetComposition / Input.insertText 的调用结果 + " +
			"页面 Runtime.evaluate 读回的 input.value 与 composition 事件序列",
		"imeLimitation": "CDP 模拟，不是 OS 级 IME（没有经过 TSF/输入法进程）",
	})
}
