package harnessmcp

// B05：浏览器工具经自有 MCP + 策略服务的真实链路（BR13、SEC03.browser、SEC04.browser）。
//
// 链路是完整的：MCP HTTP 请求 → tools/call → browser.Service → Authorize →
// scripted backend → fixture 服务端的真实 HTTP 写端点。
// 判定落在 **fixture 服务端 counter** 上：策略拒绝/过期/站点变更之后它必须仍是 0。
//
// 为什么不用 mock 的 MCP：SEC03 的口径是"未覆盖通道禁用且实际不能调用"，
// 只有真的起服务、真的发 tools/call，才谈得上"到达/未到达 handler"。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// policyHarness 是"浏览器工具 + 真策略服务"的一套环境。
type policyHarness struct {
	browser    *browser.Service
	backend    *browserfixture.ScriptedBackend
	fixture    *browserfixture.Fixture
	policy     *policy.Service
	authorizer *BrowserPolicyAuthorizer
	mcp        *harness
	exec       contracts.ExecutionContext
	tab        browser.TabID
	snap       browser.Snapshot
	approvals  []policy.ApprovalRequest
	// calls 是待审批调用的原始形参：答复时用它校验指纹（与 policybridge 同一口径），
	// Manual 用例靠它把"用户答复"精确地打在某一条审批上。
	calls map[string]policy.ToolCall
	mu    sync.Mutex
	clock *testClock
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// policyHarnessOptions 让用例按需改策略权限与许可有效期。
type policyHarnessOptions struct {
	Network  bool
	TokenTTL time.Duration
	// ApprovalTTL 是策略审批本身的有效期（0 用 1 分钟）。过期路径必须用**真实
	// 流逝时间**验证，不能用假时钟把审批"拨"过期。
	ApprovalTTL time.Duration
	AutoAllow   bool
	DenyAll     bool
	// Manual 为 true 时不自动答复审批：用例自己决定何时、以什么结论答复。
	// 用来证明"等待期间 counter 恒为 0"与"换 origin 后必须重新审批"。
	Manual bool
	// PinnedBinding 用生产构造函数 NewBrowserPolicyAuthorizer 装配，并把执行身份
	// 钉在 run binding 上（T1 的真实接线路径）。
	PinnedBinding bool
	// BrokenAuthorizer 装一个 Gate 为空的授权器（装配错误）。
	// 它必须是"默认拒绝"，绝不能退化成放行。
	BrokenAuthorizer bool
	// NoAuthorizer 完全不装授权器：服务层必须拒绝写动作。
	NoAuthorizer bool
}

// newPolicyHarness 起 fixture + scripted backend + 真策略服务 + MCP 服务。
func newPolicyHarness(t *testing.T, opts policyHarnessOptions) *policyHarness {
	t.Helper()
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	backend := browserfixture.NewScriptedBackend(fixture)

	// 时钟不注入：许可过期的判定必须用**真实流逝时间**，否则"过期"用例会退化成
	// "把一个假时钟往前拨一下"，证明不了"排队期间许可真的失效"。
	approvalTTL := opts.ApprovalTTL
	if approvalTTL <= 0 {
		approvalTTL = time.Minute
	}
	pol := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace", Network: opts.Network},
		ApprovalTTL: approvalTTL,
	})

	ttl := opts.TokenTTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-b05", SessionID: "sess-browser", RunID: "run-browser",
		ConfigRevision: 1, ControlGeneration: 1,
	}

	h := &policyHarness{
		backend: backend, fixture: fixture, policy: pol, exec: exec,
		calls: map[string]policy.ToolCall{},
	}

	// 审批回复：默认自动批准一次（模拟用户点"允许一次"）；DenyAll 时拒绝；
	// Manual 时把结论留给用例。真实路径上这一步是 UI 通过 resolve 端点做的；
	// 这里直接调策略服务，语义相同（结论仍然只在策略服务里落地，工具参数碰不到它）。
	onPending := func(p policy.ApprovalRequest, call policy.ToolCall) {
		h.mu.Lock()
		h.approvals = append(h.approvals, p)
		h.calls[p.ID] = call
		h.mu.Unlock()
		if opts.Manual {
			return
		}
		choice := policy.ApprovalOnce
		if opts.DenyAll {
			choice = policy.ApprovalDeny
		}
		go func() {
			_ = pol.ResolveApproval(context.Background(), p.ID, choice, call)
		}()
	}

	// 三条装配路径各自代表一种真实处境：
	//   - PinnedBinding：生产接线（构造函数 + run binding 身份）；
	//   - BrokenAuthorizer：装配错误（没有策略服务），必须默认拒绝；
	//   - 其余：用例直接构造（与 B05 既有写法一致）。
	var authz *BrowserPolicyAuthorizer
	switch {
	case opts.BrokenAuthorizer:
		authz = &BrowserPolicyAuthorizer{
			ApprovalWait: 5 * time.Second, PollInterval: 2 * time.Millisecond,
		}
		authz.OnPending = onPending
	case opts.PinnedBinding:
		built, berr := NewBrowserPolicyAuthorizer(BrowserAuthorizerOptions{
			Binding:      &BrowserAuthorizationBinding{Exec: exec},
			Gate:         pol,
			Guard:        browser.TokenGuard{TTL: ttl},
			ApprovalWait: 5 * time.Second,
			PollInterval: 2 * time.Millisecond,
			OnPending:    onPending,
		})
		if berr != nil {
			t.Fatalf("NewBrowserPolicyAuthorizer: %v", berr)
		}
		authz = built
	default:
		authz = &BrowserPolicyAuthorizer{
			Gate:         pol,
			Guard:        browser.TokenGuard{TTL: ttl},
			ApprovalWait: 5 * time.Second,
			PollInterval: 2 * time.Millisecond,
		}
		authz.OnPending = onPending
	}
	h.authorizer = authz

	svcOpts := browser.ServiceOptions{
		Backend:       backend,
		ActionTimeout: 3 * time.Second,
	}
	if !opts.NoAuthorizer {
		svcOpts.Authorizer = authz
	}
	svc, err := browser.NewService(svcOpts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	h.browser = svc

	tools := BrowserTools(svc, BrowserToolsOptions{Exec: exec})
	h.mcp = newHarnessWithTools(t, t.TempDir(), tools)

	// 开一个 tab 并观察：后续用例都从这个状态出发。
	tab, err := svc.OpenTab(context.Background(), exec, browser.TabOptions{URL: fixture.Primary.URL + "/form"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	snap, err := svc.Observe(context.Background(), exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	h.tab = tab
	h.snap = snap
	return h
}

// waitApprovals 等到至少 n 条审批出现（Manual 用例用它确定"现在卡在等人工"）。
func (h *policyHarness) waitApprovals(t *testing.T, n int) []policy.ApprovalRequest {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		got := append([]policy.ApprovalRequest(nil), h.approvals...)
		h.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条审批超时", n)
	return nil
}

// approvalCall 返回待审批调用的原始形参（答复时必须原样带回去）。
func (h *policyHarness) approvalCall(t *testing.T, id string) policy.ToolCall {
	t.Helper()
	h.mu.Lock()
	call, ok := h.calls[id]
	h.mu.Unlock()
	if !ok {
		t.Fatalf("没有登记审批 %s 的原始形参", id)
	}
	return call
}

// resolveApproval 以**登记时的原始形参**答复一条审批。
func (h *policyHarness) resolveApproval(t *testing.T, id string, choice policy.ApprovalChoice) error {
	t.Helper()
	return h.policy.ResolveApproval(context.Background(), id, choice, h.approvalCall(t, id))
}

// callTool 发一次 tools/call 并解析成（文本, isError）。
func browserCall(t *testing.T, h *policyHarness, name string, args map[string]any) (string, bool) {
	t.Helper()
	code, resp := h.mcp.rpc(11, "tools/call", map[string]any{"name": name, "arguments": args})
	if code != http.StatusOK {
		t.Fatalf("tools/call %s 状态码 %d", name, code)
	}
	if resp.Error != nil {
		t.Fatalf("tools/call %s 协议错误: %+v", name, resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s 结果不是对象: %+v", name, resp.Result)
	}
	content, _ := result["content"].([]any)
	text := ""
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				text += s
			}
		}
	}
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

// browserToolsByName 取工具表里的某个工具（缺失即失败）。
func browserToolsByName(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl
		}
	}
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	t.Fatalf("工具 %s 没有注册（现有 %v）", name, names)
	return Tool{}
}

// ---------- BR13：MCP 工具经策略服务，拒绝/过期/改站点 → counter 0 ----------

func TestBrowserToolDeniedLeavesCounterZero(t *testing.T) {
	t.Run("策略拒绝", func(t *testing.T) {
		// Network=false 时策略服务直接 deny（浏览器工具属于网络能力）。
		h := newPolicyHarness(t, policyHarnessOptions{Network: false})
		ref := h.backend.RefOf(h.tab, "name")

		text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID, "ref": ref, "text": "拒绝",
		})
		if !isErr {
			t.Fatalf("策略拒绝必须是 isError: %s", text)
		}
		if !strings.Contains(text, "策略拒绝") && !strings.Contains(text, "未派发") {
			t.Fatalf("拒绝结论必须说明没有派发: %s", text)
		}
		if n := h.backend.Dispatched(); n != 0 {
			t.Fatalf("策略拒绝后 backend 派发计数 %d，期望 0", n)
		}
		if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("策略拒绝后服务端 counter=%d，期望 0", n)
		}
	})

	t.Run("用户在审批里拒绝", func(t *testing.T) {
		h := newPolicyHarness(t, policyHarnessOptions{Network: true, DenyAll: true})
		ref := h.backend.RefOf(h.tab, "name")
		text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID, "ref": ref, "text": "拒绝",
		})
		if !isErr {
			t.Fatalf("用户拒绝必须是 isError: %s", text)
		}
		if n := h.backend.Dispatched(); n != 0 {
			t.Fatalf("用户拒绝后仍派发了 %d 次", n)
		}
		if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("用户拒绝后 counter=%d，期望 0", n)
		}
	})

	t.Run("许可过期（排队期间）", func(t *testing.T) {
		// 许可有效期 500ms：第一个动作慢 700ms，第二个动作在排队期间就会过期。
		h := newPolicyHarness(t, policyHarnessOptions{Network: true, TokenTTL: 500 * time.Millisecond})
		nameRef := h.backend.RefOf(h.tab, "name")
		submitRef := h.backend.RefOf(h.tab, "submit")
		h.backend.Latency = func(kind browser.ActionKind, ref string) time.Duration {
			if kind == browser.ActionFill && ref == nameRef {
				return 700 * time.Millisecond
			}
			return 0
		}

		firstDone := make(chan struct{})
		go func() {
			_, _ = browserCall(t, h, BrowserToolAct, map[string]any{
				"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID, "ref": nameRef, "text": "慢",
			})
			close(firstDone)
		}()
		waitUntil(t, 3*time.Second, "第一个动作被派发", func() bool { return h.backend.Dispatched() == 1 })

		// 第二个动作：入队时许可有效，等到它被派发时已经过期。
		text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID, "ref": submitRef,
		})
		<-firstDone
		if !isErr {
			t.Fatalf("许可过期的动作必须失败: %s", text)
		}
		if !strings.Contains(text, "过期") && !strings.Contains(text, "复查") {
			t.Fatalf("失败结论应说明复查/过期: %s", text)
		}
		if n := h.backend.Dispatched(); n != 1 {
			t.Fatalf("过期动作被派发了（dispatched=%d，期望只有第一个）", n)
		}
		if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("过期动作产生了副作用：counter=%d，期望 0", n)
		}
	})

	t.Run("站点 origin 变更", func(t *testing.T) {
		// 换 origin 之后，旧的站点许可/审批不再成立：counter 必须保持 0。
		h := newPolicyHarness(t, policyHarnessOptions{Network: true})
		nameRef := h.backend.RefOf(h.tab, "name")
		submitRef := h.backend.RefOf(h.tab, "submit")

		// 先拿到一次正常动作（证明链路本身是通的）。
		if text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID, "ref": nameRef, "text": "允许",
		}); isErr {
			t.Fatalf("允许路径不应失败: %s", text)
		}

		// 让后面的动作慢一点，好在"已入队/已派发之间"换站点。
		h.backend.Latency = func(kind browser.ActionKind, ref string) time.Duration {
			if ref == nameRef {
				return 400 * time.Millisecond
			}
			return 0
		}
		first := make(chan struct{})
		go func() {
			_, _ = browserCall(t, h, BrowserToolAct, map[string]any{
				"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID, "ref": nameRef, "text": "慢",
			})
			close(first)
		}()
		waitUntil(t, 3*time.Second, "第一个动作被派发", func() bool { return h.backend.Dispatched() == 2 })

		// 换站点（真实导航到另一个 origin）：旧快照与旧队列立即失效。
		if err := h.browser.Navigate(context.Background(), h.exec, h.tab,
			h.fixture.Secondary.URL+"/form"); err != nil {
			t.Fatalf("Navigate: %v", err)
		}
		text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID, "ref": submitRef,
		})
		<-first
		if !isErr {
			t.Fatalf("换站点后旧快照动作必须失败: %s", text)
		}
		if n := h.fixture.Secondary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("换站点后 counter（secondary）=%d，期望 0", n)
		}
		if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("换站点期间不应提交（primary counter=%d）", n)
		}
		if n := h.backend.Dispatched(); n != 2 {
			t.Fatalf("换站点后仍派发了动作：dispatched=%d", n)
		}
		observeBrowserDenials(t, h, "origin-change")
	})
}

// TestBrowserToolDeniedOriginChangeAtPolicyLayer 直接验证"改 origin 后同一条审批失效"。
//
// 与上面的端到端用例互补：这里不用页面导航，而是把**策略层**的绑定判据单独钉住
// （origin 与 navigationId 进了审批指纹），这样未来谁改了指纹组成都会红。
func TestBrowserToolDeniedOriginChangeAtPolicyLayer(t *testing.T) {
	pol := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace", Network: true},
		ApprovalTTL: time.Minute,
	})
	exec := contracts.ExecutionContext{SessionID: "sess-site", RunID: "run-site", ConfigRevision: 1}
	makeReq := func(origin, nav string) browser.AuthorizationRequest {
		return browser.AuthorizationRequest{
			TabID: "tab-1", SessionID: exec.SessionID, Origin: origin, URL: origin + "/form",
			NavigationID: nav, Generation: 3,
			Action: browser.Action{Kind: browser.ActionClick, SnapshotID: "snap-1", Ref: "nav-3-e2"},
		}
	}
	reqA := makeReq("http://127.0.0.1:5001", "nav-3")
	callA, err := buildBrowserCall(reqA)
	if err != nil {
		t.Fatalf("buildBrowserCall: %v", err)
	}
	res, err := pol.Authorize(context.Background(), exec, callA)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision != policy.DecisionAsk {
		t.Fatalf("交互式浏览器写操作默认必须是 ask，实际 %q", res.Decision)
	}
	if err := pol.ResolveApproval(context.Background(), res.ApprovalID, policy.ApprovalOnce, callA); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	// 同一条审批 + 换站点：必须被拒（参数指纹含 origin/url/navigationId）。
	reqB := makeReq("http://127.0.0.1:5002", "nav-3")
	callB, err := buildBrowserCall(reqB)
	if err != nil {
		t.Fatalf("buildBrowserCall: %v", err)
	}
	if err := pol.ValidateBinding(context.Background(), exec, res.ApprovalID, callB); err == nil {
		t.Fatal("换 origin 之后同一条审批仍然通过了复查：站点变更没有进入绑定判据")
	}

	// 同一条审批 + 同站点但文档变了（navigationId）：同样必须被拒。
	reqC := makeReq("http://127.0.0.1:5001", "nav-9")
	callC, err := buildBrowserCall(reqC)
	if err != nil {
		t.Fatalf("buildBrowserCall: %v", err)
	}
	if err := pol.ValidateBinding(context.Background(), exec, res.ApprovalID, callC); err == nil {
		t.Fatal("换文档之后同一条审批仍然通过了复查：navigationId 没有进入绑定判据")
	}

	// 原样重放一次是正确的调用：允许（并且领取一次性执行权）。
	if err := pol.ValidateBinding(context.Background(), exec, res.ApprovalID, callA); err != nil {
		t.Fatalf("原样的调用应通过复查: %v", err)
	}
	// 再领一次必须失败：once 只领取一次（不重放）。
	if err := pol.ValidateBinding(context.Background(), exec, res.ApprovalID, callA); err == nil {
		t.Fatal("同一条 once 许可被领取了两次：动作可能被重放")
	}
}

// TestModelCannotSelfConfirm 断言模型无法在工具参数里"自己确认"或换身份。
func TestModelCannotSelfConfirm(t *testing.T) {
	h := newPolicyHarness(t, policyHarnessOptions{Network: true})
	ref := h.backend.RefOf(h.tab, "name")

	for _, tc := range []struct {
		key   string
		value any
	}{
		{"confirmed", true},
		{"approved", true},
		{"approvalId", "apr-1-abcdef"},
		{"sessionId", "sess-other"},
		{"runId", "run-other"},
		{"controlGeneration", 99},
		{"decision", "allow"},
		{"skipApproval", true},
	} {
		args := map[string]any{
			"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID, "ref": ref, "text": "自证",
			tc.key: tc.value,
		}
		text, isErr := browserCall(t, h, BrowserToolAct, args)
		if !isErr {
			t.Fatalf("参数 %q 必须被拒绝，实际成功: %s", tc.key, text)
		}
		// 拒绝必须是"这类字段不允许出现"，而不是含糊的"参数不合法"：
		// 前者告诉调用方"身份/确认只能由用户与服务端绑定给出"，后者会让模型
		// 以为只是拼写错了，然后换个字段名继续试。
		if !strings.Contains(text, tc.key) {
			t.Fatalf("拒绝结论应点名具体字段 %q: %s", tc.key, text)
		}
		if !strings.Contains(text, "确认只能由用户") {
			t.Fatalf("拒绝结论必须说明审批只能由用户给出（%q）: %s", tc.key, text)
		}
	}
	if n := h.backend.Dispatched(); n != 0 {
		t.Fatalf("自证参数产生了 %d 次派发，期望 0", n)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("自证参数产生了副作用：counter=%d", n)
	}
	observeBrowserPolicyDecisions(t, h, "self-confirm-blocked")
}

// ---------- BR13 的正向路径：observe → act(fill) → act(click) → counter=1 ----------

func TestBrowserToolsFlowThroughMCPAndPolicy(t *testing.T) {
	h := newPolicyHarness(t, policyHarnessOptions{Network: true})

	// tools/list 里必须能看到浏览器工具（Agent 真正看到的就是这段 JSON）。
	names := observeToolList(t, h.mcp, "browser-tools", map[string]any{
		"sessionId": string(h.exec.SessionID),
	})
	for _, want := range []string{BrowserToolTabs, BrowserToolOpenTab, BrowserToolObserve, BrowserToolAct} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("tools/list 里缺少 %s（实际 %v）", want, names)
		}
	}

	// browser_tabs：工具输出的 tabId/URL/navigationId 必须与"面板"（Service 记录）一致。
	text, isErr := browserCall(t, h, BrowserToolTabs, map[string]any{})
	if isErr {
		t.Fatalf("browser_tabs 失败: %s", text)
	}
	var listed struct {
		Tabs []tabFacts `json:"tabs"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatalf("browser_tabs 输出不是合法 JSON: %v（%s）", err, text)
	}
	if len(listed.Tabs) != 1 {
		t.Fatalf("期望 1 个 tab，实际 %d", len(listed.Tabs))
	}
	panel, err := h.browser.TabInfo(h.exec, h.tab)
	if err != nil {
		t.Fatalf("TabInfo: %v", err)
	}
	got := listed.Tabs[0]
	if got.TabID != panel.TabID || got.URL != panel.URL || got.NavigationID != panel.NavigationID {
		t.Fatalf("工具输出与面板不一致：tool=%+v panel=%+v", got, panel)
	}

	// 观察（模型看到的 ref）。
	observeText, isErr := browserCall(t, h, BrowserToolObserve, map[string]any{"tabId": string(h.tab)})
	if isErr {
		t.Fatalf("browser_observe 失败: %s", observeText)
	}
	var observed struct {
		Tab struct {
			NavigationID string `json:"navigationId"`
			URL          string `json:"url"`
		} `json:"tab"`
		Snapshot browser.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(observeText), &observed); err != nil {
		t.Fatalf("browser_observe 输出不是合法 JSON: %v", err)
	}
	if observed.Snapshot.ID == "" || observed.Snapshot.NavigationID == "" {
		t.Fatalf("快照缺少 id/navigationId: %+v", observed.Snapshot)
	}
	if observed.Tab.NavigationID != observed.Snapshot.NavigationID {
		t.Fatalf("工具输出的 tab.navigationId 与快照不一致: %+v vs %+v", observed.Tab, observed.Snapshot)
	}

	nameRef := h.backend.RefOf(h.tab, "name")
	submitRef := h.backend.RefOf(h.tab, "submit")

	// fill：走 MCP → 授权（ask→用户批准一次）→ 派发。
	if text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "fill", "snapshotId": observed.Snapshot.ID,
		"ref": nameRef, "text": "王小明",
	}); isErr {
		t.Fatalf("fill 失败: %s", text)
	}
	if got := h.backend.Field(h.tab, "name"); got != "王小明" {
		t.Fatalf("fill 没有真的写入字段（读到 %q）", got)
	}

	// click：真的提交到 fixture 服务端，counter 恰好 +1。
	clickText, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "click", "snapshotId": observed.Snapshot.ID, "ref": submitRef,
	})
	if isErr {
		t.Fatalf("click 失败: %s", clickText)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 1 {
		t.Fatalf("click 之后 form-submit counter=%d，期望恰好 1", n)
	}

	// 结论必须带"Delivered 不等于成功"的提醒（没有后置条件时不能声称完成）。
	if !strings.Contains(clickText, "Delivered") || !strings.Contains(clickText, "重新 browser_observe") {
		t.Fatalf("click 结论应说明 Delivered 不等于业务成功: %s", clickText)
	}

	// 审批确实发生过，而且是 ask（交互式默认）。
	h.mu.Lock()
	approvals := append([]policy.ApprovalRequest(nil), h.approvals...)
	h.mu.Unlock()
	if len(approvals) < 2 {
		t.Fatalf("期望至少 2 次审批（fill/click 各一次），实际 %d", len(approvals))
	}
	if approvals[0].SessionID != h.exec.SessionID {
		t.Fatalf("审批绑定的会话是 %q，期望 %q", approvals[0].SessionID, h.exec.SessionID)
	}

	observeBrowserFlow(t, h, approvals)
}

// TestSessionGrantCoversTabWritesAndTokenStaysSiteScoped 覆盖"可授会话许可"那一半。
//
// 两条事实必须分开说，混在一起就会把口径讲错：
//
//  1. **策略的会话许可（session_scope）是"本会话 + 该工具"**，不是按站点切分的。
//     policy.Service.hasSessionGrant 在按 (参数指纹, ResourceScope) 查不到时会退到
//     "tool:<名字>"，所以同一会话里第二条浏览器写动作不会再弹审批。这是 F04 已有
//     的安全规则，B05 不改它（任务边界也明确禁止改策略规则）。
//  2. **站点维度在 token 上强制**：Authorize 签发的 token 绑定当时的 origin，
//     Recheck 在派发前复核 origin。所以"在 A 站拿到的许可"不能直接用于 B 站的
//     一次已签发动作，站点变更后必须重新授权（这一点由
//     TestBrowserToolDeniedOriginChangeAtPolicyLayer 与下面的 token 用例钉住）。
func TestSessionGrantCoversTabWritesAndTokenStaysSiteScoped(t *testing.T) {
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	backend := browserfixture.NewScriptedBackend(fixture)
	pol := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace", Network: true},
		ApprovalTTL: time.Minute,
	})
	exec := contracts.ExecutionContext{SessionID: "sess-grant", RunID: "run-grant", ConfigRevision: 1}
	var asked sync.Map
	authz := &BrowserPolicyAuthorizer{
		Gate: pol, Guard: browser.TokenGuard{TTL: time.Minute},
		ApprovalWait: 5 * time.Second, PollInterval: 2 * time.Millisecond,
	}
	authz.OnPending = func(p policy.ApprovalRequest, call policy.ToolCall) {
		asked.Store(p.ID, true)
		go func() {
			_ = pol.ResolveApproval(context.Background(), p.ID, policy.ApprovalSessionScope, call)
		}()
	}
	svc, err := browser.NewService(browser.ServiceOptions{Backend: backend, Authorizer: authz, ActionTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	tab, err := svc.OpenTab(context.Background(), exec, browser.TabOptions{URL: fixture.Primary.URL + "/form"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	first, err := svc.Observe(context.Background(), exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	nameRef := backend.RefOf(tab, "name")

	// 第一次：ask → 用户给"本会话许可"。
	if _, err := svc.Execute(context.Background(), exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: first.ID, Ref: nameRef, Text: "一",
	}); err != nil {
		t.Fatalf("第一次写失败: %v", err)
	}
	countAsks := func() int {
		n := 0
		asked.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	if got := countAsks(); got != 1 {
		t.Fatalf("第一次写应弹一次审批，实际 %d", got)
	}

	// 第二次：命中本会话许可 → 不再弹审批（这是 policy 的既定语义）。
	second, err := svc.Observe(context.Background(), exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := svc.Execute(context.Background(), exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: second.ID, Ref: nameRef, Text: "二",
	}); err != nil {
		t.Fatalf("第二次写失败: %v", err)
	}
	if got := countAsks(); got != 1 {
		t.Fatalf("命中会话许可之后不应再弹审批，实际累计 %d 次", got)
	}

	// 站点维度在 token 上强制：把 A 站签发的 token 拿到 B 站复核必须被拒。
	siteA := browser.AuthorizationRequest{
		TabID: tab, SessionID: exec.SessionID, Origin: fixture.Primary.URL,
		URL: fixture.Primary.URL + "/form", NavigationID: second.NavigationID, Generation: 1,
		Action: browser.Action{Kind: browser.ActionFill, SnapshotID: second.ID, Ref: nameRef},
	}
	token, err := authz.Authorize(context.Background(), exec, siteA)
	if err != nil {
		t.Fatalf("Authorize(站点 A): %v", err)
	}
	siteB := siteA
	siteB.Origin = fixture.Secondary.URL
	siteB.URL = fixture.Secondary.URL + "/form"
	if err := authz.Recheck(context.Background(), exec, siteB, token); err == nil {
		t.Fatal("A 站签发的许可在 B 站通过了派发前复查：站点维度没有生效")
	}
	// 同一站点仍然可用（不是把所有东西都拒了）。
	if err := authz.Recheck(context.Background(), exec, siteA, token); err != nil {
		t.Fatalf("同站点的许可应通过复查: %v", err)
	}
}

// ---------- 能力门：不可用的能力不注册 ----------

func TestBrowserToolsSkipUnsupportedCapabilities(t *testing.T) {
	// 只保留 observe：Act/Navigate=false 的工具必须不出现。
	ctl := &stubBrowserController{
		caps: browser.CapabilitySet{Platform: "stub", Observe: true},
	}
	tools := BrowserTools(ctl, BrowserToolsOptions{Exec: contracts.ExecutionContext{SessionID: "s"}})
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	for _, banned := range []string{BrowserToolAct, BrowserToolOpenTab} {
		for _, n := range names {
			if n == banned {
				t.Fatalf("能力缺失时不该注册 %s（实际 %v）", banned, names)
			}
		}
	}
	browserToolsByName(t, tools, BrowserToolObserve)
	browserToolsByName(t, tools, BrowserToolTabs)

	// nil controller：一个工具都不注册（不能"先注册再在运行时炸"）。
	if got := BrowserTools(nil, BrowserToolsOptions{}); len(got) != 0 {
		t.Fatalf("没有 controller 时不应注册任何浏览器工具: %v", got)
	}
}

// stubBrowserController 只提供能力集，用来验证注册门。
type stubBrowserController struct {
	caps browser.CapabilitySet
}

func (s *stubBrowserController) Capabilities() browser.CapabilitySet { return s.caps }
func (s *stubBrowserController) OpenTab(context.Context, contracts.ExecutionContext, browser.TabOptions) (browser.TabID, error) {
	return "", contracts.NewAppError(contracts.CodeUnsupported, "stub 不实现", false)
}
func (s *stubBrowserController) Observe(context.Context, contracts.ExecutionContext, browser.TabID, browser.ObserveOptions) (browser.Snapshot, error) {
	return browser.Snapshot{}, contracts.NewAppError(contracts.CodeUnsupported, "stub 不实现", false)
}
func (s *stubBrowserController) Execute(context.Context, contracts.ExecutionContext, browser.TabID, browser.Action) (browser.ActionResult, error) {
	return browser.ActionResult{}, contracts.NewAppError(contracts.CodeUnsupported, "stub 不实现", false)
}
func (s *stubBrowserController) CloseTab(context.Context, contracts.ExecutionContext, browser.TabID) error {
	return contracts.NewAppError(contracts.CodeUnsupported, "stub 不实现", false)
}
func (s *stubBrowserController) TabInfo(contracts.ExecutionContext, browser.TabID) (browser.TabInfo, error) {
	return browser.TabInfo{}, contracts.NewAppError(contracts.CodeUnsupported, "stub 不实现", false)
}
func (s *stubBrowserController) Control(contracts.ExecutionContext, browser.TabID) (browser.ControlState, error) {
	return browser.ControlState{}, contracts.NewAppError(contracts.CodeUnsupported, "stub 不实现", false)
}

// ---------- 工具 ----------

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时（%s）：%s", timeout, what)
}

// ---------- 证据 artifact ----------

func browserObserveDir() string { return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR")) }

func appendBrowserJSONL(t *testing.T, name string, rows []any) {
	t.Helper()
	dir := browserObserveDir()
	if dir == "" || len(rows) == 0 {
		return
	}
	var sb strings.Builder
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("序列化 %s 的一行失败: %v", name, err)
		}
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", name, err)
	}
	defer f.Close()
	if _, err := f.WriteString(sb.String()); err != nil {
		t.Fatalf("追加 %s 失败: %v", name, err)
	}
}

func writeBrowserArtifact(t *testing.T, name string, v any) {
	t.Helper()
	dir := browserObserveDir()
	if dir == "" {
		return
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", name, err)
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", name, err)
	}
}

// writeBrowserCounters 落盘服务端 counter 与 backend 派发计数的快照。
func writeBrowserCounters(t *testing.T, h *policyHarness, section string) {
	t.Helper()
	appendBrowserJSONL(t, "fixture-counters.jsonl", []any{map[string]any{
		"section":   section,
		"primary":   h.fixture.Primary.Counters(),
		"secondary": h.fixture.Secondary.Counters(),
		"backend": map[string]any{
			"createCalls": h.backend.CreateCalls(),
			"instances":   h.backend.Instances(),
			"dispatched":  h.backend.Dispatched(),
		},
	}})
}

// writeBrowserTimeline 落盘动作时间线（来自 browser.Service，不是测试自己记的）。
func writeBrowserTimeline(t *testing.T, h *policyHarness) {
	t.Helper()
	rows := make([]any, 0, 16)
	for _, e := range h.browser.Timeline() {
		rows = append(rows, e)
	}
	appendBrowserJSONL(t, "action-timeline.jsonl", rows)
}

// observeBrowserPolicyDecisions 落盘策略判定账本（工具侧的 MCP 调用记录 + 审批）。
func observeBrowserPolicyDecisions(t *testing.T, h *policyHarness, section string) {
	t.Helper()
	h.mu.Lock()
	approvals := append([]policy.ApprovalRequest(nil), h.approvals...)
	h.mu.Unlock()
	rows := make([]any, 0, len(approvals))
	for _, p := range approvals {
		rows = append(rows, map[string]any{
			"section": section, "approvalId": p.ID, "toolName": p.ToolName,
			"sessionId": p.SessionID, "arguments": json.RawMessage(p.Arguments),
		})
	}
	appendBrowserJSONL(t, "policy-decisions.jsonl", rows)
	appendBrowserJSONL(t, "mcp-calls.jsonl", []any{map[string]any{
		"section": section, "calls": h.mcp.srv.Calls(),
	}})
	writeBrowserCounters(t, h, section)
	writeBrowserTimeline(t, h)
}

// mergeBrowserObservationFromArtifacts 把落盘 artifact **读回来**再写 observation.json。
//
// 直接拿内存里的 counter 写 observation 等于自证；这里要求事实来自文件：
// fixture-counters.jsonl 的最后一条、policy-decisions.jsonl 的行数、mcp-calls.jsonl 的调用数。
func mergeBrowserObservationFromArtifacts(t *testing.T, section string) {
	t.Helper()
	dir := browserObserveDir()
	if dir == "" {
		return
	}
	readJSONL := func(name string) []map[string]any {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil
		}
		out := make([]map[string]any, 0, 8)
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) == nil {
				out = append(out, row)
			}
		}
		return out
	}

	counters := readJSONL("fixture-counters.jsonl")
	decisions := readJSONL("policy-decisions.jsonl")
	calls := readJSONL("mcp-calls.jsonl")
	if len(counters) == 0 {
		return
	}
	last := counters[len(counters)-1]
	mergeToolListObservation(t, map[string]any{
		"browser:" + section: map[string]any{
			"counterSamples":     len(counters),
			"lastCounters":       last["primary"],
			"policyRows":         len(decisions),
			"mcpCallSamples":     len(calls),
			"artifacts":          []string{"fixture-counters.jsonl", "policy-decisions.jsonl", "mcp-calls.jsonl", "action-timeline.jsonl", "mcp-session.json"},
			"factsFromArtifacts": true,
		},
	})
}

func observeBrowserDenials(t *testing.T, h *policyHarness, section string) {
	t.Helper()
	if browserObserveDir() == "" {
		return
	}
	observeBrowserPolicyDecisions(t, h, section)
	writeBrowserArtifact(t, "mcp-session.json", map[string]any{
		"section":   section,
		"sessionId": h.exec.SessionID,
		"runId":     h.exec.RunID,
		"tabId":     h.tab,
	})
	mergeBrowserObservationFromArtifacts(t, section)
}

func observeBrowserFlow(t *testing.T, h *policyHarness, approvals []policy.ApprovalRequest) {
	t.Helper()
	if browserObserveDir() == "" {
		return
	}
	rows := make([]any, 0, len(approvals))
	for _, p := range approvals {
		rows = append(rows, map[string]any{
			"section": "flow", "approvalId": p.ID, "toolName": p.ToolName,
			"sessionId": p.SessionID, "arguments": json.RawMessage(p.Arguments),
		})
	}
	appendBrowserJSONL(t, "policy-decisions.jsonl", rows)
	writeBrowserCounters(t, h, "flow")
	writeBrowserTimeline(t, h)
	info, err := h.browser.TabInfo(h.exec, h.tab)
	if err != nil {
		t.Fatalf("TabInfo: %v", err)
	}
	writeBrowserArtifact(t, "tab-identity.json", map[string]any{
		"section": "flow", "tab": info,
		"fixturePrimary":   h.fixture.Primary.URL,
		"fixtureSecondary": h.fixture.Secondary.URL,
	})
	if browserObserveDir() != "" {
		mergeBrowserObservation(t, map[string]any{
			"flow_formSubmits":      h.fixture.Primary.Counter(browserfixture.CounterFormSubmit),
			"flowApprovals":         len(approvals),
			"flowBackendDispatches": h.backend.Dispatched(),
			"flowTabId":             string(h.tab),
			"flowNavigationId":      info.NavigationID,
		})
	}
}

// mergeBrowserObservation 合并写 observation.json（与其它域同一套写法）。
func mergeBrowserObservation(t *testing.T, facts map[string]any) {
	t.Helper()
	dir := browserObserveDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "observation.json")
	merged := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if uerr := json.Unmarshal(raw, &merged); uerr != nil {
			t.Fatalf("已有 observation.json 不合法: %v", uerr)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	tests, _ := merged["tests"].([]any)
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
	}
	merged["tests"] = tests
	merged["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
