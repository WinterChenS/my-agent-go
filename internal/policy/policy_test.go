package policy

import (
	"context"
	"encoding/json"
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
