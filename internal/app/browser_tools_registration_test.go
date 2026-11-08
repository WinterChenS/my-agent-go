package app

// M1：把浏览器工具注册进自有 MCP，并证明注册门是"能力 + 授权器"两道。
//
// 这一层守四件事，全部落在服务端事实上（fixture counter / browser.Service 派发计数 /
// 审批账本），不看文案：
//  1. 只有真的装配了原生宿主（有 *browser.Service）**且**真的有授权器时才注册
//     浏览器工具；缺一条就一个都不注册（模型不该看到一个必然失败的工具）。
//  2. 通过注册出来的工具（不是直接调 Service）批准一次 → 恰好一次派发。
//  3. 拒绝 → 0 派发、counter 0。
//  4. 过期 → 0 派发、counter 0，并且工具结果里写明"过期/撤销、未派发任何输入"。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/harnessmcp"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// newBrowserToolEnv 在授权接线环境之上再装一份桥接层：
// 工具读的是 Desktop 的桥接层，不读用例手里的 service 变量。
func newBrowserToolEnv(t *testing.T, opts browserAuthorizationOptions) *browserAuthEnv {
	t.Helper()
	env := newBrowserAuthEnv(t, opts)
	// 工具的执身身份来自 Desktop 的 runtimeSessionID（与右侧面板同一份来源）。
	env.desktop.mu.Lock()
	env.desktop.runtimeSessionID = string(env.exec.SessionID)
	env.desktop.mu.Unlock()
	env.desktop.SetBrowserBridge(NewBrowserBridge(BrowserBridgeOptions{
		Service:      env.svc,
		ExecProvider: env.desktop.BrowserExecutionContext,
	}))
	return env
}

// registeredHarnessTools 返回这台 Desktop 此刻应注册进自有 MCP 的全部工具。
func registeredHarnessTools(d *Desktop) []harnessmcp.Tool {
	return d.harnessToolset(d.Settings().Workspace, nil, "")
}

func browserToolNames(tools []harnessmcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "browser_") {
			out = append(out, tool.Name)
		}
	}
	return out
}

func harnessToolNamed(t *testing.T, tools []harnessmcp.Tool, name string) harnessmcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("自有 MCP 里没有注册 %s（当前：%v）", name, toolNames(tools))
	return harnessmcp.Tool{}
}

func toolNames(tools []harnessmcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

// TestHarnessBrowserToolsNeedBothServiceAndAuthorizer 是注册门本身。
func TestHarnessBrowserToolsNeedBothServiceAndAuthorizer(t *testing.T) {
	// 1) 没有桥接层（这台机器没装原生宿主）：一个浏览器工具都不注册。
	plain, _ := newTestDesktop(t)
	if names := browserToolNames(registeredHarnessTools(plain)); len(names) != 0 {
		t.Fatalf("没有原生宿主时注册了浏览器工具 %v：模型会看到一个必然失败的工具", names)
	}
	// 目录与实际注册读同一份构造：这里一并核对，防止"目录里有、实际没注册"。
	for _, desc := range plain.harnessToolDescriptors(plain.Settings().Workspace, nil, "") {
		if strings.HasPrefix(desc.Name, "browser_") {
			t.Fatalf("没有原生宿主时工具目录里出现了 %s", desc.Name)
		}
	}

	// 2) 有服务但没有授权器（装配失败）：仍然一个都不注册。
	d, _ := newTestDesktop(t)
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })
	backend := browserfixture.NewScriptedBackend(fixture)
	// 装配失败：只有一份空授权权威（authz=nil）——T1 的 fail-closed 口径。
	browserAuthorizations.Store(d, &browserAuthorization{})
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend: backend, Authorizer: d.BrowserAuthorizer(), ActionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	d.SetBrowserBridge(NewBrowserBridge(BrowserBridgeOptions{Service: svc}))
	if names := browserToolNames(registeredHarnessTools(d)); len(names) != 0 {
		t.Fatalf("没有授权器时注册了浏览器工具 %v：写动作必然被拒，工具不该出现", names)
	}

	// 3) 服务与授权器都有：注册完整的一组，且目录与注册表一致。
	env := newBrowserToolEnv(t, browserAuthorizationOptions{ApprovalWait: 10 * time.Second})
	tools := registeredHarnessTools(env.desktop)
	want := []string{
		harnessmcp.BrowserToolTabs,
		harnessmcp.BrowserToolOpenTab,
		harnessmcp.BrowserToolObserve,
		harnessmcp.BrowserToolAct,
		harnessmcp.BrowserToolCloseTab,
	}
	got := browserToolNames(tools)
	if len(got) != len(want) {
		t.Fatalf("注册的浏览器工具 = %v，期望 %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("注册的浏览器工具 = %v，期望 %v", got, want)
		}
	}
	descNames := map[string]bool{}
	for _, desc := range env.desktop.harnessToolDescriptors(env.desktop.Settings().Workspace, nil, "") {
		descNames[desc.Name] = true
	}
	for _, name := range got {
		if !descNames[name] {
			t.Fatalf("工具 %s 注册了但目录里没有：目录与实际注册分叉", name)
		}
	}
}

// toolCallResult 是一次异步工具调用的结论。
type toolCallResult struct {
	res harnessmcp.ToolResult
	err error
}

func callBrowserToolAsync(t *testing.T, tool harnessmcp.Tool, args any) <-chan toolCallResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化工具参数: %v", err)
	}
	ch := make(chan toolCallResult, 1)
	go func() {
		res, err := tool.Handler(context.Background(), raw)
		ch <- toolCallResult{res: res, err: err}
	}()
	return ch
}

func waitToolCall(t *testing.T, ch <-chan toolCallResult) toolCallResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("工具调用 10 秒没有返回")
		return toolCallResult{}
	}
}

// actArgs 是一次 browser_act 的最小参数（fixture form 里的 name 输入框）。
func actArgs(t *testing.T, env *browserAuthEnv, kind browser.ActionKind, ref, text string) map[string]any {
	t.Helper()
	return map[string]any{
		"tabId":      string(env.tab),
		"kind":       string(kind),
		"snapshotId": env.snap.ID,
		"ref":        ref,
		"text":       text,
	}
}

// TestHarnessBrowserActToolApprovesThenDispatchesExactlyOnce：
// 通过注册出来的工具（真实 handler）走一遍审批 → 恰好一次派发。
func TestHarnessBrowserActToolApprovesThenDispatchesExactlyOnce(t *testing.T) {
	env := newBrowserToolEnv(t, defaultBrowserAuthorizationOptions())
	act := harnessToolNamed(t, registeredHarnessTools(env.desktop), harnessmcp.BrowserToolAct)
	nameRef := env.backend.RefOf(env.tab, "name")

	first := callBrowserToolAsync(t, act, actArgs(t, env, browser.ActionFill, nameRef, "M1"))

	pending := waitBrowserApprovals(t, env.desktop, 1)
	view := pending[0]
	if view.ToolName != harnessmcp.BrowserToolAct {
		t.Fatalf("审批工具名 = %q，期望 %q", view.ToolName, harnessmcp.BrowserToolAct)
	}
	if view.Origin != env.fixture.Primary.URL {
		t.Fatalf("审批 origin = %q，期望 %q", view.Origin, env.fixture.Primary.URL)
	}
	if view.NavigationID != env.snap.NavigationID || view.TabID != string(env.tab) || view.Kind != string(browser.ActionFill) {
		t.Fatalf("审批身份不全: tab=%q kind=%q navigationId=%q", view.TabID, view.Kind, view.NavigationID)
	}
	if view.ExpiresAtUnix <= time.Now().Unix() {
		t.Fatalf("审批到期时间 %d 已经过去：界面会显示一条批不了的审批", view.ExpiresAtUnix)
	}
	if view.SessionID != env.exec.SessionID {
		t.Fatalf("审批会话 = %q，期望工具的服务端身份 %q（参数改不了它）",
			view.SessionID, env.exec.SessionID)
	}
	// 动作指纹摘要要能在界面上读出来：origin/navigationId/kind/ref 都在参数里。
	// 注意 app 侧的 browserApprovalArguments 只是展示用的子集（不带 ref），
	// 这里按授权指纹的完整字段解（harnessmcp.browserCallArguments 的同形状）。
	var args struct {
		TabID        string `json:"tabId"`
		Kind         string `json:"kind"`
		Ref          string `json:"ref"`
		Text         string `json:"text"`
		Origin       string `json:"origin"`
		NavigationID string `json:"navigationId"`
	}
	if err := json.Unmarshal(view.Arguments, &args); err != nil {
		t.Fatalf("审批参数不是合法 JSON: %v", err)
	}
	if args.Ref != nameRef || args.Kind != string(browser.ActionFill) || args.Text != "M1" ||
		args.Origin != env.fixture.Primary.URL || args.NavigationID != env.snap.NavigationID {
		t.Fatalf("动作摘要字段不全: %+v", args)
	}
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("等人工答复期间派发了 %d 次", n)
	}

	if err := env.desktop.ResolveBrowserApproval(view.ApprovalID, "once"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	res := waitToolCall(t, first)
	if res.err != nil || res.res.IsError {
		t.Fatalf("批准之后工具仍然失败: err=%v text=%s", res.err, res.res.Text)
	}
	if !strings.Contains(res.res.Text, "delivered") {
		t.Fatalf("工具结果没有说明派发事实: %s", res.res.Text)
	}
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("批准一次派发了 %d 次，期望恰好 1 次", n)
	}
	if left := env.desktop.PendingBrowserApprovals(); len(left) != 0 {
		t.Fatalf("批准后仍有 %d 条待审批", len(left))
	}

	// 一次许可不能覆盖第二次写：同一份审批不会再出现在队列里。
	second := callBrowserToolAsync(t, act, actArgs(t, env, browser.ActionFill, nameRef, "第二次"))
	waitBrowserApprovals(t, env.desktop, 1)
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("第二次写动作还没批准就派发了（总派发 %d）", n)
	}
	for _, v := range env.desktop.PendingBrowserApprovals() {
		if err := env.desktop.ResolveBrowserApproval(v.ApprovalID, "deny"); err != nil {
			t.Fatalf("拒绝失败: %v", err)
		}
	}
	if res := waitToolCall(t, second); res.err == nil && !res.res.IsError {
		t.Fatal("被拒绝的第二次写动作仍然成功了")
	}
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("拒绝之后派发数 = %d，期望仍是 1", n)
	}
}

// TestHarnessBrowserActToolDenyLeavesCounterZero：拒绝 → 0 派发、counter 0。
func TestHarnessBrowserActToolDenyLeavesCounterZero(t *testing.T) {
	env := newBrowserToolEnv(t, defaultBrowserAuthorizationOptions())
	act := harnessToolNamed(t, registeredHarnessTools(env.desktop), harnessmcp.BrowserToolAct)
	submitRef := env.backend.RefOf(env.tab, "submit")

	call := callBrowserToolAsync(t, act, actArgs(t, env, browser.ActionClick, submitRef, ""))
	view := waitBrowserApprovals(t, env.desktop, 1)[0]
	if err := env.desktop.ResolveBrowserApproval(view.ApprovalID, "deny"); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	res := waitToolCall(t, call)
	if res.err == nil && !res.res.IsError {
		t.Fatal("被拒绝的写动作仍然成功了")
	}
	text := res.res.Text
	if res.err != nil {
		text = res.err.Error()
	}
	if !strings.Contains(text, "拒绝") || !strings.Contains(text, "未派发") {
		t.Fatalf("拒绝之后工具结果没有说清原因: %s", text)
	}
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("拒绝之后派发了 %d 次", n)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("拒绝之后 form-submit = %d，期望 0", n)
	}
}

// TestHarnessBrowserActToolExpiryLeavesCounterZeroWithReason：
// 审批过期 → 0 派发、counter 0，工具结果写明过期/撤销与"未派发任何输入"。
func TestHarnessBrowserActToolExpiryLeavesCounterZeroWithReason(t *testing.T) {
	env := newBrowserToolEnv(t, browserAuthorizationOptions{
		// 审批本身 80ms 过期：真实流逝时间，不靠假时钟。
		ApprovalTTL:  80 * time.Millisecond,
		TokenTTL:     time.Minute,
		ApprovalWait: 10 * time.Second,
		PollInterval: 5 * time.Millisecond,
	})
	act := harnessToolNamed(t, registeredHarnessTools(env.desktop), harnessmcp.BrowserToolAct)
	submitRef := env.backend.RefOf(env.tab, "submit")

	call := callBrowserToolAsync(t, act, actArgs(t, env, browser.ActionClick, submitRef, ""))
	waitBrowserApprovals(t, env.desktop, 1)

	res := waitToolCall(t, call)
	if res.err == nil && !res.res.IsError {
		t.Fatal("审批过期之后写动作仍然成功了")
	}
	text := res.res.Text
	if res.err != nil {
		text = res.err.Error()
	}
	if !strings.Contains(text, "未派发任何输入") {
		t.Fatalf("过期之后工具结果没有说明没有派发: %s", text)
	}
	if !strings.Contains(text, "过期") && !strings.Contains(text, "撤销") {
		t.Fatalf("过期之后工具结果没有给出原因（过期/撤销）: %s", text)
	}
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("过期之后派发了 %d 次", n)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("过期之后 form-submit = %d，期望 0", n)
	}
	if left := env.desktop.PendingBrowserApprovals(); len(left) != 0 {
		t.Fatalf("过期之后仍列着 %d 条待审批：界面会显示一条永远批不了的审批", len(left))
	}
}
