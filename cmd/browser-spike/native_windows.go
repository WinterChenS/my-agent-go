//go:build windows

package main

// B01a 模式：用 internal/browser/native 的真实 Windows 宿主把 fixture 页面
// 加载进主窗口右栏。
//
// 硬判据只有三条：
//  1. GetParent(child)==main（同一主窗口的子窗口，不是第二个顶层窗口）；
//  2. 导航完成（页面自报 readyState=complete 且标题非空）；
//  3. CDP Page.captureScreenshot 的页面像素非纯色。
//
// 截图、PageState、Capabilities 全部落盘到 evidenceDir，供证据文档逐行引用。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/browser/native"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

func runNativeProbe(state *spikeState, win *application.WebviewWindow, shell *shellServer, fixture *browserfixture.Fixture, evidenceDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fail := state.nativeFail
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		fail(fmt.Sprintf("创建证据目录 %s 失败: %v", evidenceDir, err))
		return
	}

	handle := waitForNativeHandle(ctx, win)
	if handle == nil {
		fail("等不到主窗口句柄（NativeWindow() 一直是 nil）")
		return
	}
	parent := browser.NewNativeParent(handle, "wails-main-window")
	fmt.Printf("B01|parent|%s\n", parent.String())

	events := make(chan native.BrowserEvent, 128)
	host, err := native.NewWindowsHost(parent, native.HostOptions{
		ProfilePath: filepath.Join(evidenceDir, "profile"),
		OnEvent: func(ev native.BrowserEvent) {
			select {
			case events <- ev:
			default:
			}
		},
	})
	if err != nil {
		fail(fmt.Sprintf("native.NewWindowsHost 失败: %v", err))
		return
	}

	// Attach 必须在拥有主窗口 HWND 的 UI 线程上跑：Wails 的 UI 线程就是它。
	var attachErr error
	application.InvokeSync(func() { attachErr = host.Attach(ctx, parent) })
	if attachErr != nil {
		fail(fmt.Sprintf("host.Attach 失败: %v", attachErr))
		return
	}
	info := host.WindowInfo()
	fmt.Printf("B01|hwnd|main=0x%x child=0x%x GetParent(child)=0x%x parentMatches=%t dpi=%d scale=%.2f\n",
		info.ParentHWND, info.ChildHWND, info.ParentOfChild, info.ParentMatches, info.DPI, info.Scale)
	parentMatches := info.ParentMatches
	if !parentMatches {
		fail("GetParent(child) != main：右侧不是同一主窗口的子窗口")
	}
	state.nativeStep("GetParent(child)==main",
		fmt.Sprintf("main=0x%x child=0x%x getParent=0x%x parentMatches=%t",
			info.ParentHWND, info.ChildHWND, info.ParentOfChild, parentMatches), parentMatches)

	// 等壳页面把右栏几何量出来（B00 已通的链路）。
	slot, gotSlot := state.waitSlot(ctx, 15*time.Second)
	if !gotSlot {
		fail("壳页面没有上报 SlotGeometry，无法确定右栏位置")
		return
	}
	fmt.Printf("B01|slot|rev=%d slotId=%q css=%.1f,%.1f %.1fx%.1f dpr=%.2f\n",
		slot.Revision, slot.SlotID, slot.Left, slot.Top, slot.Width, slot.Height, slot.DevicePixelRatio)

	tab, err := host.CreateTab(ctx, browser.TabOptions{URL: fixture.Primary.URL + "/"})
	if err != nil {
		fail(fmt.Sprintf("CreateTab 失败: %v", err))
		return
	}
	fmt.Printf("B01|tab|id=%s url=%s/\n", string(tab), fixture.Primary.URL)

	if err := host.Present(ctx, tab, slot.geometry()); err != nil {
		fail(fmt.Sprintf("Present 失败: %v", err))
		return
	}
	info = host.WindowInfo()
	fmt.Printf("B01|present|childRect=%d,%d-%d,%d webviewChild=0x%x\n",
		info.ChildRect.Left, info.ChildRect.Top, info.ChildRect.Right, info.ChildRect.Bottom,
		info.WebViewChildHWND)
	// z 序证据：主窗口的直接子窗口从上到下排列。原生子窗口必须在主 WebView 之上。
	for i, w := range describeChildren(info.ParentHWND) {
		fmt.Printf("B01|zorder|%d|%s\n", i, w)
	}

	// 硬判据 2：导航完成 = 页面自报 readyState=complete 且标题非空。
	pageState, err := waitForReady(ctx, host, tab, 30*time.Second)
	if err != nil {
		fail(fmt.Sprintf("等页面 readyState=complete 失败: %v", err))
		return
	}
	navigationOK := pageState.ReadyState == "complete" && strings.TrimSpace(pageState.Title) != ""
	fmt.Printf("B01|page|readyState=%q title=%q url=%s\n",
		pageState.ReadyState, pageState.Title, pageState.URL)
	counters, _ := json.Marshal(pageState.Counters)
	fmt.Printf("B01|page|fixtureCountersInPage=%s\n", counters)
	if !navigationOK {
		fail("导航没有完成（readyState/title 不符合）")
	}
	state.nativeStep("navigationCompleted",
		fmt.Sprintf("readyState=%q title=%q url=%s", pageState.ReadyState, pageState.Title, pageState.URL), navigationOK)

	// 观察：隔离世界里的有界元素表。
	snap, err := host.Observe(ctx, tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		fail(fmt.Sprintf("Observe 失败: %v", err))
		return
	}
	fmt.Printf("B01|observe|snapshot=%s nav=%s elements=%d textChars=%d viewport=%.0fx%.0f scale=%.2f unsupportedFrames=%d\n",
		snap.ID, snap.NavigationID, len(snap.Elements), len([]rune(snap.Text)),
		snap.Viewport.Width, snap.Viewport.Height, snap.Viewport.Scale, len(snap.UnsupportedFrames))

	// 硬判据 3：CDP 页面截图非纯色。
	img, err := host.Screenshot(ctx, tab)
	if err != nil {
		fail(fmt.Sprintf("Screenshot 失败: %v", err))
		return
	}
	shotPath := filepath.Join(evidenceDir, "b01-native-right-pane.png")
	if err := os.WriteFile(shotPath, img.Bytes, 0o644); err != nil {
		fail(fmt.Sprintf("写截图 %s 失败: %v", shotPath, err))
		return
	}
	decoded, err := png.Decode(bytes.NewReader(img.Bytes))
	if err != nil {
		fail(fmt.Sprintf("解码 CDP 截图失败: %v", err))
		return
	}
	shotStats := statsOfImage(decoded)
	fmt.Printf("B01|screenshot|path=%s bytes=%d pixel=%dx%d css=%.0fx%.0f scale=%.2f nav=%s stats={%s}\n",
		shotPath, len(img.Bytes), img.PixelWidth, img.PixelHeight, img.CSSWidth, img.CSSHeight,
		img.Scale, img.NavigationID, shotStats.String())
	screenshotOK := !shotStats.Uniform()
	if !screenshotOK {
		fail("CDP 截图是纯色：右栏没有真实页面像素")
	}
	state.nativeStep("cdpScreenshotNonUniform", shotStats.String(), screenshotOK)
	// 三条硬条件都过了就先把结论写进 /probe.json，让截图上的 verdict 与日志一致。
	if parentMatches && navigationOK && screenshotOK {
		state.setVerdict(verdictNativePresent)
	}

	// 动作验证 1：点“普通表单”链接并核验 url_equals。这不是硬判据，
	// 但它是 B01a 唯一能拿出的“可信输入 + 后置条件核验”活证据。
	clickResult := actClick(snap, fixture.Primary.URL+"/form")
	if clickResult != nil {
		res, err := host.Act(ctx, tab, *clickResult)
		if err != nil {
			fmt.Printf("B01|act|click|error=%v\n", err)
		} else {
			fmt.Printf("B01|act|click|delivered=%t verified=%t status=%s detail=%s\n",
				res.Delivered, res.Verified, res.Verification.Status, res.Verification.Detail)
		}
		if _, err := waitForReady(ctx, host, tab, 20*time.Second); err != nil {
			fmt.Printf("B01|act|click|navigateWaitError=%v\n", err)
		}
		// 动作验证 2：在 /form 里填姓名并核验 field_value。
		if snap2, err := host.Observe(ctx, tab, browser.ObserveOptions{IncludeText: true}); err == nil {
			if fill := actFill(snap2, "B01a-fill"); fill != nil {
				res, err := host.Act(ctx, tab, *fill)
				if err != nil {
					fmt.Printf("B01|act|fill|error=%v\n", err)
				} else {
					fmt.Printf("B01|act|fill|delivered=%t verified=%t status=%s detail=%s\n",
						res.Delivered, res.Verified, res.Verification.Status, res.Verification.Detail)
				}
			}
		}
	}

	// 能力：本实例实际成功过才为 true。
	caps := host.Capabilities()
	fmt.Printf("B01|capabilities|navigate=%t observe=%t act=%t screenshot=%t unsupported=%d\n",
		caps.Navigate, caps.Observe, caps.Act, caps.Screenshot, len(caps.Unsupported))
	for _, u := range caps.Unsupported {
		fmt.Printf("B01|capabilities|unsupported|%s|%s\n", u.Capability, u.Reason)
	}

	// 窗口级截图（辅助证据）。两条路都跑，并如实报告它们各自拍到了什么：
	//   - screen：屏幕 BitBlt，包含子窗口内容（能看到右栏的网页），前提是窗口没被盖住；
	//   - print：PrintWindow，包含不了子窗口内容（右栏仍是主 WebView 的占位 UI）。
	slotShot := captureBoth(info.ParentHWND, evidenceDir, "b01-native-window-slot", "b01-native-window-printwindow")
	state.nativeStep("windowSlotCapture", slotShot, !strings.Contains(slotShot, "screen-error"))

	if err := host.SetVisible(ctx, false); err != nil {
		fail(fmt.Sprintf("SetVisible(false) 失败: %v", err))
	} else {
		fmt.Printf("B01|occlusion|nativeVisible=false childVisible=%t\n", windowVisible(host.WindowInfo().ChildHWND))
	}
	shell.state.requestDialog()
	dialogDeadline := time.Now().Add(8 * time.Second)
	for !state.PageDialogOpen() && time.Now().Before(dialogDeadline) {
		time.Sleep(150 * time.Millisecond)
	}
	fmt.Printf("B01|occlusion|appDialogOpen=%t\n", state.PageDialogOpen())
	time.Sleep(500 * time.Millisecond)
	_ = captureBoth(info.ParentHWND, evidenceDir, "b01-native-dialog-occlusion", "b01-native-dialog-occlusion-print")
	// 恢复：隐藏不是销毁，tab 状态必须还在。
	if err := host.SetVisible(ctx, true); err != nil {
		fmt.Printf("B01|occlusion|restoreError=%v\n", err)
	}

	// 清理并复核“没有残留句柄”。
	if err := host.CloseTab(ctx, tab); err != nil {
		fmt.Printf("B01|cleanup|closeTabError=%v\n", err)
	}
	afterClose := host.WindowInfo()
	fmt.Printf("B01|cleanup|afterCloseTab child=0x%x\n", afterClose.ChildHWND)
	if err := host.Detach(ctx); err != nil {
		fmt.Printf("B01|cleanup|detachError=%v\n", err)
	}
	if err := host.Detach(ctx); err != nil {
		fmt.Printf("B01|cleanup|detachTwiceError=%v\n", err)
	}
	afterDetach := host.WindowInfo()
	fmt.Printf("B01|cleanup|afterDetach parent=0x%x child=0x%x controllerReady=%t\n",
		afterDetach.ParentHWND, afterDetach.ChildHWND, afterDetach.ControllerReady)

	if len(state.NativeFailures()) == 0 {
		state.setNativePass()
	}
	summary := map[string]any{
		"parentMatches":   parentMatches,
		"navigationOK":    navigationOK,
		"pageTitle":       pageState.Title,
		"pageReadyState":  pageState.ReadyState,
		"pageURL":         pageState.URL,
		"counters":        pageState.Counters,
		"screenshotBytes": len(img.Bytes),
		"screenshotStats": shotStats,
		"capabilities":    caps,
		"failures":        state.NativeFailures(),
	}
	raw, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(filepath.Join(evidenceDir, "b01-native-summary.json"), raw, 0o644)
	fmt.Printf("B01|probe|done|verdict=%s\n", state.computeVerdict())
	fmt.Printf("B01|probe|done|navigationCompleted=%t screenshotNonUniform=%t\n", navigationOK, screenshotOK)
}

// waitForReady 轮询 PageState 直到 readyState=complete 且标题非空。
func waitForReady(ctx context.Context, host native.NativeHost, tab browser.TabID, timeout time.Duration) (native.PageState, error) {
	deadline := time.Now().Add(timeout)
	var last native.PageState
	for time.Now().Before(deadline) {
		ps, err := host.PageState(ctx, tab)
		if err == nil {
			last = ps
			if ps.ReadyState == "complete" && strings.TrimSpace(ps.Title) != "" {
				return ps, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return last, fmt.Errorf("超时 %s 后 readyState=%q title=%q", timeout, last.ReadyState, last.Title)
}

// actClick 在快照里找“去 /form 的链接”并构造带 url_equals 的点击动作。
func actClick(snap browser.Snapshot, wantURL string) *browser.Action {
	for _, el := range snap.Elements {
		if el.Tag != "a" {
			continue
		}
		if !strings.Contains(el.Name, "普通表单") && !strings.Contains(strings.ToLower(el.Name), "form") {
			continue
		}
		return &browser.Action{
			Kind:       browser.ActionClick,
			SnapshotID: snap.ID,
			Ref:        el.Ref,
			ExpectedCondition: browser.ExpectedCondition{
				Kind:      browser.ConditionURLEquals,
				URL:       wantURL,
				TimeoutMS: 8000,
			},
		}
	}
	return nil
}

// actFill 找第一个文本输入框并填入给定文本，核验 field_value。
//
// 选择依据是元素的 name/placeholder/id 属性（fixture 里是 name="name"），
// 不靠可见文字——input 的 innerText 本来就是空的。
func actFill(snap browser.Snapshot, text string) *browser.Action {
	for _, el := range snap.Elements {
		if el.Tag != "input" || (el.InputType != "text" && el.InputType != "") {
			continue
		}
		if el.Ref == "" {
			continue
		}
		return &browser.Action{
			Kind:       browser.ActionFill,
			SnapshotID: snap.ID,
			Ref:        el.Ref,
			Text:       text,
			ExpectedCondition: browser.ExpectedCondition{
				Kind:      browser.ConditionFieldValue,
				Ref:       el.Ref,
				Value:     text,
				TimeoutMS: 5000,
			},
		}
	}
	return nil
}

// captureBoth 同时跑屏幕抓取与 PrintWindow，并写两份文件。
//
// 返回一句可复核的说明（哪个方法成功、各自像素统计），供 stdout 与 /probe.json 引用。
func captureBoth(hwnd uintptr, evidenceDir, screenName, printName string) string {
	var parts []string
	if wImg, method, err := captureWindow(hwnd, "screen"); err != nil {
		parts = append(parts, "screen-error="+err.Error())
	} else {
		st := statsOfImage(wImg)
		screenPath := filepath.Join(evidenceDir, screenName+".png")
		_ = savePNG(screenPath, wImg)
		parts = append(parts, fmt.Sprintf("%s path=%s stats={%s}", method, screenPath, st.String()))
		fmt.Printf("B01|windowcapture|method=%s path=%s stats={%s}\n", method, screenPath, st.String())
	}
	if wImg, method, err := captureWindow(hwnd, "print"); err != nil {
		parts = append(parts, "print-error="+err.Error())
	} else {
		st := statsOfImage(wImg)
		printPath := filepath.Join(evidenceDir, printName+".png")
		_ = savePNG(printPath, wImg)
		parts = append(parts, fmt.Sprintf("%s path=%s stats={%s}", method, printPath, st.String()))
		fmt.Printf("B01|windowcapture|method=%s path=%s stats={%s}\n", method, printPath, st.String())
	}
	return strings.Join(parts, " | ")
}
