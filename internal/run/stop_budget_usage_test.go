package run

// 本文件是 F05-A 的测试：停止升级、每 run 预算与用量报告。
//
// 假时钟与可阻塞的假 worker 定义在 service_test.go（测试构造放在一起），
// 这里只写用例本身：每个用例都对应一个可被独立复核的产品断言。

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/runtime/pi"
)

// TestStopCleanupFailedAtTenSecondsBlocksSession 覆盖 RUN04 的后段：
// 10 秒还没有明确的清理结果 → interrupted + CLEANUP_FAILED，并且阻止同一会话重启，
// 直到有显式的人工动作清除（ClearCleanupFailure）。
func TestStopCleanupFailedAtTenSecondsBlocksSession(t *testing.T) {
	clk := newFakeClock()
	opts := testOptions(t)
	opts.Clock = clk
	w := newFakeWorker()
	w.closeGate = make(chan struct{}) // 永远不返回
	w.ignoreForceKill = true          // 强杀也不管用
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	if err := svc.Abort(context.Background(), snap.RunID); err != nil {
		t.Fatalf("停止失败: %v", err)
	}

	clk.waitRegistrations(t, 2)
	clk.Advance(5 * time.Second)
	if !eventually(t, 2*time.Second, func() bool { return w.forceKillCount() == 1 }) {
		t.Fatalf("5 秒后必须升级强杀，实际 %d 次", w.forceKillCount())
	}
	if got := mustRunState(t, svc, snap.RunID); got != contracts.RunStateStopping {
		t.Fatalf("升级之后、清理结论之前应仍是 stopping，实际 %s", got)
	}

	// 升级之后再等 5 秒（总计 10 秒）：没有明确清理结果就必须如实记 CleanupFailed。
	clk.waitRegistrations(t, 3)
	clk.Advance(5 * time.Second)
	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateInterrupted
	}) {
		t.Fatalf("10 秒没有清理结果必须记 interrupted，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	if code := lastRunErrorCode(t, svc, snap.RunID); code != contracts.CodeCleanupFailed {
		t.Fatalf("run.error 应是 %s，实际 %s", contracts.CodeCleanupFailed, code)
	}

	// 同一会话不得被自动重启：清理结果未知时放行等于假装清理成功。
	_, err := svc.Start(context.Background(), StartRunRequest{Message: "再试"})
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("清理未完成的会话必须拒绝重启，实际 err=%v", err)
	}
	if appErr.Code != contracts.CodeCleanupFailed {
		t.Fatalf("拒绝重启的错误码应为 %s，实际 %s", contracts.CodeCleanupFailed, appErr.Code)
	}
	if !strings.Contains(appErr.Message, string(opts.SessionID)) || !strings.Contains(appErr.Message, "拒绝重启") {
		t.Fatalf("错误文案必须指明会话与拒绝原因，实际 %q", appErr.Message)
	}

	// 显式清除之后可以重启（阻断只在本次进程内有效，重启应用亦清）。
	svc.ClearCleanupFailure(opts.SessionID)
	next := startRunForTest(t, svc, "再试")
	if next.State != contracts.RunStateRunning {
		t.Fatalf("清除阻断后应能重启，实际 %s", next.State)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestBudgetDenies101stToolCall 覆盖 RUN06 的工具调用预算：
// 第 100 次放行、第 101 次拒绝，快照实时记录用量，事件只发一次。
func TestBudgetDenies101stToolCall(t *testing.T) {
	opts := testOptions(t)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	rs := runStateOf(t, svc, snap.RunID)
	gate := svc.newBudgetGate(rs)
	call := policy.ToolCall{ID: "c-1", Name: "read", Source: policy.SourceBuiltin}

	for i := 1; i <= 100; i++ {
		if err := gate.check(context.Background(), call); err != nil {
			t.Fatalf("第 %d 次工具调用应放行: %v", i, err)
		}
	}

	err := gate.check(context.Background(), call)
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("第 101 次必须因预算被拒，实际 err=%v", err)
	}
	if appErr.Code != contracts.CodeBudgetExceeded {
		t.Fatalf("错误码应为 %s，实际 %s", contracts.CodeBudgetExceeded, appErr.Code)
	}
	if !strings.Contains(appErr.Message, "100") {
		t.Fatalf("拒绝文案必须写明上限，实际 %q", appErr.Message)
	}

	b := rs.snapshot().Budget
	if b == nil {
		t.Fatal("快照必须带预算执行情况")
	}
	if b.MaxToolCalls != 100 || b.ToolCalls != 100 {
		t.Fatalf("预算记账错误: %+v", b)
	}
	if !b.Exceeded || b.ExceededReason != contracts.BudgetReasonToolCalls {
		t.Fatalf("超限原因应为 %s，实际 %+v", contracts.BudgetReasonToolCalls, b)
	}
	if b.MaxDurationMs != (30 * time.Minute).Milliseconds() {
		t.Fatalf("时间预算上限应为 30 分钟，实际 %dms", b.MaxDurationMs)
	}

	evs := eventsOfType(t, svc, snap.RunID, EventBudgetExceeded)
	if len(evs) != 1 {
		t.Fatalf("预算用尽事件应只发一次，实际 %d 条", len(evs))
	}
	var p budgetPayload
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatalf("解析预算事件失败: %v", err)
	}
	if p.Kind != contracts.BudgetReasonToolCalls || p.Limit != 100 || p.Used != 100 {
		t.Fatalf("预算事件载荷不符: %+v", p)
	}

	// 继续尝试也不重复发事件、不悄悄放行。
	_ = gate.check(context.Background(), call)
	_ = gate.check(context.Background(), call)
	if n := len(eventsOfType(t, svc, snap.RunID, EventBudgetExceeded)); n != 1 {
		t.Fatalf("重复超限不得重复发事件，实际 %d 条", n)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestBudgetGateBlocksToolCallThroughPolicyBridge 守"闸门真的挂在执行前授权端点上"：
// 只是把 newBudgetGate 写对、却没接进 policybridge，第 101 次调用照样会去问策略服务，
// 预算就是一条死代码。这里用真实端点证明"超预算时请求到不了策略层"。
func TestBudgetGateBlocksToolCallThroughPolicyBridge(t *testing.T) {
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
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	rs := runStateOf(t, svc, snap.RunID)
	if rs.bridge == nil {
		t.Fatal("启用 Policy 时必须建立执行前授权端点")
	}
	call := policy.ToolCall{ID: "c-1", Name: "read", Source: policy.SourceBuiltin}
	for i := 0; i < 100; i++ {
		if err := rs.budget.check(context.Background(), call); err != nil {
			t.Fatalf("第 %d 次应放行: %v", i+1, err)
		}
	}

	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, opts.SessionID, 5*time.Second)
	dec, reason, err := client.Authorize(context.Background(), policy.ToolCall{
		ID: "c-101", Name: "read", Source: policy.SourceBuiltin,
		ResourceScope: filepath.Join(opts.WorkspaceDir, "a.txt"),
	})
	if err != nil {
		t.Fatalf("授权请求失败: %v", err)
	}
	if dec != policy.DecisionDeny {
		t.Fatalf("超预算的调用必须 deny，实际 %s", dec)
	}
	if !strings.Contains(reason, contracts.CodeBudgetExceeded) {
		t.Fatalf("拒绝原因应带预算错误码，实际 %q", reason)
	}
	rs.mu.Lock()
	pending := len(rs.pending)
	rs.mu.Unlock()
	if pending != 0 {
		t.Fatalf("超预算的调用不得创建审批（否则会白等人确认），实际 %d 条", pending)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestTimeBudgetStopsRun 覆盖 RUN06 的时间预算：
// 30 分钟后不再发请求、不再接受工具调用，终态 failed + BUDGET_EXCEEDED。
func TestTimeBudgetStopsRun(t *testing.T) {
	clk := newFakeClock()
	opts := testOptions(t)
	opts.Clock = clk
	opts.MaxRunDuration = 30 * time.Minute
	w := newFakeWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	clk.waitRegistrations(t, 1)
	clk.Advance(30 * time.Minute)

	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateFailed
	}) {
		t.Fatalf("超出时间预算应为 failed，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	if code := lastRunErrorCode(t, svc, snap.RunID); code != contracts.CodeBudgetExceeded {
		t.Fatalf("run.error 应是 %s，实际 %s", contracts.CodeBudgetExceeded, code)
	}
	rs := runStateOf(t, svc, snap.RunID)
	b := rs.snapshot().Budget
	if b == nil || !b.Exceeded || b.ExceededReason != contracts.BudgetReasonTime {
		t.Fatalf("预算快照应记录时间超限，实际 %+v", b)
	}
	// 只发过一次 prompt：超时之后不得再发任何请求。
	if got := w.callCount(); got != 1 {
		t.Fatalf("超时后不得再发请求，prompt 调用次数应为 1，实际 %d", got)
	}
	// 超时之后也不再接受工具调用。
	if err := rs.budget.check(context.Background(), policy.ToolCall{Name: "read"}); err == nil {
		t.Fatal("时间预算用尽后必须拒绝工具调用")
	}
	// 事件：kind=time、limitMs=1800000，且只发一次。
	evs := eventsOfType(t, svc, snap.RunID, EventBudgetExceeded)
	if len(evs) != 1 {
		t.Fatalf("时间预算用尽事件应只发一次，实际 %d 条", len(evs))
	}
	var p budgetPayload
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatalf("解析预算事件失败: %v", err)
	}
	if p.Kind != contracts.BudgetReasonTime || p.LimitMs != 1800000 {
		t.Fatalf("时间预算事件载荷不符: %+v", p)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestUsageUnavailableIsNotZero 守 RUN06 的"usage 缺失显示 unavailable，不显示伪 0"。
//
// Pi 拿不到 provider usage 时会照发一条 usage 全 0 的占位记录，所以"字段存在"
// 不等于"有用量"：这里两种形态都喂进去，快照都必须说 unavailable，
// 并且 JSON 里不能出现 costUsd / totalTokens 键。
func TestUsageUnavailableIsNotZero(t *testing.T) {
	opts := testOptions(t)
	w := newFakeWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	w.events <- pi.RawEvent{Type: "message_end", Record: json.RawMessage(
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"timestamp":1}}`)}
	w.events <- pi.RawEvent{Type: "message_end", Record: json.RawMessage(
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok2"}],"timestamp":2,` +
			`"usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"total":0}}}}`)}
	w.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateCompleted
	}) {
		t.Fatalf("settle 后应 completed，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	cur, err := svc.Get(snap.RunID)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if cur.Usage == nil {
		t.Fatal("终态快照必须带 usage 记录（哪怕是 unavailable）")
	}
	if cur.Usage.Availability != contracts.UsageUnavailable {
		t.Fatalf("没有用量时必须记 unavailable，实际 %q（tokens=%d）",
			cur.Usage.Availability, cur.Usage.TotalTokens)
	}

	doc := decodeSnapshotJSON(t, cur)
	raw, ok := doc["usage"]
	if !ok {
		t.Fatal("usage 字段必须出现（缺失会被界面读成没记录）")
	}
	var usageDoc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &usageDoc); err != nil {
		t.Fatalf("usage 不是合法 JSON 对象: %v", err)
	}
	if got := string(usageDoc["availability"]); got != `"unavailable"` {
		t.Fatalf("availability 应为 unavailable，实际 %s", got)
	}
	if _, ok := usageDoc["costUsd"]; ok {
		t.Fatalf("没有用量时不得出现 costUsd（伪 0 费用）: %s", raw)
	}
	if _, ok := usageDoc["totalTokens"]; ok {
		t.Fatalf("没有用量时不得出现 totalTokens（伪 0 用量）: %s", raw)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestToolCallsDeniedAfterStopRequested 守 RUN04 的后段：
// 已经发起停止之后不得再放行工具调用（尤其是写），否则会产生无人认领的副作用。
// 这种拒绝也不能计入预算：被取消的 run 不该白白消耗配额。
func TestToolCallsDeniedAfterStopRequested(t *testing.T) {
	clk := newFakeClock()
	opts := testOptions(t)
	opts.Clock = clk
	w := newFakeWorker()
	w.closeGate = make(chan struct{}) // 让 run 停在 stopping，好观察停止中的闸门行为
	w.ignoreForceKill = true
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	rs := runStateOf(t, svc, snap.RunID)
	call := policy.ToolCall{ID: "c-1", Name: "read", Source: policy.SourceBuiltin}
	if err := rs.budget.check(context.Background(), call); err != nil {
		t.Fatalf("运行中的工具调用应放行: %v", err)
	}

	if err := svc.Abort(context.Background(), snap.RunID); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if got := mustRunState(t, svc, snap.RunID); got != contracts.RunStateStopping {
		t.Fatalf("停止后应处于 stopping，实际 %s", got)
	}

	err := rs.budget.check(context.Background(), policy.ToolCall{
		ID: "c-2", Name: "write", Source: policy.SourceBuiltin,
	})
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("停止后必须拒绝工具调用，实际 err=%v", err)
	}
	if appErr.Code != contracts.CodeRpcCancelled {
		t.Fatalf("拒绝码应为 %s，实际 %s", contracts.CodeRpcCancelled, appErr.Code)
	}
	if b := rs.snapshot().Budget; b.ToolCalls != 1 {
		t.Fatalf("停止后的拒绝不得计入预算，实际已用 %d 次", b.ToolCalls)
	}
	if n := len(eventsOfType(t, svc, snap.RunID, EventBudgetExceeded)); n != 0 {
		t.Fatalf("停止后的拒绝不是预算用尽，不得发预算事件，实际 %d 条", n)
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestUsageUnknownPriceOmitsCost 守 RUN06 的"缺失价格不能显示伪 0 费用"。
//
// 这不是编出来的形状：internal/runtime/pi/testdata/v1.5.0/text.jsonl 里 Pi 就是这么记的——
// 有真实 token，但 cost.total=0 且 totalFromProvider=false（价格未知）。
// 把它展示成 $0.00 等于告诉用户这一轮免费。
func TestUsageUnknownPriceOmitsCost(t *testing.T) {
	opts := testOptions(t)
	w := newFakeWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	w.events <- pi.RawEvent{Type: "message_end", Record: json.RawMessage(
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"timestamp":1793690788753,` +
			`"usage":{"input":12,"output":7,"cacheRead":0,"cacheWrite":0,"reasoning":0,"totalTokens":19,` +
			`"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0,"totalFromProvider":false}}}}`)}
	w.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateCompleted
	}) {
		t.Fatalf("settle 后应 completed，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	cur, err := svc.Get(snap.RunID)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	u := cur.Usage
	if u == nil || u.Availability != contracts.UsageReported {
		t.Fatalf("token 是真的，应报告用量，实际 %+v", u)
	}
	if u.TotalTokens != 19 {
		t.Fatalf("token 汇总不符: %+v", u)
	}
	if u.CostUSD != nil {
		t.Fatalf("价格未知时不得给出金额（伪 0 费用），实际 %v", *u.CostUSD)
	}

	doc := decodeSnapshotJSON(t, cur)
	var usageDoc map[string]json.RawMessage
	if err := json.Unmarshal(doc["usage"], &usageDoc); err != nil {
		t.Fatalf("usage 不是合法 JSON 对象: %v", err)
	}
	if _, ok := usageDoc["costUsd"]; ok {
		t.Fatalf("价格未知时不得出现 costUsd 键: %s", doc["usage"])
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestUsageReportedFromPiEvents 覆盖 RUN06 的"有 usage 就照实报"。
func TestUsageReportedFromPiEvents(t *testing.T) {
	opts := testOptions(t)
	w := newFakeWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap := startRunForTest(t, svc, "hi")
	w.events <- pi.RawEvent{Type: "message_end", Record: json.RawMessage(
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"timestamp":1700000000000,` +
			`"usage":{"input":1200,"output":340,"cacheRead":10,"cacheWrite":20,"totalTokens":1570,` +
			`"cost":{"input":0.01,"output":0.0023,"cacheRead":0,"cacheWrite":0,"total":0.0123}}}}`)}
	w.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateCompleted
	}) {
		t.Fatalf("settle 后应 completed，实际 %s", mustRunState(t, svc, snap.RunID))
	}
	cur, err := svc.Get(snap.RunID)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	u := cur.Usage
	if u == nil || u.Availability != contracts.UsageReported {
		t.Fatalf("有 usage 时必须记 reported，实际 %+v", u)
	}
	if u.InputTokens != 1200 || u.OutputTokens != 340 || u.TotalTokens != 1570 {
		t.Fatalf("token 汇总不符: %+v", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.0123 {
		t.Fatalf("费用应来自 provider 的 cost.total，实际 %+v", u.CostUSD)
	}
	if u.PriceSource != "provider" {
		t.Fatalf("价格来源应为 provider，实际 %q", u.PriceSource)
	}
	if u.Estimated {
		t.Fatal("provider 报告的用量不是我们的估算，estimated 必须为 false")
	}
	// 本 case 的证据 artifact：把这次 run 的事件日志写进 ANVIL_OBSERVATION_DIR（未设置时不产出）
	writeCaseJournal(t, svc)
}

// TestUsageDedupesRepeatedAssistantMessage 守"message_end 与 agent_end 重复同一条
// 助手消息时不得重复计数"：Pi 的 agent_end 会把整轮消息再发一遍。
func TestUsageDedupesRepeatedAssistantMessage(t *testing.T) {
	cases := []struct {
		name string
		// message 是助手消息本体；带/不带 timestamp 各一条，覆盖两种去重键。
		message string
	}{
		{
			name: "按 timestamp 去重",
			message: `{"role":"assistant","content":[{"type":"text","text":"ok"}],"timestamp":1700000000000,` +
				`"usage":{"input":1200,"output":340,"cacheRead":10,"cacheWrite":20,"totalTokens":1570,"cost":{"total":0.0123}}}`,
		},
		{
			name: "没有 timestamp 时按内容+用量哈希去重",
			message: `{"role":"assistant","content":[{"type":"text","text":"ok"}],` +
				`"usage":{"input":1200,"output":340,"cacheRead":10,"cacheWrite":20,"totalTokens":1570,"cost":{"total":0.0123}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			w := newFakeWorker()
			opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
				return w, nil
			}
			svc := New(opts)
			t.Cleanup(func() { _ = svc.Close(context.Background()) })

			snap := startRunForTest(t, svc, "hi")
			w.events <- pi.RawEvent{Type: "message_end",
				Record: json.RawMessage(`{"type":"message_end","message":` + tc.message + `}`)}
			w.events <- pi.RawEvent{Type: "agent_end",
				Record: json.RawMessage(`{"type":"agent_end","messages":[
					{"role":"user","content":[{"type":"text","text":"hi"}]},
					` + tc.message + `]}`)}
			w.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

			if !eventually(t, 2*time.Second, func() bool {
				return mustRunState(t, svc, snap.RunID) == contracts.RunStateCompleted
			}) {
				t.Fatalf("settle 后应 completed，实际 %s", mustRunState(t, svc, snap.RunID))
			}
			cur, err := svc.Get(snap.RunID)
			if err != nil {
				t.Fatalf("读取快照失败: %v", err)
			}
			u := cur.Usage
			if u == nil || u.Availability != contracts.UsageReported {
				t.Fatalf("应报告用量，实际 %+v", u)
			}
			if u.InputTokens != 1200 || u.OutputTokens != 340 || u.TotalTokens != 1570 {
				t.Fatalf("同一条消息被重复计数了: %+v", u)
			}
			if u.CostUSD == nil || *u.CostUSD != 0.0123 {
				t.Fatalf("重复消息导致费用翻倍: %+v", u.CostUSD)
			}
			// RUN06.service 的证据 artifact（子测试里的事件日志）
			writeCaseJournal(t, svc)
		})
	}
}
