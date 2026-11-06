package run

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/runtime/pi"
)

// TestSettledWhileWaitingApprovalDoesNotHang 覆盖「run 在等待审批时被结算」。
//
// 场景是真实存在的：审批请求发出去之后，Pi 侧的工具调用可能因为自己的超时/取消
// 先结束，于是本轮照常走到 agent_settled。此时 run 还停在 waiting_approval，
// 而 waiting_approval → completed 不是合法迁移（见 state.go 的迁移表）。
//
// 旧实现直接 `_ = s.transition(completed)` 把错误丢掉：状态没变、也没有任何终态
// 事件，界面会永远显示「等待确认」，而这一轮其实早就结束了。这里断言终态落地、
// pending 被清空、并且阻塞在审批上的工具拿到拒绝（fail-closed），而不是一直等满
// 60 秒 TTL。
func TestSettledWhileWaitingApprovalDoesNotHang(t *testing.T) {
	opts := testOptions(t)
	guard, err := policy.NewPathGuard(opts.WorkspaceDir)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	opts.Policy = policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace"},
		PathGuard:   guard,
		ApprovalTTL: time.Minute,
	})
	w := newFakeWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	rs := runStateOf(t, svc, snap.RunID)
	if rs.bridge == nil {
		t.Fatal("启用 Policy 时必须建立执行前授权端点")
	}

	type authResult struct {
		decision policy.Decision
		err      error
	}
	done := make(chan authResult, 1)
	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, opts.SessionID, 30*time.Second)
	go func() {
		dec, _, err := client.Authorize(context.Background(), policy.ToolCall{
			ID: "c-1", Name: "fixture_write", Source: policy.SourceExternalMCP, Origin: "user",
		})
		done <- authResult{decision: dec, err: err}
	}()

	if !eventually(t, 5*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateWaitingApproval
	}) {
		t.Fatalf("authorize 需要审批时 run 应进入 waiting_approval，实际 %s", mustRunState(t, svc, snap.RunID))
	}

	// Pi 侧本轮结束：agent_settled 落在 waiting_approval 上。
	sendRaw(w, "agent_settled", `{"type":"agent_settled","aborted":false}`)

	if !eventually(t, 5*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateCompleted
	}) {
		t.Fatalf("等待审批期间的结算必须落地为终态，实际停在 %s", mustRunState(t, svc, snap.RunID))
	}

	rs.mu.Lock()
	pending := len(rs.pending)
	rs.mu.Unlock()
	if pending != 0 {
		t.Fatalf("结算时必须释放等待中的审批，pending 还剩 %d 条", pending)
	}

	var resolved struct {
		ApprovalID string `json:"approvalId"`
		Choice     string `json:"choice"`
		Reason     string `json:"reason"`
	}
	evs := eventsOfType(t, svc, snap.RunID, EventApprovalResolved)
	if len(evs) != 1 {
		t.Fatalf("应只发一条 approval.resolved，实际 %d 条", len(evs))
	}
	if err := json.Unmarshal(evs[0].Payload, &resolved); err != nil {
		t.Fatalf("解析 approval.resolved 载荷失败: %v", err)
	}
	if resolved.Choice != string(policy.ApprovalDeny) || resolved.Reason != "run_settled" {
		t.Fatalf("结算释放的审批必须 fail-closed（deny/run_settled），实际 %+v", resolved)
	}

	select {
	case res := <-done:
		if res.err == nil && res.decision == policy.DecisionAllow {
			t.Fatal("本轮已经结束，等待中的工具调用不得被放行")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("结算后审批请求仍挂着（在等 60 秒 TTL）")
	}

	writeCaseJournal(t, svc)
}
