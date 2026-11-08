package policy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// fakeClock 让"60 秒过期"这类断言不依赖真实等待。
type fakeClock struct{ t time.Time }

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newService(clock *fakeClock) *Service {
	return New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Network: false, Shell: false},
		Now:         clock.Now,
		ApprovalTTL: 60 * time.Second,
	})
}

func testExec(session contracts.SessionID) contracts.ExecutionContext {
	return contracts.ExecutionContext{
		WorkspaceID:       "ws",
		SessionID:         session,
		RunID:             "run-1",
		ConfigRevision:    3,
		ControlGeneration: 1,
	}
}

func jsonArgs(s string) json.RawMessage { return json.RawMessage(s) }

// TestApprovalBindsArgumentsAndExpires 覆盖 SEC06：参数变更、跨会话、过期、二次消费均失效；
// 被拒绝时工具副作用计数必须为 0。
func TestApprovalBindsArgumentsAndExpires(t *testing.T) {
	ctx := context.Background()
	clock := newClock()
	svc := newService(clock)
	exec := testExec("s1")

	var sideEffects int32
	effect := func(context.Context) error { atomic.AddInt32(&sideEffects, 1); return nil }

	call := ToolCall{ID: "c1", Name: "mcp_custom", Source: SourceExternalMCP, Arguments: jsonArgs(`{"q":"a"}`), Origin: "user"}
	res, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision != DecisionAsk || res.ApprovalID == "" {
		t.Fatalf("应要求审批并返回审批 ID，实际 %+v", res)
	}
	approvalID := res.ApprovalID

	// ask 阶段不得执行副作用。
	if _, err := svc.Guard(ctx, exec, call, effect); err != nil {
		t.Fatalf("Guard: %v", err)
	}
	if got := atomic.LoadInt32(&sideEffects); got != 0 {
		t.Fatalf("ask 阶段不得执行，实际 sideEffects=%d", got)
	}

	// 1) 参数变更：同一条审批不认新参数。
	changed := call
	changed.Arguments = jsonArgs(`{"q":"b"}`)
	if err := svc.ResolveApproval(ctx, approvalID, ApprovalOnce, changed); err == nil {
		t.Fatalf("参数变更后审批应失效")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// 2) 原参数仍可批准；批准所有权属于 s1。
	if err := svc.ResolveApproval(ctx, approvalID, ApprovalOnce, call); err != nil {
		t.Fatalf("合法审批应成功: %v", err)
	}

	// 3) 二次消费：一次性审批只能消费一次。
	if err := svc.ResolveApproval(ctx, approvalID, ApprovalOnce, call); err == nil {
		t.Fatalf("二次消费应被拒绝")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// 4) 跨会话绑定：其它 session 拿同一条审批无效。
	res2, err := svc.Authorize(ctx, testExec("s2"), call)
	if err != nil {
		t.Fatalf("Authorize(s2): %v", err)
	}
	if err := svc.ValidateBinding(ctx, testExec("s2"), res2.ApprovalID, call); err != nil {
		t.Fatalf("s2 自己的审批应可校验: %v", err)
	}
	if err := svc.ValidateBinding(ctx, testExec("s2"), approvalID, call); err == nil {
		t.Fatalf("s2 不得使用 s1 的审批")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// 5) 过期：60 秒后审批失效。
	res3, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	clock.Advance(61 * time.Second)
	if err := svc.ResolveApproval(ctx, res3.ApprovalID, ApprovalOnce, call); err == nil {
		t.Fatalf("过期审批应被拒绝")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// oracle：全程副作用计数仍为 0（没有任何路径绕过 allow）。
	if got := atomic.LoadInt32(&sideEffects); got != 0 {
		t.Fatalf("全过程不得执行工具副作用，实际 %d", got)
	}
}

// TestApprovalBindsObservedBySubprocessExecutor 是 SEC01 的外部观察版本。
//
// 与 TestApprovalBindsArgumentsAndExpires 判的是同一批结论（ask 不执行、一次性
// 不可重放、会话级放行、接管后失效），区别只在"执行"由谁完成：这里是一个
// **独立进程**（testdata/executor），它自己把每次真实执行追加进 executions.jsonl
// 并写出目标文件。断言与观察值都从磁盘读回，测试进程内的计数器不参与判定。
func TestApprovalBindsObservedBySubprocessExecutor(t *testing.T) {
	ctx := context.Background()
	clock := newClock()
	root := t.TempDir()
	svc := New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Network: false, Shell: false},
		PathGuard:   mustGuard(t, root),
		Now:         clock.Now,
		ApprovalTTL: 60 * time.Second,
	})
	exec := testExec("s1")
	bin := executorPath(t)
	logPath := filepath.Join(policyArtifactDir(t), "executions.jsonl")
	// 账本必须存在："一次都没执行"本身也是观察值，不能靠文件缺失来表达。
	_ = os.Remove(logPath)
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("创建执行账本失败: %v", err)
	}

	call := ToolCall{ID: "c1", Name: "mcp_custom", Source: SourceExternalMCP, Arguments: jsonArgs(`{"q":"a"}`), Origin: "user"}
	onceTarget := filepath.Join(root, "once.txt")
	sessionTarget := filepath.Join(root, "session.txt")

	// ── 1) ask 与权限不足：Guard 是唯一入口，effect 不得被调用 ──
	// 下面两个闭包里才调独立执行进程；策略不放行时闭包根本不会跑。
	if _, err := svc.Guard(ctx, exec, call, func(context.Context) error {
		runExecutor(t, bin, logPath, "ask-denied", call.Name, filepath.Join(root, "ask.txt"), "should-not-exist")
		return nil
	}); err != nil {
		t.Fatalf("Guard(ask): %v", err)
	}
	bash := ToolCall{ID: "c2", Name: "bash", Source: SourceBuiltin, Arguments: jsonArgs(`{}`), Origin: "user"}
	if _, err := svc.Guard(ctx, exec, bash, func(context.Context) error {
		runExecutor(t, bin, logPath, "perm-denied", bash.Name, filepath.Join(root, "bash.txt"), "should-not-exist")
		return nil
	}); err != nil {
		t.Fatalf("Guard(perm): %v", err)
	}

	// ── 2) 一次性审批：批准后执行一次，重放不得执行 ──
	res, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("外部 MCP 调用应要求审批，实际 %s", res.Decision)
	}
	if err := svc.ResolveApproval(ctx, res.ApprovalID, ApprovalOnce, call); err != nil {
		t.Fatalf("批准一次: %v", err)
	}
	if err := svc.ValidateBinding(ctx, exec, res.ApprovalID, call); err != nil {
		t.Fatalf("批准后的执行前复查应通过: %v", err)
	}
	runExecutor(t, bin, logPath, "once", call.Name, onceTarget, "once")
	if err := svc.ValidateBinding(ctx, exec, res.ApprovalID, call); err == nil {
		t.Fatal("同一条一次性许可不得被领取两次")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// ── 3) 会话级许可：同工具后续调用直接放行，并真的执行一次 ──
	resGrant, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize(grant): %v", err)
	}
	if err := svc.ResolveApproval(ctx, resGrant.ApprovalID, ApprovalSessionScope, call); err != nil {
		t.Fatalf("会话级授权: %v", err)
	}
	next := call
	next.ID = "c3"
	next.Arguments = jsonArgs(`{"q":"b"}`)
	resNext, err := svc.Authorize(ctx, exec, next)
	if err != nil {
		t.Fatalf("Authorize(next): %v", err)
	}
	if resNext.Decision != DecisionAllow {
		t.Fatalf("会话级授权后应 allow，实际 %s", resNext.Decision)
	}
	runExecutor(t, bin, logPath, "session-scope", next.Name, sessionTarget, "session")

	// 另一个会话不受影响：仍是 ask，闭包不执行。
	resOther, err := svc.Authorize(ctx, testExec("s2"), call)
	if err != nil {
		t.Fatalf("Authorize(s2): %v", err)
	}
	if resOther.Decision != DecisionAsk {
		t.Fatalf("会话级授权不得跨会话生效，实际 %s", resOther.Decision)
	}

	// ── 4) 接管后旧审批失效：复查失败，不得执行 ──
	// 换一个工具名：call 已经命中上面的会话级许可，Authorize 会直接 allow 而不签发审批，
	// 那样这一阶段测的就不是"旧审批失效"了。
	stale := call
	stale.ID = "c4"
	stale.Name = "mcp_stale"
	resStale, err := svc.Authorize(ctx, exec, stale)
	if err != nil {
		t.Fatalf("Authorize(stale): %v", err)
	}
	taken := exec
	taken.ControlGeneration = 2
	if err := svc.ValidateBinding(ctx, taken, resStale.ApprovalID, stale); err == nil {
		t.Fatal("接管后旧审批必须失效")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// ── 观察值：全部从磁盘读回 ──
	records := readExecRecords(t, logPath)
	tree := walkObsTree(t, root)
	filesOnDisk := treeFilePaths(tree)

	if len(records) != 2 {
		t.Fatalf("账本里应有 2 次真实执行（once + session-scope），实际 %d 次: %v", len(records), execLabels(records))
	}
	if got := countExecLabel(records, "ask-denied"); got != 0 {
		t.Fatalf("未审批路径不得执行，账本里出现 %d 次", got)
	}
	if got := countExecLabel(records, "perm-denied"); got != 0 {
		t.Fatalf("权限不足路径不得执行，账本里出现 %d 次", got)
	}
	if got := countExecLabel(records, "once"); got != 1 {
		t.Fatalf("一次性许可应恰好执行 1 次，账本里是 %d 次", got)
	}
	if got := countExecLabel(records, "session-scope"); got != 1 {
		t.Fatalf("会话级许可应恰好执行 1 次，账本里是 %d 次", got)
	}
	if got := countExecLabel(records, "stale-generation"); got != 0 {
		t.Fatalf("接管后不得执行，账本里出现 %d 次", got)
	}

	// 磁盘上的文件是第二条独立证据：只有两次放行各自的目标文件存在。
	want := []string{"once.txt", "session.txt"}
	if strings.Join(filesOnDisk, ",") != strings.Join(want, ",") {
		t.Fatalf("沙箱根下应只有 %v，实际 %v", want, filesOnDisk)
	}
	for _, rec := range records {
		if _, err := os.Stat(rec.Target); err != nil {
			t.Fatalf("账本记录了执行但目标文件不存在（%s）: %v", rec.Target, err)
		}
	}

	pids := make([]int, 0, len(records))
	seen := map[int]bool{}
	for _, rec := range records {
		if !seen[rec.PID] {
			seen[rec.PID] = true
			pids = append(pids, rec.PID)
		}
	}
	treeJSON, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		t.Fatalf("序列化 tree.json 失败: %v", err)
	}
	writePolicyObservation(t, map[string]any{
		"executions":                len(records),
		"executedLabels":            execLabels(records),
		"executedArgs":              records,
		"deniedWithoutApproval":     countExecLabel(records, "ask-denied") + countExecLabel(records, "perm-denied"),
		"sessionScopeExecutions":    countExecLabel(records, "session-scope"),
		"staleGenerationExecutions": countExecLabel(records, "stale-generation"),
		"filesOnDisk":               filesOnDisk,
		"treeEntries":               len(tree),
		"executorPIDs":              pids,
		"recordedBy":                "独立进程 internal/policy/testdata/executor 追加的 executions.jsonl；tree.json 由父进程遍历磁盘",
	}, map[string][]byte{"tree.json": treeJSON})
}

// TestPermissionDeniesWithoutApproval 覆盖"权限不足直接 deny，不给审批机会"。
func TestPermissionDeniesWithoutApproval(t *testing.T) {
	ctx := context.Background()
	svc := newService(newClock())
	exec := testExec("s1")

	cases := []struct {
		name string
		call ToolCall
	}{
		{"bash 需要 shell 权限", ToolCall{ID: "c", Name: "bash", Source: SourceBuiltin, Arguments: jsonArgs(`{}`)}},
		{"http 需要网络权限", ToolCall{ID: "c", Name: "http_fetch", Source: SourceBuiltin, Arguments: jsonArgs(`{}`)}},
		{"未知通道一律拒绝", ToolCall{ID: "c", Name: "mystery", Source: Source("unknown"), Arguments: jsonArgs(`{}`)}},
	}
	for _, tc := range cases {
		res, err := svc.Authorize(ctx, exec, tc.call)
		if err != nil {
			t.Fatalf("%s: Authorize 报错: %v", tc.name, err)
		}
		if res.Decision != DecisionDeny {
			t.Fatalf("%s: 应为 deny，实际 %s（%s）", tc.name, res.Decision, res.Reason)
		}
		if res.ApprovalID != "" {
			t.Fatalf("%s: deny 不应产生审批", tc.name)
		}
	}
}

// TestUntrustedProjectDoesNotExecuteMarker 覆盖 SEC08 的策略层部分：
// 未受信任项目的扩展/MCP 调用直接拒绝，marker 副作用为 0。
func TestUntrustedProjectDoesNotExecuteMarker(t *testing.T) {
	ctx := context.Background()
	clock := newClock()
	svc := New(Options{
		Permissions:    config.Permissions{Filesystem: "workspace", Network: true, Shell: true},
		TrustedOrigins: map[string]bool{`D:\trusted\proj`: true},
		Now:            clock.Now,
	})
	exec := testExec("s1")

	var marker int32
	effect := func(context.Context) error { atomic.AddInt32(&marker, 1); return nil }

	untrusted := ToolCall{
		ID: "c1", Name: "proj_extension_tool", Source: SourceExtension,
		Arguments: jsonArgs(`{}`), Origin: `project:D:\untrusted\proj`,
	}
	res, err := svc.Guard(ctx, exec, untrusted, effect)
	if err != nil {
		t.Fatalf("Guard: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("未信任项目应 deny，实际 %s", res.Decision)
	}
	if got := atomic.LoadInt32(&marker); got != 0 {
		t.Fatalf("未信任项目不得执行 marker，实际执行 %d 次", got)
	}

	// 受信任项目走正常审批路径（ask），未批准前仍不执行。
	trusted := untrusted
	trusted.ID = "c2"
	trusted.Origin = `D:\trusted\proj`
	trusted.Source = SourceExtension
	res, err = svc.Guard(ctx, exec, trusted, effect)
	if err != nil {
		t.Fatalf("Guard(trusted): %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("受信任项目的扩展调用应进入审批，实际 %s（%s）", res.Decision, res.Reason)
	}
	if got := atomic.LoadInt32(&marker); got != 0 {
		t.Fatalf("未批准前不得执行，实际 %d", got)
	}
}

// TestSessionScopeGrantAllowsSubsequentCalls 覆盖 session_scope：
// 授权后同会话同工具的后续调用不再逐次审批，但超期后重新审批。
func TestSessionScopeGrantAllowsSubsequentCalls(t *testing.T) {
	ctx := context.Background()
	clock := newClock()
	svc := newService(clock)
	exec := testExec("s1")
	call := ToolCall{ID: "c1", Name: "mcp_custom", Source: SourceExternalMCP, Arguments: jsonArgs(`{"q":"a"}`), Origin: "user"}

	res, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := svc.ResolveApproval(ctx, res.ApprovalID, ApprovalSessionScope, call); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	// 同工具不同参数也应放行（session_scope 语义按工具授权）。
	next := call
	next.ID = "c2"
	next.Arguments = jsonArgs(`{"q":"b"}`)
	res2, err := svc.Authorize(ctx, exec, next)
	if err != nil {
		t.Fatalf("Authorize(next): %v", err)
	}
	if res2.Decision != DecisionAllow {
		t.Fatalf("会话级授权后应 allow，实际 %s", res2.Decision)
	}

	// 另一个会话不受影响。
	res3, err := svc.Authorize(ctx, testExec("s2"), call)
	if err != nil {
		t.Fatalf("Authorize(s2): %v", err)
	}
	if res3.Decision != DecisionAsk {
		t.Fatalf("会话级授权不得跨会话生效，实际 %s", res3.Decision)
	}

	// 过期后重新要求审批。
	clock.Advance(61 * time.Second)
	res4, err := svc.Authorize(ctx, exec, next)
	if err != nil {
		t.Fatalf("Authorize(expired): %v", err)
	}
	if res4.Decision != DecisionAsk {
		t.Fatalf("会话级授权过期后应重新审批，实际 %s", res4.Decision)
	}
}

// TestControlGenerationInvalidatesApproval 覆盖"接管后旧许可失效"。
func TestControlGenerationInvalidatesApproval(t *testing.T) {
	ctx := context.Background()
	svc := newService(newClock())
	exec := testExec("s1")
	call := ToolCall{ID: "c1", Name: "mcp_custom", Source: SourceExternalMCP, Arguments: jsonArgs(`{}`), Origin: "user"}

	res, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	taken := exec
	taken.ControlGeneration = 2
	if err := svc.ValidateBinding(ctx, taken, res.ApprovalID, call); err == nil {
		t.Fatalf("接管后旧审批必须失效")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	bumped := exec
	bumped.ConfigRevision = 4
	if err := svc.ValidateBinding(ctx, bumped, res.ApprovalID, call); err == nil {
		t.Fatalf("配置变更后旧审批必须失效")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	appErr, ok := err.(*contracts.AppError)
	if !ok {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestBrowserToolsAreNotGatedByNetworkPermission 守一条实测踩到的产品缺陷：
//
// 浏览器工具是**产品内代理**（只操作已挂载的 WebView2，写动作还要人工审批），
// 模型拿它们不能发起任意出网请求。用通用"网络权限"拦它们，等于把浏览器能力整体
// 关掉——用户看到"未获授权：当前有效配置未授予网络权限"，而面板明明可用。
//
// 真正的任意出网工具（http/fetch/web）继续受网络权限约束（下一条断言）。
func TestBrowserToolsAreNotGatedByNetworkPermission(t *testing.T) {
	svc := New(Options{Permissions: config.Permissions{Filesystem: "workspace", Network: false, Shell: false}})
	exec := contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-1", RunID: "run-1", ConfigRevision: 1}

	// 浏览器读：不因网络权限被 deny。
	res, err := svc.Authorize(context.Background(), exec, ToolCall{
		ID: "c-tabs", Name: "browser_tabs", Source: SourceHarnessMCP, Origin: "user",
		Arguments: jsonArgs(`{}`),
	})
	if err != nil {
		t.Fatalf("Authorize(browser_tabs): %v", err)
	}
	if res.Decision == DecisionDeny && strings.Contains(res.Reason, "网络权限") {
		t.Fatalf("浏览器工具不应被网络权限拒绝: %+v", res)
	}

	// 任意出网工具仍然受限（证明不是把网络权限整体关掉）。
	fetchRes, err := svc.Authorize(context.Background(), exec, ToolCall{
		ID: "c-fetch", Name: "fetch_url", Source: SourceHarnessMCP, Origin: "user",
		Arguments: jsonArgs(`{"url":"http://example.com"}`),
	})
	if err != nil {
		t.Fatalf("Authorize(fetch_url): %v", err)
	}
	if fetchRes.Decision != DecisionDeny || !strings.Contains(fetchRes.Reason, "网络权限") {
		t.Fatalf("fetch_url 在未授予网络权限时必须 deny 并说明原因: %+v", fetchRes)
	}
}
