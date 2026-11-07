package browser_test

// B06：window.open 必须变成**同一 session 的受控 tab**（BR16）。
//
// 判据落在两处计数上，而不是"看起来打开了"：
//   - backend 的 CreateCalls/Instances：被拒绝的弹窗**没有**创建实例；
//   - 服务里登记的 tab 与 owner：新 tab 属于原 session，别的 session 连控制都拿不到。
//
// "不受控顶层窗口"这一条无法在进程内直接观察窗口，所以用"登记一致性"表达：
// backend 的存活实例数必须等于服务登记的 tab 数，弹窗 tab 必须在 svc.Tabs(owner) 里
// 且能被 TabInfo/Control 找到；否则就是有人在服务之外开了一个窗口。

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// optsRecorder 包住 scripted backend，记下每次 CreateTab 的入参。
//
// 为什么需要它：BR16 要求"popup 继承 session/profile"，而这两项只出现在
// CreateTab 的入参里。scripted backend 不暴露入参，包一层比改 fixture 更克制
// （fixture 是 B00–B07 共用的，B06 不该为了自己的断言改它）。
type optsRecorder struct {
	browser.BrowserBackend

	mu   sync.Mutex
	opts []browser.TabOptions
}

func (r *optsRecorder) CreateTab(ctx context.Context, opts browser.TabOptions) (browser.TabID, error) {
	r.mu.Lock()
	r.opts = append(r.opts, opts)
	r.mu.Unlock()
	return r.BrowserBackend.CreateTab(ctx, opts)
}

func (r *optsRecorder) recorded() []browser.TabOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]browser.TabOptions, len(r.opts))
	copy(out, r.opts)
	return out
}

type popupEnv struct {
	svc     *browser.Service
	rec     *optsRecorder
	backend *browserfixture.ScriptedBackend
	fixture *browserfixture.Fixture
	execA   contracts.ExecutionContext
	execB   contracts.ExecutionContext
}

func newPopupEnv(t *testing.T) *popupEnv {
	t.Helper()
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	backend := browserfixture.NewScriptedBackend(fixture)
	rec := &optsRecorder{BrowserBackend: backend}
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend: rec, Authorizer: newAuthorizer(time.Minute), ActionTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return &popupEnv{
		svc: svc, rec: rec, backend: backend, fixture: fixture,
		execA: contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-a", RunID: "run-1", ConfigRevision: 1},
		execB: contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-b", RunID: "run-2", ConfigRevision: 1},
	}
}

// ---- BR16 ----

func TestPopupInheritsSessionAndTabLimit(t *testing.T) {
	env := newPopupEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	parentURL := env.fixture.Primary.URL + "/popup"
	parentTab, err := env.svc.OpenTab(ctx, env.execA, browser.TabOptions{
		URL: parentURL, ProfileID: contracts.ProfileID("profile-alpha"),
	})
	if err != nil {
		t.Fatalf("OpenTab(parent): %v", err)
	}

	var attempts []map[string]any
	record := func(step, url string, before counters, tab browser.TabID, err error) {
		attempts = append(attempts, map[string]any{
			"step": step, "url": url, "tabId": string(tab),
			"errorCode": errCode(err), "error": errText(err),
			"createCallsBefore": before.CreateCalls, "createCallsAfter": env.backend.CreateCalls(),
			"instancesBefore": before.Instances, "instancesAfter": env.backend.Instances(),
		})
	}

	targetURL := env.fixture.Primary.URL + "/popup-target"
	before := snapshotCounters(env.backend)
	popup, err := env.svc.HandleNewWindow(ctx, env.execA, browser.NewWindowRequest{
		ParentTab: parentTab, URL: targetURL, WindowFeatures: "width=800,height=600",
	})
	record("same-session-popup", targetURL, before, popup, err)
	if err != nil {
		t.Fatalf("同 session 的 window.open 被拒: %v", err)
	}
	if popup == parentTab {
		t.Fatalf("window.open 必须产生新 tab，实际复用了父 tab %s", parentTab)
	}

	info, err := env.svc.TabInfo(env.execA, popup)
	if err != nil {
		t.Fatalf("TabInfo(popup): %v", err)
	}
	if info.OwnerSessionID != env.execA.SessionID {
		t.Fatalf("popup 归属会话 %q，期望 %q（未继承 session）", info.OwnerSessionID, env.execA.SessionID)
	}
	if info.URL != targetURL {
		t.Fatalf("popup 的 URL 是 %q，期望 %q", info.URL, targetURL)
	}
	opts := env.rec.recorded()
	last := opts[len(opts)-1]
	if last.OwnerSessionID != env.execA.SessionID {
		t.Fatalf("CreateTab 的 owner 是 %q，期望 %q", last.OwnerSessionID, env.execA.SessionID)
	}
	if last.ProfileID != contracts.ProfileID("profile-alpha") {
		t.Fatalf("popup 没有继承父 tab 的 profile：CreateTab profile=%q", last.ProfileID)
	}
	if last.URL != targetURL {
		t.Fatalf("CreateTab 的 URL 是 %q，期望 %q", last.URL, targetURL)
	}

	// 可见性：A 看得见两个 tab，B 一个都看不见；B 也控制不了 popup。
	tabsA := env.svc.Tabs(env.execA)
	if len(tabsA) != 2 || !containsTab(tabsA, popup) {
		t.Fatalf("session A 的 tab 列表不是 [parent, popup]: %v", tabsA)
	}
	if tabsB := env.svc.Tabs(env.execB); len(tabsB) != 0 {
		t.Fatalf("session B 看见了 A 的 tab: %v", tabsB)
	}
	if _, err := env.svc.Control(env.execB, popup); err == nil {
		t.Fatal("session B 能拿到 popup 的控制状态：跨 session 未被拒")
	} else if code := errCode(err); code != contracts.CodeUnauthorized {
		t.Fatalf("跨 session 控制的错误码 %q，期望 UNAUTHORIZED", code)
	}

	// 弹窗关系可查（面板/工具输出与证据用同一份事实）。
	children, err := env.svc.Popups(env.execA, parentTab)
	if err != nil {
		t.Fatalf("Popups(parent): %v", err)
	}
	if len(children) != 1 || children[0] != popup {
		t.Fatalf("Popups(parent) = %v，期望 [%s]", children, popup)
	}

	// "不受控窗口"判据：backend 存活实例数 == 服务登记的 tab 数。
	if got, want := env.backend.Instances(), len(env.svc.Tabs(env.execA))+len(env.svc.Tabs(env.execB)); got != want {
		t.Fatalf("backend 存活实例 %d 与服务登记 tab %d 不一致：存在服务之外创建的窗口", got, want)
	}

	// 跨 session 的 window.open：父 tab 不属于 B，必须拒绝且不创建实例。
	before = snapshotCounters(env.backend)
	tabCross, err := env.svc.HandleNewWindow(ctx, env.execB, browser.NewWindowRequest{
		ParentTab: parentTab, URL: targetURL,
	})
	record("cross-session-popup", targetURL, before, tabCross, err)
	if err == nil {
		t.Fatalf("session B 用 A 的父 tab 打开了弹窗：%s", tabCross)
	}
	if code := errCode(err); code != contracts.CodeUnauthorized {
		t.Fatalf("跨 session 弹窗错误码 %q，期望 UNAUTHORIZED（消息: %s）", code, err.Error())
	}
	if env.backend.CreateCalls() != before.CreateCalls {
		t.Fatalf("被拒的跨 session 弹窗仍创建了实例：CreateCalls %d → %d", before.CreateCalls, env.backend.CreateCalls())
	}

	// 非 http(s) 的 window.open：拒绝，不创建实例，也不可能变成宿主窗口。
	for _, bad := range []string{"javascript:alert(1)", "file:///C:/Windows/win.ini", ""} {
		before = snapshotCounters(env.backend)
		tabBad, err := env.svc.HandleNewWindow(ctx, env.execA, browser.NewWindowRequest{ParentTab: parentTab, URL: bad})
		record("bad-url:"+bad, bad, before, tabBad, err)
		if err == nil {
			t.Fatalf("非 http(s) 的 window.open 被接受：%q → %s", bad, tabBad)
		}
		if code := errCode(err); code != contracts.CodeUnsupported {
			t.Fatalf("%q 的错误码 %q，期望 UNSUPPORTED（消息: %s）", bad, code, err.Error())
		}
		if env.backend.CreateCalls() != before.CreateCalls {
			t.Fatalf("%q 被拒但仍创建了实例：CreateCalls %d → %d", bad, before.CreateCalls, env.backend.CreateCalls())
		}
	}

	// 限额：该 session 补到 4 个 tab，再 window.open 必须被拒且无新实例。
	for len(env.svc.Tabs(env.execA)) < browser.MaxTabsPerSession {
		if _, err := env.svc.OpenTab(ctx, env.execA, browser.TabOptions{URL: parentURL}); err != nil {
			t.Fatalf("填充到限额时失败: %v", err)
		}
	}
	before = snapshotCounters(env.backend)
	tabOver, err := env.svc.HandleNewWindow(ctx, env.execA, browser.NewWindowRequest{
		ParentTab: parentTab, URL: targetURL,
	})
	record("tab-limit-popup", targetURL, before, tabOver, err)
	if err == nil {
		t.Fatalf("第 %d 个 tab 的 window.open 未被限额拒绝: %s", browser.MaxTabsPerSession+1, tabOver)
	}
	if code := errCode(err); code != browser.CodeTabLimitExceeded {
		t.Fatalf("限额弹窗错误码 %q，期望 %q（消息: %s）", code, browser.CodeTabLimitExceeded, err.Error())
	}
	if env.backend.CreateCalls() != before.CreateCalls {
		t.Fatalf("限额弹窗仍创建了实例：CreateCalls %d → %d", before.CreateCalls, env.backend.CreateCalls())
	}
	if env.backend.Instances() != before.Instances {
		t.Fatalf("限额弹窗改变了存活实例数：%d → %d", before.Instances, env.backend.Instances())
	}
	if n := len(env.svc.Tabs(env.execA)); n != browser.MaxTabsPerSession {
		t.Fatalf("限额拒绝后 session A 的 tab 数 %d，期望 %d", n, browser.MaxTabsPerSession)
	}

	observeBR16(t, env, attempts, map[string]any{
		"parentTab":     string(parentTab),
		"popupTab":      string(popup),
		"popupOwner":    info.OwnerSessionID,
		"popupURL":      info.URL,
		"sessionATabs":  len(tabsA),
		"sessionBTabs":  len(env.svc.Tabs(env.execB)),
		"createCalls":   env.backend.CreateCalls(),
		"instances":     env.backend.Instances(),
		"maxPerSession": browser.MaxTabsPerSession,
	})
}

// browser.Counters 是本测试用来对比前后计数的快照。
type counters = struct {
	CreateCalls int
	Instances   int
}

func snapshotCounters(b *browserfixture.ScriptedBackend) counters {
	return counters{CreateCalls: b.CreateCalls(), Instances: b.Instances()}
}

func containsTab(tabs []browser.TabID, want browser.TabID) bool {
	for _, t := range tabs {
		if t == want {
			return true
		}
	}
	return false
}

// observeBR16 落盘 BR16 的 artifact：每次 window.open 的尝试 + 登记一致性 + 计数。
func observeBR16(t *testing.T, env *popupEnv, attempts []map[string]any, facts map[string]any) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	rows := make([]any, 0, len(attempts))
	for i, a := range attempts {
		row := map[string]any{"seq": i + 1}
		for k, v := range a {
			row[k] = v
		}
		rows = append(rows, row)
	}
	browserObsWrite(t, "popups.jsonl", mustJSONL(t, rows))

	tabs := []map[string]any{}
	for _, exec := range []contracts.ExecutionContext{env.execA, env.execB} {
		for _, tab := range env.svc.Tabs(exec) {
			info, err := env.svc.TabInfo(exec, tab)
			if err != nil {
				t.Fatalf("TabInfo(%s): %v", tab, err)
			}
			tabs = append(tabs, map[string]any{
				"tabId": string(info.TabID), "ownerSessionId": string(info.OwnerSessionID),
				"url": info.URL, "navigationId": info.NavigationID, "mode": string(info.Mode),
			})
		}
	}
	// 服务侧时间线里必须能看到弹窗被接受/被拒的记录（证据与 UI 读同一份事实）。
	timeline := []any{}
	for _, e := range env.svc.Timeline() {
		if e.Kind == browser.TimelinePopupOpened || e.Kind == browser.TimelinePopupDenied ||
			e.Kind == browser.TimelineTabLimitExceeded {
			timeline = append(timeline, e)
		}
	}
	browserObsWrite(t, "tabs.json", mustJSON(t, map[string]any{
		"tabs": tabs, "timeline": timeline,
		"backendInstances": env.backend.Instances(),
		"registeredTabs":   len(tabs),
	}))
	browserObsWrite(t, "counters-popups.json", mustJSON(t, map[string]any{
		"backend": map[string]any{
			"createCalls": env.backend.CreateCalls(),
			"instances":   env.backend.Instances(),
		},
		"fixture": map[string]any{
			"primary":   env.fixture.Primary.Counters(),
			"secondary": env.fixture.Secondary.Counters(),
		},
	}))
	browserObsMerge(t, prefixFacts("BR16", facts))
}

// mustJSON 序列化并缩进；失败即让用例失败（证据写不出来不能静默）。
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化证据失败: %v", err)
	}
	return append(raw, '\n')
}

func mustJSONL(t *testing.T, rows []any) []byte {
	t.Helper()
	var out []byte
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("序列化证据行失败: %v", err)
		}
		out = append(out, raw...)
		out = append(out, '\n')
	}
	return out
}
