package run

// C06f：澄清桥（clarification）的失败测试。
//
// 这一组只回答三个问题，每个都用"worker 自己记下来的事实"作证，不看界面文案：
//  1. Pi 的四种 dialog UI 请求是否被映射成类型正确的 clarification.requested；
//  2. AnswerClarification 的三个分支是否产生**对应的** extension_ui_response；
//  3. 澄清是不是彻底独立于审批：回答路径不产生 approval / session grant，
//     而第三方同名工具仍然必须走 policy。
//
// 纪律：全部在 t.TempDir() 里跑；worker 是脚本化的替身，但它的"收到什么"是
// 它自己写下的记录，而不是被断言对象自己汇报的结论。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/runtime/pi"
)

// uiReply 是 worker 收到的一条 extension_ui_response。
type uiReply struct {
	ID   string
	Body map[string]any
}

// clarifyingWorker 是一个会真的发起 extension UI 请求、并等答复的脚本化 worker。
//
// 它复现的时序取自真实 Pi 1.5.0 的观测（见 C06f 报告）：prompt 之后模型请求
// anvil_ask_user → 扩展发 extension_ui_request（带产品身份标题前缀）→ 客户端回
// extension_ui_response → 工具结果 → agent_settled。
type clarifyingWorker struct {
	kind     string
	options  []string
	question string
	// foreignTitle 非空时用它当标题（模拟第三方工具的 UI 请求：没有产品令牌前缀）。
	foreignTitle string

	events chan pi.RawEvent
	once   sync.Once

	mu      sync.Mutex
	calls   []json.RawMessage
	replies []uiReply
	settled bool

	replyCh chan uiReply
	tag     string

	done      chan struct{}
	closeOnce sync.Once
}

func newClarifyingWorker(kind, question string, options ...string) *clarifyingWorker {
	return &clarifyingWorker{
		kind:     kind,
		options:  options,
		question: question,
		events:   make(chan pi.RawEvent, 32),
		replyCh:  make(chan uiReply, 8),
		done:     make(chan struct{}),
	}
}

func (w *clarifyingWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	w.mu.Lock()
	w.calls = append(w.calls, command)
	w.mu.Unlock()

	var cmd struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(command, &cmd)
	if cmd.Type == "prompt" {
		go w.turn()
	}
	return json.RawMessage(`{"type":"response","success":true}`), nil
}

// turn 发出 UI 请求、等一条答复，然后结束本轮。
func (w *clarifyingWorker) turn() {
	title := w.tag + w.question
	if w.foreignTitle != "" {
		title = w.foreignTitle
	}
	req := map[string]any{
		"type":   pi.TypeExtensionUIRequest,
		"id":     "ui-req-1",
		"method": w.kind,
		"title":  title,
	}
	if len(w.options) > 0 {
		req["options"] = w.options
	}
	raw, _ := json.Marshal(req)
	w.push(pi.RawEvent{Type: pi.TypeExtensionUIRequest, Record: raw})

	select {
	case reply := <-w.replyCh:
		text := "clarification answered"
		if _, cancelled := reply.Body["cancelled"]; cancelled {
			text = "clarification cancelled"
		}
		w.emitToolEnd(text)
	case <-time.After(5 * time.Second):
		// 超时后照样收尾：这个分支本身就是要暴露"答复没送到"的事实，
		// 断言会看到 settled 但 replies 为空。
		w.emitToolEnd("clarification unanswered")
	}
}

func (w *clarifyingWorker) emitToolEnd(text string) {
	body := map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "call_fixture_1",
		"toolName":   "anvil_ask_user",
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		},
		"isError": false,
	}
	raw, _ := json.Marshal(body)
	w.push(pi.RawEvent{Type: "tool_execution_end", Record: raw})
	w.push(pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)})
}

func (w *clarifyingWorker) push(ev pi.RawEvent) {
	// 事件流在 Close 时关闭；收尾与推送可能并发（清理顺序由测试清理期决定），
	// 这里只保护测试替身不会被自己的清理动作打成 panic。
	defer func() { _ = recover() }()
	select {
	case <-w.done:
		return
	default:
	}
	select {
	case w.events <- ev:
	case <-w.done:
	case <-time.After(5 * time.Second):
	}
}

func (w *clarifyingWorker) Events() <-chan pi.RawEvent { return w.events }

func (w *clarifyingWorker) Close(ctx context.Context) error {
	w.closeOnce.Do(func() {
		close(w.done)
		close(w.events)
	})
	return nil
}

func (w *clarifyingWorker) PID() int                   { return 0 }
func (w *clarifyingWorker) StderrTail() (string, bool) { return "", false }
func (w *clarifyingWorker) ExitCode() (int, bool)      { return 0, false }
func (w *clarifyingWorker) Limitations() []string      { return nil }

// RespondExtensionUI 记录答复（这就是"worker 自己看到的答复"）。
func (w *clarifyingWorker) RespondExtensionUI(ctx context.Context, requestID string, body map[string]any) error {
	reply := uiReply{ID: requestID, Body: body}
	w.mu.Lock()
	w.replies = append(w.replies, reply)
	w.mu.Unlock()
	select {
	case w.replyCh <- reply:
	default:
	}
	return nil
}

// repliesSeen 返回 worker 收到的答复副本。
func (w *clarifyingWorker) repliesSeen() []uiReply {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uiReply{}, w.replies...)
}

// promptMessage 取 worker 收到的 prompt 里的 message 字段（核对 prompt 未被污染）。
func (w *clarifyingWorker) promptMessage(t *testing.T) string {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, raw := range w.calls {
		var cmd struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &cmd) == nil && cmd.Type == "prompt" {
			return cmd.Message
		}
	}
	t.Fatal("worker 没有收到 prompt")
	return ""
}

// envFrom 从 WorkerSpec 环境里取一个值。
func envFrom(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	t.Fatalf("worker 环境里没有 %s", key)
	return ""
}

// clarificationOptions 构造一份启用的澄清配置（扩展路径用临时目录里的桩文件）。
func clarificationOptions(t *testing.T, dir string, timeout time.Duration) ClarificationOptions {
	t.Helper()
	ext := filepath.Join(dir, "anvil-clarification.ts")
	if err := os.WriteFile(ext, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写澄清扩展桩: %v", err)
	}
	return ClarificationOptions{Enabled: true, ExtensionPath: ext, Timeout: timeout}
}

// startClarificationRun 用给定的 worker / 选项启动一次 run，并把 worker 的
// 身份令牌（用于构造 UI 标题前缀）交给它。
func startClarificationRun(t *testing.T, opts Options, w *clarifyingWorker, req StartRunRequest) (*Service, contracts.RunSnapshot) {
	t.Helper()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w.tag = clarificationTag(envFrom(t, spec.Env, EnvClarificationToken))
		// 身份必须在启动时就绪：turn 可能在 Call 返回后立刻执行。
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	snap, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("启动 run 失败: %v", err)
	}
	return svc, snap
}

// waitClarificationRequested 等到本次 run 发出 clarification.requested，返回载荷。
func waitClarificationRequested(t *testing.T, svc *Service, id contracts.RunID) map[string]any {
	t.Helper()
	var payload map[string]any
	ok := eventually(t, 3*time.Second, func() bool {
		evs := eventsOfType(t, svc, id, EventClarificationRequested)
		if len(evs) == 0 {
			return false
		}
		if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
			t.Fatalf("解析 clarification.requested 失败: %v", err)
		}
		return true
	})
	if !ok {
		t.Fatalf("没有收到 clarification.requested；事件：%s", eventTypes(t, svc, id))
	}
	return payload
}

// eventTypes 给失败信息用：列出本 run 出现过的事件类型。
func eventTypes(t *testing.T, svc *Service, id contracts.RunID) string {
	t.Helper()
	all, err := svc.Replay(id, 0)
	if err != nil {
		t.Fatalf("重放事件失败: %v", err)
	}
	seen := make([]string, 0, len(all))
	for _, ev := range all {
		seen = append(seen, ev.Type)
	}
	return strings.Join(seen, ",")
}

// waitTerminal 等到 run 进入终态。
func waitTerminal(t *testing.T, svc *Service, id contracts.RunID) string {
	t.Helper()
	var state string
	ok := eventually(t, 5*time.Second, func() bool {
		snap, err := svc.Get(id)
		if err != nil {
			return false
		}
		state = snap.State
		return IsTerminal(state)
	})
	if !ok {
		t.Fatalf("run 没有在时限内进入终态（当前 %s）", state)
	}
	return state
}

// TestClarificationMapsEachUIKind 覆盖四种 dialog 方法 → clarification.requested 的映射。
func TestClarificationMapsEachUIKind(t *testing.T) {
	cases := []struct {
		kind       string
		options    []string
		wantOption bool
	}{
		{kind: "input"},
		{kind: "editor"},
		{kind: "select", options: []string{"staging", "prod"}, wantOption: true},
		{kind: "confirm"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			dir := t.TempDir()
			opts := testOptions(t)
			opts.Clarification = clarificationOptions(t, dir, 2*time.Second)
			w := newClarifyingWorker(tc.kind, "请回答这个问题", tc.options...)
			svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})

			payload := waitClarificationRequested(t, svc, snap.RunID)
			if got := fmt.Sprint(payload["kind"]); got != tc.kind {
				t.Fatalf("kind 应为 %s，实际 %s（载荷 %v）", tc.kind, got, payload)
			}
			if got := fmt.Sprint(payload["question"]); got != "请回答这个问题" {
				t.Fatalf("question 应去掉身份前缀，实际 %q", got)
			}
			if got := fmt.Sprint(payload["requestId"]); got != "ui-req-1" {
				t.Fatalf("requestId 必须是 Pi 的请求 id，实际 %q", got)
			}
			rawOptions, hasOptions := payload["options"]
			if tc.wantOption {
				got, _ := rawOptions.([]any)
				if !hasOptions || len(got) != len(tc.options) {
					t.Fatalf("select 的 options 应原样带出 %v，实际 %v", tc.options, rawOptions)
				}
			} else if hasOptions {
				t.Fatalf("%s 不应带 options，实际 %v", tc.kind, rawOptions)
			}
			// 身份令牌不能出现在事件里（它不是给前端的）。
			if strings.Contains(fmt.Sprint(payload), "anvil-clarification") {
				t.Fatalf("澄清事件泄露了身份标题前缀: %v", payload)
			}
			// 收尾：留一条答复，免得 worker 等满 5 秒。
			_ = svc.AnswerClarification(context.Background(), opts0(svc, snap.RunID), "ui-req-1", ClarificationResponse{
				Cancelled: boolPtr(true),
			})
		})
	}
}

// opts0 取本次 run 冻结的会话 ID（用于 AnswerClarification）。
func opts0(svc *Service, id contracts.RunID) contracts.SessionID {
	snap, err := svc.Get(id)
	if err != nil {
		return ""
	}
	return snap.SessionID
}

func boolPtr(v bool) *bool { return &v }

// TestAnswerClarificationSendsMatchingExtensionUIResponse 覆盖三个答复分支。
func TestAnswerClarificationSendsMatchingExtensionUIResponse(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		resp     ClarificationResponse
		wantKey  string
		wantVal  any
		outcome  string
		sendText string
	}{
		{
			name: "value", kind: "input",
			resp:    ClarificationResponse{Value: &ClarificationValue{Text: "用 staging"}},
			wantKey: "value", wantVal: "用 staging", outcome: "value", sendText: "用 staging",
		},
		{
			name: "confirmed", kind: "confirm",
			resp:    ClarificationResponse{Confirmed: boolPtr(true)},
			wantKey: "confirmed", wantVal: true, outcome: "confirmed",
		},
		{
			name: "cancelled", kind: "editor",
			resp:    ClarificationResponse{Cancelled: boolPtr(true)},
			wantKey: "cancelled", wantVal: true, outcome: "cancelled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := testOptions(t)
			opts.Clarification = clarificationOptions(t, dir, 2*time.Second)
			w := newClarifyingWorker(tc.kind, "请回答")
			svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})
			waitClarificationRequested(t, svc, snap.RunID)

			if err := svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1", tc.resp); err != nil {
				t.Fatalf("答复澄清失败: %v", err)
			}
			// worker 自己记下的答复就是"Pi 实际收到了什么"。
			var seen *uiReply
			if !eventually(t, 3*time.Second, func() bool {
				replies := w.repliesSeen()
				if len(replies) == 0 {
					return false
				}
				seen = &replies[0]
				return true
			}) {
				t.Fatalf("worker 没有收到任何 extension_ui_response（事件：%s）", eventTypes(t, svc, snap.RunID))
			}
			if seen.ID != "ui-req-1" {
				t.Fatalf("答复的 id 必须是 Pi 的请求 id，实际 %q", seen.ID)
			}
			got, ok := seen.Body[tc.wantKey]
			if !ok {
				t.Fatalf("答复缺少 %s 分支：%v", tc.wantKey, seen.Body)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.wantVal) {
				t.Fatalf("%s 分支应为 %v，实际 %v", tc.wantKey, tc.wantVal, got)
			}
			if len(seen.Body) != 1 {
				t.Fatalf("答复只能有一个分支，实际 %v", seen.Body)
			}

			// 事件侧：resolved 的 outcome 必须与分支一致。
			var resolved map[string]any
			if !eventually(t, 3*time.Second, func() bool {
				evs := eventsOfType(t, svc, snap.RunID, EventClarificationResolved)
				if len(evs) == 0 {
					return false
				}
				return json.Unmarshal(evs[len(evs)-1].Payload, &resolved) == nil
			}) {
				t.Fatalf("没有收到 clarification.resolved（事件：%s）", eventTypes(t, svc, snap.RunID))
			}
			if fmt.Sprint(resolved["outcome"]) != tc.outcome {
				t.Fatalf("outcome 应为 %s，实际 %v", tc.outcome, resolved["outcome"])
			}
			if tc.sendText != "" && fmt.Sprint(resolved["value"]) != tc.sendText {
				t.Fatalf("resolved 里的 value 应为 %q，实际 %v", tc.sendText, resolved["value"])
			}

			// run 正常收尾（澄清不是失败，也不是审批）。
			if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
				t.Fatalf("澄清答复后 run 应 completed，实际 %s", state)
			}
		})
	}
}

// TestClarificationAnswerRejectsMismatchedBranch 守"分支与提问类型必须匹配"。
func TestClarificationAnswerRejectsMismatchedBranch(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(t)
	opts.Clarification = clarificationOptions(t, dir, 2*time.Second)
	w := newClarifyingWorker("confirm", "确定继续吗")
	svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})
	waitClarificationRequested(t, svc, snap.RunID)

	// confirm 用了 value 分支：模型会收到一个它没法解释的答案，必须拒绝。
	err := svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1",
		ClarificationResponse{Value: &ClarificationValue{Text: "是"}})
	if err == nil {
		t.Fatal("confirm 提问接受 value 分支：分支与类型错配必须结构化失败")
	}
	// 空 value 也必须拒绝：那会被读成"用户给了空答案"。
	err = svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1",
		ClarificationResponse{Value: &ClarificationValue{Text: "   "}})
	if err == nil {
		t.Fatal("空 value 被当成有效答案，必须拒绝")
	}
	// 两个分支同时给：tagged union 只能有一个真值。
	err = svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1",
		ClarificationResponse{Confirmed: boolPtr(true), Cancelled: boolPtr(true)})
	if err == nil {
		t.Fatal("同时给出两个分支必须被拒绝")
	}
	// 另一会话回答：跨会话代答内容问题必须拒绝。
	err = svc.AnswerClarification(context.Background(), "sess-other", "ui-req-1",
		ClarificationResponse{Confirmed: boolPtr(true)})
	if err == nil {
		t.Fatal("别的会话替我回答了澄清，必须拒绝")
	}
	// 合法答复仍然可用（前面的拒绝不能把 pending 弄丢）。
	if err := svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1",
		ClarificationResponse{Confirmed: boolPtr(false)}); err != nil {
		t.Fatalf("合法答复失败: %v", err)
	}
}

// TestClarificationTimeoutCancelsAndClearsPending 覆盖超时收口。
func TestClarificationTimeoutCancelsAndClearsPending(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(t)
	opts.Clarification = clarificationOptions(t, dir, 120*time.Millisecond)
	w := newClarifyingWorker("input", "没人会回答的问题")
	svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})
	waitClarificationRequested(t, svc, snap.RunID)

	// 超时后 worker 应收到 cancelled:true。
	var seen *uiReply
	if !eventually(t, 3*time.Second, func() bool {
		replies := w.repliesSeen()
		if len(replies) == 0 {
			return false
		}
		seen = &replies[0]
		return true
	}) {
		t.Fatalf("超时后没有给 Pi 发 cancelled 答复（事件：%s）", eventTypes(t, svc, snap.RunID))
	}
	if got, ok := seen.Body["cancelled"]; !ok || got != true {
		t.Fatalf("超时答复必须是 cancelled:true，实际 %v", seen.Body)
	}

	// 事件里要说清是 timeout，而不是"用户取消"。
	var resolved map[string]any
	if !eventually(t, 3*time.Second, func() bool {
		evs := eventsOfType(t, svc, snap.RunID, EventClarificationResolved)
		if len(evs) == 0 {
			return false
		}
		return json.Unmarshal(evs[len(evs)-1].Payload, &resolved) == nil
	}) {
		t.Fatalf("超时没有产生 clarification.resolved（事件：%s）", eventTypes(t, svc, snap.RunID))
	}
	if fmt.Sprint(resolved["outcome"]) != "cancelled" || fmt.Sprint(resolved["reason"]) != "timeout" {
		t.Fatalf("超时收口应为 cancelled/timeout，实际 %v", resolved)
	}

	// pending 必须清掉：再回答一次只能是 NOT_FOUND。
	err := svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1",
		ClarificationResponse{Cancelled: boolPtr(true)})
	if err == nil {
		t.Fatal("超时收口后 pending 仍在：迟到的答复被接受了")
	}

	// run 仍应正常结算（超时不是 run 失败）。
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("超时后 run 应 completed，实际 %s", state)
	}
}

// TestClarificationRunAbortCancelsAndSettles 覆盖 run 终止时的收口。
func TestClarificationRunAbortCancelsAndSettles(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(t)
	opts.Clarification = clarificationOptions(t, dir, 30*time.Second)
	w := newClarifyingWorker("select", "选一个", "a", "b")
	svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})
	waitClarificationRequested(t, svc, snap.RunID)

	if err := svc.Abort(context.Background(), snap.RunID); err != nil {
		t.Fatalf("停止 run 失败: %v", err)
	}
	var seen *uiReply
	if !eventually(t, 3*time.Second, func() bool {
		replies := w.repliesSeen()
		if len(replies) == 0 {
			return false
		}
		seen = &replies[0]
		return true
	}) {
		t.Fatalf("停止时没有给 Pi 发 cancelled 答复（事件：%s）", eventTypes(t, svc, snap.RunID))
	}
	if got := seen.Body["cancelled"]; got != true {
		t.Fatalf("停止答复必须是 cancelled:true，实际 %v", seen.Body)
	}
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCancelled {
		t.Fatalf("停止后应为 cancelled，实际 %s", state)
	}
}

// TestForeignUIRequestIsNotTreatedAsClarification 守身份判据：
// 没有产品身份前缀的 UI 请求不会被当成澄清，也不会被代答内容。
func TestForeignUIRequestIsNotTreatedAsClarification(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(t)
	opts.Clarification = clarificationOptions(t, dir, 2*time.Second)
	w := newClarifyingWorker("select", "", "a", "b")
	w.foreignTitle = "第三方扩展想问你一个问题"
	svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})

	// 应当只有诊断，没有 clarification.requested。
	var diagnostic map[string]any
	if !eventually(t, 3*time.Second, func() bool {
		evs := eventsOfType(t, svc, snap.RunID, EventDiagnostic)
		for _, ev := range evs {
			var p map[string]any
			if json.Unmarshal(ev.Payload, &p) == nil && fmt.Sprint(p["kind"]) == "clarification.foreign_ui_request" {
				diagnostic = p
				return true
			}
		}
		return false
	}) {
		t.Fatalf("第三方 UI 请求没有产生 clarification.foreign_ui_request 诊断（事件：%s）",
			eventTypes(t, svc, snap.RunID))
	}
	if evs := eventsOfType(t, svc, snap.RunID, EventClarificationRequested); len(evs) != 0 {
		t.Fatalf("第三方 UI 请求被当成了澄清事件：%d 条", len(evs))
	}
	// 必须 fail closed：给 Pi 回 cancelled，不让它挂着。
	var seen *uiReply
	if !eventually(t, 3*time.Second, func() bool {
		replies := w.repliesSeen()
		if len(replies) == 0 {
			return false
		}
		seen = &replies[0]
		return true
	}) {
		t.Fatalf("第三方 UI 请求没有被 fail-closed 收口")
	}
	if got := seen.Body["cancelled"]; got != true {
		t.Fatalf("第三方 UI 请求必须以 cancelled 收口，实际 %v", seen.Body)
	}
	if diagnostic != nil {
		msg := fmt.Sprint(diagnostic["message"])
		if !strings.Contains(msg, "select") {
			t.Fatalf("诊断应带上被拒绝的 method，实际 %q", msg)
		}
	}
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("第三方 UI 请求不该让 run 失败，实际 %s", state)
	}
}

// TestClarificationCreatesNoApprovalButSameNameToolDoes 守策略隔离。
//
// 两个方向都要有：
//   - 产品自带的澄清工具（身份=产品扩展路径）→ 直接 allow，零审批；
//   - 第三方同名工具（身份=别的扩展路径）→ 照常走 policy（ask + 审批事件）。
//
// "零审批"用三个外部事实钉住：澄清收口之前本 run 没有任何 approval.request；
// 产品身份的授权调用不产生审批；策略服务签发下一条审批时编号仍是 apr-1
// （说明澄清路径没消耗过任何审批）。
func TestClarificationCreatesNoApprovalButSameNameToolDoes(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(t)
	guard, err := policy.NewPathGuard(opts.WorkspaceDir)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	pol := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace"},
		PathGuard:   guard,
		ApprovalTTL: time.Minute,
	})
	opts.Policy = pol
	opts.Clarification = clarificationOptions(t, dir, 30*time.Second)

	w := newClarifyingWorker("input", "请给一个值")
	svc, snap := startClarificationRun(t, opts, w, StartRunRequest{Message: "问个问题"})
	waitClarificationRequested(t, svc, snap.RunID)

	rs := runStateOf(t, svc, snap.RunID)
	if rs.bridge == nil {
		t.Fatal("启用 Policy 时必须建立执行前授权端点")
	}
	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, snap.SessionID, 10*time.Second)

	productPath := opts.Clarification.ExtensionPath
	thirdPartyPath := filepath.Join(dir, "vendor", "anvil-clarification.ts")

	// 正方向：产品身份的澄清工具 → 本地 catalog 命中 → allow，且不产生审批。
	dec, reason, err := client.AuthorizeWithIdentity(context.Background(), policy.ToolCall{
		ID: "c-product", Name: clarificationToolName, Source: policy.SourceExtension,
		Arguments: json.RawMessage(`{"question":"q","kind":"input"}`),
	}, productPath)
	if err != nil {
		t.Fatalf("产品身份的授权请求失败: %v", err)
	}
	if dec != policy.DecisionAllow {
		t.Fatalf("产品自带的澄清工具应直接 allow，实际 %s（%s）", dec, reason)
	}
	if evs := eventsOfType(t, svc, snap.RunID, EventApprovalRequest); len(evs) != 0 {
		t.Fatalf("产品自带澄清工具的授权产生了 %d 条审批请求", len(evs))
	}

	// 反方向：第三方同名工具身份不同 → 照常走 policy（ask），并且这是策略服务
	// 签发的**第一条**审批（编号 apr-1-…）。
	decisionCh := make(chan policy.Decision, 1)
	go func() {
		dec, _, aerr := client.AuthorizeWithIdentity(context.Background(), policy.ToolCall{
			ID: "c-third", Name: clarificationToolName, Source: policy.SourceExtension,
			Arguments: json.RawMessage(`{"question":"q","kind":"input"}`),
		}, thirdPartyPath)
		if aerr != nil {
			dec = policy.Decision("error:" + aerr.Error())
		}
		decisionCh <- dec
	}()

	var approvalID string
	if !eventually(t, 3*time.Second, func() bool {
		evs := eventsOfType(t, svc, snap.RunID, EventApprovalRequest)
		if len(evs) == 0 {
			return false
		}
		var p map[string]any
		if json.Unmarshal(evs[len(evs)-1].Payload, &p) != nil {
			return false
		}
		approvalID = fmt.Sprint(p["approvalId"])
		return approvalID != ""
	}) {
		t.Fatalf("第三方同名工具没有走 policy（事件：%s）", eventTypes(t, svc, snap.RunID))
	}
	if !strings.HasPrefix(approvalID, "apr-1-") {
		t.Fatalf("审批编号应为 apr-1-…（澄清不该创建审批），实际 %s", approvalID)
	}
	if evs := eventsOfType(t, svc, snap.RunID, EventApprovalRequest); len(evs) != 1 {
		t.Fatalf("只有第三方同名工具该产生审批，实际 %d 条", len(evs))
	}
	if err := svc.ResolveApproval(context.Background(), snap.RunID, approvalID, string(policy.ApprovalDeny)); err != nil {
		t.Fatalf("拒绝审批失败: %v", err)
	}
	select {
	case dec := <-decisionCh:
		if dec != policy.DecisionDeny {
			t.Fatalf("第三方同名工具被拒绝后应得到 deny，实际 %s", dec)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("第三方同名工具的授权调用没有返回")
	}

	// 澄清本身走完：答复 → 本轮正常结算，且答复没有产生新的 approval.resolved。
	resolvedBefore := len(eventsOfType(t, svc, snap.RunID, EventApprovalResolved))
	if err := svc.AnswerClarification(context.Background(), snap.SessionID, "ui-req-1",
		ClarificationResponse{Value: &ClarificationValue{Text: "值"}}); err != nil {
		t.Fatalf("答复失败: %v", err)
	}
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("澄清流程应正常完成，实际 %s", state)
	}
	resolvedAfter := len(eventsOfType(t, svc, snap.RunID, EventApprovalResolved))
	if resolvedAfter != resolvedBefore {
		t.Fatalf("澄清答复不应产生 approval.resolved：前 %d，后 %d", resolvedBefore, resolvedAfter)
	}
}
