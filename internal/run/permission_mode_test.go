package run

// 审批档位在 run 层的口径（规格 docs/specs/2026-11-08-permission-mode-full-access.md）。
//
// 这里钉住三件事：
//   1. 档位在 admission 冻结进 ExecutionBinding（不是每次判定时现读设置）；
//   2. 运行中提交的第二条 run 换档位，不影响第一条；
//   3. 每次自动放行发一条 policy.auto_allowed，走与其它 run 事件相同的订阅者。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/runtime/pi"
)

// permissionModeOptions 构造一套带策略服务（工作区内写动作本来会 ask）的选项。
func permissionModeOptions(t *testing.T) Options {
	t.Helper()
	opts := testOptions(t)
	guard, err := policy.NewPathGuard(opts.WorkspaceDir)
	if err != nil {
		t.Fatalf("建路径守卫失败: %v", err)
	}
	perms := config.Permissions{Filesystem: "workspace", Shell: true}
	opts.Policy = policy.New(policy.Options{Permissions: perms, PathGuard: guard})
	opts.PolicyCeiling = PolicyCeiling{Permissions: perms, Ref: "ceiling:test"}
	opts.PolicySnapshot = PolicySnapshot{Permissions: perms}
	opts.StartWorker = func(context.Context, pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}
	return opts
}

// writeCallIn 构造一次工作区内的写入调用（本来一定会 ask）。
func writeCallIn(dir string) policy.ToolCall {
	return policy.ToolCall{
		ID:            "call-write",
		Name:          "write",
		Source:        policy.SourceBuiltin,
		Arguments:     json.RawMessage(`{"path":"a.txt","content":"你好"}`),
		Origin:        "user",
		ResourceScope: filepath.Join(dir, "a.txt"),
	}
}

// TestPermissionModePinnedAtAdmissionOnlyAffectsNextRun 覆盖“运行中切换只影响新 run”：
//
// 第一条 run 以 full_access 受理并仍在跑；第二条以 ask 受理。
// 两条 run 各自的判定必须按各自的快照走，而不是按“此刻的设置”。
func TestPermissionModePinnedAtAdmissionOnlyAffectsNextRun(t *testing.T) {
	ctx := context.Background()
	opts := permissionModeOptions(t)
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	first, err := svc.Start(ctx, StartRunRequest{
		Message:        "第一条：完全访问",
		PermissionMode: config.PermissionModeFullAccess,
	})
	if err != nil {
		t.Fatalf("启动第一条失败: %v", err)
	}
	if first.State != contracts.RunStateRunning {
		t.Fatalf("第一条应已在跑，实际 %s", first.State)
	}

	second, err := svc.Start(ctx, StartRunRequest{
		Message:        "第二条：询问",
		PermissionMode: config.PermissionModeAsk,
	})
	if err != nil {
		t.Fatalf("启动第二条失败: %v", err)
	}

	firstBinding, ok := svc.Binding(first.RunID)
	if !ok {
		t.Fatal("找不到第一条 run 的 binding")
	}
	secondBinding, ok := svc.Binding(second.RunID)
	if !ok {
		t.Fatal("找不到第二条 run 的 binding")
	}
	if firstBinding.PermissionMode != config.PermissionModeFullAccess || !firstBinding.AutoApproves() {
		t.Fatalf("第一条 run 的档位应钉成 full_access，实际 %q", firstBinding.PermissionMode)
	}
	if secondBinding.PermissionMode != config.PermissionModeAsk || secondBinding.AutoApproves() {
		t.Fatalf("第二条 run 的档位应钉成 ask，实际 %q", secondBinding.PermissionMode)
	}

	// 判定按快照走：第一条自动放行，第二条仍然要人批。
	call := writeCallIn(opts.WorkspaceDir)
	res, err := opts.Policy.Authorize(ctx, firstBinding.ExecutionContext, call)
	if err != nil {
		t.Fatalf("第一条 Authorize 出错: %v", err)
	}
	if res.Decision != policy.DecisionAllow || res.Reason != policy.ReasonAutoAllowed {
		t.Fatalf("第一条 run 应被档位自动放行，实际 %+v", res)
	}
	res, err = opts.Policy.Authorize(ctx, secondBinding.ExecutionContext, call)
	if err != nil {
		t.Fatalf("第二条 Authorize 出错: %v", err)
	}
	if res.Decision != policy.DecisionAsk {
		t.Fatalf("第二条 run 仍应签发审批，实际 %+v", res)
	}

	// 空/未知档位一律钉成 ask：写错的配置不能变成永久免审。
	third, err := svc.Start(ctx, StartRunRequest{Message: "第三条：没写档位"})
	if err != nil {
		t.Fatalf("启动第三条失败: %v", err)
	}
	thirdBinding, _ := svc.Binding(third.RunID)
	if thirdBinding.PermissionMode != config.PermissionModeAsk {
		t.Fatalf("未指定档位应钉成 ask，实际 %q", thirdBinding.PermissionMode)
	}
}

// TestAutoAllowedCallEmitsEventOnRunStream：自动放行必须留痕，且走同一个事件流。
func TestAutoAllowedCallEmitsEventOnRunStream(t *testing.T) {
	ctx := context.Background()
	opts := permissionModeOptions(t)
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	var (
		mu     sync.Mutex
		events []contracts.Event
	)
	unsubscribe := svc.Subscribe(func(ev contracts.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	defer unsubscribe()

	snap, err := svc.Start(ctx, StartRunRequest{
		Message:        "跑一次完全访问",
		PermissionMode: config.PermissionModeFullAccess,
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	binding, ok := svc.Binding(snap.RunID)
	if !ok {
		t.Fatal("找不到 run binding")
	}

	call := writeCallIn(opts.WorkspaceDir)
	res, err := opts.Policy.Authorize(ctx, binding.ExecutionContext, call)
	if err != nil {
		t.Fatalf("Authorize 出错: %v", err)
	}
	if res.Decision != policy.DecisionAllow {
		t.Fatalf("full_access 下应放行，实际 %+v", res)
	}

	var found *contracts.Event
	if !eventually(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for i := range events {
			if events[i].Type == EventPolicyAutoAllowed {
				found = &events[i]
				return true
			}
		}
		return false
	}) {
		t.Fatal("自动放行没有产出 policy.auto_allowed 事件")
	}

	if found.RunID != snap.RunID || found.SessionID != snap.SessionID {
		t.Fatalf("事件归属应是这一条 run/会话，实际 run=%s session=%s", found.RunID, found.SessionID)
	}
	var payload autoAllowedPayload
	if err := json.Unmarshal(found.Payload, &payload); err != nil {
		t.Fatalf("事件载荷不是合法 JSON: %v", err)
	}
	if payload.ToolName != call.Name || payload.ToolCallID != call.ID {
		t.Fatalf("事件里应带上工具名与调用 ID，实际 %+v", payload)
	}
	if payload.RunID != string(snap.RunID) || payload.SessionID != string(snap.SessionID) {
		t.Fatalf("载荷里应带上 run/session，实际 %+v", payload)
	}
	if payload.PermissionMode != config.PermissionModeFullAccess {
		t.Fatalf("载荷应说明生效档位，实际 %q", payload.PermissionMode)
	}
	if payload.Reason != policy.ReasonAutoAllowed {
		t.Fatalf("载荷应带上可识别原因，实际 %q", payload.Reason)
	}
}

// TestPermissionModeReleasedWhenRunTerminates：run 终止后策略服务不再留着这条 run 的档位。
//
// 不留登记既是不让 map 无界增长，也是“终止的 run 不再有权威授权请求”的显式表达。
func TestPermissionModeReleasedWhenRunTerminates(t *testing.T) {
	ctx := context.Background()
	opts := permissionModeOptions(t)
	var w *fakeWorker
	opts.StartWorker = func(context.Context, pi.WorkerSpec) (pi.Worker, error) {
		w = newFakeWorker()
		return w, nil
	}
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	snap, err := svc.Start(ctx, StartRunRequest{
		Message:        "跑完就释放",
		PermissionMode: config.PermissionModeFullAccess,
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if got, ok := opts.Policy.RunPermissionMode(snap.RunID); !ok || got != config.PermissionModeFullAccess {
		t.Fatalf("运行中应登记 full_access，实际 %q (存在=%v)", got, ok)
	}

	w.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("应结算为 completed，实际 %s", state)
	}
	if !eventually(t, 2*time.Second, func() bool {
		_, ok := opts.Policy.RunPermissionMode(snap.RunID)
		return !ok
	}) {
		t.Fatal("run 终止后必须摘掉档位登记（否则登记表随 run 数量无界增长）")
	}
}
