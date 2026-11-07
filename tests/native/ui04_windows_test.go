//go:build windows

package nativetest

// UI04.native（B07）：右栏原生 slot、遮挡协调、停止按钮位置与接管交接。
//
// 与 React 测试的分工（这条界线不能糊）：
//   - BrowserPanel/Coordinator 的用例证明**前端**只上报几何与事件、按 code 显示失败、
//     不重建 slot。它们证明不了原生窗口真的被摆到那块矩形上、真的被隐藏/恢复。
//   - 这里必须给出**真实进程外部事实**：GetParent/GetWindowRect/IsWindowVisible/
//     WindowFromPoint 的直读值、宿主计数（Attach/CreateTab/Present/SetVisible）、
//     fixture 服务端 counter 与它收到的表单载荷。
//
// 全程走应用层（internal/app 的 BrowserBridge/Desktop 绑定），不是直接调 native 包：
// 这样"React 调的那条路径"和"证据里的这条路径"是同一条。
//
// 两条用例：
//   TestUI04NativeSlotOcclusionAndStopButton —— slot 几何 revision/拒绝覆盖工具条、
//     设置 dialog → 命令面板 → 审批框依次弹出时的隐藏/恢复、tab 不被重建、
//     停止按钮区域不被原生视图覆盖、恢复后页面仍可交互（服务端 counter +1）。
//   TestUI04NativeHandOver —— agent 填入文本 → 用户直接看到并手改 → 恢复后 agent
//     观察到人工修改 → 提交的服务端载荷就是人工改过的值。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/app"
	"my-agent-go/internal/browser"
	"my-agent-go/internal/browser/native"
	"my-agent-go/internal/contracts"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// ---------- 计数宿主：挂载/创建/摆位/可见性的真实调用次数 ----------

// ui04Counts 是计数器的一次快照（**不含**锁：它会被当成普通值读/传）。
type ui04Counts struct {
	attaches int
	creates  int
	closes   int
	presents int
	visible  int
}

type ui04Counters struct {
	mu sync.Mutex
	ui04Counts
}

func (c *ui04Counters) bump(field *int) {
	c.mu.Lock()
	*field++
	c.mu.Unlock()
}

func (c *ui04Counters) snapshot() ui04Counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ui04Counts
}

// ui04Host 把宿主包一层只做计数：它是"原生实例有没有被重建"的直接证据。
type ui04Host struct {
	native.NativeHost
	counters *ui04Counters
}

func (h *ui04Host) Attach(ctx context.Context, parent browser.NativeParent) error {
	h.counters.bump(&h.counters.attaches)
	return h.NativeHost.Attach(ctx, parent)
}

func (h *ui04Host) CreateTab(ctx context.Context, opts browser.TabOptions) (browser.TabID, error) {
	h.counters.bump(&h.counters.creates)
	return h.NativeHost.CreateTab(ctx, opts)
}

func (h *ui04Host) CloseTab(ctx context.Context, tab browser.TabID) error {
	h.counters.bump(&h.counters.closes)
	return h.NativeHost.CloseTab(ctx, tab)
}

func (h *ui04Host) Present(ctx context.Context, tab browser.TabID, slot browser.SlotGeometry) error {
	h.counters.bump(&h.counters.presents)
	return h.NativeHost.Present(ctx, tab, slot)
}

func (h *ui04Host) SetVisible(ctx context.Context, visible bool) error {
	h.counters.bump(&h.counters.visible)
	return h.NativeHost.SetVisible(ctx, visible)
}

// ---------- 授权替身（B07 不测授权，只测面板/遮挡/接管） ----------

type ui04Authorizer struct{ guard browser.TokenGuard }

func (a *ui04Authorizer) Authorize(_ context.Context, exec contracts.ExecutionContext, req browser.AuthorizationRequest) (browser.AuthorizationToken, error) {
	return a.guard.Issue(exec.SessionID, req, "apr-ui04", "UI04 测试许可"), nil
}

func (a *ui04Authorizer) Recheck(context.Context, contracts.ExecutionContext, browser.AuthorizationRequest, browser.AuthorizationToken) error {
	return nil
}

// ---------- 环境：真实窗口 + WebView2 + 服务 + 应用层桥接 ----------

type ui04Env struct {
	*harness
	counting *ui04Host
	svc      *browser.Service
	desktop  *app.Desktop
	bridge   *app.BrowserBridge
	exec     contracts.ExecutionContext
	slot     browser.SlotGeometry
}

// ui04Slot 是右栏工具条（96 CSS px，与 app.BrowserChromeReservedTop 同一个值）之下的槽位。
func ui04Slot(revision uint64) browser.SlotGeometry {
	return browser.SlotGeometry{
		SlotID: "browser-slot", Revision: revision,
		Left: 640, Top: float64(app.BrowserChromeReservedTop), Width: 480, Height: 600,
		Visible: true, Zoom: 1,
	}
}

func newUI04Env(t *testing.T) *ui04Env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	win, err := NewTestHostWindow("MyAgent UI04 native host")
	if err != nil {
		t.Fatalf("创建测试主窗口: %v", err)
	}
	t.Cleanup(win.Close)

	profileDir, err := os.MkdirTemp("", "ui04-native-profile-*")
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
	raw, err := native.NewWindowsHost(parent, native.HostOptions{ProfilePath: profileDir})
	if err != nil {
		t.Fatalf("NewWindowsHost: %v", err)
	}
	var attachErr error
	if err := win.CallOnUI(func() { attachErr = raw.Attach(ctx, parent) }); err != nil {
		t.Fatalf("CallOnUI(Attach): %v", err)
	}
	if attachErr != nil {
		t.Fatalf("Attach: %v", attachErr)
	}
	t.Cleanup(func() {
		detachCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = raw.Detach(detachCtx)
	})

	counting := &ui04Host{NativeHost: raw, counters: &ui04Counters{}}
	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-ui04", SessionID: "sess-ui04", RunID: "run-ui04",
		ConfigRevision: 1, ControlGeneration: 1,
	}
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend:       counting,
		Authorizer:    &ui04Authorizer{guard: browser.TokenGuard{TTL: time.Minute}},
		ActionTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	// 应用层：和 React 调的正是同一条路径（Desktop 绑定 → BrowserBridge）。
	t.Setenv("MYAGENT_PI_HOME", t.TempDir())
	desktop, err := app.New(t.TempDir())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(desktop.Shutdown)
	bridge := app.NewBrowserBridge(app.BrowserBridgeOptions{
		Surface: counting,
		Service: svc,
		Exec:    exec,
	})
	desktop.SetBrowserBridge(bridge)

	// 刻意先上报几何、后建 tab：真实面板也是这个顺序（面板先挂上，agent 稍后才开 tab）。
	// 这条路径在 controller 就绪前就到 SetVisible，B07 因此修掉了宿主那处顺序问题。
	slot := ui04Slot(1)
	if callErr := desktop.UpdateBrowserSlot(slot); callErr != nil {
		t.Fatalf("UpdateBrowserSlot(初始几何): %+v", callErr)
	}
	tab, err := svc.OpenTab(ctx, exec, browser.TabOptions{URL: fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if callErr := desktop.SelectBrowserTab(string(tab)); callErr != nil {
		t.Fatalf("SelectBrowserTab: %+v", callErr)
	}
	h := &harness{win: win, host: counting, fixture: fixture, tab: tab, slot: slot}
	if err := h.waitReady(ctx); err != nil {
		t.Fatalf("等页面 ready: %v", err)
	}
	return &ui04Env{
		harness: h, counting: counting, svc: svc, desktop: desktop,
		bridge: bridge, exec: exec, slot: slot,
	}
}

// isVisible 是 IsWindowVisible 直读。
func isVisible(hwnd uintptr) bool {
	r, _, _ := tIsWindowVisible.Call(hwnd)
	return r != 0
}

// screenPoint 把应用 WebView 的 CSS 坐标换算成屏幕物理坐标（父客户区原点 + DPI）。
func screenPoint(env *ui04Env, cssX, cssY float64) (int32, int32) {
	scale := env.host.WindowInfo().Scale
	ox, oy := env.win.ClientOrigin()
	return int32(float64(ox) + math.Round(cssX*scale)), int32(float64(oy) + math.Round(cssY*scale))
}

// expectedChildRect 是 slot 几何对应的**期望**屏幕矩形（只由 CSS × DPI 决定）。
func expectedChildRect(env *ui04Env, slot browser.SlotGeometry) (left, top, width, height float64) {
	scale := env.host.WindowInfo().Scale
	ox, oy := env.win.ClientOrigin()
	return float64(ox) + math.Round(slot.Left*scale), float64(oy) + math.Round(slot.Top*scale),
		math.Round(slot.Width * scale), math.Round(slot.Height * scale)
}

// rectError 返回子窗口实际矩形与期望值的最大偏差（物理 px）。
func rectError(env *ui04Env, slot browser.SlotGeometry) (float64, obsFacts) {
	wantL, wantT, wantW, wantH := expectedChildRect(env, slot)
	gotL, gotT, gotW, gotH := childRect(env.harness)
	err := math.Max(
		math.Max(math.Abs(gotL-wantL), math.Abs(gotT-wantT)),
		math.Max(math.Abs(gotW-wantW), math.Abs(gotH-wantH)))
	return err, obsFacts{
		"expected": obsFacts{"left": wantL, "top": wantT, "width": wantW, "height": wantH},
		"actual":   obsFacts{"left": gotL, "top": gotT, "width": gotW, "height": gotH},
	}
}

// ---------- UI04.native：slot / 遮挡 / 停止按钮 ----------

func TestUI04NativeSlotOcclusionAndStopButton(t *testing.T) {
	env := newUI04Env(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	info := env.host.WindowInfo()
	if !info.ParentMatches || info.ChildHWND == 0 || !info.ControllerReady {
		t.Fatalf("原生视图没挂上：%+v", info)
	}
	main, child := info.ParentHWND, info.ChildHWND
	if GetParentHWND(child) != main || RootAncestor(child) != main {
		t.Fatalf("子窗口不在主窗口树里：GetParent=0x%x root=0x%x main=0x%x",
			GetParentHWND(child), RootAncestor(child), main)
	}

	// ---- 1. slot 几何：接受的 revision 之后，子窗口矩形与期望一致 ----
	slack, rectFacts := rectError(env, env.slot)
	if slack > rectTolerancePX {
		t.Fatalf("子窗口矩形误差 %.2fpx 超过 %.0f：%v", slack, rectTolerancePX, rectFacts)
	}
	recordSlot(t, obsFacts{
		"revision":     env.slot.Revision,
		"accepted":     true,
		"cssRect":      obsFacts{"left": env.slot.Left, "top": env.slot.Top, "width": env.slot.Width, "height": env.slot.Height},
		"expectedRect": rectFacts["expected"],
		"actualRect":   rectFacts["actual"],
		"rectErrorPX":  slack,
	})

	// ---- 2. 覆盖工具条的几何必须被拒绝（停止按钮不能被原生网页盖住）----
	violating := ui04Slot(2)
	violating.Top = float64(app.BrowserChromeReservedTop) - 32
	callErr := env.desktop.UpdateBrowserSlot(violating)
	if callErr == nil {
		t.Fatal("覆盖工具条的几何被接受了：停止按钮会被原生网页盖住")
	}
	if callErr.Code != contracts.CodeUnsupported {
		t.Fatalf("拒绝覆盖工具条的几何用了错误码 %q，期望 %q（%+v）",
			callErr.Code, contracts.CodeUnsupported, callErr)
	}
	if slack2, _ := rectError(env, env.slot); slack2 > rectTolerancePX {
		t.Fatalf("被拒绝的几何移动了原生视图（误差 %.2fpx）", slack2)
	}
	recordSlot(t, obsFacts{
		"revision":    violating.Revision,
		"accepted":    false,
		"errorCode":   callErr.Code,
		"cssRect":     obsFacts{"left": violating.Left, "top": violating.Top, "width": violating.Width, "height": violating.Height},
		"rectUnmoved": true,
	})

	// ---- 3. 停止按钮位置：用 OS 命中测试证明保留区里没有任何原生视图 ----
	stopX, stopY := screenPoint(env, env.slot.Left+env.slot.Width/2, float64(app.BrowserChromeReservedTop)/2)
	hitStop := WindowAt(stopX, stopY)
	slotCenterX, slotCenterY := screenPoint(env, env.slot.Left+env.slot.Width/2, env.slot.Top+env.slot.Height/2)
	hitSlot := WindowAt(slotCenterX, slotCenterY)
	stopOutside := !IsDescendantOf(hitStop, child)
	slotInside := IsDescendantOf(hitSlot, child)
	if !stopOutside {
		t.Fatalf("停止按钮区域 0x%x 落在原生子窗口树里：网页会盖住停止按钮", hitStop)
	}
	if !slotInside {
		t.Fatalf("槽位中心 0x%x 不在原生子窗口树里：那块像素不是我们的网页", hitSlot)
	}
	observeUI04StopButton(t, obsFacts{
		"stopPointScreen":                 obsFacts{"x": stopX, "y": stopY},
		"windowAtStopPoint":               hitStop,
		"stopPointInsideNativeChildTree":  !stopOutside,
		"slotCenterScreen":                obsFacts{"x": slotCenterX, "y": slotCenterY},
		"windowAtSlotCenter":              hitSlot,
		"slotCenterInsideNativeChildTree": slotInside,
		"chromeReservedTopCSS":            app.BrowserChromeReservedTop,
		"childHWND":                       child,
		"childClassName":                  ClassNameOf(child),
	})

	// ---- 4. 遮挡：设置 dialog → 命令面板 → 审批框，各自隐藏/恢复 ----
	before := env.counting.counters.snapshot()
	tabsBefore := env.svc.Tabs(env.exec)
	pageBefore, err := env.host.PageState(ctx, env.tab)
	if err != nil {
		t.Fatalf("PageState(遮挡前): %v", err)
	}

	steps := []struct {
		source string
		open   bool
	}{
		{"settings-dialog", true},
		{"settings-dialog", false},
		{"command-palette", true},
		{"command-palette", false},
		{"approval-dialog", true},
		{"approval-dialog", false},
	}
	for _, step := range steps {
		if callErr := env.desktop.SetBrowserOverlay(step.source, step.open); callErr != nil {
			t.Fatalf("SetBrowserOverlay(%s,%t): %+v", step.source, step.open, callErr)
		}
		state := env.desktop.BrowserState()
		now := env.host.WindowInfo()
		counters := env.counting.counters.snapshot()
		tabs := env.svc.Tabs(env.exec)
		recordOcclusion(t, obsFacts{
			"source":          step.source,
			"open":            step.open,
			"childVisible":    isVisible(now.ChildHWND),
			"controllerReady": now.ControllerReady,
			"childHWND":       now.ChildHWND,
			"parentMatches":   now.ParentMatches,
			"stateVisible":    state.Visible,
			"stateRequested":  state.RequestedVisible,
			"stateOverlays":   len(state.Overlays),
			"tabs":            len(tabs),
			"creates":         counters.creates,
			"presented":       counters.presents,
			"visibilityCalls": counters.visible,
			"closes":          counters.closes,
			"sameTabSet":      equalTabs(tabs, tabsBefore),
		})
		if step.open {
			if isVisible(now.ChildHWND) {
				t.Fatalf("%s 打开后原生视图仍然可见：浮层会被网页盖住", step.source)
			}
			if state.Visible {
				t.Fatalf("%s 打开后 state.visible 仍为 true", step.source)
			}
		} else {
			if !isVisible(now.ChildHWND) {
				t.Fatalf("%s 关闭后原生视图没有恢复", step.source)
			}
			if !state.Visible {
				t.Fatalf("%s 关闭后 state.visible 为 false：没有恢复", step.source)
			}
		}
		if !now.ParentMatches || !now.ControllerReady || now.ChildHWND != child {
			t.Fatalf("%s 之后原生视图被重建了：%+v", step.source, now)
		}
	}

	after := env.counting.counters.snapshot()
	if after.creates != before.creates {
		t.Fatalf("遮挡期间创建了 %d 个新 tab（挂载计数必须不变）", after.creates-before.creates)
	}
	if after.closes != before.closes {
		t.Fatalf("遮挡期间关闭了 %d 个 tab", after.closes-before.closes)
	}
	if after.attaches != before.attaches {
		t.Fatalf("遮挡期间重新 Attach 了 %d 次", after.attaches-before.attaches)
	}
	if got := env.svc.Tabs(env.exec); len(got) != len(tabsBefore) {
		t.Fatalf("遮挡改变了 tab 列表：%v → %v", tabsBefore, got)
	}
	pageAfter, err := env.host.PageState(ctx, env.tab)
	if err != nil {
		t.Fatalf("PageState(遮挡后): %v", err)
	}
	if pageAfter.URL != pageBefore.URL || pageAfter.Title != pageBefore.Title {
		t.Fatalf("遮挡后页面身份变了：before=%+v after=%+v", pageBefore, pageAfter)
	}

	// ---- 5. 恢复后页面仍可交互：可信点击让服务端 counter 恰好 +1 ----
	// 换到一个有可点按钮的 fixture 页（trusted-only 页的 #trusted-target 是服务端的
	// 可信输入闸门；点它 = 一次真实交互 + 服务端计数）。
	navigateTo(t, ctx, env.harness, "/trusted-only", "trusted-only")
	before1 := fixtureCounter(t, env.fixture.Primary.URL, browserfixture.CounterTrustedOnly)
	var rect struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	evalJSONInto(t, ctx, env.harness, `(() => {
  const el = document.getElementById('trusted-target');
  el.scrollIntoView({block: 'center'});
  const r = el.getBoundingClientRect();
  return JSON.stringify({x: r.left, y: r.top, w: r.width, h: r.height});
})()`, &rect)
	clickAtCSS(t, ctx, env.harness, rect.X+rect.W/2, rect.Y+rect.H/2)
	waitCounter(t, env.fixture.Primary, browserfixture.CounterTrustedOnly, before1+1, 10*time.Second)
	after1 := fixtureCounter(t, env.fixture.Primary.URL, browserfixture.CounterTrustedOnly)
	if after1 != before1+1 {
		t.Fatalf("遮挡恢复后可信点击 counter=%d，期望 %d（页面还能用吗？）", after1, before1+1)
	}

	// 截图：恢复态一张（CDP 页面像素，不是窗口截图）。
	//
	// 隐藏态**拍不到**页内像素：controller 被隐藏后 CDP Page.captureScreenshot 会超时
	// （实测 8s 未返回）。所以"隐藏"由 IsWindowVisible(child)=false、SetVisible 计数与
	// 恢复后同一 navigationId 的页面事实证明，不用一张拍不到的图充数。
	img, err := env.host.Screenshot(ctx, env.tab)
	if err != nil {
		t.Fatalf("Screenshot(ui04-restored): %v", err)
	}
	observationWrite(t, "screenshots/ui04-restored.png", img.Bytes)

	observeUI04WindowFacts(t, env, before, after, before1, after1)
	observeUI04OcclusionFacts(t)
}

// equalTabs 比较两组 tab id（顺序无关；服务不保证顺序以外的语义）。
func equalTabs(a, b []browser.TabID) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[browser.TabID]bool{}
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

// ---------- UI04.native：接管交接 ----------

func TestUI04NativeHandOver(t *testing.T) {
	env := newUI04Env(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	if got := fixtureCounter(t, env.fixture.Primary.URL, browserfixture.CounterFormSubmit); got != 0 {
		t.Fatalf("开始前 form-submit counter=%d，期望 0", got)
	}
	navigateTo(t, ctx, env.harness, "/form", "普通表单")

	snap, err := env.svc.Observe(ctx, env.exec, env.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/form): %v", err)
	}
	nameInput := findByInput(t, snap, "input", "text")

	// ---- 1. agent 填入文本（经服务的写路径），后置条件读回 ----
	const agentText = "agent-b07-写入"
	res, err := env.svc.Execute(ctx, env.exec, env.tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameInput.Ref, Text: agentText,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionFieldValue, Ref: nameInput.Ref, Value: agentText, TimeoutMS: 8000,
		},
	})
	if err != nil {
		t.Fatalf("agent fill: %v", err)
	}
	if !res.Delivered || !res.Verified {
		t.Fatalf("agent fill 没有被核验：delivered=%t verified=%t detail=%s",
			res.Delivered, res.Verified, res.Verification.Detail)
	}
	recordHandover(t, obsFacts{"step": "agent-fill", "value": agentText,
		"verified": res.Verified, "status": string(res.Verification.Status)})

	// ---- 2. 用户**直接看到**同一段文本（页面里真的那个 input 的值）----
	pageValue := pageInputValue(t, ctx, env)
	if pageValue != agentText {
		t.Fatalf("页面里的输入框值 = %q，期望 agent 写入的 %q（用户看不到 agent 写的内容）",
			pageValue, agentText)
	}

	// ---- 3. 用户接管：控制权交人工，agent 写动作立刻被拒绝 ----
	ctrl := env.desktop.TakeOverBrowserTab(string(env.tab))
	if ctrl.Error != nil {
		t.Fatalf("TakeOver: %+v", ctrl.Error)
	}
	if ctrl.Control.Mode != string(browser.ControlHuman) {
		t.Fatalf("接管后状态 = %q，期望 human", ctrl.Control.Mode)
	}
	_, writeErr := env.svc.Execute(ctx, env.exec, env.tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameInput.Ref, Text: "agent-should-not-write",
	})
	if writeErr == nil {
		t.Fatal("人工接管期间 agent 的写动作没有被拒绝")
	}
	if code := appErrorCode(writeErr); code != contracts.CodeNeedsHuman {
		t.Fatalf("人工控制期间写动作错误码 = %q，期望 %q（%v）", code, contracts.CodeNeedsHuman, writeErr)
	}
	recordHandover(t, obsFacts{"step": "takeover", "mode": ctrl.Control.Mode,
		"agentWriteCode": appErrorCode(writeErr)})

	// ---- 4. 用户手改：OS 物理点击 + OS 键盘输入（不是 CDP 假装的输入）----
	const humanSuffix = "-human-edit"
	humanText := agentText + humanSuffix
	inputPath, focused := typeIntoField(t, ctx, env, nameInput.Bounds, humanSuffix)
	if got := pageInputValue(t, ctx, env); got != humanText {
		t.Fatalf("手改后页面输入框 = %q，期望 %q（inputPath=%s focused=%t）", got, humanText, inputPath, focused)
	}
	recordHandover(t, obsFacts{"step": "human-edit", "value": humanText,
		"inputPath": inputPath, "fieldFocused": focused})

	// ---- 5. 恢复：没有新快照必须 STALE_SNAPSHOT；有新快照才 resuming ----
	early := env.desktop.ResumeBrowserTab(string(env.tab))
	if early.Error == nil || early.Error.Code != contracts.CodeStaleSnapshot {
		t.Fatalf("没有新快照就恢复没有被拒绝：%+v", early.Error)
	}
	if _, err := env.svc.Observe(ctx, env.exec, env.tab, browser.ObserveOptions{IncludeText: true}); err != nil {
		t.Fatalf("Observe(恢复前): %v", err)
	}
	resumed := env.desktop.ResumeBrowserTab(string(env.tab))
	if resumed.Error != nil {
		t.Fatalf("Resume: %+v", resumed.Error)
	}
	if resumed.Control.Mode != string(browser.ControlResuming) {
		t.Fatalf("恢复后状态 = %q，期望 resuming", resumed.Control.Mode)
	}
	if _, err := env.svc.Observe(ctx, env.exec, env.tab, browser.ObserveOptions{IncludeText: true}); err != nil {
		t.Fatalf("Observe(恢复后): %v", err)
	}
	ctrlState, err := env.svc.Control(env.exec, env.tab)
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if ctrlState.Mode != browser.ControlAgent {
		t.Fatalf("恢复并重新观察后状态 = %q，期望 agent", ctrlState.Mode)
	}

	// ---- 6. agent 观察到人工修改：field_value 后置条件读到的是人工文本 ----
	fresh, err := env.svc.Observe(ctx, env.exec, env.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(新建快照): %v", err)
	}
	fieldAgain := findByInput(t, fresh, "input", "text")
	scrollRes, err := env.svc.Execute(ctx, env.exec, env.tab, browser.Action{
		Kind: browser.ActionScroll, SnapshotID: fresh.ID, DeltaY: 8,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionFieldValue, Ref: fieldAgain.Ref, Value: humanText, TimeoutMS: 8000,
		},
	})
	if err != nil {
		t.Fatalf("agent 读回人工修改: %v", err)
	}
	if !scrollRes.Verified {
		t.Fatalf("agent 没观察到人工修改：verified=%t detail=%s",
			scrollRes.Verified, scrollRes.Verification.Detail)
	}
	recordHandover(t, obsFacts{"step": "agent-observed-human-edit", "value": humanText,
		"verified": scrollRes.Verified, "status": string(scrollRes.Verification.Status)})

	// ---- 7. 端到端：提交的服务端载荷就是人工改过的值，counter 恰好 1 ----
	submitSnap, err := env.svc.Observe(ctx, env.exec, env.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(提交前): %v", err)
	}
	submit := findByName(t, submitSnap, "button", "提交")
	submitCount := findElement(t, submitSnap, "#submit-count（初始值 0）", func(el browser.Element) bool {
		return el.Tag == "strong" && strings.TrimSpace(el.Name) == "0"
	})
	clickRes, err := env.svc.Execute(ctx, env.exec, env.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: submitSnap.ID, Ref: submit.Ref,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionElementText, Ref: submitCount.Ref, Value: "1", TimeoutMS: 10000,
		},
	})
	if err != nil {
		t.Fatalf("提交: %v", err)
	}
	if !clickRes.Verified {
		t.Fatalf("提交没有被核验：%s", clickRes.Verification.Detail)
	}
	after := fixtureCounter(t, env.fixture.Primary.URL, browserfixture.CounterFormSubmit)
	if after != 1 {
		t.Fatalf("提交一次后服务端 counter=%d，期望恰好 1", after)
	}
	payload := lastForm(t, env.fixture.Primary.URL)
	if got := payload["name"]; got != humanText {
		t.Fatalf("服务端收到的 name=%v，期望人工改过的 %q（完整载荷 %v）", got, humanText, payload)
	}
	recordHandover(t, obsFacts{"step": "submit", "counter": after,
		"payloadName": payload["name"], "verified": clickRes.Verified})

	observeUI04HandOverFacts(t)
}

// pageInputValue 读页面上那个文本输入框的真实值（页面自报，只用于"用户看得见"的判断）。
func pageInputValue(t *testing.T, ctx context.Context, env *ui04Env) string {
	t.Helper()
	return evalString(t, ctx, env.harness, `(() => {
  const el = document.querySelector('form input[type="text"]');
  return el ? el.value : '';
})()`)
}

// typeIntoField 模拟用户直接编辑：OS 物理点击把光标放到字段末尾，再用 OS 键盘敲入后缀。
//
// 返回实际生效的输入通道（os-keyboard / cdp-insertText）与字段是否真的获得焦点。
// 两个通道都失败就直接失败——不能用"看起来改了"冒充人工修改。
func typeIntoField(t *testing.T, ctx context.Context, env *ui04Env, bounds browser.Rect, suffix string) (string, bool) {
	t.Helper()
	info := env.host.WindowInfo()
	if _, restore := RaiseToFront(info.ParentHWND); restore != nil {
		t.Cleanup(restore)
	}
	_ = env.host.Focus(ctx, env.tab)

	// 字段右端（不是正中间）：光标落在文本末尾，追加结果才确定。
	cssX := bounds.X + bounds.Width - 6
	cssY := bounds.Y + bounds.Height/2
	childLeft, childTop, _, _ := childRect(env.harness)
	scale := info.Scale
	screenX := int32(childLeft + cssX*scale)
	screenY := int32(childTop + cssY*scale)
	restoreCursor := PhysicalClick(screenX, screenY)
	t.Cleanup(restoreCursor)
	time.Sleep(150 * time.Millisecond)

	focused := evalString(t, ctx, env.harness, `(() => {
  const el = document.querySelector('form input[type="text"]');
  return document.activeElement === el ? 'true' : 'false';
})()`) == "true"

	keyboardOK := true
	if err := typeASCII(suffix); err != nil {
		t.Logf("OS 键盘输入不可用（%v），退回 CDP insertText 并如实记录", err)
		keyboardOK = false
	}
	time.Sleep(250 * time.Millisecond)
	if keyboardOK {
		if got := pageInputValue(t, ctx, env); strings.HasSuffix(got, suffix) {
			return "os-keyboard", focused
		}
		t.Logf("OS 键盘输入没有落到页面（当前值 %q），退回 CDP insertText", pageInputValue(t, ctx, env))
	}

	params, _ := json.Marshal(map[string]any{"text": suffix})
	if _, err := env.host.CallCDP(ctx, env.tab, "Input.insertText", string(params), 10*time.Second); err != nil {
		t.Fatalf("CDP Input.insertText 失败: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := pageInputValue(t, ctx, env); !strings.HasSuffix(got, suffix) {
		t.Fatalf("人工修改没有生效：页面值 %q，期望以 %q 结尾", got, suffix)
	}
	return "cdp-insertText", focused
}

// typeASCII 用 OS 键盘事件（keybd_event）敲入小写 ASCII。
//
// 只支持小写字母/数字/连字符：这正是用例需要的，也不涉及 IME。
// 真机手输中文仍要人工步骤（B01b 的 IME 用例同样如实标注了这一点）。
func typeASCII(text string) error {
	keybd := tUser32.NewProc("keybd_event")
	const keyUp = 0x0002
	for _, r := range text {
		var vk uintptr
		switch {
		case r >= 'a' && r <= 'z':
			vk = uintptr(r - 'a' + 0x41)
		case r >= '0' && r <= '9':
			vk = uintptr(r)
		case r == '-':
			vk = 0xBD
		default:
			return fmt.Errorf("typeASCII 不支持字符 %q（只用于小写 ASCII 用例）", r)
		}
		keybd.Call(vk, 0, 0, 0)
		keybd.Call(vk, 0, keyUp, 0)
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// appErrorCode 取契约错误码（空串表示不是契约错误）。
func appErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var appErr *contracts.AppError
	if ok := asError(err, &appErr); ok {
		return appErr.Code
	}
	return ""
}

func asError(err error, target **contracts.AppError) bool {
	for err != nil {
		if ae, ok := err.(*contracts.AppError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---------- 观察装置（事实全部从落盘文件读回） ----------

func recordSlot(t *testing.T, facts obsFacts) {
	t.Helper()
	observationAppendJSONL(t, "slot-geometry.jsonl", facts)
}

func recordOcclusion(t *testing.T, facts obsFacts) {
	t.Helper()
	observationAppendJSONL(t, "occlusion-steps.jsonl", facts)
}

func recordHandover(t *testing.T, facts obsFacts) {
	t.Helper()
	observationAppendJSONL(t, "handover.jsonl", facts)
}

func observeUI04StopButton(t *testing.T, facts obsFacts) {
	t.Helper()
	observationUpdateSection(t, "stop-button.json", "UI04.stop-button", facts)
}

func observeUI04WindowFacts(t *testing.T, env *ui04Env, before, after ui04Counts, counterBefore, counterAfter int) {
	t.Helper()
	info := env.host.WindowInfo()
	observationUpdateSection(t, "window-facts.json", "UI04.window", obsFacts{
		"capturedAt":               time.Now().UTC().Format(time.RFC3339Nano),
		"mainHWND":                 info.ParentHWND,
		"childHWND":                info.ChildHWND,
		"parentOfChild":            GetParentHWND(info.ChildHWND),
		"rootAncestorOfChild":      RootAncestor(info.ChildHWND),
		"childClassName":           ClassNameOf(info.ChildHWND),
		"childDescendantOfMain":    IsDescendantOf(info.ChildHWND, info.ParentHWND),
		"controllerReady":          info.ControllerReady,
		"dpiForWindow":             dpiForWindow(info.ParentHWND),
		"scale":                    info.Scale,
		"childWindowRect":          obsRect(WindowRectOf(info.ChildHWND)),
		"childVisibleAfterRestore": isVisible(info.ChildHWND),
		"createsBefore":            before.creates,
		"createsAfter":             after.creates,
		"closesBefore":             before.closes,
		"closesAfter":              after.closes,
		"attachesBefore":           before.attaches,
		"attachesAfter":            after.attaches,
		"visibilityCallsBefore":    before.visible,
		"visibilityCallsAfter":     after.visible,
		"counterBefore":            counterBefore,
		"counterAfter":             counterAfter,
	})
}

// observeUI04OcclusionFacts 从 occlusion-steps.jsonl / stop-button.json 读回事实。
func observeUI04OcclusionFacts(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	rows := observationReadJSONL(t, "occlusion-steps.jsonl")
	var hidden, restored int
	childHWNDs := map[float64]bool{}
	createsDelta := 0.0
	var firstCreates float64 = -1
	sources := map[string]bool{}
	for _, row := range rows {
		sources[row.text(t, "source")] = true
		childHWNDs[row.num(t, "childHWND")] = true
		if firstCreates < 0 {
			firstCreates = row.num(t, "creates")
		}
		createsDelta = row.num(t, "creates") - firstCreates
		if row.flag(t, "open") {
			if !row.flag(t, "childVisible") {
				hidden++
			}
		} else if row.flag(t, "childVisible") {
			restored++
		}
		if !row.flag(t, "controllerReady") || !row.flag(t, "parentMatches") {
			t.Fatalf("occlusion-steps.jsonl 里有一次遮挡让原生视图失联：%v", row)
		}
	}

	stop := observationSection(t, "stop-button.json", "UI04.stop-button")
	slots := observationReadJSONL(t, "slot-geometry.jsonl")
	accepted := 0
	refused := 0
	for _, row := range slots {
		if row.flag(t, "accepted") {
			accepted++
			continue
		}
		refused++
		if row.text(t, "errorCode") != contracts.CodeUnsupported {
			t.Fatalf("slot-geometry.jsonl 里被拒绝的几何错误码是 %q，期望 %q",
				row.text(t, "errorCode"), contracts.CodeUnsupported)
		}
	}
	window := observationSection(t, "window-facts.json", "UI04.window")
	counterDelta := window.num(t, "counterAfter") - window.num(t, "counterBefore")

	// 观察装置自检：文件里的事实必须支持结论。
	if len(sources) != 3 {
		t.Fatalf("遮挡记录只覆盖 %d 个来源（%v），期望设置/命令面板/审批框三个", len(sources), sources)
	}
	if hidden != 3 || restored != 3 {
		t.Fatalf("隐藏/恢复次数 = %d/%d，期望 3/3（每层各一次）", hidden, restored)
	}
	if len(childHWNDs) != 1 {
		t.Fatalf("遮挡期间出现了 %d 个不同的子窗口句柄：原生视图被重建过", len(childHWNDs))
	}
	if createsDelta != 0 {
		t.Fatalf("遮挡期间 CreateTab 计数变化了 %.0f 次：tab 被重建", createsDelta)
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("slot 几何记录 = 接受 %d / 拒绝 %d，期望 1/1", accepted, refused)
	}
	if stop.flag(t, "stopPointInsideNativeChildTree") {
		t.Fatal("stop-button.json 说停止按钮位置落在原生子窗口树里")
	}
	if !stop.flag(t, "slotCenterInsideNativeChildTree") {
		t.Fatal("stop-button.json 说槽位中心不在原生子窗口树里")
	}
	if counterDelta != 1 {
		t.Fatalf("恢复后可信点击的 counter 变化 = %.0f，期望 1", counterDelta)
	}

	observationMergeFacts(t, obsFacts{
		"parentMatches":                   window.flag(t, "childDescendantOfMain") && window.flag(t, "controllerReady"),
		"childClassInstances":             1,
		"slotRevisionsAccepted":           accepted,
		"slotRevisionsRefused":            refused,
		"occlusionSources":                len(sources),
		"occlusionHiddenTransitions":      hidden,
		"occlusionRestoredTransitions":    restored,
		"occlusionDistinctChildHWND":      len(childHWNDs),
		"occlusionCreatesDelta":           createsDelta,
		"stopPointInsideNativeChildTree":  stop.flag(t, "stopPointInsideNativeChildTree"),
		"slotCenterInsideNativeChildTree": stop.flag(t, "slotCenterInsideNativeChildTree"),
		"postRestoreCounterDelta":         counterDelta,
	})
}

// observeUI04HandOverFacts 从 handover.jsonl 读回交接事实。
func observeUI04HandOverFacts(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	rows := observationReadJSONL(t, "handover.jsonl")
	byStep := map[string]obsFacts{}
	for _, row := range rows {
		byStep[row.text(t, "step")] = row
	}
	for _, step := range []string{"agent-fill", "takeover", "human-edit", "agent-observed-human-edit", "submit"} {
		if _, ok := byStep[step]; !ok {
			t.Fatalf("handover.jsonl 缺少步骤 %q（现有 %d 行）", step, len(rows))
		}
	}
	agentValue := byStep["agent-fill"].text(t, "value")
	humanValue := byStep["human-edit"].text(t, "value")
	observed := byStep["agent-observed-human-edit"].text(t, "value")
	payloadName := byStep["submit"].text(t, "payloadName")
	takeoverMode := byStep["takeover"].text(t, "mode")
	takeoverCode := byStep["takeover"].text(t, "agentWriteCode")
	counter := byStep["submit"].num(t, "counter")

	if !strings.HasPrefix(humanValue, agentValue) || humanValue == agentValue {
		t.Fatalf("人工修改值 %q 不是 agent 写入 %q 的用户追加", humanValue, agentValue)
	}
	if observed != humanValue {
		t.Fatalf("agent 观察到 %q，人工改的是 %q", observed, humanValue)
	}
	if payloadName != humanValue {
		t.Fatalf("服务端收到 %q，人工改的是 %q", payloadName, humanValue)
	}
	if takeoverMode != string(browser.ControlHuman) || takeoverCode != contracts.CodeNeedsHuman {
		t.Fatalf("接管事实不对：mode=%q agentWriteCode=%q", takeoverMode, takeoverCode)
	}
	if counter != 1 {
		t.Fatalf("提交后服务端 counter=%.0f，期望 1", counter)
	}

	observationMergeFacts(t, obsFacts{
		"agentFillVisibleInPage":      true,
		"humanEditValue":              humanValue,
		"humanInputPath":              byStep["human-edit"].text(t, "inputPath"),
		"agentObservedHumanEdit":      byStep["agent-observed-human-edit"].flag(t, "verified"),
		"takeoverMode":                takeoverMode,
		"agentWriteRejectedCode":      takeoverCode,
		"submitCounter":               counter,
		"submittedPayloadEqualsHuman": payloadName == humanValue,
	})
}

// 编译期断言：ui04Host 必须同时满足 backend 与 surface（否则接不进 bridge/service）。
var (
	_ browser.BrowserBackend = (*ui04Host)(nil)
	_ browser.BrowserSurface = (*ui04Host)(nil)
)
