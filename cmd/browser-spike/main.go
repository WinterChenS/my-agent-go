// Command browser-spike 是 B00/B01a 的最小 Wails 宿主 spike。
//
// B00 模式（默认）要证明“右侧还没有原生视图”：真实 Wails v3 窗口，主窗口里预留
// 右侧 slot，另有一个应用内 dialog；BrowserSurface/BrowserBackend 用占位实现，
// Attach/Present/CreateTab… 必然失败，且失败是显式的 NOT_IMPLEMENTED。
//
// B01a 模式（-native）换成 internal/browser/native 的真实 Windows 宿主：在主窗口
// 下挂一个 WS_CHILD 子窗口，嵌真实 WebView2，把 fixture 页面加载进右栏。
// 只有三件事都成立才判 PASS：GetParent(child)==main、导航完成、CDP 截图非纯色。
//
// 刻意不做的事：不用 iframe、不弹独立顶层窗口、不把 fixture 页面塞进主 WebView
// 冒充右侧网页（R06 明确排除这三种）。
//
// 退出码：0 = 本次模式的预期结论成立；
//
//	1 = 出现“静默成功”、非预期错误，或 B01a 三条硬条件有任一不成立。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"my-agent-go/internal/browser"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

const (
	// spikeWindowTitle 同时是截图脚本定位窗口的锚点。
	spikeWindowTitle = "MyAgent Browser Spike (B00)"
	// nativeWindowTitle 是 B01a 模式的窗口标题（同样给截图脚本用）。
	nativeWindowTitle = "MyAgent Browser Spike (B01a native)"
	// slotID 与壳页面里 #browser-slot 的 data-slot-id 一致。
	slotID = "browser-slot"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("spike| ")

	nativeMode := flag.Bool("native", envBool("MYAGENT_SPIKE_NATIVE"),
		"用 B01a 的 Windows 原生 WebView2 宿主把 fixture 加载进右栏")
	evidenceDir := flag.String("evidence-dir", ".tmp/b01-native",
		"B01a 模式写截图与 PageState 的目录（默认为 .tmp，不入库）")
	flag.Parse()

	fixture, err := browserfixture.New()
	if err != nil {
		log.Fatalf("启动双 origin fixture: %v", err)
	}
	fmt.Printf("B00|fixture|primary=%s\n", fixture.Primary.URL)
	fmt.Printf("B00|fixture|secondary=%s\n", fixture.Secondary.URL)

	state := &spikeState{verdict: "PENDING", nativeMode: *nativeMode}
	shell, err := newShellServer(fixture, state)
	if err != nil {
		_ = fixture.Close()
		log.Fatalf("启动壳页面服务: %v", err)
	}
	fmt.Printf("B00|shell|url=%s\n", shell.URL())
	fmt.Printf("B00|env|goos=%s goarch=%s webview2=%q native=%t\n",
		runtime.GOOS, runtime.GOARCH, webView2RuntimeVersion(), *nativeMode)

	title := spikeWindowTitle
	if *nativeMode {
		title = nativeWindowTitle
	}

	a := application.New(application.Options{
		Name:        "MyAgent Browser Spike",
		Description: "B00/B01a: 右侧原生浏览器视图与初始失败记录",
		Windows:     application.WindowsOptions{},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	win := a.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "browser-spike",
		Title:            title,
		Width:            1180,
		Height:           820,
		MinWidth:         900,
		MinHeight:        600,
		URL:              shell.URL(),
		DevToolsEnabled:  true,
		BackgroundColour: application.NewRGB(15, 18, 24),
	})

	a.OnShutdown(func() {
		state.setVerdict(state.computeVerdict())
		_ = shell.Close()
		_ = fixture.Close()
		fmt.Printf("B00|shutdown|verdict=%s\n", state.Verdict())
	})

	// 探针必须在窗口出现之后跑：没有原生父窗口就谈不上“挂不进去”。
	if *nativeMode {
		go runNativeProbe(state, win, shell, fixture, *evidenceDir)
	} else {
		go runProbe(state, win)
	}

	// 自动化证据用的自退出。默认不启用，人工观察时窗口一直留着。
	if sec := autoQuitSeconds(); sec > 0 {
		fmt.Printf("B00|autoquit|after=%ds\n", sec)
		go func() {
			time.Sleep(time.Duration(sec) * time.Second)
			a.Quit()
		}()
	}

	if err := a.Run(); err != nil {
		log.Fatalf("应用退出: %v", err)
	}

	verdict := state.Verdict()
	fmt.Printf("B00|summary|verdict=%s\n", verdict)
	expected := verdictAsExpected
	if *nativeMode {
		expected = verdictNativePresent
	}
	if verdict != expected {
		os.Exit(1)
	}
}

// envBool 把 MYAGENT_SPIKE_* 这类开关读成布尔值。
func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// runProbe 依次触达右侧视图相关的每个入口，并核对失败是否"按预期失败"。
func runProbe(state *spikeState, win *application.WebviewWindow) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle := waitForNativeHandle(ctx, win)
	state.note("window.NativeWindow()", func() probeStep {
		if handle == nil {
			return probeStep{
				Want: "非空原生窗口句柄",
				Got:  "nil（窗口还没创建出来或已销毁）",
				Code: "NO_NATIVE_HANDLE",
			}
		}
		return probeStep{
			Want: "非空原生窗口句柄",
			Got:  "非空句柄（已封装进 browser.NativeParent，不进 JSON）",
			Code: "",
		}
	}())

	parent := browser.NewNativeParent(handle, "wails-main-window")
	parentStep := probeStep{
		Want: "valid=true 且不可序列化",
		Got:  parent.String(),
	}
	if !parent.Valid() {
		parentStep.Code = "NO_NATIVE_HANDLE"
	}
	state.note("browser.NativeParent", parentStep)

	slot, gotSlot := state.waitSlot(ctx, 10*time.Second)
	state.note("页面上报 SlotGeometry", func() probeStep {
		if !gotSlot {
			return probeStep{
				Want: "壳页面 POST /slot 的实测几何",
				Got:  "未收到（用合成几何继续，Present 依然会失败）",
				Code: "NO_SLOT_REPORT",
			}
		}
		return probeStep{
			Want: "壳页面 POST /slot 的实测几何",
			Got: fmt.Sprintf("slot=%s rev=%d left=%.1f top=%.1f w=%.1f h=%.1f visible=%t zoom=%.2f dpr=%.2f",
				slot.SlotID, slot.Revision, slot.Left, slot.Top, slot.Width, slot.Height,
				slot.Visible, slot.Zoom, slot.DevicePixelRatio),
			Code: "",
		}
	}())

	geometry := slot.geometry()
	if !gotSlot {
		geometry = browser.SlotGeometry{
			SlotID: slotID, Revision: 1, Left: 740, Top: 0, Width: 440, Height: 760,
			Visible: true, Zoom: 1,
		}
	}

	// 这里开始是 B00 的"初始失败"正题：每个调用都必须返回 NOT_IMPLEMENTED。
	surface := browser.UnimplementedSurface{
		Reason: "B00 只做契约：没有 WebView2/WKWebView/WebKitGTK 子视图",
	}
	state.expectNotImplemented("surface.Attach(parent)", surface.Attach(ctx, parent))
	state.expectNotImplemented("surface.Present(tab, slot)", surface.Present(ctx, "tab-b00", geometry))
	state.expectNotImplemented("surface.SetVisible(true)", surface.SetVisible(ctx, true))
	state.expectNotImplemented("surface.Detach()", surface.Detach(ctx))

	backend := browser.UnimplementedBackend{
		Reason: "B00 只做契约：没有任何平台的 native backend",
	}
	caps := backend.Capabilities()
	state.note("backend.Capabilities()", probeStep{
		Want: "navigate/observe/act/screenshot 全为 false 并逐项给原因",
		Got: fmt.Sprintf("navigate=%t observe=%t act=%t screenshot=%t unsupported=%d 项",
			caps.Navigate, caps.Observe, caps.Act, caps.Screenshot, len(caps.Unsupported)),
		Code: "",
	})
	if caps.Navigate || caps.Observe || caps.Act || caps.Screenshot {
		state.markUnexpected("backend.Capabilities()", "未实现却报告了可用能力")
	}

	_, errTab := backend.CreateTab(ctx, browser.TabOptions{URL: "about:blank"})
	state.expectNotImplemented("backend.CreateTab()", errTab)
	state.expectNotImplemented("backend.Navigate()", backend.Navigate(ctx, "tab-b00", "about:blank"))
	_, errObserve := backend.Observe(ctx, "tab-b00", browser.ObserveOptions{})
	state.expectNotImplemented("backend.Observe()", errObserve)
	_, errAct := backend.Act(ctx, "tab-b00", browser.Action{Kind: browser.ActionClick, SnapshotID: "snap-b00", Ref: "e1"})
	state.expectNotImplemented("backend.Act()", errAct)
	_, errShot := backend.Screenshot(ctx, "tab-b00")
	state.expectNotImplemented("backend.Screenshot()", errShot)
	state.expectNotImplemented("backend.CloseTab()", backend.CloseTab(ctx, "tab-b00"))

	state.setVerdict(state.computeVerdict())
	fmt.Printf("B00|probe|done|verdict=%s\n", state.Verdict())
}

func waitForNativeHandle(ctx context.Context, win *application.WebviewWindow) any {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if h := win.NativeWindow(); h != nil {
			return h
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func autoQuitSeconds() int {
	raw := strings.TrimSpace(os.Getenv("MYAGENT_SPIKE_AUTOQUIT_SECONDS"))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
