package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/runtime/pi"
)

// 默认值。与基线 §6 对齐。
const (
	defaultMaxActive    = 2
	defaultPromptWait   = 60 * time.Second
	defaultStopDeadline = 10 * time.Second
	defaultDeltaWindow  = 50 * time.Millisecond
	maxReplayEvents     = 20000
)

// WorkerStarter 允许注入 worker 构造（测试用假 worker；生产用 pi.StartWorker）。
type WorkerStarter func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error)

// Options 构造 RunService。
type Options struct {
	WorkspaceID  contracts.WorkspaceID
	SessionID    contracts.SessionID
	WorkspaceDir string
	AgentDir     string

	// Installation 是已定位并认证的 Pi。Executable 必填。
	Installation pi.PiInstallation
	// Isolation 是项目资源隔离参数；必须通过 Validate 才能启动。
	Isolation pi.IsolationSpec

	// Provider/Model 交给 Pi 的 --provider/--model。空则用 Pi 自己的默认。
	Provider string
	Model    string

	// Policy 为空时不启用执行前授权端点。生产路径必须提供：
	// 没有端点时策略扩展 fail-closed，工具调用一律被拒。
	Policy *policy.Service

	// HarnessEnv 是自有 MCP 服务的端点与令牌（harnessmcp.Server.Env()）。
	// run 包只负责透传给 worker，不依赖 harnessmcp 包本身：
	// 谁提供端点谁负责写 mcp.json，两边共用同一份环境变量。
	HarnessEnv []string

	MaxActive   int
	StartWorker WorkerStarter
	Now         func() time.Time
}

// Service 管理 run 的生命周期。并发安全。
type Service struct {
	opts Options

	mu     sync.Mutex
	runs   map[contracts.RunID]*runState
	queue  []contracts.RunID
	active int
	closed bool

	subMu sync.RWMutex
	subs  []func(contracts.Event)

	runSeq atomic.Uint64
}

type runState struct {
	id contracts.RunID

	mu    sync.Mutex
	snap  contracts.RunSnapshot
	text  string
	seq   uint64
	state []contracts.Event // 重放用；注意与 snap.State 区分命名

	worker pi.Worker
	bridge *policybridge.Server
	token  string

	pending map[string]policy.ApprovalRequest

	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
	released atomic.Bool
}

// New 创建 RunService。
func New(opts Options) *Service {
	if opts.MaxActive <= 0 {
		opts.MaxActive = defaultMaxActive
	}
	if opts.StartWorker == nil {
		opts.StartWorker = pi.StartWorker
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		opts: opts,
		runs: map[contracts.RunID]*runState{},
	}
}

// StartRunRequest 描述一次用户请求。
type StartRunRequest struct {
	Message string `json:"message"`
}

// Subscribe 注册事件订阅者。回调在独立 goroutine 中执行，避免阻塞事件泵。
func (s *Service) Subscribe(fn func(contracts.Event)) {
	if fn == nil {
		return
	}
	s.subMu.Lock()
	s.subs = append(s.subs, fn)
	s.subMu.Unlock()
}

// Start 接受一次请求并启动（或排队）run。
//
// 返回时的状态一定是真实状态：能立刻启动就是 running，槽位满就是 queued。
// 不会出现"返回 running 但其实还在排队"这种谎报。
func (s *Service) Start(ctx context.Context, req StartRunRequest) (contracts.RunSnapshot, error) {
	text := strings.TrimSpace(req.Message)
	if text == "" {
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeUnsupported, "消息为空", false)
	}
	if err := s.validate(); err != nil {
		return contracts.RunSnapshot{}, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeInternal, "服务已关闭", false)
	}
	id := contracts.RunID(fmt.Sprintf("run-%d-%d", s.opts.Now().Unix(), s.runSeq.Add(1)))
	rs := &runState{
		id:   id,
		text: text,
		snap: contracts.RunSnapshot{
			RunID:       id,
			SessionID:   s.opts.SessionID,
			WorkspaceID: s.opts.WorkspaceID,
			State:       contracts.RunStateQueued,
		},
		pending: map[string]policy.ApprovalRequest{},
		done:    make(chan struct{}),
	}
	s.runs[id] = rs
	canStart := s.active < s.opts.MaxActive && len(s.queue) == 0
	if canStart {
		s.active++
	}
	s.mu.Unlock()

	if !canStart {
		// 排队不启动：这是 RUN03 的核心断言——第三个 run 不能偷偷跑起来。
		s.mu.Lock()
		s.queue = append(s.queue, id)
		s.mu.Unlock()
		s.emit(rs, EventRunState, statePayload{State: contracts.RunStateQueued})
		return rs.snapshot(), nil
	}

	// 必须真的迁移状态，而不是只发一条事件：否则内部仍是 queued，
	// 后面 queued → running 会被状态表拒绝。
	if err := s.transition(rs, contracts.RunStateStarting); err != nil {
		return rs.snapshot(), err
	}
	if err := s.launch(ctx, rs, text); err != nil {
		return rs.snapshot(), err
	}
	return rs.snapshot(), nil
}

// validate 检查启动前置条件。缺任何一项都不能"先跑起来再说"。
func (s *Service) validate() error {
	if strings.TrimSpace(s.opts.Installation.Executable) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "未指定 Pi 可执行文件", false)
	}
	if strings.TrimSpace(s.opts.WorkspaceDir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "未指定工作区目录", false)
	}
	if strings.TrimSpace(s.opts.AgentDir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "未指定隔离 agentDir", false)
	}
	// 隔离参数不完整时拒绝启动：带着"项目资源还能加载"的状态跑 agent 更危险。
	return s.opts.Isolation.Validate()
}

// IsolationArgs 返回**实际**交给 Pi 的隔离参数。
//
// 诊断展示必须读这里，而不是在展示侧另建一份"看起来一样"的参数：
// 两处各写一份的历史结果是界面显示 --no-mcp、实际却启用了 harness MCP，
// 诊断信息说谎比没有诊断信息更糟——它会把人带去错误的方向。
func (s *Service) IsolationArgs() []string { return s.opts.Isolation.Args() }

// launch 启动 worker、建策略端点、下发 prompt。失败时负责资源清理与终态收尾。
func (s *Service) launch(ctx context.Context, rs *runState, text string) error {
	runCtx, cancel := context.WithCancel(context.Background())
	rs.cancel = cancel

	// 每个 run 一个策略端点：审批绑定 session+run，跨 run 不共享结论。
	var env []string
	if s.opts.Policy != nil {
		srv, err := policybridge.Start(runCtx, policybridge.Options{
			Service: s.opts.Policy,
			Binding: policybridge.Binding{
				SessionID:         s.opts.SessionID,
				RunID:             rs.id,
				ConfigRevision:    1,
				ControlGeneration: 1,
			},
			OnPending: func(p policy.ApprovalRequest) {
				rs.mu.Lock()
				rs.pending[p.ID] = p
				rs.mu.Unlock()
				_ = s.transition(rs, contracts.RunStateWaitingApproval)
				s.emit(rs, EventApprovalRequest, map[string]any{
					"approvalId":    p.ID,
					"toolName":      p.ToolName,
					"toolCallId":    p.ToolCallID,
					"arguments":     p.Arguments,
					"expiresAtUnix": p.ExpiresAtUnix,
				})
			},
		})
		if err != nil {
			cancel()
			s.finishFailed(rs, err)
			return err
		}
		rs.bridge = srv
		env = append(env, srv.Env()...)
		rs.token = tokenFromEnv(env)
	}

	// 自有 MCP 的端点与令牌：与策略端点同样是"只经环境变量交付"，
	// 不写进 mcp.json 明文（那里只有 ${...} 引用）。
	env = append(env, s.opts.HarnessEnv...)

	args := append([]string{"--mode", "rpc"}, s.opts.Isolation.Args()...)
	// 会话必须落在隔离 agentDir 下：否则 Pi 会写进用户真实配置目录，
	// 既污染用户数据，也让"这次 run 到底读了哪些会话"无法审计。
	args = append(args, "--session-dir", filepath.Join(s.opts.AgentDir, "sessions"))
	if s.opts.Provider != "" {
		args = append(args, "--provider", s.opts.Provider)
	}
	if s.opts.Model != "" {
		args = append(args, "--model", s.opts.Model)
	}

	w, err := s.opts.StartWorker(ctx, pi.WorkerSpec{
		Installation: s.opts.Installation,
		WorkingDir:   s.opts.WorkspaceDir,
		AgentDir:     s.opts.AgentDir,
		Env:          env,
		Args:         args,
	})
	if err != nil {
		cancel()
		s.closeBridge(rs)
		s.finishFailed(rs, err)
		return err
	}
	rs.worker = w

	go s.pump(rs, runCtx)

	// prompt 只下发一次：写失败或超时都不自动重放（可能已部分送达）。
	prompt, _ := json.Marshal(map[string]string{"type": "prompt", "message": text})
	callCtx, callCancel := context.WithTimeout(ctx, defaultPromptWait)
	defer callCancel()
	if _, err := w.Call(callCtx, prompt); err != nil {
		go s.stopWorker(rs)
		s.finishFailed(rs, err)
		return err
	}
	return s.transition(rs, contracts.RunStateRunning)
}

// stopWorker 异步关闭 worker，不在调用方栈上阻塞。
func (s *Service) stopWorker(rs *runState) {
	if rs.worker != nil {
		_ = rs.worker.Close(context.Background())
	}
}

// pump 把 Pi 原始事件映射成产品事件，并处理最终 idle。
func (s *Service) pump(rs *runState, ctx context.Context) {
	defer s.closeDone(rs)

	var (
		pending   strings.Builder
		lastFlush = time.Now()
		ticker    = time.NewTicker(defaultDeltaWindow)
	)
	defer ticker.Stop()

	flush := func() {
		if pending.Len() == 0 {
			return
		}
		s.emit(rs, EventMessageDelta, map[string]any{"text": pending.String()})
		pending.Reset()
		lastFlush = time.Now()
	}

	events := rs.worker.Events()
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-ticker.C:
			// 50ms 文本合并：流式 delta 逐条推 UI 会造成大量无意义重排（RUN02）。
			if time.Since(lastFlush) >= defaultDeltaWindow {
				flush()
			}
		case ev, ok := <-events:
			if !ok {
				flush()
				// 事件流关闭（进程退出）而没有 settled：这是"结果未知"，不是完成。
				s.interrupted(rs, "Pi 事件流在最终 idle 之前关闭（进程退出或输出中断）")
				return
			}
			if s.handleRaw(rs, ev, &pending) {
				flush()
				return
			}
		}
	}
}

// handleRaw 处理一条原始事件；返回 true 表示本轮已终止。
func (s *Service) handleRaw(rs *runState, ev pi.RawEvent, pending *strings.Builder) bool {
	if ev.Type == "message_update" {
		if delta, ok := textDelta(ev.Record); ok {
			pending.WriteString(delta)
			return false
		}
	}

	switch ev.Type {
	case "tool_execution_start":
		var e struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Args       json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(ev.Record, &e); err == nil {
			s.emit(rs, EventToolStart, map[string]any{
				"toolCallId": e.ToolCallID, "toolName": e.ToolName, "args": e.Args,
			})
		}
	case "tool_execution_end":
		var e struct {
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
			IsError    bool   `json:"isError"`
			Result     struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				StructuredContent struct {
					ExitCode *int `json:"exit_code"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(ev.Record, &e); err == nil {
			var text strings.Builder
			for _, c := range e.Result.Content {
				text.WriteString(c.Text)
			}
			s.emit(rs, EventToolEnd, map[string]any{
				"toolCallId": e.ToolCallID, "toolName": e.ToolName,
				"isError": e.IsError, "text": text.String(),
				"exitCode": e.Result.StructuredContent.ExitCode,
			})
		}
	case "message_end":
		var e struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		_ = json.Unmarshal(ev.Record, &e)
		if e.Message.Role == "assistant" {
			s.emit(rs, EventMessageDone, map[string]any{})
		}
	case pi.TypeAgentSettled:
		var h struct {
			Aborted bool `json:"aborted"`
		}
		_ = json.Unmarshal(ev.Record, &h)
		s.settle(rs, h.Aborted)
		return true
	case pi.TypeProtocolDiagnostic:
		var d struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Record, &d)
		s.emit(rs, EventDiagnostic, map[string]any{"kind": d.Kind, "message": d.Message})
	}
	return false
}

// textDelta 从 message_update 记录里取文本增量。
func textDelta(raw json.RawMessage) (string, bool) {
	var e struct {
		AssistantMessageEvent struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", false
	}
	if e.AssistantMessageEvent.Type != "text_delta" {
		return "", false
	}
	return e.AssistantMessageEvent.Delta, true
}

// settle 处理 agent_settled：这是唯一的完成信号。
func (s *Service) settle(rs *runState, aborted bool) {
	state := rs.snapshot().State
	if aborted || state == contracts.RunStateStopping {
		_ = s.transition(rs, contracts.RunStateCancelled)
	} else {
		_ = s.transition(rs, contracts.RunStateCompleted)
	}
	s.release(rs)
}

// Abort 停止一个 run。
//
// 排队中的 run 直接取消且永不启动；运行中的 run 先进入 stopping（UI 2 秒内可见），
// 再由 worker 有序关闭（静默宽限 → 关 stdin → 超时强杀）。
func (s *Service) Abort(ctx context.Context, id contracts.RunID) error {
	s.mu.Lock()
	rs, ok := s.runs[id]
	if !ok {
		s.mu.Unlock()
		return contracts.NewAppError(contracts.CodeNotFound, "run 不存在: "+string(id), false)
	}
	queuedIdx := -1
	for i, q := range s.queue {
		if q == id {
			queuedIdx = i
			break
		}
	}
	if queuedIdx >= 0 {
		s.queue = append(s.queue[:queuedIdx], s.queue[queuedIdx+1:]...)
	}
	s.mu.Unlock()

	if queuedIdx >= 0 {
		_ = s.transition(rs, contracts.RunStateCancelled)
		s.closeDone(rs)
		return nil
	}

	state := rs.snapshot().State
	if IsTerminal(state) {
		return nil
	}

	_ = s.transition(rs, contracts.RunStateStopping)
	go s.stopRun(rs)
	return nil
}

// stopRun 执行有序停止，并守住"必须有明确清理结果"的底线。
func (s *Service) stopRun(rs *runState) {
	done := make(chan error, 1)
	go func() { done <- s.stopWorkerWait(rs) }()

	select {
	case err := <-done:
		if err != nil {
			// 清理未完成时不能显示"已取消"：如实报 interrupted + 原因。
			s.finishInterrupted(rs, err.Error())
			return
		}
		if st := rs.snapshot().State; st == contracts.RunStateStopping {
			_ = s.transition(rs, contracts.RunStateCancelled)
		}
		s.release(rs)
	case <-time.After(defaultStopDeadline):
		s.finishInterrupted(rs, "停止超时：10 秒内未拿到明确的清理结果")
	}
}

func (s *Service) stopWorkerWait(rs *runState) error {
	if rs.worker == nil {
		return nil
	}
	return rs.worker.Close(context.Background())
}

func (s *Service) finishFailed(rs *runState, cause error) {
	code := contracts.CodeInternal
	msg := cause.Error()
	if ae, ok := cause.(*contracts.AppError); ok {
		code = ae.Code
		msg = ae.Message
	}
	if !IsTerminal(rs.snapshot().State) {
		_ = s.transition(rs, contracts.RunStateFailed)
	}
	s.emit(rs, EventRunError, runErrorPayload{Code: code, Message: msg})
	s.release(rs)
}

func (s *Service) finishInterrupted(rs *runState, reason string) {
	if !IsTerminal(rs.snapshot().State) {
		_ = s.transition(rs, contracts.RunStateInterrupted)
	}
	s.emit(rs, EventRunError, runErrorPayload{
		Code: contracts.CodeCleanupFailed, Message: reason, Retryable: false,
	})
	s.release(rs)
}

func (s *Service) interrupted(rs *runState, reason string) {
	if IsTerminal(rs.snapshot().State) {
		s.release(rs)
		return
	}
	s.finishInterrupted(rs, reason)
}

func (s *Service) closeBridge(rs *runState) {
	if rs.bridge != nil {
		_ = rs.bridge.Close(context.Background())
	}
}

// release 释放并发槽位并按 FIFO 启动下一个排队 run。重复调用无副作用。
func (s *Service) release(rs *runState) {
	if !rs.released.CompareAndSwap(false, true) {
		return
	}
	s.closeBridge(rs)
	if rs.cancel != nil {
		rs.cancel()
	}

	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	if s.closed || len(s.queue) == 0 {
		s.mu.Unlock()
		return
	}
	nextID := s.queue[0]
	s.queue = s.queue[1:]
	next, ok := s.runs[nextID]
	if ok {
		s.active++
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	if IsTerminal(next.snapshot().State) {
		s.release(next)
		return
	}
	_ = s.transition(next, contracts.RunStateStarting)
	if err := s.launch(context.Background(), next, next.text); err != nil {
		return
	}
}

// transition 迁移状态并发出事件；非法迁移返回错误且不改变状态。
func (s *Service) transition(rs *runState, to string) error {
	rs.mu.Lock()
	from := rs.snap.State
	if from == to {
		rs.mu.Unlock()
		return nil
	}
	if !CanTransition(from, to) {
		rs.mu.Unlock()
		return contracts.NewAppError(contracts.CodeInternal,
			fmt.Sprintf("非法状态迁移: %s → %s", from, to), false)
	}
	rs.snap.State = to
	rs.mu.Unlock()

	s.emit(rs, EventRunState, statePayload{State: to, Previous: from})
	return nil
}

// emit 写入事件（seq 单调递增并保留用于重放）并广播给订阅者。
func (s *Service) emit(rs *runState, typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"error":"payload marshal failed"}`)
	}

	rs.mu.Lock()
	rs.seq++
	ev := contracts.Event{
		SchemaVersion: contracts.EventSchemaVersion,
		EventID:       fmt.Sprintf("%s-%d", rs.id, rs.seq),
		WorkspaceID:   s.opts.WorkspaceID,
		SessionID:     s.opts.SessionID,
		RunID:         rs.id,
		Seq:           rs.seq,
		Type:          typ,
		OccurredAt:    s.opts.Now().UTC(),
		Payload:       raw,
	}
	rs.state = append(rs.state, ev)
	if len(rs.state) > maxReplayEvents {
		rs.state = rs.state[len(rs.state)-maxReplayEvents:]
	}
	rs.snap.LastSeq = rs.seq
	rs.mu.Unlock()

	s.subMu.RLock()
	subs := append([]func(contracts.Event){}, s.subs...)
	s.subMu.RUnlock()
	for _, fn := range subs {
		go fn(ev)
	}
}

// snapshot 返回当前快照的副本。
func (rs *runState) snapshot() contracts.RunSnapshot {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.snap
}

func (s *Service) closeDone(rs *runState) {
	rs.doneOnce.Do(func() { close(rs.done) })
}

// Get 返回 run 快照。
func (s *Service) Get(id contracts.RunID) (contracts.RunSnapshot, error) {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	return rs.snapshot(), nil
}

// Replay 返回 afterSeq 之后的事件，用于重连补发（不重复、不丢）。
func (s *Service) Replay(id contracts.RunID, afterSeq uint64) ([]contracts.Event, error) {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]contracts.Event, 0, len(rs.state))
	for _, ev := range rs.state {
		if ev.Seq > afterSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ResolveApproval 提交一次人工授权结论。
//
// 必须经策略端点转发：端点在等这个答复，直接改策略服务状态会让等待方超时。
func (s *Service) ResolveApproval(ctx context.Context, id contracts.RunID, approvalID string, choice string) error {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	if rs.bridge == nil {
		return contracts.NewAppError(contracts.CodeInternal, "该 run 没有启用授权端点", false)
	}
	c, err := parseChoice(choice)
	if err != nil {
		return err
	}
	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, s.opts.SessionID, 10*time.Second)
	if err := client.Resolve(ctx, approvalID, c); err != nil {
		return err
	}
	rs.mu.Lock()
	delete(rs.pending, approvalID)
	state := rs.snap.State
	rs.mu.Unlock()

	s.emit(rs, EventApprovalResolved, map[string]any{"approvalId": approvalID, "choice": string(c)})
	if state == contracts.RunStateWaitingApproval {
		_ = s.transition(rs, contracts.RunStateRunning)
	}
	return nil
}

func parseChoice(v string) (policy.ApprovalChoice, error) {
	switch policy.ApprovalChoice(strings.TrimSpace(v)) {
	case policy.ApprovalOnce:
		return policy.ApprovalOnce, nil
	case policy.ApprovalSessionScope:
		return policy.ApprovalSessionScope, nil
	case policy.ApprovalDeny:
		return policy.ApprovalDeny, nil
	}
	return "", contracts.NewAppError(contracts.CodeUnsupported, "未知审批选项: "+v, false)
}

func tokenFromEnv(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "MYAGENT_POLICY_TOKEN=") {
			return strings.TrimPrefix(kv, "MYAGENT_POLICY_TOKEN=")
		}
	}
	return ""
}

// ActiveCount 返回当前活跃 run 数（诊断/测试用）。
func (s *Service) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// QueuedCount 返回排队 run 数（诊断/测试用）。
func (s *Service) QueuedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Close 关闭服务：停掉所有 run 并释放资源。
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	runs := make([]*runState, 0, len(s.runs))
	for _, rs := range s.runs {
		runs = append(runs, rs)
	}
	s.mu.Unlock()

	var firstErr error
	for _, rs := range runs {
		if IsTerminal(rs.snapshot().State) {
			continue
		}
		if err := s.Abort(ctx, rs.id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// EnsureAgentDir 保证隔离 agentDir 存在。
func EnsureAgentDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "agentDir 为空", false)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建 agentDir 失败: "+err.Error(), false)
	}
	return nil
}
