package browser_test

// B05：控制权 / 每 tab 串行队列 / 限额 / 授权复查（BR11、BR12、BR14）。
//
// 判定落在两处**可观测量**上，而不是错误文案：
//   - backend 的计数（CreateCalls/Instances/Dispatched/ActLog）：证明"有没有派发"、
//     "有没有创建实例"；
//   - fixture 服务端的 counter：证明"服务端有没有真的收到副作用"。
//
// 为什么用 scripted backend：控制平面与平台无关，而 BR11/BR12/BR14 要点是
// 顺序与计数。真实 WebView2 的输入证据由 tests/native（B01/B04）产出，这里不冒充。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// ---------- 授权替身 ----------

// recordingAuthorizer 记录每次授权/复查，并按用例设定拒绝/过期/换站点。
type recordingAuthorizer struct {
	mu        sync.Mutex
	guard     browser.TokenGuard
	deny      bool
	denyAt    int // >0 时第 N 次 Authorize 之后开始拒绝
	calls     int
	rechecks  int
	decisions []browserDecision
}

type browserDecision struct {
	Stage  string `json:"stage"`
	Action string `json:"action"`
	Ref    string `json:"ref"`
	Origin string `json:"origin"`
	Gen    uint64 `json:"generation"`
	Result string `json:"result"`
}

func newAuthorizer(ttl time.Duration) *recordingAuthorizer {
	return &recordingAuthorizer{guard: browser.TokenGuard{TTL: ttl}}
}

func (a *recordingAuthorizer) Authorize(ctx context.Context, exec contracts.ExecutionContext, req browser.AuthorizationRequest) (browser.AuthorizationToken, error) {
	a.mu.Lock()
	a.calls++
	n := a.calls
	deny := a.deny || (a.denyAt > 0 && n >= a.denyAt)
	a.mu.Unlock()

	if deny {
		err := contracts.NewAppError(contracts.CodeUnauthorized, "策略拒绝：用户拒绝本次浏览器写操作", false)
		a.record(browserDecision{Stage: "authorize", Action: string(req.Action.Kind), Ref: req.Action.Ref,
			Origin: req.Origin, Gen: req.Generation, Result: "deny"})
		return browser.AuthorizationToken{}, err
	}
	a.record(browserDecision{Stage: "authorize", Action: string(req.Action.Kind), Ref: req.Action.Ref,
		Origin: req.Origin, Gen: req.Generation, Result: "allow"})
	return a.guard.Issue(exec.SessionID, req, "apr-test", "测试许可"), nil
}

func (a *recordingAuthorizer) Recheck(ctx context.Context, exec contracts.ExecutionContext, req browser.AuthorizationRequest, token browser.AuthorizationToken) error {
	a.mu.Lock()
	a.rechecks++
	a.mu.Unlock()
	if err := a.guard.Check(exec, req, token); err != nil {
		a.record(browserDecision{Stage: "recheck", Action: string(req.Action.Kind), Ref: req.Action.Ref,
			Origin: req.Origin, Gen: req.Generation, Result: "deny: " + errCode(err)})
		return err
	}
	a.record(browserDecision{Stage: "recheck", Action: string(req.Action.Kind), Ref: req.Action.Ref,
		Origin: req.Origin, Gen: req.Generation, Result: "allow"})
	return nil
}

func (a *recordingAuthorizer) record(d browserDecision) {
	a.mu.Lock()
	a.decisions = append(a.decisions, d)
	a.mu.Unlock()
}

func (a *recordingAuthorizer) snapshot() []browserDecision {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]browserDecision, len(a.decisions))
	copy(out, a.decisions)
	return out
}

func (a *recordingAuthorizer) recheckCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rechecks
}

// ---------- 环境 ----------

type ctlEnv struct {
	svc     *browser.Service
	backend *browserfixture.ScriptedBackend
	fixture *browserfixture.Fixture
	authz   *recordingAuthorizer
	exec    contracts.ExecutionContext
	other   contracts.ExecutionContext
}

func newCtlEnv(t *testing.T, ttl time.Duration) *ctlEnv {
	t.Helper()
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	backend := browserfixture.NewScriptedBackend(fixture)
	authz := newAuthorizer(ttl)
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend:       backend,
		Authorizer:    authz,
		ActionTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return &ctlEnv{
		svc: svc, backend: backend, fixture: fixture, authz: authz,
		exec:  contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-a", RunID: "run-1", ConfigRevision: 1, ControlGeneration: 1},
		other: contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-b", RunID: "run-2", ConfigRevision: 1, ControlGeneration: 1},
	}
}

// openAndObserve 建一个 tab 并观察一次，返回 tab 与快照。
func (e *ctlEnv) openAndObserve(t *testing.T) (browser.TabID, browser.Snapshot) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tab, err := e.svc.OpenTab(ctx, e.exec, browser.TabOptions{URL: e.fixture.Primary.URL + "/form"})
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	snap, err := e.svc.Observe(ctx, e.exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	return tab, snap
}

func errCode(err error) string {
	var app *contracts.AppError
	if errors.As(err, &app) && app != nil {
		return app.Code
	}
	return ""
}

func mustErrCode(t *testing.T, err error, want string, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：期望失败（%s），实际成功", what, want)
	}
	if got := errCode(err); got != want {
		t.Fatalf("%s：错误码 %q，期望 %q（消息: %s）", what, got, want, err.Error())
	}
}

// ---------- BR11：接管撤销队列，Human 之后不再有 agent 输入 ----------

func TestQueuedActionRevokedOnTakeover(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	tab, snap := env.openAndObserve(t)
	nameRef := snap.Elements[0].Ref
	submitRef := snap.Elements[1].Ref

	// 第一个动作用"慢动作"制造真实的重叠：它已经在飞，接管时无法撤回。
	env.backend.Latency = func(kind browser.ActionKind, ref string) time.Duration {
		if kind == browser.ActionFill && ref == nameRef {
			return 600 * time.Millisecond
		}
		return 0
	}

	type outcome struct {
		name string
		res  browser.ActionResult
		err  error
	}
	results := make(chan outcome, 3)
	go func() {
		res, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
			Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "第一个",
		})
		results <- outcome{"slow", res, err}
	}()

	waitFor(t, 3*time.Second, "慢动作被派发", func() bool { return env.backend.Dispatched() == 1 })

	// 两个排队写操作：此刻队列里应当正好有 2 个待派发动作。
	for i, text := range []string{"第二个", "第三个"} {
		text := text
		name := []string{"queued-1", "queued-2"}[i]
		go func() {
			res, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
				Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: text,
			})
			results <- outcome{name, res, err}
		}()
	}
	waitFor(t, 3*time.Second, "两个动作入队", func() bool {
		info, err := env.svc.TabInfo(env.exec, tab)
		return err == nil && info.Queued == 2
	})

	before, err := env.svc.Control(env.exec, tab)
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if before.Mode != browser.ControlAgent {
		t.Fatalf("接管前状态应为 agent，实际 %q", before.Mode)
	}

	// 接管：队列必须归零，且因为有一个已派发动作在飞，状态只能是 pausing。
	after, err := env.svc.TakeOver(context.Background(), env.exec, tab)
	if err != nil {
		t.Fatalf("TakeOver: %v", err)
	}
	if after.Queued != 0 {
		t.Fatalf("接管后 queued=%d，期望 0", after.Queued)
	}
	if after.Mode != browser.ControlPausing {
		t.Fatalf("有在飞动作时接管必须停在 pausing（不能先显示 human），实际 %q（%s）",
			after.Mode, after.Detail)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("接管必须递增 controlGeneration：%d → %d", before.Generation, after.Generation)
	}
	if after.InFlight != 1 {
		t.Fatalf("接管时在飞动作数应为 1，实际 %d", after.InFlight)
	}

	// 收集三个结论：两个排队动作必须拿到"未派发即撤销"，慢动作自己结束。
	got := map[string]outcome{}
	for i := 0; i < 3; i++ {
		select {
		case o := <-results:
			got[o.name] = o
		case <-time.After(10 * time.Second):
			t.Fatalf("只收到 %d/3 个结果", len(got))
		}
	}
	for _, name := range []string{"queued-1", "queued-2"} {
		mustErrCode(t, got[name].err, browser.CodeActionRevoked, name)
		if !strings.Contains(got[name].err.Error(), "没有") ||
			!strings.Contains(got[name].err.Error(), "输入发出") {
			t.Fatalf("%s 的撤销结论必须写明没有发出输入: %v", name, got[name].err)
		}
	}
	if got["slow"].err != nil {
		t.Fatalf("已派发的慢动作自身应当正常结束，实际 %v", got["slow"].err)
	}

	// 派发计数恰好 1：接管之后没有任何新的 agent 写入落到引擎。
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("接管后 backend 派发计数为 %d，期望恰好 1（排队动作一个都不许派发）", n)
	}
	// 服务端只看到一次提交：慢动作是 fill，本来就不提交，因此 counter 仍然是 0。
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("接管期间不应发生提交，counter=%d", n)
	}

	// 慢动作静止后，状态必须自己走向 human（q不再有 agent 输入）。
	waitFor(t, 5*time.Second, "接管完成（human）", func() bool {
		cs, err := env.svc.Control(env.exec, tab)
		return err == nil && cs.Mode == browser.ControlHuman && cs.InFlight == 0
	})

	// human 之后 agent 写操作稳定拒绝，且不产生任何派发。
	_, err = env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: submitRef,
	})
	mustErrCode(t, err, contracts.CodeNeedsHuman, "human 期间的 agent 写操作")
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("human 之后仍有派发：%d", n)
	}

	// 时间线顺序：撤销发生在"接管"之前，且 human 之后没有 dispatched 行。
	timeline := env.svc.TimelineFor(tab)
	kinds := make([]string, 0, len(timeline))
	for _, e := range timeline {
		kinds = append(kinds, e.Kind)
	}
	takeoverIdx, humanIdx := -1, -1
	lastDispatch := -1
	for i, e := range timeline {
		switch e.Kind {
		case browser.TimelineTakeOver:
			takeoverIdx = i
		case browser.TimelineDispatched:
			lastDispatch = i
		}
		if e.Kind == browser.TimelineTakeOver && e.Mode == browser.ControlHuman {
			humanIdx = i
		}
	}
	if takeoverIdx < 0 {
		t.Fatalf("时间线里没有 takeover: %v", kinds)
	}
	if lastDispatch > takeoverIdx {
		t.Fatalf("接管之后还有 dispatched 事件（顺序: %v）", kinds)
	}
	if humanIdx >= 0 {
		for i := humanIdx + 1; i < len(timeline); i++ {
			if timeline[i].Kind == browser.TimelineDispatched {
				t.Fatalf("human 显示之后仍有 agent 派发（顺序: %v）", kinds)
			}
		}
	}
	revokedRows := 0
	for _, e := range timeline {
		if e.Kind == browser.TimelineRevoked && e.ErrorCode == browser.CodeActionRevoked {
			revokedRows++
		}
	}
	if revokedRows != 2 {
		t.Fatalf("时间线里撤销行数为 %d，期望 2（两个排队动作各一行）", revokedRows)
	}

	observeBR11(t, env, tab, "takeover", map[string]any{
		"queuedAfterTakeover": after.Queued,
		"modeAfterTakeover":   string(after.Mode),
		"generationBefore":    before.Generation,
		"generationAfter":     after.Generation,
		"backendDispatched":   env.backend.Dispatched(),
		"revokedRows":         revokedRows,
		"slowActionErr":       errText(got["slow"].err),
		"queuedErrors":        []string{errText(got["queued-1"].err), errText(got["queued-2"].err)},
	})
}

// ---------- BR11：恢复必须基于新快照 ----------

func TestResumeRequiresFreshSnapshot(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	tab, snap := env.openAndObserve(t)
	nameRef := snap.Elements[0].Ref

	taken, err := env.svc.TakeOver(context.Background(), env.exec, tab)
	if err != nil {
		t.Fatalf("TakeOver: %v", err)
	}

	// 接管之后还没有新快照：恢复必须被稳定拒绝（STALE_SNAPSHOT）。
	_, err = env.svc.Resume(context.Background(), env.exec, tab)
	mustErrCode(t, err, contracts.CodeStaleSnapshot, "没有新快照就恢复")

	// 被拒绝的恢复不能顺手把控制权还回去：状态与代数都必须原样。
	afterReject, err := env.svc.Control(env.exec, tab)
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if afterReject.Mode != browser.ControlHuman {
		t.Fatalf("恢复被拒后状态应保持 human，实际 %q", afterReject.Mode)
	}
	if afterReject.Generation != taken.Generation {
		t.Fatalf("恢复被拒后代数不应变化：%d → %d", taken.Generation, afterReject.Generation)
	}

	// 旧快照的动作在 human 期间也被拒绝。
	_, err = env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "旧快照",
	})
	mustErrCode(t, err, contracts.CodeNeedsHuman, "human 期间用旧快照写")

	// 人工/面板观察一次：这才有"接管之后"的快照。
	fresh, err := env.svc.Observe(context.Background(), env.exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe(接管后): %v", err)
	}
	if fresh.ControlGeneration == snap.ControlGeneration {
		t.Fatalf("接管之后的快照代数应与接管前不同：%d", fresh.ControlGeneration)
	}

	resumed, err := env.svc.Resume(context.Background(), env.exec, tab)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Mode != browser.ControlResuming {
		t.Fatalf("恢复后应进入 resuming（等待新快照），实际 %q", resumed.Mode)
	}

	// 恢复会再递增一次代数：接管前/恢复前的 ref 都失效，且没有派发。
	for _, tc := range []struct {
		name string
		snap browser.Snapshot
	}{
		{"接管前的快照", snap},
		{"恢复前（human 期间）的快照", fresh},
	} {
		_, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
			Kind: browser.ActionFill, SnapshotID: tc.snap.ID, Ref: nameRef, Text: "旧",
		})
		mustErrCode(t, err, contracts.CodeStaleSnapshot, tc.name)
	}
	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("旧快照的动作被派发了 %d 次", n)
	}

	// resuming 期间也不能写：必须 observe 一份新快照。
	if _, err := env.svc.Observe(context.Background(), env.exec, tab, browser.ObserveOptions{}); err != nil {
		t.Fatalf("Observe(resuming): %v", err)
	}
	cs, err := env.svc.Control(env.exec, tab)
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if cs.Mode != browser.ControlAgent {
		t.Fatalf("新快照之后应回到 agent，实际 %q", cs.Mode)
	}
	newSnap, err := env.svc.Observe(context.Background(), env.exec, tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: newSnap.ID, Ref: env.backend.RefOf(tab, "name"), Text: "新",
	}); err != nil {
		t.Fatalf("恢复后应当可以用新快照写: %v", err)
	}
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("新快照的动作派发计数 %d，期望 1", n)
	}

	observeBR11(t, env, tab, "resume", map[string]any{
		"resumeWithoutSnapshot": "STALE_SNAPSHOT",
		"modeAfterResume":       string(resumed.Mode),
		"generationAfterResume": resumed.Generation,
		"staleRejections":       2,
		"backendDispatched":     env.backend.Dispatched(),
	})
}

// ---------- BR12：跨 session 与 tab 限额 ----------

func TestCrossSessionTabDenied(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	tab, snap := env.openAndObserve(t)
	nameRef := snap.Elements[0].Ref

	// session B 观察/写/关闭 A 的 tab：全部稳定拒绝。
	_, err := env.svc.Observe(context.Background(), env.other, tab, browser.ObserveOptions{})
	mustErrCode(t, err, contracts.CodeUnauthorized, "session B observe A 的 tab")
	_, err = env.svc.Execute(context.Background(), env.other, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "B",
	})
	mustErrCode(t, err, contracts.CodeUnauthorized, "session B 写 A 的 tab")
	_, err = env.svc.TakeOver(context.Background(), env.other, tab)
	mustErrCode(t, err, contracts.CodeUnauthorized, "session B 接管 A 的 tab")
	err = env.svc.CloseTab(context.Background(), env.other, tab)
	mustErrCode(t, err, contracts.CodeUnauthorized, "session B 关闭 A 的 tab")

	if n := env.backend.Dispatched(); n != 0 {
		t.Fatalf("跨 session 操作派发了 %d 次输入", n)
	}
	if n := env.backend.Instances(); n != 1 {
		t.Fatalf("跨 session 拒绝不应影响实例数，实际 %d", n)
	}

	// A 自己仍然是好的（不是把 tab 弄坏了的那种"拒绝"）。
	if _, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "A",
	}); err != nil {
		t.Fatalf("owner 的写操作失败: %v", err)
	}

	observeBR12(t, env, "cross-session", map[string]any{
		"tabId":             string(tab),
		"ownerSession":      string(env.exec.SessionID),
		"foreignSession":    string(env.other.SessionID),
		"backendDispatched": env.backend.Dispatched(),
		"foreignObserve":    "UNAUTHORIZED",
		"foreignExecute":    "UNAUTHORIZED",
		"foreignTakeOver":   "UNAUTHORIZED",
		"foreignClose":      "UNAUTHORIZED",
	})
}

func TestTabLimit(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	ctx := context.Background()

	// session A 建 4 个（上限），第 5 个必须在创建实例之前被拒绝。
	for i := 0; i < browser.MaxTabsPerSession; i++ {
		if _, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.fixture.Primary.URL + "/form"}); err != nil {
			t.Fatalf("第 %d 个 tab 创建失败: %v", i+1, err)
		}
	}
	created := env.backend.CreateCalls()
	_, err := env.svc.OpenTab(ctx, env.exec, browser.TabOptions{URL: env.fixture.Primary.URL + "/form"})
	mustErrCode(t, err, browser.CodeTabLimitExceeded, "第 5 个 tab（session 上限 4）")
	if got := env.backend.CreateCalls(); got != created {
		t.Fatalf("第 5 个 tab 被拒绝时仍创建了实例：CreateCalls %d → %d", created, got)
	}
	if got := env.backend.Instances(); got != browser.MaxTabsPerSession {
		t.Fatalf("实例数 %d，期望 %d", got, browser.MaxTabsPerSession)
	}

	// 三个 session 各 4 个 = 12（总计上限）。第 13 个拒绝且不创建实例。
	for _, s := range []contracts.SessionID{"sess-b", "sess-c"} {
		exec := contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: s, RunID: "run-x", ConfigRevision: 1}
		for i := 0; i < browser.MaxTabsPerSession; i++ {
			if _, err := env.svc.OpenTab(ctx, exec, browser.TabOptions{URL: env.fixture.Primary.URL + "/form"}); err != nil {
				t.Fatalf("session %s 第 %d 个 tab 创建失败: %v", s, i+1, err)
			}
		}
	}
	beforeTotal := env.backend.CreateCalls()
	sessD := contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-d", RunID: "run-y", ConfigRevision: 1}
	_, err = env.svc.OpenTab(ctx, sessD, browser.TabOptions{URL: env.fixture.Primary.URL + "/form"})
	mustErrCode(t, err, browser.CodeTabLimitExceeded, "第 13 个 tab（总计上限 12）")
	if got := env.backend.CreateCalls(); got != beforeTotal {
		t.Fatalf("第 13 个 tab 被拒绝时仍创建了实例：CreateCalls %d → %d", beforeTotal, got)
	}
	if got := env.backend.Instances(); got != browser.MaxTabsTotal {
		t.Fatalf("实例数 %d，期望 %d", got, browser.MaxTabsTotal)
	}

	observeBR12(t, env, "limits", map[string]any{
		"maxPerSession":      browser.MaxTabsPerSession,
		"maxTotal":           browser.MaxTabsTotal,
		"createCalls":        env.backend.CreateCalls(),
		"instances":          env.backend.Instances(),
		"fifthRejected":      browser.CodeTabLimitExceeded,
		"thirteenthRejected": browser.CodeTabLimitExceeded,
	})
}

// ---------- BR14：已派发超时 → OutcomeUnknown，且不重放 ----------

func TestDispatchedTimeoutIsOutcomeUnknownAndNotReplayed(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	// 把派发超时压到毫秒级：真实值是 ActionTimeout(15s)，语义一致。
	svc, err := browser.NewService(browser.ServiceOptions{
		Backend:       env.backend,
		Authorizer:    env.authz,
		ActionTimeout: 60 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	env.svc = svc

	tab, snap := env.openAndObserve(t)
	nameRef := snap.Elements[0].Ref
	env.backend.Latency = func(kind browser.ActionKind, ref string) time.Duration {
		if kind == browser.ActionFill {
			return 400 * time.Millisecond
		}
		return 0
	}

	res, err := svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "超时",
	})
	mustErrCode(t, err, contracts.CodeOutcomeUnknown, "已派发但超时")
	if !strings.Contains(err.Error(), "不得自动重放") && !strings.Contains(err.Error(), "不重放") {
		t.Fatalf("OutcomeUnknown 的文案必须明确不得重放: %v", err)
	}
	if res.Delivered {
		t.Fatalf("超时时不应声称 Delivered=true（输入是否落地未知）")
	}

	// 恰好一次尝试：没有自动重试。
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("backend 派发计数 %d，期望恰好 1（不得自动重放）", n)
	}
	if log := env.backend.ActLog(); len(log) != 1 {
		t.Fatalf("动作日志 %d 行，期望 1 行", len(log))
	}

	// 无法确认静止 → 状态保持 control_uncertain，不能自动回到 agent。
	cs, err := svc.Control(env.exec, tab)
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if cs.Mode != browser.ControlUncertain {
		t.Fatalf("已派发超时后状态应为 control_uncertain，实际 %q", cs.Mode)
	}

	// 再写一次必须被拒绝且不派发（不确定状态下不叠加新输入）。
	_, err = svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "重放",
	})
	mustErrCode(t, err, contracts.CodeControlUncertain, "control_uncertain 期间的写操作")
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("不确定状态下仍然派发了输入：%d", n)
	}

	observeBR14(t, env, tab, "timeout", map[string]any{
		"outcomeUnknown":    contracts.CodeOutcomeUnknown,
		"mode":              string(cs.Mode),
		"backendDispatched": env.backend.Dispatched(),
		"actLogRows":        len(env.backend.ActLog()),
		"replayBlocked":     contracts.CodeControlUncertain,
	})
}

// ---------- BR14：导航后不派发旧队列写操作 ----------

func TestNavigationRevokesQueuedWrites(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	tab, snap := env.openAndObserve(t)
	nameRef := snap.Elements[0].Ref

	env.backend.Latency = func(kind browser.ActionKind, ref string) time.Duration {
		if ref == nameRef {
			return 300 * time.Millisecond
		}
		return 0
	}
	done := make(chan error, 1)
	go func() {
		_, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
			Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "慢",
		})
		done <- err
	}()
	waitFor(t, 3*time.Second, "慢动作被派发", func() bool { return env.backend.Dispatched() == 1 })

	queued := make(chan error, 1)
	go func() {
		_, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
			Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "排队",
		})
		queued <- err
	}()
	waitFor(t, 3*time.Second, "排队动作入队", func() bool {
		info, err := env.svc.TabInfo(env.exec, tab)
		return err == nil && info.Queued == 1
	})

	// 导航：旧快照与旧队列项立即失效，且排队项不会被派发。
	if err := env.svc.Navigate(context.Background(), env.exec, tab, env.fixture.Primary.URL+"/form?step=2"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	select {
	case err := <-queued:
		mustErrCode(t, err, browser.CodeActionRevoked, "导航后旧队列写动作")
	case <-time.After(5 * time.Second):
		t.Fatal("导航后排队动作既没有撤销也没有结束")
	}
	// 已在飞的那一个不受"不派发"保护：它可能因为页面已经换文档而在引擎侧失败
	// （STALE_SNAPSHOT），也可能正常完成——两种都不改变"没有新的派发"这一结论。
	if err := <-done; err != nil {
		if got := errCode(err); got != contracts.CodeStaleSnapshot {
			t.Fatalf("已在飞的慢动作只允许以 STALE_SNAPSHOT 结束（页面已换文档），实际 %v", err)
		}
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("导航后不应发生任何提交，counter=%d", n)
	}
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("导航后仍派发了旧队列写操作：派发计数 %d", n)
	}

	observeBR14(t, env, tab, "navigation", map[string]any{
		"queuedRevoked":     browser.CodeActionRevoked,
		"backendDispatched": env.backend.Dispatched(),
	})
}

// ---------- 授权：改写期间的两次检查 ----------

func TestAuthorizationIsCheckedBeforeEnqueueAndBeforeInput(t *testing.T) {
	env := newCtlEnv(t, time.Minute)
	tab, snap := env.openAndObserve(t)
	nameRef := snap.Elements[0].Ref

	if _, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "允许",
	}); err != nil {
		t.Fatalf("允许路径失败: %v", err)
	}
	if env.authz.recheckCount() < 1 {
		t.Fatal("派发前没有做第二次授权检查")
	}

	// 第 2 次授权起拒绝：入队前就被挡住，backend 派发计数不变。
	env.authz.mu.Lock()
	env.authz.denyAt = 2
	env.authz.mu.Unlock()
	_, err := env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "拒绝",
	})
	mustErrCode(t, err, contracts.CodeUnauthorized, "策略拒绝的写动作")
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("策略拒绝后仍有派发：%d", n)
	}

	env.authz.mu.Lock()
	env.authz.denyAt = 0
	env.authz.deny = true
	env.authz.mu.Unlock()
	_, err = env.svc.Execute(context.Background(), env.exec, tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: nameRef, Text: "再拒绝",
	})
	mustErrCode(t, err, contracts.CodeUnauthorized, "持续拒绝的写动作")
	if n := env.backend.Dispatched(); n != 1 {
		t.Fatalf("持续拒绝后仍有派发：%d", n)
	}

	observePolicyDecisions(t, env, "authorize")
}

// ---------- 工具 ----------

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
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

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------- 证据 artifact ----------

func browserObsDir() string { return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR")) }

func browserObsWrite(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := browserObsDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写 artifact %s 失败: %v", name, err)
	}
}

func browserObsAppendJSONL(t *testing.T, name string, rows []any) {
	t.Helper()
	if browserObsDir() == "" || len(rows) == 0 {
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
	path := filepath.Join(browserObsDir(), filepath.FromSlash(name))
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

// browserObsMerge 把若干事实合并进 observation.json（与其它域同一套写法）。
func browserObsMerge(t *testing.T, facts map[string]any) {
	t.Helper()
	dir := browserObsDir()
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

// observeBR11 落盘 BR11 的 artifact：动作时间线 + 控制状态快照 + 服务端计数。
func observeBR11(t *testing.T, env *ctlEnv, tab browser.TabID, section string, facts map[string]any) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	writeTimelineArtifact(t, env)
	writeControlStates(t, env, tab, section)
	writeCountersArtifact(t, env, section)
	browserObsMerge(t, prefixFacts(section, facts))
}

func observeBR12(t *testing.T, env *ctlEnv, section string, facts map[string]any) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	writeTimelineArtifact(t, env)
	writeCountersArtifact(t, env, section)
	browserObsMerge(t, prefixFacts(section, facts))
}

func observeBR14(t *testing.T, env *ctlEnv, tab browser.TabID, section string, facts map[string]any) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	writeTimelineArtifact(t, env)
	writeControlStates(t, env, tab, section)
	writeActionLog(t, env)
	writeCountersArtifact(t, env, section)
	browserObsMerge(t, prefixFacts(section, facts))
}

func observePolicyDecisions(t *testing.T, env *ctlEnv, section string) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	rows := make([]any, 0, 8)
	for _, d := range env.authz.snapshot() {
		rows = append(rows, map[string]any{
			"section": section, "stage": d.Stage, "action": d.Action,
			"ref": d.Ref, "origin": d.Origin, "generation": d.Gen, "result": d.Result,
		})
	}
	browserObsAppendJSONL(t, "policy-decisions.jsonl", rows)
}

func writeTimelineArtifact(t *testing.T, env *ctlEnv) {
	t.Helper()
	rows := make([]any, 0, len(env.svc.Timeline()))
	for _, e := range env.svc.Timeline() {
		rows = append(rows, e)
	}
	browserObsAppendJSONL(t, "action-timeline.jsonl", rows)
}

func writeActionLog(t *testing.T, env *ctlEnv) {
	t.Helper()
	rows := make([]any, 0, len(env.backend.ActLog()))
	for _, r := range env.backend.ActLog() {
		rows = append(rows, r)
	}
	browserObsAppendJSONL(t, "backend-actions.jsonl", rows)
}

func writeControlStates(t *testing.T, env *ctlEnv, tab browser.TabID, section string) {
	t.Helper()
	cs, err := env.svc.Control(env.exec, tab)
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	info, err := env.svc.TabInfo(env.exec, tab)
	if err != nil {
		t.Fatalf("TabInfo: %v", err)
	}
	browserObsAppendJSONL(t, "control-states.jsonl", []any{map[string]any{
		"section": section, "control": cs, "tab": info,
	}})
}

func writeCountersArtifact(t *testing.T, env *ctlEnv, section string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"section":   section,
		"primary":   env.fixture.Primary.Counters(),
		"secondary": env.fixture.Secondary.Counters(),
		"backend": map[string]any{
			"createCalls": env.backend.CreateCalls(),
			"instances":   env.backend.Instances(),
			"dispatched":  env.backend.Dispatched(),
		},
	})
	if err != nil {
		t.Fatalf("序列化计数快照失败: %v", err)
	}
	browserObsWrite(t, "counters-"+section+".json", raw)
}

func prefixFacts(section string, facts map[string]any) map[string]any {
	out := map[string]any{"section": section}
	for k, v := range facts {
		out[section+"_"+k] = v
	}
	return out
}
