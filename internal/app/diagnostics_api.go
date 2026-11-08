package app

// C07 诊断绑定层：usage 汇总、run 详情、诊断包导出。
//
// 三条纪律：
//   - "不知道"必须原样传出去：UsageSummary/Cost/Token 的 availability 与指针字段
//     直接来自 internal/diagnostics，绑定层不把 nil 变成 0，也不把 unavailable
//     翻译成"暂无数据，费用 0"；
//   - 时间范围由前端给 RFC3339 字符串：解析失败是 CONFIG_MISSING，不是静默忽略
//     （忽略一个边界会让"这个月花了多少"答成"全部时间"）；
//   - 诊断包只写到本机 <appRoot>/diagnostics，不上传任何地方。

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/diagnostics"
)

// UsageSummaryQuery 是 usage 汇总的范围（时间为 RFC3339 字符串，空表示不限）。
type UsageSummaryQuery struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	WorkspaceID string   `json:"workspaceId"`
	SessionID   string   `json:"sessionId"`
	RunIDs      []string `json:"runIds"`
}

// UsageSummaryResult 是 usage 汇总结果。
type UsageSummaryResult struct {
	Summary diagnostics.UsageSummary `json:"summary"`
	Error   *CallError               `json:"error,omitempty"`
}

// DiagnosticsExportRequest 是诊断导出请求。
type DiagnosticsExportRequest struct {
	RunIDs      []string `json:"runIds"`
	SessionID   string   `json:"sessionId"`
	WorkspaceID string   `json:"workspaceId"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Target      string   `json:"target"`
}

// DiagnosticsExportResult 是一次诊断导出的结果。
type DiagnosticsExportResult struct {
	Path          string     `json:"path"`
	Bytes         int        `json:"bytes"`
	SHA256        string     `json:"sha256"`
	Redactions    int        `json:"redactions"`
	Runs          int        `json:"runs"`
	Events        int        `json:"events"`
	Artifacts     int        `json:"artifacts"`
	SchemaVersion int        `json:"schemaVersion"`
	GeneratedAt   time.Time  `json:"generatedAt"`
	Error         *CallError `json:"error,omitempty"`
}

// RunDetailsResult 是一个 run 的可观测事实。
type RunDetailsResult struct {
	Details diagnostics.RunDetails `json:"details"`
	Error   *CallError             `json:"error,omitempty"`
}

// RunUsageListQuery 是跨 run 列表的查询（时间范围为 RFC3339，空表示不限）。
type RunUsageListQuery struct {
	From        string `json:"from"`
	To          string `json:"to"`
	WorkspaceID string `json:"workspaceId"`
	SessionID   string `json:"sessionId"`
	Limit       int    `json:"limit"`
	Offset      int    `json:"offset"`
}

// RunUsageListResult 是一页 run 记录。
//
// Availability 区分两件必须分开的事：
//   - reported：查询真的做了（哪怕是 0 条）——“这段时间没有 run”，
//   - unavailable：查询没做成（索引库不在、时间范围非法等）——“读不出来”，
//     此时 Error 与 Note 说明原因。
//
// Items 恒为非 nil，否则序列化成 null，界面只能把 null 当成“没有数据”。
type RunUsageListResult struct {
	Availability string                  `json:"availability"`
	Items        []diagnostics.RunRecord `json:"items"`
	Total        int                     `json:"total"`
	Limit        int                     `json:"limit"`
	Offset       int                     `json:"offset"`
	Note         string                  `json:"note,omitempty"`
	Error        *CallError              `json:"error,omitempty"`
}

// ListRunUsage 分页列出范围内的 run（含 usage/预算/错误结论）。
//
// 分页与排序由 diagnostics 决定（updated_at DESC, run_id DESC），绑定层不重写。
func (d *Desktop) ListRunUsage(q RunUsageListQuery) RunUsageListResult {
	unavailable := func(err error) RunUsageListResult {
		return RunUsageListResult{
			Availability: contracts.UsageUnavailable,
			Items:        []diagnostics.RunRecord{},
			Limit:        q.Limit,
			Offset:       q.Offset,
			Note:         "读不出这一段时间的 run 记录（不是“没有记录”）：错误码与原因见 error",
			Error:        toCallError(err),
		}
	}
	from, err := parseOptionalTime(q.From)
	if err != nil {
		return unavailable(err)
	}
	to, err := parseOptionalTime(q.To)
	if err != nil {
		return unavailable(err)
	}
	svc, err := d.diagnosticsService()
	if err != nil {
		return unavailable(err)
	}
	list, err := svc.ListRunUsage(context.Background(), diagnostics.RunUsageQuery{
		UsageRange: diagnostics.UsageRange{
			From:        from,
			To:          to,
			WorkspaceID: strings.TrimSpace(q.WorkspaceID),
			SessionID:   strings.TrimSpace(q.SessionID),
		},
		Limit:  q.Limit,
		Offset: q.Offset,
	})
	if err != nil {
		return unavailable(err)
	}
	items := list.Items
	if items == nil {
		items = []diagnostics.RunRecord{}
	}
	res := RunUsageListResult{
		Availability: contracts.UsageReported,
		Items:        items,
		Total:        list.Total,
		Limit:        list.Limit,
		Offset:       list.Offset,
	}
	if list.Total == 0 {
		res.Note = "这个范围内没有 run 记录（查询已执行）"
	}
	return res
}

// diagnosticsService 装配诊断服务（每次现装配：服务无状态，状态在库里）。
func (d *Desktop) diagnosticsService() (*diagnostics.Service, error) {
	db, err := d.indexDB()
	if err != nil {
		return nil, err
	}
	return diagnostics.New(db, diagnostics.Options{
		Root:    filepath.Join(d.root, "diagnostics"),
		Version: d.version(),
	})
}

// diagnosticsRecorder 装配诊断记录器（run 快照与事件落库）。
//
// 返回 nil 表示"诊断不可用"：调用方必须容忍它，并把原因暴露出来，
// 而不是让整个聊天因为诊断库打不开而启动不了。
func (d *Desktop) diagnosticsRecorder() (*diagnostics.Recorder, error) {
	db, err := d.indexDB()
	if err != nil {
		return nil, err
	}
	return diagnostics.NewRecorder(db)
}

// UsageSummary 汇总一段范围内的 usage。
func (d *Desktop) UsageSummary(q UsageSummaryQuery) UsageSummaryResult {
	from, err := parseOptionalTime(q.From)
	if err != nil {
		return UsageSummaryResult{Error: toCallError(err)}
	}
	to, err := parseOptionalTime(q.To)
	if err != nil {
		return UsageSummaryResult{Error: toCallError(err)}
	}
	svc, err := d.diagnosticsService()
	if err != nil {
		return UsageSummaryResult{Error: toCallError(err)}
	}
	summary, err := svc.UsageSummary(context.Background(), diagnostics.UsageRange{
		From:        from,
		To:          to,
		WorkspaceID: strings.TrimSpace(q.WorkspaceID),
		SessionID:   strings.TrimSpace(q.SessionID),
		RunIDs:      q.RunIDs,
	})
	if err != nil {
		return UsageSummaryResult{Error: toCallError(err)}
	}
	return UsageSummaryResult{Summary: summary}
}

// ExportDiagnostics 导出诊断包到本机诊断目录。
func (d *Desktop) ExportDiagnostics(req DiagnosticsExportRequest) DiagnosticsExportResult {
	from, err := parseOptionalTime(req.From)
	if err != nil {
		return DiagnosticsExportResult{Error: toCallError(err)}
	}
	to, err := parseOptionalTime(req.To)
	if err != nil {
		return DiagnosticsExportResult{Error: toCallError(err)}
	}
	svc, err := d.diagnosticsService()
	if err != nil {
		return DiagnosticsExportResult{Error: toCallError(err)}
	}
	res, err := svc.ExportDiagnostics(context.Background(), diagnostics.ExportRequest{
		RunIDs:      req.RunIDs,
		SessionID:   strings.TrimSpace(req.SessionID),
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		From:        derefTime(from),
		To:          derefTime(to),
		Target:      strings.TrimSpace(req.Target),
	})
	if err != nil {
		return DiagnosticsExportResult{Error: toCallError(err)}
	}
	return DiagnosticsExportResult{
		Path:          res.Path,
		Bytes:         res.Bytes,
		SHA256:        res.SHA256,
		Redactions:    res.Redactions,
		Runs:          res.Runs,
		Events:        res.Events,
		Artifacts:     res.Artifacts,
		SchemaVersion: res.SchemaVersion,
		GeneratedAt:   res.GeneratedAt,
	}
}

// RunDetails 返回一个 run 的索引/用量/预算/事件事实。
//
// 答不出来的段落会出现在 Details.Unavailable 里——界面必须显示"未知"，
// 不能把空数组当成"确实没有发生过工具调用"。
func (d *Desktop) RunDetails(runID string) RunDetailsResult {
	svc, err := d.diagnosticsService()
	if err != nil {
		return RunDetailsResult{Error: toCallError(err)}
	}
	details, err := svc.RunDetails(context.Background(), runID)
	if err != nil {
		return RunDetailsResult{Error: toCallError(err)}
	}
	return RunDetailsResult{Details: details}
}

// parseOptionalTime 把可选的 RFC3339 字符串解析成时间指针。
func parseOptionalTime(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing,
			"时间范围必须用 RFC3339 格式（例如 2026-11-05T00:00:00Z）: "+raw, false)
	}
	utc := ts.UTC()
	return &utc, nil
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
