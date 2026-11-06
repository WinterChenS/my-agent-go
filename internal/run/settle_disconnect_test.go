package run

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/runtime/pi"
)

// runWithWorker 用固定假 worker 启动一个 run，返回服务、worker 与快照。
func runWithWorker(t *testing.T, opts Options, w *fakeWorker) (*Service, contracts.RunSnapshot) {
	t.Helper()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	snapshot := startRunForTest(t, svc, "hi")
	return svc, snapshot
}

// feedRecording 把一份真实 Pi 捕获记录（jsonl）原样灌进假 worker 的事件通道。
//
// 用真实记录而不是手写事件，是因为"synthetic 里写得对、真实记录里字段名不同"
// 正是这类完成语义 bug 的典型来源。记录文件本身不动。
func feedRecording(t *testing.T, w *fakeWorker, scene string) {
	t.Helper()
	path := filepath.Join("..", "runtime", "pi", "testdata", "v1.5.0", scene)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开真实记录 %s 失败: %v", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			t.Fatalf("真实记录不是合法 JSON: %v", err)
		}
		w.events <- pi.RawEvent{Type: head.Type, Record: json.RawMessage(line)}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取真实记录失败: %v", err)
	}
}

// runStates 返回这个 run 收到过的全部 run.state 载荷。
func runStates(t *testing.T, svc *Service, id contracts.RunID) []string {
	t.Helper()
	var out []string
	for _, ev := range eventsOfType(t, svc, id, EventRunState) {
		var p statePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 run.state 载荷失败: %v", err)
		}
		out = append(out, p.State)
	}
	return out
}

func lastRunErrorMessage(t *testing.T, svc *Service, id contracts.RunID) string {
	t.Helper()
	evs := eventsOfType(t, svc, id, EventRunError)
	if len(evs) == 0 {
		t.Fatalf("run %s 没有任何 run.error 事件", id)
	}
	var p runErrorPayload
	if err := json.Unmarshal(evs[len(evs)-1].Payload, &p); err != nil {
		t.Fatalf("解析 run.error 载荷失败: %v", err)
	}
	return p.Message
}

func hasDiagnosticKind(t *testing.T, svc *Service, id contracts.RunID, kind string) bool {
	t.Helper()
	for _, ev := range eventsOfType(t, svc, id, EventDiagnostic) {
		var p struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err == nil && p.Kind == kind {
			return true
		}
	}
	return false
}

func sendRaw(w *fakeWorker, typ, record string) {
	w.events <- pi.RawEvent{Type: typ, Record: json.RawMessage(record)}
}

// TestRetryExhaustedSettlesAsFailedProviderError 覆盖 RUN01.service 的
// "重试耗尽不是完成"：用真实捕获的 provider-error.jsonl（3 次 auto_retry →
// auto_retry_end{success:false} → agent_settled{aborted:false}）驱动，
// 终态必须是 failed + PROVIDER_ERROR，且从未出现 completed。
func TestRetryExhaustedSettlesAsFailedProviderError(t *testing.T) {
	w := newFakeWorker()
	svc, snap := runWithWorker(t, testOptions(t), w)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	feedRecording(t, w, "provider-error.jsonl")

	if !eventually(t, 3*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateFailed
	}) {
		t.Fatalf("重试耗尽应为 failed，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	for _, st := range runStates(t, svc, snap.RunID) {
		if st == contracts.RunStateCompleted {
			t.Fatal("重试耗尽的 run 绝不能出现 completed")
		}
	}
	if code := lastRunErrorCode(t, svc, snap.RunID); code != contracts.CodeProviderError {
		t.Fatalf("run.error 应是 %s，实际 %s", contracts.CodeProviderError, code)
	}
	if msg := lastRunErrorMessage(t, svc, snap.RunID); !strings.Contains(msg, "500 fixture upstream failure") {
		t.Fatalf("run.error 必须带 provider 给的错误信息，实际 %q", msg)
	}

	// message.done 必须带上真实的 stopReason，不能是空对象。
	dones := eventsOfType(t, svc, snap.RunID, EventMessageDone)
	if len(dones) == 0 {
		t.Fatal("缺失 message.done 事件")
	}
	var sawError bool
	for _, ev := range dones {
		var p struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 message.done 载荷失败: %v", err)
		}
		if p.StopReason == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("message.done 必须带 stopReason=error（真实记录里 assistant 消息就是 error）")
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestSettledDuringCompactionDoesNotComplete 覆盖"压缩上下文期间的 settle 不得提前完成"。
//
// 合成序列（标注：synthetic）：真实的 compaction.jsonl 里 compaction_start 是 manual
// 且发生在最后一次 agent_settled 之后，无法构造"settle 落在压缩中间"的时序，
// 所以这里显式合成最小序列。
func TestSettledDuringCompactionDoesNotComplete(t *testing.T) {
	w := newFakeWorker()
	svc, snap := runWithWorker(t, testOptions(t), w)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	id := snap.RunID

	// synthetic: compaction_start → agent_settled → 仍是 running
	sendRaw(w, "compaction_start", `{"type":"compaction_start","reason":"threshold"}`)

	sendRaw(w, "agent_settled", `{"type":"agent_settled","aborted":false}`)

	if !eventually(t, 3*time.Second, func() bool {
		return hasDiagnosticKind(t, svc, id, "settle.deferred")
	}) {
		t.Fatal("压缩期间的 agent_settled 必须发 settle.deferred 诊断")
	}
	if st := mustRunState(t, svc, id); st != contracts.RunStateRunning {
		t.Fatalf("压缩期间结算被延后，状态应仍是 running，实际 %s", st)
	}

	// synthetic: compaction_end → completed
	sendRaw(w, "compaction_end", `{"type":"compaction_end","reason":"threshold"}`)

	if !eventually(t, 3*time.Second, func() bool {
		return mustRunState(t, svc, id) == contracts.RunStateCompleted
	}) {
		t.Fatalf("压缩结束后应补做结算并 completed，实际 %s", mustRunState(t, svc, id))
	}
	states := runStates(t, svc, id)
	firstCompleted := -1
	for i, st := range states {
		if st == contracts.RunStateCompleted {
			firstCompleted = i
			break
		}
	}
	if firstCompleted < 0 {
		t.Fatal("缺少 completed 状态事件")
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestSettledDuringAutoRetryDoesNotComplete 覆盖"自动重试期间的 settle 不得提前完成"。
//
// 合成序列（标注：synthetic）：真实的 provider-error.jsonl 里 agent_settled 永远在
// auto_retry_end 之后，无法证明"延迟结算"这条路径，因此合成最小序列。
func TestSettledDuringAutoRetryDoesNotComplete(t *testing.T) {
	w := newFakeWorker()
	svc, snap := runWithWorker(t, testOptions(t), w)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	id := snap.RunID

	// synthetic: auto_retry_start → agent_settled → 仍是 running
	sendRaw(w, "auto_retry_start",
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"boom"}`)
	sendRaw(w, "agent_settled", `{"type":"agent_settled","aborted":false}`)

	if !eventually(t, 3*time.Second, func() bool {
		return hasDiagnosticKind(t, svc, id, "settle.deferred")
	}) {
		t.Fatal("重试期间的 agent_settled 必须发 settle.deferred 诊断")
	}
	if st := mustRunState(t, svc, id); st != contracts.RunStateRunning {
		t.Fatalf("重试期间结算被延后，状态应仍是 running，实际 %s", st)
	}

	// synthetic: auto_retry_end{success:true} → completed
	sendRaw(w, "auto_retry_end", `{"type":"auto_retry_end","success":true,"attempt":1}`)

	if !eventually(t, 3*time.Second, func() bool {
		return mustRunState(t, svc, id) == contracts.RunStateCompleted
	}) {
		t.Fatalf("重试成功后应补做结算并 completed，实际 %s", mustRunState(t, svc, id))
	}
	for _, st := range runStates(t, svc, id) {
		if st == contracts.RunStateFailed {
			t.Fatal("重试成功的 run 不应 failed")
		}
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestUnknownSettledPayloadIsCompatibilityError 覆盖 RUN01.service 的
// "不认识的完成语义不得猜 completed"：缺 aborted 字段时终态是 failed + COMPATIBILITY。
func TestUnknownSettledPayloadIsCompatibilityError(t *testing.T) {
	w := newFakeWorker()
	svc, snap := runWithWorker(t, testOptions(t), w)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	id := snap.RunID

	sendRaw(w, "agent_settled", `{"type":"agent_settled"}`)

	if !eventually(t, 3*time.Second, func() bool {
		return mustRunState(t, svc, id) == contracts.RunStateFailed
	}) {
		t.Fatalf("无法识别的完成语义应 failed，实际 %s", mustRunState(t, svc, id))
	}
	if code := lastRunErrorCode(t, svc, id); code != contracts.CodeCompatibility {
		t.Fatalf("run.error 应是 %s，实际 %s", contracts.CodeCompatibility, code)
	}
	for _, st := range runStates(t, svc, id) {
		if st == contracts.RunStateCompleted {
			t.Fatal("完成语义未知时绝不能 completed")
		}
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestSubscriberDisconnectDeniesPendingApproval 覆盖 RUN04.service 的断开侧：
// 最后一个事件订阅者消失时，等待中的审批必须 fail-closed（deny），
// 副作用计数保持 0，run 不得卡在 waiting_approval 等满 60 秒 TTL。
func TestSubscriberDisconnectDeniesPendingApproval(t *testing.T) {
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

	var sideEffects atomic.Int32
	unsub := svc.Subscribe(func(contracts.Event) {})

	snap := startRunForTest(t, svc, "hi")
	rs := runStateOf(t, svc, snap.RunID)
	if rs.bridge == nil {
		t.Fatal("启用 Policy 时必须建立执行前授权端点")
	}

	type authResult struct {
		decision policy.Decision
		reason   string
		err      error
	}
	done := make(chan authResult, 1)
	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, opts.SessionID, 30*time.Second)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		dec, reason, err := client.Authorize(context.Background(), policy.ToolCall{
			ID: "c-1", Name: "fixture_write", Source: policy.SourceExternalMCP, Origin: "user",
		})
		// 只有真的放行才会执行写入：handler 的副作用用计数器代替文件系统。
		if err == nil && dec == policy.DecisionAllow {
			sideEffects.Add(1)
		}
		done <- authResult{decision: dec, reason: reason, err: err}
	}()

	if !eventually(t, 5*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateWaitingApproval
	}) {
		t.Fatalf("authorize 需要审批时 run 应进入 waiting_approval，实际 %s", mustRunState(t, svc, snap.RunID))
	}

	unsub()
	unsub() // 幂等：重复调用不得重复拒绝

	var res authResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("订阅者断开后审批没有被拒绝（在等 60 秒 TTL）")
	}
	wg.Wait()
	if res.err != nil {
		t.Fatalf("authorize 请求失败: %v", res.err)
	}
	if res.decision != policy.DecisionDeny {
		t.Fatalf("UI 断开时审批必须 deny，实际 %s", res.decision)
	}
	if got := sideEffects.Load(); got != 0 {
		t.Fatalf("审批被拒绝时副作用计数必须为 0，实际 %d", got)
	}

	resolved := eventsOfType(t, svc, snap.RunID, EventApprovalResolved)
	if len(resolved) != 1 {
		t.Fatalf("应只发一条 approval.resolved，实际 %d 条", len(resolved))
	}
	var p struct {
		ApprovalID string `json:"approvalId"`
		Choice     string `json:"choice"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(resolved[0].Payload, &p); err != nil {
		t.Fatalf("解析 approval.resolved 载荷失败: %v", err)
	}
	if p.Choice != string(policy.ApprovalDeny) || p.Reason != "ui_disconnected" || p.ApprovalID == "" {
		t.Fatalf("approval.resolved 载荷不符: %+v", p)
	}

	if !eventually(t, 3*time.Second, func() bool {
		st := mustRunState(t, svc, snap.RunID)
		return st == contracts.RunStateRunning
	}) {
		t.Fatalf("拒绝后 run 应回到 running，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	rs.mu.Lock()
	pending := len(rs.pending)
	rs.mu.Unlock()
	if pending != 0 {
		t.Fatalf("被拒绝的审批必须从 pending 里移除，实际还剩 %d 条", pending)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}
