//go:build windows

package nativetest

// BR01 的外部观察：右侧是主窗口的**真实 WebView2 子窗口**。
//
// 观察源两路，都在测试进程之外或由 OS 直接给出：
//   - window-facts.json：Win32 GetParent/GetAncestor/GetWindowRect/GetClientRect/
//     GetDpiForWindow/WindowFromPoint 的直读值，落盘后读回；
//   - screenshots/right-pane.png：CDP Page.captureScreenshot 的原始字节，尺寸/
//     颜色数/sha256 从 PNG 文件读回。
//
// 事实（childIsNotTopLevel / childParentIsMain / rootIsMain / pagePixelDistinctColors /
// screenshotSha256）全部由这两个文件算出，不读断言变量。

import (
	"testing"
	"time"
)

// observeRightPane 是 BR01 的外部观察装置。
func observeRightPane(t *testing.T, h *harness, pagePNG []byte) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	info := h.host.WindowInfo()
	main, child := info.ParentHWND, info.ChildHWND
	childRect := WindowRectOf(child)
	mainRect := WindowRectOf(main)
	childCW, childCH := ClientSize(child)
	mainCW, mainCH := ClientSize(main)
	centerX := (childRect.Left + childRect.Right) / 2
	centerY := (childRect.Top + childRect.Bottom) / 2
	atChildCenter := WindowAt(centerX, centerY)
	ox, oy := h.win.ClientOrigin()
	// 主窗口客户区左上角一点：在右栏之外，应当落在主窗口自己的窗口树里。
	atMainArea := WindowAt(ox+5, oy+5)

	observationUpdateSection(t, "window-facts.json", "BR01.right-pane", obsFacts{
		"capturedAt":                            time.Now().UTC().Format(time.RFC3339Nano),
		"mainHWND":                              main,
		"childHWND":                             child,
		"childClassName":                        ClassNameOf(child),
		"parentOfChild":                         GetParentHWND(child),
		"rootAncestorOfChild":                   RootAncestor(child),
		"childDescendantOfMain":                 IsDescendantOf(child, main),
		"childIsTopLevel":                       RootAncestor(child) == child,
		"childWindowRect":                       obsRect(childRect),
		"mainWindowRect":                        obsRect(mainRect),
		"childClientSize":                       obsFacts{"width": childCW, "height": childCH},
		"mainClientSize":                        obsFacts{"width": mainCW, "height": mainCH},
		"dpiForWindowChild":                     dpiForWindow(child),
		"dpiForWindowMain":                      dpiForWindow(main),
		"hostReportedDPI":                       info.DPI,
		"hostReportedScale":                     info.Scale,
		"childIsVisible":                        WindowVisible(child),
		"mainIsVisible":                         WindowVisible(main),
		"windowFromPointChildCenter":            atChildCenter,
		"windowFromPointChildCenterInChildTree": IsDescendantOf(atChildCenter, child),
		"windowFromPointMainArea":               atMainArea,
		"windowFromPointMainAreaInMainTree":     IsDescendantOf(atMainArea, main),
		"childInsideMainRect": childRect.Left >= mainRect.Left && childRect.Top >= mainRect.Top &&
			childRect.Right <= mainRect.Right && childRect.Bottom <= mainRect.Bottom,
	})
	observationWrite(t, "screenshots/right-pane.png", pagePNG)

	// ── 从文件读回，事实只能来自这里 ──
	facts := observationSection(t, "window-facts.json", "BR01.right-pane")
	px := readPNGFacts(t, "screenshots/right-pane.png")

	mainHWND := uint64(facts.num(t, "mainHWND"))
	childHWND := uint64(facts.num(t, "childHWND"))
	parentOfChild := uint64(facts.num(t, "parentOfChild"))
	rootAncestor := uint64(facts.num(t, "rootAncestorOfChild"))

	childParentIsMain := parentOfChild == mainHWND
	rootIsMain := rootAncestor == mainHWND
	childIsNotTopLevel := childHWND != 0 && childHWND != mainHWND && rootAncestor != childHWND
	centerInChild := facts.flag(t, "windowFromPointChildCenterInChildTree")
	mainAreaInMain := facts.flag(t, "windowFromPointMainAreaInMainTree")
	childVisible := facts.flag(t, "childIsVisible")
	childInsideMain := facts.flag(t, "childInsideMainRect")
	dpi := facts.num(t, "dpiForWindowChild")

	// 观察装置自检：文件里的 Win32 事实必须支持"真实子视图"这个结论。
	if !childParentIsMain {
		t.Fatalf("window-facts.json 里 GetParent(child)=0x%x，主窗口=0x%x：不是子窗口", parentOfChild, mainHWND)
	}
	if !rootIsMain {
		t.Fatalf("window-facts.json 里 GetAncestor(child,GA_ROOT)=0x%x，主窗口=0x%x：子视图的顶层祖先不是主窗口", rootAncestor, mainHWND)
	}
	if !childIsNotTopLevel {
		t.Fatalf("window-facts.json 里 child=0x%x root=0x%x：子窗口自己就是顶层窗口", childHWND, rootAncestor)
	}
	if !centerInChild {
		t.Fatalf("WindowFromPoint(子窗口中心 %d,%d)=0x%x 不在子窗口树里：屏幕上那块像素不是这个子窗口",
			centerX, centerY, uint64(facts.num(t, "windowFromPointChildCenter")))
	}
	if !childVisible {
		t.Fatal("window-facts.json 里 IsWindowVisible(child)=false")
	}
	if !childInsideMain {
		t.Fatal("window-facts.json 里子窗口矩形不在主窗口矩形内")
	}
	if dpi == 0 {
		t.Fatal("window-facts.json 里 GetDpiForWindow(child)=0")
	}
	if px.DistinctColors <= 4 {
		t.Fatalf("落盘的页面截图只有 %d 种颜色（纯色），不是真实页面像素", px.DistinctColors)
	}
	if px.Width <= 0 || px.Height <= 0 {
		t.Fatalf("落盘的页面截图尺寸 %dx%d 非法", px.Width, px.Height)
	}

	observationMergeFacts(t, obsFacts{
		"childIsNotTopLevel":              childIsNotTopLevel,
		"childParentIsMain":               childParentIsMain,
		"rootIsMain":                      rootIsMain,
		"childClassName":                  facts.text(t, "childClassName"),
		"childWindowFromPointInChildTree": centerInChild,
		"mainWindowFromPointInMainTree":   mainAreaInMain,
		"childVisible":                    childVisible,
		"childDpi":                        dpi,
		"pagePixelDistinctColors":         px.DistinctColors,
		"pagePixelWidth":                  px.Width,
		"pagePixelHeight":                 px.Height,
		"screenshotSha256":                px.SHA256,
		"windowFactsArtifact":             "window-facts.json",
		"screenshotArtifact":              "screenshots/right-pane.png",
		"observedBy": "window-facts.json 由测试用 Win32 GetParent/GetAncestor/GetWindowRect/" +
			"GetClientRect/GetDpiForWindow/WindowFromPoint 直读落盘；screenshots/right-pane.png 是 " +
			"CDP Page.captureScreenshot 的原始字节，尺寸/颜色数/sha256 从文件读回",
		"note": "主窗口自身像素在本机无法抓取（本会话屏幕 DC 不可用：GetForegroundWindow()=0），" +
			"父子关系用 Win32 句柄与 WindowFromPoint 证明，页面像素用 CDP 截图证明",
	})
}
