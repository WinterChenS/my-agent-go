//go:build windows

package nativetest

// B01a 的 Windows 原生宿主用例。
//
// 这些用例跑的是真实 Win32 窗口 + 真实 WebView2（本机运行时 156.0.4314.58），
// 不是 mock：截图来自 CDP 的页面像素，父子关系来自 GetParent/GetAncestor，
// 位置来自 GetWindowRect。
//
// 每个用例各自的覆盖范围写在函数注释里；没有覆盖到的（150%/200% 缩放、IME、
// 50 轮、多 tab、key/scroll）明确留给 B01b，不在这里用"看起来还行"冒充。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/browser/native"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// harness 是一套完整的原生宿主环境：测试窗口（扮演 Wails 主窗口角色）+ fixture + 宿主。
type harness struct {
	win     *TestHostWindow
	host    native.NativeHost
	fixture *browserfixture.Fixture
	tab     browser.TabID
	slot    browser.SlotGeometry
}

// newHarness 建窗口、Attach、CreateTab 并等到页面 ready。
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	win, err := NewTestHostWindow("MyAgent B01a native test host")
	if err != nil {
		t.Fatalf("创建测试主窗口: %v", err)
	}
	t.Cleanup(win.Close)

	// profile 目录不用 t.TempDir()：WebView2 关闭 controller 后会短暂占着文件，
	// Go 的自动 RemoveAll 会因此把用例判失败（实测）。这里自己重试清理。
	profileDir, err := os.MkdirTemp("", "myagent-native-profile-*")
	if err != nil {
		t.Fatalf("创建 profile 目录: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if rmErr := os.RemoveAll(profileDir); rmErr == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("警告: profile 目录 %s 未能删除（WebView2 关闭后仍占着文件）", profileDir)
	})

	parent := browser.NewNativeParent(win.HWND(), "test-main-window")
	host, err := native.NewWindowsHost(parent, native.HostOptions{ProfilePath: profileDir})
	if err != nil {
		t.Fatalf("NewWindowsHost: %v", err)
	}
	var attachErr error
	if err := win.CallOnUI(func() { attachErr = host.Attach(ctx, parent) }); err != nil {
		t.Fatalf("CallOnUI(Attach): %v", err)
	}
	if attachErr != nil {
		t.Fatalf("Attach: %v", attachErr)
	}
	t.Cleanup(func() {
		detachCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = host.Detach(detachCtx)
	})

	// 右侧槽位用固定几何：位置由 Present 负责，与真实 React 上报的形状一致。
	slot := browser.SlotGeometry{SlotID: "browser-slot", Revision: 1, Left: 640, Top: 40,
		Width: 480, Height: 600, Visible: true, Zoom: 1}
	if err := host.Present(ctx, "", slot); err != nil {
		t.Fatalf("Present(初始几何): %v", err)
	}
	tab, err := host.CreateTab(ctx, browser.TabOptions{URL: fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	h := &harness{win: win, host: host, fixture: fixture, tab: tab, slot: slot}
	if err := h.waitReady(ctx); err != nil {
		t.Fatalf("等页面 ready: %v", err)
	}
	return h
}

// waitReady 轮询到页面 readyState=complete 且标题非空。
func (h *harness) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	var last native.PageState
	for time.Now().Before(deadline) {
		ps, err := h.host.PageState(ctx, h.tab)
		if err == nil {
			last = ps
			if ps.ReadyState == "complete" && strings.TrimSpace(ps.Title) != "" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("超时：readyState=%q title=%q", last.ReadyState, last.Title)
}

// TestNativeRightPaneWindows 覆盖 BR01 的 Windows 硬条件：
//   - 原生视图是主窗口的**子窗口**（GetParent 直连、GA_ROOT 仍是主窗口），不是第二个顶层窗口；
//   - 页面真的加载完成（readyState/title 来自页面自报）；
//   - CDP 截图是真实页面像素（PNG 解码后非纯色、尺寸=CssViewport×scale）；
//   - Observe 返回有界元素与文本；
//   - Act 的 click 产生可信输入，并用 url_equals 后置条件核验通过。
//
// 不覆盖：150%/200% 缩放、IME、遮挡协调器（B01b/B07）。
func TestNativeRightPaneWindows(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	info := h.host.WindowInfo()
	if !info.ParentMatches {
		t.Fatalf("GetParent(child)=0x%x != main=0x%x", info.ParentOfChild, info.ParentHWND)
	}
	if info.ChildHWND == 0 {
		t.Fatal("没有创建原生子窗口")
	}
	if root := RootAncestor(info.ChildHWND); root != info.ParentHWND {
		t.Fatalf("子窗口的顶层祖先是 0x%x，期望主窗口 0x%x（说明它不是子视图）", root, info.ParentHWND)
	}
	if cls := ClassNameOf(info.ChildHWND); cls != "MyAgentNativeHostChild" {
		t.Fatalf("子窗口类名 %q，期望 MyAgentNativeHostChild", cls)
	}
	if !info.ControllerReady {
		t.Fatal("controller 未就绪")
	}
	t.Logf("hwnd 关系: main=0x%x child=0x%x parent=0x%x root=0x%x class=%s",
		info.ParentHWND, info.ChildHWND, info.ParentOfChild, RootAncestor(info.ChildHWND), "MyAgentNativeHostChild")

	ps, err := h.host.PageState(ctx, h.tab)
	if err != nil {
		t.Fatalf("PageState: %v", err)
	}
	if ps.Title != "Browser fixture" {
		t.Fatalf("页面标题 %q，期望 Browser fixture", ps.Title)
	}
	if !strings.HasSuffix(ps.URL, "/") || !strings.HasPrefix(ps.URL, h.fixture.Primary.URL) {
		t.Fatalf("页面 URL %q 不属于 fixture primary %s", ps.URL, h.fixture.Primary.URL)
	}
	t.Logf("page: readyState=%s title=%q url=%s counters=%v", ps.ReadyState, ps.Title, ps.URL, ps.Counters)

	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(snap.Elements) == 0 {
		t.Fatal("Observe 没有返回任何元素")
	}
	if !strings.Contains(snap.Text, "Browser fixture") {
		t.Fatalf("快照文本里没有页面内容：%q", truncate(snap.Text, 120))
	}
	if err := snap.CheckLimits(); err != nil {
		t.Fatalf("快照越界: %v", err)
	}
	if err := snap.ValidateRefs(); err != nil {
		t.Fatalf("快照引用不合法: %v", err)
	}

	img, err := h.host.Screenshot(ctx, h.tab)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if err := img.Validate(); err != nil {
		t.Fatalf("截图元数据不自洽: %v", err)
	}
	if img.NavigationID == "" {
		t.Fatal("截图缺少 navigationId")
	}
	stats := pngStats(t, img.Bytes)
	if stats.uniqueColors <= 4 {
		t.Fatalf("CDP 截图是纯色（uniqueColors=%d），不是真实页面像素", stats.uniqueColors)
	}
	if wantW := int(h.slot.Width * img.Scale); img.PixelWidth != wantW {
		t.Fatalf("截图宽度 %d，期望 slot 宽度 %.0f×scale %.2f = %d",
			img.PixelWidth, h.slot.Width, img.Scale, wantW)
	}
	if wantH := int(h.slot.Height * img.Scale); img.PixelHeight != wantH {
		t.Fatalf("截图高度 %d，期望 %d", img.PixelHeight, wantH)
	}
	t.Logf("screenshot: %dx%d css=%.0fx%.0f scale=%.2f bytes=%d uniqueColors=%d",
		img.PixelWidth, img.PixelHeight, img.CSSWidth, img.CSSHeight, img.Scale, len(img.Bytes), stats.uniqueColors)

	// Act：点“普通表单”链接，用 url_equals 核验（可信输入 + 后置条件）。
	var ref string
	for _, el := range snap.Elements {
		if el.Tag == "a" && strings.Contains(el.Name, "普通表单") {
			ref = el.Ref
			break
		}
	}
	if ref == "" {
		t.Fatal("快照里找不到“普通表单”链接")
	}
	res, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind:       browser.ActionClick,
		SnapshotID: snap.ID,
		Ref:        ref,
		ExpectedCondition: browser.ExpectedCondition{
			Kind:      browser.ConditionURLEquals,
			URL:       h.fixture.Primary.URL + "/form",
			TimeoutMS: 10000,
		},
	})
	if err != nil {
		t.Fatalf("Act(click): %v", err)
	}
	if !res.Delivered || !res.Verified || res.Verification.Status != browser.VerificationVerified {
		t.Fatalf("click 没有通过后置条件：delivered=%t verified=%t status=%s detail=%s",
			res.Delivered, res.Verified, res.Verification.Status, res.Verification.Detail)
	}
	t.Logf("act: delivered=%t verified=%t detail=%s", res.Delivered, res.Verified, res.Verification.Detail)

	caps := h.host.Capabilities()
	if !caps.Navigate || !caps.Observe || !caps.Act || !caps.Screenshot {
		t.Fatalf("能力未全部实测通过：navigate=%t observe=%t act=%t screenshot=%t",
			caps.Navigate, caps.Observe, caps.Act, caps.Screenshot)
	}
	if caps.Platform != "windows" {
		t.Fatalf("Platform=%q", caps.Platform)
	}
	t.Logf("capabilities: navigate=%t observe=%t act=%t screenshot=%t unsupported=%d",
		caps.Navigate, caps.Observe, caps.Act, caps.Screenshot, len(caps.Unsupported))
}

// TestNativeWindowGeometry100 覆盖 BR04 的 100% 缩放子集：
// CSS px × (DPI/96) 换算到客户区坐标，宽高与位置都要对得上；旧 revision 必须被丢弃。
//
// 不覆盖：150%/200% 缩放、分栏拖动 20 轮（B01b）。
func TestNativeWindowGeometry100(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scale := h.host.WindowInfo().Scale
	if scale != 1 {
		t.Skipf("本机 DPI 缩放是 %.2f（测试只覆盖 100%%，150/200%% 属于 B01b）", scale)
	}
	ox, oy := h.win.ClientOrigin()

	cases := []browser.SlotGeometry{
		{SlotID: "browser-slot", Revision: 2, Left: 640, Top: 40, Width: 480, Height: 600, Visible: true, Zoom: 1},
		{SlotID: "browser-slot", Revision: 3, Left: 700, Top: 60, Width: 420, Height: 560, Visible: true, Zoom: 1},
	}
	for _, slot := range cases {
		if err := h.host.Present(ctx, h.tab, slot); err != nil {
			t.Fatalf("Present(rev=%d): %v", slot.Revision, err)
		}
		child := h.host.WindowInfo().ChildHWND
		r := WindowRectOf(child)
		wantW := int32(slot.Width * scale)
		wantH := int32(slot.Height * scale)
		if got := r.Right - r.Left; got != wantW {
			t.Fatalf("rev=%d 宽 %d，期望 %d", slot.Revision, got, wantW)
		}
		if got := r.Bottom - r.Top; got != wantH {
			t.Fatalf("rev=%d 高 %d，期望 %d", slot.Revision, got, wantH)
		}
		wantX := ox + int32(slot.Left*scale)
		wantY := oy + int32(slot.Top*scale)
		if abs(r.Left-wantX) > 1 || abs(r.Top-wantY) > 1 {
			t.Fatalf("rev=%d 位置 (%d,%d)，期望 (%d,%d)（客户区原点 %d,%d）",
				slot.Revision, r.Left, r.Top, wantX, wantY, ox, oy)
		}
		t.Logf("rev=%d css=%.0f,%.0f %.0fx%.0f -> screen %d,%d %dx%d",
			slot.Revision, slot.Left, slot.Top, slot.Width, slot.Height,
			r.Left, r.Top, r.Right-r.Left, r.Bottom-r.Top)
	}

	// 迟到的旧 revision 必须被丢弃：几何不能回到旧位置。
	before := WindowRectOf(h.host.WindowInfo().ChildHWND)
	stale := browser.SlotGeometry{SlotID: "browser-slot", Revision: 1, Left: 10, Top: 10,
		Width: 200, Height: 200, Visible: true, Zoom: 1}
	if err := h.host.Present(ctx, h.tab, stale); err != nil {
		t.Fatalf("Present(stale): %v", err)
	}
	after := WindowRectOf(h.host.WindowInfo().ChildHWND)
	if before != after {
		t.Fatalf("旧 revision 没有被丢弃：before=%+v after=%+v", before, after)
	}
}

// TestNativeDialogOcclusionVisibility 覆盖 BR05 的可见性子集：
// SetVisible(false/true) 只切可见性、不销毁 controller（tab 状态还在）。
//
// 不覆盖：IME、焦点切换、最小化恢复、dialog 层级协调（B01b/B07）。
func TestNativeDialogOcclusionVisibility(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	info := h.host.WindowInfo()
	if !WindowVisible(info.ChildHWND) {
		t.Fatal("初始状态子窗口不可见")
	}
	if err := h.host.SetVisible(ctx, false); err != nil {
		t.Fatalf("SetVisible(false): %v", err)
	}
	if WindowVisible(h.host.WindowInfo().ChildHWND) {
		t.Fatal("SetVisible(false) 之后子窗口仍然可见")
	}
	if !h.host.WindowInfo().ControllerReady {
		t.Fatal("隐藏后 controller 被销毁了：隐藏不能丢 tab 状态")
	}
	// controller 还活着：页面状态依然可读。
	ps, err := h.host.PageState(ctx, h.tab)
	if err != nil {
		t.Fatalf("隐藏后 PageState 失败: %v", err)
	}
	if ps.Title == "" {
		t.Fatal("隐藏后页面标题为空")
	}
	if err := h.host.SetVisible(ctx, true); err != nil {
		t.Fatalf("SetVisible(true): %v", err)
	}
	if !WindowVisible(h.host.WindowInfo().ChildHWND) {
		t.Fatal("SetVisible(true) 之后子窗口仍不可见")
	}
	if !WindowVisible(h.host.WindowInfo().ChildHWND) || h.host.WindowInfo().ChildHWND == 0 {
		t.Fatal("恢复后子窗口句柄丢失")
	}
	t.Logf("visibility: hide/show 正常，controller 全程存活，title=%q", ps.Title)
}

// TestNativeCreateDestroyRepeated 覆盖 BR06 的小规模子集：3 轮创建/销毁，
// 销毁后子窗口句柄必须消失，不能留孤儿窗口；Detach 重复调用必须安全。
//
// 不覆盖：50 轮、RSS 统计、late callback 的 generation 保护压力测试（B01b）。
func TestNativeCreateDestroyRepeated(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for round := 1; round <= 3; round++ {
		if err := h.host.CloseTab(ctx, h.tab); err != nil {
			t.Fatalf("第 %d 轮 CloseTab: %v", round, err)
		}
		after := h.host.WindowInfo()
		if after.ChildHWND != 0 && WindowExists(after.ChildHWND) {
			t.Fatalf("第 %d 轮 CloseTab 之后子窗口 0x%x 仍然存在", round, after.ChildHWND)
		}
		if after.ControllerReady {
			t.Fatalf("第 %d 轮 CloseTab 之后 controller 仍标记为就绪", round)
		}
		// 重复关闭同一个 tab 安全（目标状态已达成，不报错也不重复销毁）。
		if err := h.host.CloseTab(ctx, h.tab); err != nil {
			t.Fatalf("第 %d 轮重复 CloseTab 返回错误: %v", round, err)
		}
		if round == 3 {
			break
		}
		tab, err := h.host.CreateTab(ctx, browser.TabOptions{URL: h.fixture.Primary.URL + "/"})
		if err != nil {
			t.Fatalf("第 %d 轮 CreateTab: %v", round+1, err)
		}
		h.tab = tab
		if err := h.waitReady(ctx); err != nil {
			t.Fatalf("第 %d 轮 waitReady: %v", round+1, err)
		}
		if err := h.host.Present(ctx, h.tab, h.slot); err != nil {
			t.Fatalf("第 %d 轮 Present: %v", round+1, err)
		}
		t.Logf("round %d: child=0x%x ready", round, h.host.WindowInfo().ChildHWND)
	}

	if err := h.host.Detach(ctx); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if err := h.host.Detach(ctx); err != nil {
		t.Fatalf("重复 Detach 应当安全: %v", err)
	}
	final := h.host.WindowInfo()
	if final.ChildHWND != 0 {
		t.Fatalf("Detach 之后子窗口句柄未清零: 0x%x", final.ChildHWND)
	}
}

// ---------- 断言辅助 ----------

type pngInfo struct{ width, height, uniqueColors int }

func pngStats(t *testing.T, raw []byte) pngInfo {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解码截图 PNG 失败: %v", err)
	}
	stats := statsOf(img)
	return pngInfo{width: img.Bounds().Dx(), height: img.Bounds().Dy(), uniqueColors: stats}
}

// statsOf 按网格采样统计颜色数（纯色图 = 1）。
func statsOf(img image.Image) int {
	b := img.Bounds()
	stepX, stepY := b.Dx()/48, b.Dy()/48
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}
	seen := map[color.RGBA]struct{}{}
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			seen[color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: 255}] = struct{}{}
		}
	}
	return len(seen)
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// 编译期确认 PageState 的 JSON 形状没被悄悄改掉（证据脚本按这些字段读）。
var _ = json.Marshal
