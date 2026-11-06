package diagnostics

// 诊断记录器：把 run 快照与产品事件落进 007 的三张表。
//
// 它实现 run.HistorySink（RecordRun）并额外做事件订阅（RecordEvent）。
// 这样做的原因：run 的 usage/预算/失败结论只存在于 run 快照里（事件载荷里没有），
// 而"故障可定位"需要落盘的 eventId/时间/错误码/工具与审批轨迹（事件只在内存里
// 保留最近一段用于重连重放，进程重启就没了）。
//
// 纪律
//   - 落库也不写伪 0：unavailable 的数字列写 NULL，不是 0；
//   - 载荷写入时先脱敏（导出时还会再脱敏一遍，两侧都做）；
//   - message.delta 不落盘：它是高频增量，诊断价值低、体量却最大；
//   - 记录失败不改变 run 的结论（run 已经产生真实副作用），但会被计数并暴露出来，
//     不能静默丢弃。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
	"my-agent-go/internal/storage"
)

// maxEventPayloadBytes 是单条事件载荷的落盘上限。超限只记"被截断"的事实
// （类型/时间/事件 ID 仍然保留，故障依然可定位），不保留半份 JSON。
const maxEventPayloadBytes = 8 << 10

// eventDeltaType 是不落盘的事件类型（高频增量）。
const eventDeltaType = "message.delta"

// Recorder 是诊断记录器。并发安全（事件从 run 的广播协程并发到达）。
type Recorder struct {
	db  *storage.DB
	now func() time.Time

	mu        sync.Mutex
	failures  int
	lastError string
}

// NewRecorder 构造记录器。
func NewRecorder(db *storage.DB) (*Recorder, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "诊断记录器缺少数据库句柄", false)
	}
	return &Recorder{db: db, now: time.Now}, nil
}

// SetClock 注入时钟，仅用于测试确定性。
func (r *Recorder) SetClock(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

// Failures 返回记录失败次数与最近一次原因（供状态页/诊断包展示）。
func (r *Recorder) Failures() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures, r.lastError
}

// RecordRun 实现 run.HistorySink：把一次 run 快照落进 run_usage。
//
// 同一 run 重复记录只更新一行（run_id 是主键）；started_at 保留第一次的值，
// updated_at 跟着最新快照走。
func (r *Recorder) RecordRun(ctx context.Context, snap contracts.RunSnapshot) error {
	runID := strings.TrimSpace(string(snap.RunID))
	if runID == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "run 快照缺少 run id，不能记录 usage", false)
	}
	now := formatTS(r.now())

	var (
		availability  = contracts.UsageUnavailable
		input, output any
		total, cost   any
		priceSource   string
		estimated     int
	)
	if u := snap.Usage; u != nil && u.Availability == contracts.UsageReported {
		availability = contracts.UsageReported
		input, output, total = u.InputTokens, u.OutputTokens, u.TotalTokens
		if u.CostUSD != nil {
			cost = *u.CostUSD
		}
		priceSource = u.PriceSource
		if u.Estimated {
			estimated = 1
		}
	}

	var (
		maxTool, toolCalls int
		maxDuration        int64
		over               int
		budgetReason       string
	)
	if b := snap.Budget; b != nil {
		maxTool = b.MaxToolCalls
		maxDuration = b.MaxDurationMs
		toolCalls = b.ToolCalls
		budgetReason = b.ExceededReason
		if b.Exceeded {
			over = 1
		}
	}

	var errorCode, errorMsg string
	var errorRetry int
	if e := snap.Error; e != nil {
		errorCode = e.Code
		errorMsg = e.Message
		if e.Retryable {
			errorRetry = 1
		}
	}

	if _, err := r.db.SQL().ExecContext(ctx, `
		INSERT INTO run_usage(run_id, session_id, workspace_id, state, pi_version, provider, model,
		    config_revision, last_seq, usage_availability, input_tokens, output_tokens, total_tokens,
		    cost_usd, price_source, estimated, max_tool_calls, max_duration_ms, tool_calls,
		    budget_exceeded, budget_exceeded_reason, error_code, error_message, error_retryable,
		    started_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
		    session_id             = excluded.session_id,
		    workspace_id           = excluded.workspace_id,
		    state                  = excluded.state,
		    pi_version             = excluded.pi_version,
		    provider               = excluded.provider,
		    model                  = excluded.model,
		    config_revision        = excluded.config_revision,
		    last_seq               = max(run_usage.last_seq, excluded.last_seq),
		    usage_availability     = excluded.usage_availability,
		    input_tokens           = excluded.input_tokens,
		    output_tokens          = excluded.output_tokens,
		    total_tokens           = excluded.total_tokens,
		    cost_usd               = excluded.cost_usd,
		    price_source           = excluded.price_source,
		    estimated              = excluded.estimated,
		    max_tool_calls         = excluded.max_tool_calls,
		    max_duration_ms        = excluded.max_duration_ms,
		    tool_calls             = excluded.tool_calls,
		    budget_exceeded        = excluded.budget_exceeded,
		    budget_exceeded_reason = excluded.budget_exceeded_reason,
		    error_code             = excluded.error_code,
		    error_message          = excluded.error_message,
		    error_retryable        = excluded.error_retryable,
		    updated_at             = excluded.updated_at`,
		runID, strings.TrimSpace(string(snap.SessionID)), strings.TrimSpace(string(snap.WorkspaceID)),
		snap.State, snap.PiVersion, snap.Provider, snap.Model,
		int64(snap.ConfigRevision), int64(snap.LastSeq), availability,
		input, output, total, cost, priceSource, estimated,
		maxTool, maxDuration, toolCalls, over, budgetReason,
		errorCode, errorMsg, errorRetry, now, now); err != nil {
		r.noteFailure("写入 run usage 失败: " + err.Error())
		return contracts.NewAppError(contracts.CodeInternal, "写入 run usage 失败: "+err.Error(), true)
	}
	return nil
}

// RecordEvent 记录一条产品事件（供 run.Service.Subscribe 使用）。
//
// 它不返回错误（订阅回调没有返回通道），失败会被计数；失败不能让 run 出问题：
// 诊断是派生数据，而 run 已经产生了真实副作用。
func (r *Recorder) RecordEvent(ev contracts.Event) {
	typ := strings.TrimSpace(ev.Type)
	if typ == "" || typ == eventDeltaType {
		return
	}
	runID := strings.TrimSpace(string(ev.RunID))
	if runID == "" || ev.Seq == 0 {
		r.noteFailure("事件缺少 run id 或 seq，无法定位: " + typ)
		return
	}
	payload, truncated, _ := redactPayload(ev.Payload)
	truncatedInt := 0
	if truncated {
		truncatedInt = 1
	}
	occurred := ev.OccurredAt
	if occurred.IsZero() {
		occurred = r.now()
	}
	if _, err := r.db.SQL().ExecContext(context.Background(), `
		INSERT INTO run_events(run_id, seq, event_id, session_id, type, occurred_at, payload, payload_truncated)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, seq) DO UPDATE SET
		    event_id   = excluded.event_id,
		    type       = excluded.type,
		    occurred_at = excluded.occurred_at,
		    payload    = excluded.payload,
		    payload_truncated = excluded.payload_truncated`,
		runID, int64(ev.Seq), ev.EventID, strings.TrimSpace(string(ev.SessionID)), typ,
		formatTS(occurred), payload, truncatedInt); err != nil {
		r.noteFailure("写入 run 事件失败: " + err.Error())
	}
}

func (r *Recorder) noteFailure(msg string) {
	r.mu.Lock()
	r.failures++
	r.lastError = msg
	r.mu.Unlock()
}

// 让"忘了实现接口"在编译期就暴露，而不是在装配时静默退化成 nil。
var _ run.HistorySink = (*Recorder)(nil)

// eventFacts 是事件日志的一行（本包内部的读取形状）。
type eventFacts struct {
	RunID      string
	Seq        uint64
	EventID    string
	SessionID  string
	Type       string
	OccurredAt string
	Payload    string
	Truncated  bool
}

// readRunEvents 读一个 run 的事件（按 seq 升序）。
func (s *Service) readRunEvents(ctx context.Context, runID string, limit int) ([]eventFacts, error) {
	key := strings.TrimSpace(runID)
	if key == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 run id", false)
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT run_id, seq, event_id, session_id, type, occurred_at, payload, payload_truncated
		  FROM run_events WHERE run_id = ? ORDER BY seq ASC LIMIT ?`, key, limit)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 run 事件失败: "+err.Error(), true)
	}
	defer rows.Close()
	out := make([]eventFacts, 0, 16)
	for rows.Next() {
		var (
			ev        eventFacts
			seq       int64
			truncated int
		)
		if err := rows.Scan(&ev.RunID, &seq, &ev.EventID, &ev.SessionID, &ev.Type,
			&ev.OccurredAt, &ev.Payload, &truncated); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描 run 事件失败: "+err.Error(), true)
		}
		if seq > 0 {
			ev.Seq = uint64(seq)
		}
		ev.Truncated = truncated != 0
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "run 事件结果异常: "+err.Error(), true)
	}
	return out, nil
}

// payloadField 从事件载荷里读一个字符串字段；载荷不是对象时返回空。
func payloadField(payload, field string) string {
	if strings.TrimSpace(payload) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
