package app

// B07：右栏原生 slot / 遮挡协调 / 接管的绑定层（UI04.native 的应用侧那一半）。
//
// 这里守的是四件"看起来能用其实会烂掉"的事：
//
//  1. **几何去重与 revision**。React 每帧都可能上报几何；旧 revision 迟到必须丢弃，
//     相同几何不能反复 Present（否则拖动分栏时原生视图会抖）。
//  2. **停止按钮永远在原生网页之外**。slot 的 top 小于工具条高度时，宁可拒绝这次
//     几何也不能把原生视图盖到停止按钮上。
//  3. **遮挡 ≠ 暂停**。设置 dialog / 命令面板 / 审批框依次弹出时只隐藏原生视图，
//     不接管、不关闭 tab、不动控制代数；隐藏期间 agent 的写动作照样派发。
//  4. **React 拿不到原生句柄**。BrowserState 必须能 JSON 序列化，且字段里没有
//     hwnd/handle 之类的东西。
//
// 本文件用 scripted backend + fake surface：布局与协调逻辑与平台无关。
// 真实 WebView2 的窗口事实、遮挡转换与挂载计数由 tests/native 的 UI04.native 产出，
// 这里不冒充原生证据。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// ---------- 替身 ----------

// fakeSurface 记录每一次 Present/SetVisible。
//
// 它同时是"挂载计数"的观察点：遮挡/切换 tab 时 Present 与 SetVisible 的调用序列
// 必须能解释，不能"看起来隐藏了"。
type fakeSurface struct {
	mu          sync.Mutex
	attaches    int
	presents    []browser.SlotGeometry
	presentTabs []browser.TabID
	visibles    []bool
	detaches    int
	presentErr  error
	visibleErr  error
}

func (f *fakeSurface) Attach(context.Context, browser.NativeParent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attaches++
	return nil
}

func (f *fakeSurface) Present(_ context.Context, tab browser.TabID, g browser.SlotGeometry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.presentErr != nil {
		return f.presentErr
	}
	f.presents = append(f.presents, g)
	f.presentTabs = append(f.presentTabs, tab)
	return nil
}

func (f *fakeSurface) SetVisible(_ context.Context, visible bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.visibleErr != nil {
		return f.visibleErr
	}
	f.visibles = append(f.visibles, visible)
	return nil
}

func (f *fakeSurface) Detach(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detaches++
	return nil
}

func (f *fakeSurface) snapshot() (presents []browser.SlotGeometry, visibles []bool, tabs []browser.TabID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]browser.SlotGeometry(nil), f.presents...),
		append([]bool(nil), f.visibles...),
		append([]browser.TabID(nil), f.presentTabs...)
}

// allowAllBrowserAuthorizer 是"用户已批准"的替身：本文件不测授权，只测绑定层。
type allowAllBrowserAuthorizer struct{ guard browser.TokenGuard }

func (a *allowAllBrowserAuthorizer) Authorize(_ context.Context, exec contracts.ExecutionContext, req browser.AuthorizationRequest) (browser.AuthorizationToken, error) {
	return a.guard.Issue(exec.SessionID, req, "apr-b07", "B07 测试许可"), nil
}

func (a *allowAllBrowserAuthorizer) Recheck(_ context.Context, _ contracts.ExecutionContext, _ browser.AuthorizationRequest, _ browser.AuthorizationToken) error {
	return nil
}

// browserEnv 是一套绑定层环境：bridge + scripted backend + fake surface + service。
type browserEnv struct {
	bridge  *BrowserBridge
	surface *fakeSurface
	backend *browserfixture.ScriptedBackend
	svc     *browser.Service
	exec    contracts.ExecutionContext
}

func newBrowserEnv(t *testing.T) *browserEnv {
	t.Helper()
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	backend := browserfixture.NewScriptedBackend(fixture)
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend:       backend,
		Authorizer:    &allowAllBrowserAuthorizer{guard: browser.TokenGuard{TTL: time.Minute}},
		ActionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	surface := &fakeSurface{}
	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-b07", SessionID: "sess-b07", RunID: "run-b07",
		ConfigRevision: 1, ControlGeneration: 1,
	}
	bridge := NewBrowserBridge(BrowserBridgeOptions{
		Surface: surface,
		Service: svc,
		Exec:    exec,
	})
	return &browserEnv{bridge: bridge, surface: surface, backend: backend, svc: svc, exec: exec}
}

// slotAt 造一份几何（top 默认落在工具条之下）。
func slotAt(revision uint64, top, width, height float64, visible bool) browser.SlotGeometry {
	return browser.SlotGeometry{
		SlotID: "browser-slot", Revision: revision,
		Left: 640, Top: top, Width: width, Height: height,
		Visible: visible, Zoom: 1,
	}
}

// ---------- 几何：revision、去重、工具条保留区 ----------

func TestBrowserSlotDropsStaleRevisionAndDedupesIdenticalGeometry(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge

	if err := b.UpdateSlot(slotAt(1, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("revision 1 被拒绝: %v", err)
	}
	if err := b.UpdateSlot(slotAt(2, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("revision 2 被拒绝: %v", err)
	}
	if err := b.UpdateSlot(slotAt(3, BrowserChromeReservedTop, 480, 640, true)); err != nil {
		t.Fatalf("revision 3 被拒绝: %v", err)
	}
	// 迟到的旧 revision：不能把视图拽回旧位置。
	if err := b.UpdateSlot(slotAt(2, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("迟到的 revision 2 不应报错（丢弃是正常结局）: %v", err)
	}

	presents, _, _ := env.surface.snapshot()
	if len(presents) != 2 {
		t.Fatalf("Present 次数 = %d，期望 2（revision 2 与 revision 3 几何相同应去重）", len(presents))
	}
	if got := presents[len(presents)-1].Height; got != 640 {
		t.Fatalf("最后一次 Present 的高度 = %v，期望 640（迟到的旧 revision 把视图拽回去了）", got)
	}

	st := b.State()
	if st.Slot.Revision != 3 {
		t.Fatalf("state.slot.revision = %d，期望 3", st.Slot.Revision)
	}
	if st.Counters.SlotDropped != 1 {
		t.Fatalf("丢弃的旧 revision = %d，期望 1", st.Counters.SlotDropped)
	}
	if st.Counters.SlotDeduped != 1 {
		t.Fatalf("去重的相同几何 = %d，期望 1", st.Counters.SlotDeduped)
	}
	if st.Counters.SlotPresented != 2 {
		t.Fatalf("实际 Present 计数 = %d，期望 2", st.Counters.SlotPresented)
	}
}

func TestBrowserSlotNeverCoversChromeOrStopButton(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge

	// top 落在工具条里 = 原生网页会盖住地址栏/停止按钮。
	err := b.UpdateSlot(slotAt(1, BrowserChromeReservedTop-20, 480, 600, true))
	if err == nil {
		t.Fatal("覆盖工具条的几何被接受了：停止按钮会被原生网页盖住")
	}
	if code := errorCodeOf(err); code != contracts.CodeUnsupported {
		t.Fatalf("错误码 = %q，期望 %q（%v）", code, contracts.CodeUnsupported, err)
	}
	presents, _, _ := env.surface.snapshot()
	if len(presents) != 0 {
		t.Fatalf("被拒绝的几何仍调用了 %d 次 Present", len(presents))
	}
	if b.State().HasSlot {
		t.Fatal("被拒绝的几何被记成了当前 slot")
	}

	// 同样的高度，但落到保留区之下：接受并 Present 一次。
	if err := b.UpdateSlot(slotAt(2, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("合规几何被拒绝: %v", err)
	}
	presents, visibles, _ := env.surface.snapshot()
	if len(presents) != 1 {
		t.Fatalf("Present 次数 = %d，期望 1", len(presents))
	}
	if presents[0].Top != BrowserChromeReservedTop {
		t.Fatalf("Present 的 top = %v，期望 %v", presents[0].Top, BrowserChromeReservedTop)
	}
	if len(visibles) != 1 || !visibles[0] {
		t.Fatalf("可见性调用 = %v，期望一次 true（首次上报要确立可见状态）", visibles)
	}
}

// ---------- 遮挡：隐藏/恢复，且不动 tab 与控制权 ----------

func TestBrowserOcclusionHidesAndRestoresWithoutTouchingTabs(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge
	ctx := context.Background()

	tab, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.backend.Fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if err := b.SelectTab(string(tab)); err != nil {
		t.Fatalf("SelectTab: %v", err)
	}
	if err := b.UpdateSlot(slotAt(1, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("UpdateSlot: %v", err)
	}
	before, err := env.svc.TabInfo(env.exec, tab)
	if err != nil {
		t.Fatalf("TabInfo: %v", err)
	}
	creates := env.backend.CreateCalls()

	// 三个浮层依次弹：设置 dialog → 命令面板 → 审批框。
	steps := []struct {
		source string
		open   bool
	}{
		{"settings-dialog", true},
		{"command-palette", true},
		{"settings-dialog", false},
		{"approval-dialog", true},
		{"command-palette", false},
		{"approval-dialog", false},
	}
	for _, step := range steps {
		if err := b.SetOverlay(step.source, step.open); err != nil {
			t.Fatalf("SetOverlay(%s,%t): %v", step.source, step.open, err)
		}
	}

	_, visibles, _ := env.surface.snapshot()
	if len(visibles) != 3 {
		// 首次 Present 确立可见(true) + 打开浮层隐藏(false) + 全部关闭恢复(true)
		t.Fatalf("可见性调用 = %v，期望 [true false true]（重复隐藏不应重复调用）", visibles)
	}
	if !(visibles[0] && !visibles[1] && visibles[2]) {
		t.Fatalf("可见性调用 = %v，期望 [true false true]", visibles)
	}

	if got := env.backend.CreateCalls(); got != creates {
		t.Fatalf("遮挡期间新建了 tab：CreateCalls %d → %d", creates, got)
	}
	after, err := env.svc.TabInfo(env.exec, tab)
	if err != nil {
		t.Fatalf("TabInfo(遮挡后): %v", err)
	}
	if after.NavigationID != before.NavigationID || after.ControlGeneration != before.ControlGeneration {
		t.Fatalf("遮挡改变了 tab 事实：before=%+v after=%+v", before, after)
	}
	if after.Mode != browser.ControlAgent {
		t.Fatalf("遮挡把控制状态改成了 %q，期望 %q", after.Mode, browser.ControlAgent)
	}

	st := b.State()
	if !st.Visible {
		t.Fatal("所有浮层已关闭，state.visible 仍是 false（原生网页没有恢复）")
	}
	if !st.RequestedVisible {
		t.Fatal("requestedVisible 应为 true（面板自己没有被关闭）")
	}
	if len(st.Overlays) != 0 {
		t.Fatalf("浮层集合应为空，实际 %v", st.Overlays)
	}
	if len(st.Tabs) != 1 || !st.Tabs[0].Active {
		t.Fatalf("遮挡改变了 tab 列表：%+v", st.Tabs)
	}
	if st.Counters.TabsClosed != 0 {
		t.Fatalf("遮挡关闭了 tab：%+v", st.Counters)
	}
}

func TestBrowserOcclusionDoesNotPauseAgentWrites(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge
	ctx := context.Background()

	tab, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.backend.Fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if err := b.UpdateSlot(slotAt(1, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("UpdateSlot: %v", err)
	}
	snap, err := env.svc.Observe(ctx, env.exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	ref := env.backend.RefOf(tab, "name")
	if ref == "" {
		t.Fatalf("快照里没有名字输入框：%+v", snap.Elements)
	}
	before := b.State().Control
	if before.Mode != string(browser.ControlAgent) {
		t.Fatalf("初始控制状态 = %q，期望 agent", before.Mode)
	}

	// 浮层弹出（原生网页被隐藏），随后 agent 写动作必须照常派发。
	if err := b.SetOverlay("approval-dialog", true); err != nil {
		t.Fatalf("SetOverlay: %v", err)
	}
	res, err := env.svc.Execute(ctx, env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: ref, Text: "面板隐藏期间写入",
	})
	if err != nil {
		t.Fatalf("遮挡期间的写动作被拒绝：%v（隐藏面板不能暂停 agent）", err)
	}
	if !res.Delivered {
		t.Fatal("遮挡期间的写动作没有被派发")
	}
	if got := env.backend.Field(tab, "name"); got != "面板隐藏期间写入" {
		t.Fatalf("字段值 = %q，期望「面板隐藏期间写入」", got)
	}
	after := b.State().Control
	if after.Generation != before.Generation || after.Mode != before.Mode {
		t.Fatalf("遮挡改动了控制状态：before=%+v after=%+v", before, after)
	}
}

func TestBrowserOverlaySourceMustBeNamed(t *testing.T) {
	env := newBrowserEnv(t)
	for _, source := range []string{"", "   "} {
		if err := env.bridge.SetOverlay(source, true); err == nil {
			t.Fatalf("空浮层来源 %q 被接受：遮挡协调器会退化成「一律隐藏」", source)
		}
	}
	if got := env.bridge.State().Overlays; len(got) != 0 {
		t.Fatalf("浮层集合 = %v，期望空", got)
	}
}

// ---------- tab 选择：不重建 tab，不改几何 ----------

func TestBrowserSelectTabDoesNotRecreateTabsOrSlot(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge
	ctx := context.Background()

	tabA, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.backend.Fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab(A): %v", err)
	}
	tabB, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.backend.Fixture.Secondary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab(B): %v", err)
	}
	if err := b.UpdateSlot(slotAt(1, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("UpdateSlot: %v", err)
	}

	if err := b.SelectTab(string(tabA)); err != nil {
		t.Fatalf("SelectTab(A): %v", err)
	}
	if err := b.SelectTab(string(tabA)); err != nil {
		t.Fatalf("SelectTab(A) 重复: %v", err)
	}
	presents, _, tabs := env.surface.snapshot()
	if len(presents) != 2 {
		// 初始几何 1 次 + 首次选中 tab 1 次；重复选中同一 tab 不再 Present。
		t.Fatalf("Present 次数 = %d，期望 2（重复选中同一 tab 不应重摆）", len(presents))
	}
	if tabs[len(tabs)-1] != tabA {
		t.Fatalf("Present 的 tab = %q，期望 %q", tabs[len(tabs)-1], tabA)
	}

	if err := b.SelectTab(string(tabB)); err != nil {
		t.Fatalf("SelectTab(B): %v", err)
	}
	presents, _, _ = env.surface.snapshot()
	if len(presents) != 3 {
		t.Fatalf("Present 次数 = %d，期望 3", len(presents))
	}
	if presents[len(presents)-1].Revision != 1 {
		t.Fatalf("切换 tab 时几何 revision = %d，期望仍是 1（没有重建 slot）",
			presents[len(presents)-1].Revision)
	}
	if got := env.backend.CreateCalls(); got != 2 {
		t.Fatalf("切换 tab 期间 CreateCalls = %d，期望 2（不允许重建 tab）", got)
	}
	if st := b.State(); st.ActiveTab != string(tabB) {
		t.Fatalf("activeTab = %q，期望 %q", st.ActiveTab, tabB)
	}

	// 未知 tab：明确拒绝，且不改变当前选中。
	err = b.SelectTab("scripted-tab-999")
	if err == nil {
		t.Fatal("选中不存在的 tab 被接受了")
	}
	if st := b.State(); st.ActiveTab != string(tabB) {
		t.Fatalf("失败的选择改动了 activeTab：%q", st.ActiveTab)
	}
}

// ---------- 接管 / 恢复 ----------

func TestBrowserTakeOverResumeWrappersKeepControlStateMachine(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge
	ctx := context.Background()

	tab, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.backend.Fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if err := b.SelectTab(string(tab)); err != nil {
		t.Fatalf("SelectTab: %v", err)
	}

	cs, err := b.TakeOver(ctx, string(tab))
	if err != nil {
		t.Fatalf("TakeOver: %v", err)
	}
	if cs.Mode != browser.ControlHuman {
		t.Fatalf("接管后状态 = %q，期望 human", cs.Mode)
	}
	if got := b.State().Control.Mode; got != string(browser.ControlHuman) {
		t.Fatalf("state.control.mode = %q，期望 human", got)
	}

	// 人工控制期间 agent 写动作必须被拒绝（未派发任何输入）。
	_, err = env.svc.Execute(ctx, env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: "snap-x", Ref: "r1", Text: "不该写入",
	})
	if code := errorCodeOf(err); code != contracts.CodeNeedsHuman {
		t.Fatalf("人工控制期间写动作错误码 = %q，期望 %q（%v）", code, contracts.CodeNeedsHuman, err)
	}

	// 没有接管后的新快照就恢复：必须 STALE_SNAPSHOT。
	if _, err := b.Resume(ctx, string(tab)); errorCodeOf(err) != contracts.CodeStaleSnapshot {
		t.Fatalf("无新快照恢复的错误码 = %q，期望 %q（%v）",
			errorCodeOf(err), contracts.CodeStaleSnapshot, err)
	}
	if got := b.State().Control.Mode; got != string(browser.ControlHuman) {
		t.Fatalf("恢复失败后状态 = %q，期望仍是 human", got)
	}

	if _, err := env.svc.Observe(ctx, env.exec, tab, browser.ObserveOptions{}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	cs, err = b.Resume(ctx, string(tab))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if cs.Mode != browser.ControlResuming {
		t.Fatalf("恢复后状态 = %q，期望 resuming", cs.Mode)
	}
	if got := b.State().Control.Mode; got != string(browser.ControlResuming) {
		t.Fatalf("state.control.mode = %q，期望 resuming", got)
	}
}

// ---------- 地址栏：导航 / 回退 / 刷新 ----------

func TestBrowserNavigateBackAndReloadKeepPanelHistory(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge
	ctx := context.Background()

	first := env.backend.Fixture.Primary.URL + "/"
	second := env.backend.Fixture.Primary.URL + "/form"
	tab, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: first})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if err := b.SelectTab(string(tab)); err != nil {
		t.Fatalf("SelectTab: %v", err)
	}

	if err := b.Navigate(ctx, string(tab), second); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if got := env.backend.URL(tab); got != second {
		t.Fatalf("后端 URL = %q，期望 %q", got, second)
	}
	if st := b.State(); len(st.History) != 2 || st.History[len(st.History)-1] != second {
		t.Fatalf("地址历史 = %v，期望以 %q 结尾且含两条", st.History, second)
	}

	if err := b.Back(ctx, string(tab)); err != nil {
		t.Fatalf("Back: %v", err)
	}
	if got := env.backend.URL(tab); got != first {
		t.Fatalf("回退后 URL = %q，期望 %q", got, first)
	}
	if st := b.State(); len(st.History) != 1 {
		t.Fatalf("回退后地址历史 = %v，期望剩一条", st.History)
	}

	// 已经到最早一条：回退必须明确拒绝，不能假装导航。
	if err := b.Back(ctx, string(tab)); errorCodeOf(err) != contracts.CodeUnsupported {
		t.Fatalf("空历史回退的错误码 = %q，期望 %q（%v）",
			errorCodeOf(err), contracts.CodeUnsupported, err)
	}

	// 刷新 = 重新导航到当前 URL（应用层没有引擎级 reload，见 browser_api.go 注释）。
	if err := b.Reload(ctx, string(tab)); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := env.backend.URL(tab); got != first {
		t.Fatalf("刷新后 URL = %q，期望仍是 %q", got, first)
	}
}

// ---------- 状态 DTO：可序列化、不含句柄 ----------

func TestBrowserStateIsSerializableAndCarriesNoNativeHandle(t *testing.T) {
	env := newBrowserEnv(t)
	b := env.bridge
	ctx := context.Background()

	tab, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.backend.Fixture.Primary.URL + "/"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if err := b.SelectTab(string(tab)); err != nil {
		t.Fatalf("SelectTab: %v", err)
	}
	if err := b.UpdateSlot(slotAt(7, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("UpdateSlot: %v", err)
	}

	st := b.State()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("BrowserState 无法序列化: %v", err)
	}
	lower := strings.ToLower(string(raw))
	for _, banned := range []string{"handle", "hwnd", "nativeparent", "0x"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("BrowserState 里出现了 %q：React 只该拿到事件与状态，不该拿到原生句柄（%s）",
				banned, raw)
		}
	}
	if !st.Available {
		t.Fatal("有 surface 时 available 应为 true")
	}
	if len(st.Tabs) != 1 || st.Tabs[0].TabID != string(tab) || !st.Tabs[0].Active {
		t.Fatalf("tabs = %+v，期望一条且是当前 tab", st.Tabs)
	}
	if st.Capabilities.Platform == "" {
		t.Fatalf("capabilities 缺失: %+v", st.Capabilities)
	}
	if st.ChromeReservedTop != BrowserChromeReservedTop {
		t.Fatalf("chromeReservedTop = %v，期望 %v（前端用它算 slot top）",
			st.ChromeReservedTop, BrowserChromeReservedTop)
	}
	if st.Slot.Top < st.ChromeReservedTop {
		t.Fatalf("slot.top = %v 落进了工具条保留区 %v", st.Slot.Top, st.ChromeReservedTop)
	}
}

// ---------- 绑定层转发：没有原生宿主时如实报 Unavailable ----------

func TestDesktopBrowserBindingsReportUnavailableWithoutNativeHost(t *testing.T) {
	d, _ := newTestDesktop(t)

	st := d.BrowserState()
	if st.Available {
		t.Fatal("没有挂原生 host 时 available 应为 false")
	}
	if st.Error != nil {
		t.Fatalf("BrowserState 不该用 error 表达「没有宿主」：%+v", st.Error)
	}
	if len(st.Unsupported) == 0 {
		t.Fatal("没有宿主时必须给出具体的 Unsupported 原因，不能只是空白面板")
	}
	found := false
	for _, u := range st.Unsupported {
		if u.Capability == "surface" && strings.TrimSpace(u.Reason) != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Unsupported 里缺少 surface 的具体原因：%+v", st.Unsupported)
	}

	// 几何上报本身不是动作：没有宿主也要如实记下来（面板位置是真的），不报错。
	if err := d.UpdateBrowserSlot(slotAt(1, BrowserChromeReservedTop, 480, 600, true)); err != nil {
		t.Fatalf("没有宿主时上报几何报错: %+v", err)
	}
	if st := d.BrowserState(); !st.HasSlot || st.Slot.Width != 480 {
		t.Fatalf("上报的几何没有被记下来：%+v", st.Slot)
	}

	// 浮层状态照样要记：这样前端能解释「为什么原生视图没出现」。
	if err := d.SetBrowserOverlay("settings-dialog", true); err != nil {
		t.Fatalf("SetBrowserOverlay: %+v", err)
	}
	st = d.BrowserState()
	if len(st.Overlays) != 1 || st.Overlays[0] != "settings-dialog" {
		t.Fatalf("overlays = %v，期望 [settings-dialog]", st.Overlays)
	}
	if st.Visible {
		t.Fatal("设置了 Visible=true 的几何且有浮层时，visible 应为 false")
	}

	// 没有浏览器服务：选中/接管必须明确失败，不能默默成功。
	if err := d.SelectBrowserTab("tab-1"); err == nil || err.Code == "" {
		t.Fatalf("没有浏览器服务时 SelectBrowserTab 应返回结构化错误，实际 %v", err)
	}
	if res := d.TakeOverBrowserTab("tab-1"); res.Error == nil {
		t.Fatalf("没有浏览器服务时 TakeOverBrowserTab 应返回结构化错误，实际 %+v", res)
	}
}

// errorCodeOf 把任意错误里的契约错误码取出来（空串表示不是契约错误）。
func errorCodeOf(err error) string {
	if err == nil {
		return ""
	}
	var appErr *contracts.AppError
	if ok := asAppError(err, &appErr); ok {
		return appErr.Code
	}
	return ""
}

// asAppError 是 errors.As 的小包装，避免测试里到处 import errors。
func asAppError(err error, target **contracts.AppError) bool {
	for err != nil {
		if ae, ok := err.(*contracts.AppError); ok {
			*target = ae
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---------- 面板跟随当前 run（W01） ----------

// TestBrowserBridgeExecProviderFollowsCurrentRun 守住“面板跟随当前 run”。
//
// 产品只有一个 Desktop、一个浏览器服务，而 tab 的归属是**会话**：面板如果一直用
// 启动那一刻的执行身份去查，agent 在它自己的会话里开的 tab 就永远不出现——
// 面板会说“没有 tab”，而右侧其实有页面。这里用两条会话把这件事钉住：
// 固定身份看不到别人会话的 tab，身份切换后才看得到，且 tab 动作走的是同一个身份。
func TestBrowserBridgeExecProviderFollowsCurrentRun(t *testing.T) {
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	backend := browserfixture.NewScriptedBackend(fixture)
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend:       backend,
		Authorizer:    &allowAllBrowserAuthorizer{guard: browser.TokenGuard{TTL: time.Minute}},
		ActionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	panelExec := contracts.ExecutionContext{WorkspaceID: "ws-b07", SessionID: "sess-panel", ControlGeneration: 1}
	runExec := contracts.ExecutionContext{WorkspaceID: "ws-b07", SessionID: "sess-run", ControlGeneration: 1}
	current := panelExec

	bridge := NewBrowserBridge(BrowserBridgeOptions{
		Surface:      &fakeSurface{},
		Service:      svc,
		Exec:         panelExec,
		ExecProvider: func() contracts.ExecutionContext { return current },
	})

	ctx := context.Background()
	// agent 在它自己的会话里开 tab（和 MCP 工具调用走同一条服务路径）。
	tab, err := svc.OpenTab(ctx, runExec, browser.TabOptions{URL: fixture.Primary.URL + "/form"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}

	if got := bridge.State().Tabs; len(got) != 0 {
		t.Fatalf("身份还是面板自己的会话时不该看到 agent 的 tab，实际 %d 条", len(got))
	}

	// 下一轮 run：身份切到那个会话，面板就该看到同一个 tab。
	current = runExec
	state := bridge.State()
	if len(state.Tabs) != 1 || state.Tabs[0].TabID != string(tab) {
		t.Fatalf("切换身份后应看到 agent 的 tab %q，实际 %+v", tab, state.Tabs)
	}
	if state.ActiveTab != string(tab) {
		t.Fatalf("唯一的 tab 应当成为 activeTab，实际 %q", state.ActiveTab)
	}

	// tab 动作也走当前身份：身份没跟上时这里会是 NOT_FOUND/UNSUPPORTED。
	if err := bridge.SelectTab(string(tab)); err != nil {
		t.Fatalf("SelectTab: %v", err)
	}
	if err := bridge.CloseTab(ctx, string(tab)); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	if got := bridge.State().Tabs; len(got) != 0 {
		t.Fatalf("关闭后不该再有 tab，实际 %+v", got)
	}
}

// TestBrowserExecutionContextFollowsRuntimeSession 守住绑定层给出的身份来源。
//
// 面板身份取的是“最近一次构建的 run 会话”（与 RunService.Start 拿到的同一个值），
// 而不是进程启动那一刻的快照：否则第一次 run 之后面板就一直是错的。
func TestBrowserExecutionContextFollowsRuntimeSession(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 还没有跑过任何一轮：会话为空。这是“真的没有 tab”，不是读不到。
	if got := d.BrowserExecutionContext(); got.SessionID != "" {
		t.Fatalf("未运行时不应凭空给出会话，实际 %q", got.SessionID)
	}
	if got := d.BrowserExecutionContext(); got.WorkspaceID != defaultWorkspaceID {
		t.Fatalf("工作区身份应为 %q，实际 %q", defaultWorkspaceID, got.WorkspaceID)
	}

	d.mu.Lock()
	d.runtimeSessionID = "sess-run-7"
	d.mu.Unlock()

	if got := d.BrowserExecutionContext(); got.SessionID != "sess-run-7" {
		t.Fatalf("应跟随当前 run 的会话，实际 %q", got.SessionID)
	}
}

// TestResolveBrowserApprovalReturnsCallError 守绑定层的返回形状。
//
// 为什么不是裸 error：Wails 把裸 error 压成一句消息（甚至只给 runtime 层的
// "call failed"），前端拿不到 Code，就分不清"已过期/已撤销/参数变了/没这条"。
// 用户实测"授权确认会报错"时，正是缺这个 Code 导致无从定位。
func TestResolveBrowserApprovalReturnsCallError(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 不存在的审批：结构化 NOT_FOUND，而不是抛异常。
	ce := d.ResolveBrowserApproval("apr-does-not-exist", "once")
	if ce == nil {
		t.Fatal("未知审批必须返回错误")
	}
	if ce.Code != contracts.CodeNotFound {
		t.Fatalf("错误码 = %s，期望 %s（%s）", ce.Code, contracts.CodeNotFound, ce.Message)
	}

	// 未知选项：策略层给 UNSUPPORTED，绑定层原样转成 CallError。
	st := d.browserAuthorizationState()
	if st == nil || st.pol == nil {
		t.Fatal("授权权威没有装配")
	}
}
