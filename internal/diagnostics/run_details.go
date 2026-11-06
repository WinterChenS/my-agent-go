package diagnostics

// Run 详情：把"这一轮发生了什么"的**已知事实**拼在一起，并把不知道的段落显式列出来。
//
// 为什么要有 Unavailable 段落：ART03 要求"呈现真实或明确 unknown"。
// 返回一个字段全空但结构完整的对象，会让界面以为"查过了，什么都没有"；
// 列出 unavailable 才是诚实的答案。
//
// 本版本能说清的：run 的索引/用量/预算/失败结论（run_usage）、事件日志里的
// 工具执行、审批、压缩、诊断事件（run_events）、这一轮登记的产物（artifacts）。
// 说不了的：Pi 内部上下文压缩的**明细**（哪些消息被压缩、压缩后 token 数）——
// Pi 的事件只报 compaction_start/end 的事实，不报明细；这里不编造。

import (
	"context"
	"strconv"
	"strings"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
)

// UsageFacts 是单个 run 的用量事实（指针 nil = 不知道，序列化时键不出现）。
type UsageFacts struct {
	Availability string   `json:"availability"`
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
	TotalTokens  *int64   `json:"totalTokens,omitempty"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
	PriceSource  string   `json:"priceSource,omitempty"`
	Estimated    *bool    `json:"estimated,omitempty"`
}

// BudgetFacts 是预算执行情况。
type BudgetFacts struct {
	MaxToolCalls   int    `json:"maxToolCalls"`
	MaxDurationMs  int64  `json:"maxDurationMs"`
	ToolCalls      int    `json:"toolCalls"`
	Exceeded       bool   `json:"exceeded"`
	ExceededReason string `json:"exceededReason,omitempty"`
}

// ErrorFacts 是 run 的失败结论（错误码是机器可读的定位键）。
type ErrorFacts struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// RunRecord 是 run 的索引/用量事实。
type RunRecord struct {
	RunID          string       `json:"runId"`
	SessionID      string       `json:"sessionId,omitempty"`
	WorkspaceID    string       `json:"workspaceId,omitempty"`
	State          string       `json:"state,omitempty"`
	PiVersion      string       `json:"piVersion,omitempty"`
	Provider       string       `json:"provider,omitempty"`
	Model          string       `json:"model,omitempty"`
	ConfigRevision uint64       `json:"configRevision"`
	LastSeq        uint64       `json:"lastSeq"`
	StartedAt      string       `json:"startedAt,omitempty"`
	UpdatedAt      string       `json:"updatedAt,omitempty"`
	Usage          UsageFacts   `json:"usage"`
	Budget         *BudgetFacts `json:"budget,omitempty"`
	Error          *ErrorFacts  `json:"error,omitempty"`
}

// ArtifactRef 是 run 详情里的一条产物引用。
//
// Status 是**索引层**的结论（indexed）：run 详情不逐条核对磁盘。
// 要"现在还在不在、内容有没有变"，调用方必须再走一次 GetArtifact。
type ArtifactRef struct {
	ArtifactID  string `json:"artifactId"`
	DisplayName string `json:"displayName"`
	MediaType   string `json:"mediaType"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Status      string `json:"status"`
}

// 产物在 run 详情里的状态（只有索引层结论）。
const artifactStatusIndexed = "indexed"

// ToolCall 是一次工具执行（来自事件日志）。
type ToolCall struct {
	Seq        uint64 `json:"seq"`
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	IsError    *bool  `json:"isError,omitempty"`
}

// ApprovalFact 是一次审批请求/答复（来自事件日志）。
type ApprovalFact struct {
	Seq        uint64 `json:"seq"`
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	ApprovalID string `json:"approvalId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Choice     string `json:"choice,omitempty"`
}

// EventFact 是一条对定位有用的事件（压缩/诊断/错误）。
type EventFact struct {
	Seq        uint64 `json:"seq"`
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	Kind       string `json:"kind,omitempty"`
	Message    string `json:"message,omitempty"`
}

// UnavailableSection 是一段"这次答不出来"的说明。
type UnavailableSection struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

// RunDetails 是一次 run 的可观测事实集合。
type RunDetails struct {
	RunID       string               `json:"runId"`
	Record      *RunRecord           `json:"record,omitempty"`
	Usage       UsageSummary         `json:"usage"`
	Artifacts   []ArtifactRef        `json:"artifacts"`
	Tools       []ToolCall           `json:"tools"`
	Approvals   []ApprovalFact       `json:"approvals"`
	Compactions []EventFact          `json:"compactions"`
	Diagnostics []EventFact          `json:"diagnostics"`
	Errors      []EventFact          `json:"errors"`
	EventCount  int                  `json:"eventCount"`
	LastEventID string               `json:"lastEventId,omitempty"`
	Unavailable []UnavailableSection `json:"unavailable"`
}

// maxRunEvents 是 run 详情读取的事件上限（超出时显式说明被截断）。
const maxRunEvents = 500

// RunDetails 汇总一个 run 的可观测事实。
func (s *Service) RunDetails(ctx context.Context, runID string) (RunDetails, error) {
	if s == nil {
		return RunDetails{}, contracts.NewAppError(contracts.CodeInternal, "诊断服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return RunDetails{}, err
	}
	key := strings.TrimSpace(runID)
	if key == "" {
		return RunDetails{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 run id", false)
	}
	out := RunDetails{
		RunID:       key,
		Artifacts:   []ArtifactRef{},
		Tools:       []ToolCall{},
		Approvals:   []ApprovalFact{},
		Compactions: []EventFact{},
		Diagnostics: []EventFact{},
		Errors:      []EventFact{},
		Unavailable: []UnavailableSection{},
	}

	row, err := s.getRunUsage(ctx, key)
	if err != nil {
		return RunDetails{}, err
	}
	if row == nil {
		out.Unavailable = append(out.Unavailable, UnavailableSection{
			Section: "record",
			Reason:  "没有这个 run 的 usage/索引记录（可能未记录，或记录器装配在 run 之后）",
		})
		out.Usage = summarizeUsage(UsageRange{RunIDs: []string{key}}, nil)
	} else {
		rec := recordFromRow(*row)
		out.Record = &rec
		out.Usage = summarizeUsage(UsageRange{RunIDs: []string{key}}, []runUsageRow{*row})
	}

	arts, err := s.readArtifacts(ctx, key, 200)
	if err != nil {
		return RunDetails{}, err
	}
	out.Artifacts = arts

	events, err := s.readRunEvents(ctx, key, maxRunEvents)
	if err != nil {
		return RunDetails{}, err
	}
	out.EventCount = len(events)
	if len(events) == 0 {
		out.Unavailable = append(out.Unavailable, UnavailableSection{
			Section: "events",
			Reason:  "事件日志里没有这个 run 的记录（旧版本或日志被清空时无法还原工具/审批轨迹）",
		})
	} else {
		out.LastEventID = events[len(events)-1].EventID
		if len(events) >= maxRunEvents {
			out.Unavailable = append(out.Unavailable, UnavailableSection{
				Section: "events.truncated",
				Reason:  "事件超过读取上限，只展示最早的 " + strconv.Itoa(maxRunEvents) + " 条",
			})
		}
	}
	for _, ev := range events {
		switch ev.Type {
		case run.EventToolStart, run.EventToolEnd:
			call := ToolCall{
				Seq:        ev.Seq,
				EventID:    ev.EventID,
				Type:       ev.Type,
				OccurredAt: ev.OccurredAt,
				ToolCallID: payloadField(ev.Payload, "toolCallId"),
				ToolName:   payloadField(ev.Payload, "toolName"),
			}
			if raw := payloadField(ev.Payload, "isError"); raw == "true" {
				v := true
				call.IsError = &v
			} else if raw == "false" {
				v := false
				call.IsError = &v
			}
			out.Tools = append(out.Tools, call)
		case run.EventApprovalRequest, run.EventApprovalResolved:
			fact := ApprovalFact{
				Seq:        ev.Seq,
				EventID:    ev.EventID,
				Type:       ev.Type,
				OccurredAt: ev.OccurredAt,
				ApprovalID: payloadField(ev.Payload, "approvalId"),
				ToolName:   payloadField(ev.Payload, "toolName"),
				Choice:     payloadField(ev.Payload, "choice"),
			}
			out.Approvals = append(out.Approvals, fact)
		case "compaction_start", "compaction_end":
			out.Compactions = append(out.Compactions, EventFact{
				Seq:        ev.Seq,
				EventID:    ev.EventID,
				Type:       ev.Type,
				OccurredAt: ev.OccurredAt,
				Kind:       payloadField(ev.Payload, "reason"),
			})
		case run.EventDiagnostic:
			out.Diagnostics = append(out.Diagnostics, EventFact{
				Seq:        ev.Seq,
				EventID:    ev.EventID,
				Type:       ev.Type,
				OccurredAt: ev.OccurredAt,
				Kind:       payloadField(ev.Payload, "kind"),
				Message:    payloadField(ev.Payload, "message"),
			})
		case run.EventRunError, run.EventBudgetExceeded:
			out.Errors = append(out.Errors, EventFact{
				Seq:        ev.Seq,
				EventID:    ev.EventID,
				Type:       ev.Type,
				OccurredAt: ev.OccurredAt,
				Kind:       firstNonEmpty(payloadField(ev.Payload, "code"), payloadField(ev.Payload, "kind")),
				Message:    payloadField(ev.Payload, "message"),
			})
		}
	}
	out.Unavailable = append(out.Unavailable, UnavailableSection{
		Section: "context",
		Reason:  "本版本不重建 run 级上下文来源明细：Pi 只报压缩事件，不报被压缩的消息清单",
	})
	return out, nil
}

// recordFromRow 把内部行转成对外事实。
func recordFromRow(row runUsageRow) RunRecord {
	rec := RunRecord{
		RunID:          row.RunID,
		SessionID:      row.SessionID,
		WorkspaceID:    row.WorkspaceID,
		State:          row.State,
		PiVersion:      row.PiVersion,
		Provider:       row.Provider,
		Model:          row.Model,
		ConfigRevision: row.ConfigRev,
		LastSeq:        row.LastSeq,
		UpdatedAt:      formatTS(row.UpdatedAt),
		Usage: UsageFacts{
			Availability: row.Availability,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			TotalTokens:  row.TotalTokens,
			CostUSD:      row.CostUSD,
			PriceSource:  row.PriceSource,
		},
	}
	if row.StartedAt != nil {
		rec.StartedAt = formatTS(*row.StartedAt)
	}
	if row.CostUSD != nil {
		estimated := row.Estimated
		rec.Usage.Estimated = &estimated
	}
	if row.MaxToolCalls > 0 || row.MaxDuration > 0 {
		rec.Budget = &BudgetFacts{
			MaxToolCalls:   row.MaxToolCalls,
			MaxDurationMs:  row.MaxDuration,
			ToolCalls:      row.ToolCalls,
			Exceeded:       row.BudgetOver,
			ExceededReason: row.BudgetReason,
		}
	}
	if row.ErrorCode != "" || row.ErrorMsg != "" {
		rec.Error = &ErrorFacts{Code: row.ErrorCode, Message: row.ErrorMsg, Retryable: row.ErrorRetry}
	}
	return rec
}

// readArtifacts 读某个 run 登记的产物（索引层，不核对磁盘）。
func (s *Service) readArtifacts(ctx context.Context, runID string, limit int) ([]ArtifactRef, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, display_name, media_type, size_bytes, sha256
		  FROM artifacts WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 run 产物失败: "+err.Error(), true)
	}
	defer rows.Close()
	out := make([]ArtifactRef, 0, 8)
	for rows.Next() {
		var ref ArtifactRef
		if err := rows.Scan(&ref.ArtifactID, &ref.DisplayName, &ref.MediaType, &ref.Size, &ref.SHA256); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描 run 产物失败: "+err.Error(), true)
		}
		ref.Status = artifactStatusIndexed
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "run 产物结果异常: "+err.Error(), true)
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
