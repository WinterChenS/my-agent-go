package app

// T1（BR07.tool）：应用层把浏览器写动作的授权真正接上。
//
// 这一层与 internal/harnessmcp 的分工：
//   - harnessmcp 证明"MCP 工具 → 授权器 → 策略服务 → 派发"这条链路本身；
//   - 这里证明**产品装配**：Desktop 持有的授权权威（策略服务 + 审批队列）被
//     cmd/myagent 装进 browser.Service，用户通过绑定答复审批之后写入恰好一次；
//     没有装配、装配错误、无人答复、拒绝时一律不派发（默认拒绝，不是默认放行）。
//
// 断言落在 fixture 服务端 counter 与 browser.Service 的派发计数上：文案可以改得
// "看起来对"，服务端计数不会。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/harnessmcp"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// browserAuthEnv 是应用层授权接线的环境。
type browserAuthEnv struct {
	desktop *Desktop
	backend *browserfixture.ScriptedBackend
	fixture *browserfixture.Fixture
	svc     *browser.Service
	auth    *browserAuthorization
	exec    contracts.ExecutionContext
	tab     browser.TabID
	snap    browser.Snapshot
}

func newBrowserAuthEnv(t *testing.T, opts browserAuthorizationOptions) *browserAuthEnv {
	t.Helper()
	d, _ := newTestDesktop(t)

	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })
	backend := browserfixture.NewScriptedBackend(fixture)

	auth := newBrowserAuthorization(opts)
	if auth == nil || auth.Authorizer() == nil {
		t.Fatal("应用层没有装配出浏览器授权器：写动作会一直是默认拒绝")
	}
	// 让 Desktop 的绑定与用例看同一份授权权威（生产路径由 BrowserAuthorizer() 懒加载）。
	browserAuthorizations.Store(d, auth)

	svc, err := browser.NewService(browser.ServiceOptions{
		Backend:       backend,
		Authorizer:    d.BrowserAuthorizer(),
		ActionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-default", SessionID: "sess-br07tool", RunID: "run-br07tool",
		ConfigRevision: 1, ControlGeneration: 1,
	}
	tab, err := svc.OpenTab(context.Background(), exec, browser.TabOptions{URL: fixture.Primary.URL + "/form"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	snap, err := svc.Observe(context.Background(), exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	return &browserAuthEnv{
		desktop: d, backend: backend, fixture: fixture, svc: svc, auth: auth,
		exec: exec, tab: tab, snap: snap,
	}
}

// actionResult 是异步写动作的结论（Execute 会阻塞在等审批上）。
type appActionResult struct {
	res browser.ActionResult
	err error
}

func (e *browserAuthEnv) executeAsync(kind browser.ActionKind, ref, text string) <-chan appActionResult {
	ch := make(chan appActionResult, 1)
	action := browser.Action{Kind: kind, SnapshotID: e.snap.ID, Ref: ref, Text: text}
	go func() {
		res, err := e.svc.Execute(context.Background(), e.exec, e.tab, action)
		ch <- appActionResult{res: res, err: err}
	}()
	return ch
}

// waitBrowserApprovals 等到面板/绑定能看到 n 条待审批。
func waitBrowserApprovals(t *testing.T, d *Desktop, n int) []BrowserApprovalView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.PendingBrowserApprovals(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条浏览器审批超时（当前 %d）", n, len(d.PendingBrowserApprovals()))
	return nil
}

// TestBrowserAuthorizerWiringApprovesExactlyOneWrite：用户批准一次 → 恰好一次派发；
// 同一次许可不能覆盖第二次写动作（必须重新审批）。
func TestBrowserAuthorizerWiringApprovesExactlyOneWrite(t *testing.T) {
	env := newBrowserAuthEnv(t, defaultBrowserAuthorizationOptions())
	nameRef := env.backend.RefOf(env.tab, "name")
	submitRef := env.backend.RefOf(env.tab, "submit")

	// 第一次写：等人工批准。
	first := env.executeAsync(browser.ActionFill, nameRef, "王小明")
	pending := waitBrowserApprovals(t, env.desktop, 1)
	view := pending[0]
	if view.ToolName != harnessmcp.BrowserToolAct {
		t.Fatalf("审批工具名 = %q，期望 %q", view.ToolName, harnessmcp.BrowserToolAct)
	}
	if view.SessionID != env.exec.SessionID || view.RunID != env.exec.RunID {
		t.Fatalf("审批身份 = (%s,%s)，期望 run binding (%s,%s)",
			view.SessionID, view.RunID, env.exec.SessionID, env.exec.RunID)
	}
	// 审批指纹必须带 origin 与 navigationId：站点/文档变了，同一条审批不再成立。
	if view.Origin != env.fixture.Primary.URL {
		t.Fatalf("审批 origin = %q，期望 %q", view.Origin, env.fixture.Primary.URL)
	}
	if view.NavigationID != env.snap.NavigationID {
		t.Fatalf("审批 navigationId = %q，期望 %q", view.NavigationID, env.snap.NavigationID)
	}
	// 等待期间页面不能被写：派发与 counter 都是 0。
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("等待审批期间派发了 %d 次", n)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("等待审批期间 form-submit = %d，期望 0", n)
	}

	if err := env.desktop.ResolveBrowserApproval(view.ApprovalID, "once"); err != nil {
		t.Fatalf("答复「允许一次」失败: %v", err)
	}
	out := <-first
	if out.err != nil {
		t.Fatalf("批准之后写动作失败: %v", out.err)
	}
	if !out.res.Delivered {
		t.Fatal("批准之后动作没有被派发")
	}
	if got := env.backend.Field(env.tab, "name"); got != "王小明" {
		t.Fatalf("fill 没有写入字段（读到 %q）", got)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("fill 不应提交表单，form-submit = %d", n)
	}
	if left := env.desktop.PendingBrowserApprovals(); len(left) != 0 {
		t.Fatalf("答复之后仍有 %d 条待审批", len(left))
	}

	// 第二次写：一次性许可已被消费，必须重新审批；这里拒绝它。
	second := env.executeAsync(browser.ActionClick, submitRef, "")
	pending = waitBrowserApprovals(t, env.desktop, 1)
	secondView := pending[0]
	if secondView.ApprovalID == view.ApprovalID {
		t.Fatalf("第二次写复用了同一条审批 %s：一次性许可被重放了", view.ApprovalID)
	}
	if err := env.desktop.ResolveBrowserApproval(secondView.ApprovalID, "deny"); err != nil {
		t.Fatalf("答复「拒绝」失败: %v", err)
	}
	if out := <-second; out.err == nil {
		t.Fatal("用户拒绝之后写动作仍然成功了")
	}
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("拒绝之后派发计数 = %d，期望仍为 1", n)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("拒绝之后 form-submit = %d，期望 0", n)
	}

	// 第三次写：再批准一次，服务端 counter 恰好 1。
	third := env.executeAsync(browser.ActionClick, submitRef, "")
	pending = waitBrowserApprovals(t, env.desktop, 1)
	if err := env.desktop.ResolveBrowserApproval(pending[0].ApprovalID, "once"); err != nil {
		t.Fatalf("答复第三次审批失败: %v", err)
	}
	if out := <-third; out.err != nil {
		t.Fatalf("第三次写失败: %v", out.err)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 1 {
		t.Fatalf("允许路径 form-submit = %d，期望恰好 1", n)
	}

	observeBrowserAppWiring(t, env, "approved-once", map[string]any{
		"approvalOrigin":       view.Origin,
		"approvalNavigationId": view.NavigationID,
		"formSubmits":          env.fixture.Primary.Counter(browserfixture.CounterFormSubmit),
		"dispatches":           env.backend.Dispatched(),
	})
}

// TestBrowserAuthorizerWiringDenialLeavesCounterZero：拒绝之后服务端 counter 恒为 0。
func TestBrowserAuthorizerWiringDenialLeavesCounterZero(t *testing.T) {
	env := newBrowserAuthEnv(t, defaultBrowserAuthorizationOptions())
	done := env.executeAsync(browser.ActionClick, env.backend.RefOf(env.tab, "submit"), "")
	pending := waitBrowserApprovals(t, env.desktop, 1)
	if err := env.desktop.ResolveBrowserApproval(pending[0].ApprovalID, "deny"); err != nil {
		t.Fatalf("答复「拒绝」失败: %v", err)
	}
	out := <-done
	if out.err == nil {
		t.Fatal("用户拒绝之后写动作仍然成功了")
	}
	// 拒绝结论必须让调用方知道"没有发出输入"，而不是含糊失败。
	if code := errorCodeOf(out.err); code != contracts.CodeUnauthorized {
		t.Fatalf("拒绝的错误码 = %q，期望 %q（%v）", code, contracts.CodeUnauthorized, out.err)
	}
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("拒绝之后派发了 %d 次", n)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("拒绝之后 form-submit = %d，期望 0", n)
	}
	observeBrowserAppWiring(t, env, "denied", map[string]any{
		"formSubmits": env.fixture.Primary.Counter(browserfixture.CounterFormSubmit),
		"dispatches":  env.backend.Dispatched(),
	})
}

// TestBrowserAuthorizerTimesOutWithoutDispatching：没人答复时超时必须拒绝，
// 不能"用户没看也算同意"（这条会在有人把默认分支改成放行时变红）。
func TestBrowserAuthorizerTimesOutWithoutDispatching(t *testing.T) {
	env := newBrowserAuthEnv(t, browserAuthorizationOptions{
		ApprovalTTL:  time.Minute,
		TokenTTL:     time.Minute,
		ApprovalWait: 80 * time.Millisecond,
		PollInterval: 2 * time.Millisecond,
	})
	done := env.executeAsync(browser.ActionClick, env.backend.RefOf(env.tab, "submit"), "")
	out := <-done
	if out.err == nil {
		t.Fatal("没人答复审批时写动作仍然成功了：超时被当成了同意")
	}
	if !strings.Contains(out.err.Error(), "超时") && !strings.Contains(out.err.Error(), "撤销") {
		t.Fatalf("超时结论应说明撤销/超时: %v", out.err)
	}
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("超时之后派发了 %d 次", n)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("超时之后 form-submit = %d，期望 0", n)
	}
	if left := env.desktop.PendingBrowserApprovals(); len(left) != 0 {
		t.Fatalf("超时之后仍有 %d 条待审批（撤销没有生效）", len(left))
	}
	observeBrowserAppWiring(t, env, "timeout", map[string]any{
		"formSubmits": env.fixture.Primary.Counter(browserfixture.CounterFormSubmit),
		"dispatches":  env.backend.Dispatched(),
	})
}

// TestBrowserAuthorizerResolveRejectsFingerprintChange：答复方不能借同一条审批
// 调换参数（例如换个 tab / 换个 ref 再批准一次）。
func TestBrowserAuthorizerResolveRejectsFingerprintChange(t *testing.T) {
	env := newBrowserAuthEnv(t, defaultBrowserAuthorizationOptions())
	done := env.executeAsync(browser.ActionFill, env.backend.RefOf(env.tab, "name"), "原参数")
	pending := waitBrowserApprovals(t, env.desktop, 1)
	id := pending[0].ApprovalID

	// 用一条"参数不同"的调用去答复：必须被策略服务拒绝（指纹不一致）。
	swapped := env.auth.calls[id]
	swapped.Arguments = []byte(`{"tabId":"tab-other","kind":"fill","origin":"http://evil.example","navigationId":"nav-x"}`)
	if err := env.auth.pol.ResolveApproval(context.Background(), id, "once", swapped); err == nil {
		t.Fatal("换了参数的答复被接受：审批没有绑定参数指纹")
	}
	// 原参数仍然可以正确答复（不是把审批弄坏了）。
	if err := env.desktop.ResolveBrowserApproval(id, "once"); err != nil {
		t.Fatalf("原参数答复失败: %v", err)
	}
	if out := <-done; out.err != nil {
		t.Fatalf("原参数批准之后写动作失败: %v", out.err)
	}
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("派发计数 = %d，期望 1", n)
	}
	observeBrowserAppWiring(t, env, "fingerprint", map[string]any{
		"formSubmits": env.fixture.Primary.Counter(browserfixture.CounterFormSubmit),
		"dispatches":  env.backend.Dispatched(),
	})
}

// TestBrowserAuthorizerNilKeepsWritesDenied：应用层装配失败（授权器为 nil）时，
// browser.Service 必须拒绝写动作——这是“默认放行”回归在应用层的捕手。
func TestBrowserAuthorizerNilKeepsWritesDenied(t *testing.T) {
	d, _ := newTestDesktop(t)
	// 模拟装配失败：只有一份空授权权威（authz=nil）。
	browserAuthorizations.Store(d, &browserAuthorization{})
	if got := d.BrowserAuthorizer(); got != nil {
		t.Fatalf("装配失败时 BrowserAuthorizer 返回了 %T：默认拒绝的入口被换成了一个可用的授权器", got)
	}

	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })
	backend := browserfixture.NewScriptedBackend(fixture)

	// cmd/myagent 的写法：把 nil 直接交给 browser.Service。
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend: backend, Authorizer: d.BrowserAuthorizer(), ActionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-default", SessionID: "sess-br07tool", RunID: "run-br07tool",
		ConfigRevision: 1, ControlGeneration: 1,
	}
	tab, err := svc.OpenTab(context.Background(), exec, browser.TabOptions{URL: fixture.Primary.URL + "/form"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	snap, err := svc.Observe(context.Background(), exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	_, err = svc.Execute(context.Background(), exec, tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: backend.RefOf(tab, "submit"),
	})
	if err == nil {
		t.Fatal("没有授权器时写动作仍然成功了：默认放行回归")
	}
	if code := errorCodeOf(err); code != contracts.CodeUnauthorized {
		t.Fatalf("错误码 = %q，期望 %q（%v）", code, contracts.CodeUnauthorized, err)
	}
	if n := backend.Dispatched(); n != 0 {
		t.Fatalf("没有授权器时派发了 %d 次", n)
	}
	if n := fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("没有授权器时 form-submit = %d，期望 0", n)
	}
}

// ---------- 观察装置：从落盘文件读回，不用内存事实充数 ----------

func appObserveDir() string { return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR")) }

func appendAppJSONL(t *testing.T, name string, rows []any) {
	t.Helper()
	dir := appObserveDir()
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

func readAppJSONL(t *testing.T, name string) []map[string]any {
	t.Helper()
	dir := appObserveDir()
	if dir == "" {
		return nil
	}
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

// observeBrowserAppWiring 落盘应用层的事实，再把它们从磁盘读回来汇总成
// browser-authorizer-wiring.json。
//
// 读回这一步是刻意的：如果谁把事实停在内存里（或者文件根本没写成），
// 汇总出来的样本数会是 0，证据一眼就能看出不对。
func observeBrowserAppWiring(t *testing.T, env *browserAuthEnv, section string, facts map[string]any) {
	t.Helper()
	dir := appObserveDir()
	if dir == "" {
		return
	}
	// 每次落一行：服务端计数快照 + 动作时间线。
	appendAppJSONL(t, "fixture-counters.jsonl", []any{map[string]any{
		"section":   section,
		"source":    "app",
		"primary":   env.fixture.Primary.Counters(),
		"secondary": env.fixture.Secondary.Counters(),
		"backend":   map[string]any{"dispatched": env.backend.Dispatched()},
	}})
	timeline := make([]any, 0, 16)
	for _, e := range env.svc.Timeline() {
		timeline = append(timeline, e)
	}
	appendAppJSONL(t, "action-timeline.jsonl", timeline)

	// 从磁盘读回来。
	counters := readAppJSONL(t, "fixture-counters.jsonl")
	timelineRows := readAppJSONL(t, "action-timeline.jsonl")
	decisions := readAppJSONL(t, "policy-decisions.jsonl")

	// 多条用例写同一份摘要：按 section 累积，别让最后一条把前面的结论盖掉。
	sections := map[string]any{}
	summaryPath := filepath.Join(dir, "browser-authorizer-wiring.json")
	if existing, err := os.ReadFile(summaryPath); err == nil {
		var prev map[string]any
		if json.Unmarshal(existing, &prev) == nil {
			if got, ok := prev["sections"].(map[string]any); ok {
				sections = got
			}
		}
	}
	sectionFacts := map[string]any{
		"formSubmits":     env.fixture.Primary.Counter(browserfixture.CounterFormSubmit),
		"dispatches":      env.backend.Dispatched(),
		"authorizerWired": env.auth.Authorizer() != nil,
	}
	for k, v := range facts {
		sectionFacts[k] = v
	}
	sections[section] = sectionFacts

	summary := map[string]any{
		"source":             "app-boundary",
		"counterSamples":     len(counters),
		"timelineSamples":    len(timelineRows),
		"policyDecisionRows": len(decisions),
		"factsFromArtifacts": len(counters) > 0 && len(timelineRows) > 0,
		"sections":           sections,
	}
	if len(counters) > 0 {
		summary["lastCountersRow"] = counters[len(counters)-1]
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatalf("序列化 browser-authorizer-wiring.json 失败: %v", err)
	}
	if err := os.WriteFile(summaryPath, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("写 browser-authorizer-wiring.json 失败: %v", err)
	}
}
