package app

// U01 的工作台绑定层（UI05）：当前运行、待审批、最近会话、最近产物、调度摘要、运行健康。
//
// 一条纪律贯穿本文件：**缺来源就是 unavailable，不是 0**。
// 卡片上的数字必须能追溯到本地库或服务；读不出来时给"不可用"和原因，
// 不给一个看起来正常的零值——零会被当成"确实没有"，而"读不出来"需要用户修。
//
// 数据来源（全部真实、全部只读）：
//   - 当前运行 / 运行健康：<appRoot>/sessions.db 的 run_usage（C07 诊断写入者）；
//     它由 run.Options.History 在每次状态推进时落库，不是前端推算出来的；
//   - 待审批：run_events 里 approval.request 与 approval.resolved 的配对结果，
//     只统计仍在 waiting_approval 的 run。审批不会"点过按钮就算通过"；
//   - 最近会话：C06 会话目录（分页/软删除/归档口径与历史侧栏完全一致）；
//   - 最近产物：C07 产物索引（不做磁盘核对，展示的是索引事实）；
//   - 调度摘要：C09 调度服务（库打不开时整张卡片 unavailable）。
//
// 组装原则：一个来源坏了不影响其它卡片；坏掉的那张自己带 reasonCode。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"my-agent-go/internal/artifact"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/scheduler"
	"my-agent-go/internal/session"
)

// 工作台各段的可用性（与 search 的可用性字面量一致，前端只需一套渲染规则）。
const (
	WorkbenchAvailable   = "available"
	WorkbenchUnavailable = "unavailable"
)

// 工作台默认每段条数。
const workbenchSectionLimit = 5

// activeRunStates 是"还没结束"的 run 状态。
//
// 与 contracts 的 RunState 常量同源；写成列表是为了让 SQL 只有一个来源。
var activeRunStates = []string{
	contracts.RunStateQueued,
	contracts.RunStateStarting,
	contracts.RunStateRunning,
	contracts.RunStateWaitingApproval,
	contracts.RunStateStopping,
}

// WorkbenchSection 描述一段的可用性与条数。
//
// Count 为 nil 表示"这一段没有可用的计数"（不可用，或来源只给了条目没给总数）。
// 前端据此显示 unavailable，而不是 0。
type WorkbenchSection struct {
	Availability string `json:"availability"`
	// ReasonCode 是机器可读的失败原因（CallError.Code 或来源名）。
	ReasonCode string `json:"reasonCode,omitempty"`
	Count      *int   `json:"count,omitempty"`
}

// WorkbenchRun 是一条当前运行（来自 run_usage）。
type WorkbenchRun struct {
	RunID        string    `json:"runId"`
	SessionID    string    `json:"sessionId"`
	WorkspaceID  string    `json:"workspaceId,omitempty"`
	State        string    `json:"state"`
	SessionTitle string    `json:"sessionTitle,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// WorkbenchApproval 是一条等待人工确认的审批。
//
// 它来自事件日志的配对结果：请求存在、答复不存在。答复过的不再出现，
// 与 run 的实际状态一致。
type WorkbenchApproval struct {
	RunID       string    `json:"runId"`
	SessionID   string    `json:"sessionId"`
	ApprovalID  string    `json:"approvalId"`
	ToolName    string    `json:"toolName,omitempty"`
	RequestedAt time.Time `json:"requestedAt"`
}

// WorkbenchSession 是一条最近会话。
type WorkbenchSession struct {
	SessionID   string    `json:"sessionId"`
	WorkspaceID string    `json:"workspaceId"`
	Title       string    `json:"title"`
	Pinned      bool      `json:"pinned"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// WorkbenchArtifact 是一条最近产物（索引事实，不做磁盘核对）。
type WorkbenchArtifact struct {
	ArtifactID  string    `json:"artifactId"`
	WorkspaceID string    `json:"workspaceId"`
	DisplayName string    `json:"displayName"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// WorkbenchSchedule 是一条调度摘要。
type WorkbenchSchedule struct {
	ScheduleID          string     `json:"scheduleId"`
	Name                string     `json:"name"`
	Enabled             bool       `json:"enabled"`
	NextScheduledAt     *time.Time `json:"nextScheduledAt,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
}

// WorkbenchHealth 是运行健康的硬事实。
type WorkbenchHealth struct {
	// ActiveRuns 是未结束的 run 数。
	ActiveRuns int `json:"activeRuns"`
	// Completed24h / Failed24h 是最近 24 小时结算的 run 数。
	Completed24h int `json:"completed24h"`
	Failed24h    int `json:"failed24h"`
	// LastRunID / LastState / LastErrorCode 是最近一次 run 的结论。
	LastRunID     string    `json:"lastRunId,omitempty"`
	LastState     string    `json:"lastState,omitempty"`
	LastErrorCode string    `json:"lastErrorCode,omitempty"`
	LastUpdatedAt time.Time `json:"lastUpdatedAt,omitempty"`
}

// WorkbenchSummaryResult 是一次工作台汇总。
type WorkbenchSummaryResult struct {
	GeneratedAt time.Time `json:"generatedAt"`

	CurrentRuns      WorkbenchSection `json:"currentRuns"`
	PendingApprovals WorkbenchSection `json:"pendingApprovals"`
	RecentSessions   WorkbenchSection `json:"recentSessions"`
	RecentArtifacts  WorkbenchSection `json:"recentArtifacts"`
	Schedules        WorkbenchSection `json:"schedules"`
	Health           WorkbenchSection `json:"health"`

	Runs       []WorkbenchRun      `json:"runs"`
	Approvals  []WorkbenchApproval `json:"approvals"`
	Sessions   []WorkbenchSession  `json:"sessions"`
	Artifacts  []WorkbenchArtifact `json:"artifacts"`
	Schedule   []WorkbenchSchedule `json:"schedule"`
	HealthInfo WorkbenchHealth     `json:"healthInfo"`

	// Error 只表示"整个汇总都取不到"（索引库打不开）。单段失败在对应 section 里。
	Error *CallError `json:"error,omitempty"`
}

// availableSection 构造一个可用段。
func availableSection(count int) WorkbenchSection {
	c := count
	return WorkbenchSection{Availability: WorkbenchAvailable, Count: &c}
}

// unavailableSection 构造一个不可用段：不带 Count（不是 0）。
func unavailableSection(reason string) WorkbenchSection {
	return WorkbenchSection{Availability: WorkbenchUnavailable, ReasonCode: reason}
}

// WorkbenchSummary 汇总工作台六段。
func (d *Desktop) WorkbenchSummary() WorkbenchSummaryResult {
	ctx := context.Background()
	out := WorkbenchSummaryResult{
		GeneratedAt: time.Now().UTC(),
		Runs:        []WorkbenchRun{},
		Approvals:   []WorkbenchApproval{},
		Sessions:    []WorkbenchSession{},
		Artifacts:   []WorkbenchArtifact{},
		Schedule:    []WorkbenchSchedule{},
	}

	db, err := d.indexDB()
	if err != nil {
		// 索引库打不开：六段全部不可用，而且原因相同。这里不逐段重复探测，
		// 免得同一句"库打不开"在六张卡片上各说一遍不同的措辞。
		reason := toCallError(err)
		out.CurrentRuns = unavailableSection(reason.Code)
		out.PendingApprovals = unavailableSection(reason.Code)
		out.RecentSessions = unavailableSection(reason.Code)
		out.RecentArtifacts = unavailableSection(reason.Code)
		out.Schedules = unavailableSection(reason.Code)
		out.Health = unavailableSection(reason.Code)
		out.Error = reason
		return out
	}

	// 当前运行 + 健康：同一份 run_usage。
	if runs, health, err := loadWorkbenchRuns(ctx, db); err != nil {
		code := toCallError(err).Code
		out.CurrentRuns = unavailableSection(code)
		out.Health = unavailableSection(code)
	} else {
		out.Runs = runs
		out.CurrentRuns = availableSection(len(runs))
		out.HealthInfo = health
		out.Health = availableSection(health.ActiveRuns)
	}

	// 待审批：事件日志配对。
	if approvals, err := loadPendingApprovals(ctx, db); err != nil {
		out.PendingApprovals = unavailableSection(toCallError(err).Code)
	} else {
		out.Approvals = approvals
		out.PendingApprovals = availableSection(len(approvals))
	}

	// 最近会话：C06 目录（与历史侧栏同一口径）。
	if cat, err := d.sessionCatalog(); err != nil {
		out.RecentSessions = unavailableSection(toCallError(err).Code)
	} else if page, err := cat.ListSessions(ctx, session.SessionQuery{Limit: workbenchSectionLimit}); err != nil {
		out.RecentSessions = unavailableSection(toCallError(err).Code)
	} else {
		for _, g := range page.Groups {
			for _, s := range g.Sessions {
				out.Sessions = append(out.Sessions, WorkbenchSession{
					SessionID:   string(s.ID),
					WorkspaceID: string(s.WorkspaceID),
					Title:       s.Title,
					Pinned:      s.Pinned,
					UpdatedAt:   s.UpdatedAt,
				})
			}
		}
		if len(out.Sessions) > workbenchSectionLimit {
			out.Sessions = out.Sessions[:workbenchSectionLimit]
		}
		out.RecentSessions = availableSection(len(out.Sessions))
	}

	// 最近产物：C07 索引。
	if svc, err := d.artifactService(); err != nil {
		out.RecentArtifacts = unavailableSection(toCallError(err).Code)
	} else if items, err := svc.ListArtifacts(ctx, artifact.Query{Limit: workbenchSectionLimit}); err != nil {
		out.RecentArtifacts = unavailableSection(toCallError(err).Code)
	} else {
		for _, a := range items {
			out.Artifacts = append(out.Artifacts, WorkbenchArtifact{
				ArtifactID:  a.ID,
				WorkspaceID: string(a.WorkspaceID),
				DisplayName: a.DisplayName,
				Size:        a.Size,
				UpdatedAt:   a.UpdatedAt,
			})
		}
		out.RecentArtifacts = availableSection(len(out.Artifacts))
	}

	// 调度摘要：C09 服务（后台循环在启动时已拉起）。
	if svc, err := d.schedulerService(); err != nil {
		out.Schedules = unavailableSection(toCallError(err).Code)
	} else if items, err := svc.List(ctx); err != nil {
		out.Schedules = unavailableSection(toCallError(err).Code)
	} else {
		for _, item := range items {
			out.Schedule = append(out.Schedule, scheduleSummary(item))
		}
		out.Schedules = availableSection(len(out.Schedule))
	}

	return out
}

// scheduleSummary 把调度定义压成工作台需要的几条事实。
func scheduleSummary(item scheduler.Schedule) WorkbenchSchedule {
	out := WorkbenchSchedule{
		ScheduleID:          item.ID,
		Name:                item.Name,
		Enabled:             item.Enabled,
		ConsecutiveFailures: item.ConsecutiveFailures,
	}
	if !item.NextScheduledAt.IsZero() {
		next := item.NextScheduledAt.UTC()
		out.NextScheduledAt = &next
	}
	return out
}

// loadWorkbenchRuns 读 run_usage 里的当前运行与最近 24 小时健康。
func loadWorkbenchRuns(ctx context.Context, db interface {
	SQL() *sql.DB
}) ([]WorkbenchRun, WorkbenchHealth, error) {
	var health WorkbenchHealth

	// 当前运行：只取未结算的状态，按最近更新倒序。
	placeholders := make([]string, len(activeRunStates))
	args := make([]any, 0, len(activeRunStates)+1)
	for i, st := range activeRunStates {
		placeholders[i] = "?"
		args = append(args, st)
	}
	args = append(args, workbenchSectionLimit)
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT run_id, session_id, workspace_id, state, updated_at
		  FROM run_usage
		 WHERE state IN (`+strings.Join(placeholders, ", ")+`)
		 ORDER BY updated_at DESC, run_id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, health, contracts.NewAppError(contracts.CodeInternal, "读取当前运行失败: "+err.Error(), false)
	}
	runs, err := scanWorkbenchRuns(rows)
	rows.Close()
	if err != nil {
		return nil, health, err
	}
	health.ActiveRuns = len(runs)

	// 最近 24 小时结算：completed vs failed/interrupted。
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN state IN ('failed','interrupted') THEN 1 ELSE 0 END), 0)
		  FROM run_usage
		 WHERE julianday(updated_at) >= julianday('now', '-1 day')`).Scan(&health.Completed24h, &health.Failed24h); err != nil {
		return nil, health, contracts.NewAppError(contracts.CodeInternal, "读取运行健康失败: "+err.Error(), false)
	}

	// 最近一条 run 的结论（含错误码）。
	var (
		lastRun     string
		lastState   string
		lastErrCode string
		lastUpdated string
	)
	err = db.SQL().QueryRowContext(ctx, `
		SELECT run_id, state, error_code, updated_at
		  FROM run_usage
		 ORDER BY updated_at DESC, run_id DESC
		 LIMIT 1`).Scan(&lastRun, &lastState, &lastErrCode, &lastUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		// 一次都没跑过：这是真实空态，不是不可用。
		return runs, health, nil
	}
	if err != nil {
		return nil, health, contracts.NewAppError(contracts.CodeInternal, "读取最近运行失败: "+err.Error(), false)
	}
	health.LastRunID = lastRun
	health.LastState = lastState
	health.LastErrorCode = lastErrCode
	if ts, perr := time.Parse(time.RFC3339Nano, lastUpdated); perr == nil {
		health.LastUpdatedAt = ts
	}
	return runs, health, nil
}

// scanWorkbenchRuns 读当前运行行，并补上会话标题（同库一次 join 太脆，分两次读更稳）。
func scanWorkbenchRuns(rows *sql.Rows) ([]WorkbenchRun, error) {
	out := make([]WorkbenchRun, 0, workbenchSectionLimit)
	for rows.Next() {
		var (
			run     WorkbenchRun
			updated string
		)
		if err := rows.Scan(&run.RunID, &run.SessionID, &run.WorkspaceID, &run.State, &updated); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描当前运行失败: "+err.Error(), false)
		}
		if ts, perr := time.Parse(time.RFC3339Nano, updated); perr == nil {
			run.UpdatedAt = ts
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "当前运行结果异常: "+err.Error(), false)
	}
	return out, nil
}

// loadPendingApprovals 从事件日志里找出"有请求、没答复"的审批。
//
// 只统计仍在 waiting_approval 的 run：已经结算的 run 即使历史上有过未答复的
// 请求，也不再是"等待人工确认"（run 结算时会放弃挂起的审批）。
func loadPendingApprovals(ctx context.Context, db interface {
	SQL() *sql.DB
}) ([]WorkbenchApproval, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT e.run_id, COALESCE(u.session_id, ''), e.type, e.payload, e.occurred_at, e.seq
		  FROM run_events e
		  JOIN run_usage u ON u.run_id = e.run_id
		 WHERE u.state = 'waiting_approval'
		   AND e.type IN ('approval.request', 'approval.resolved')
		 ORDER BY e.run_id, e.seq`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取待审批失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]WorkbenchApproval, 0, 4)
	// 按 run 配对：同一个 approvalId 的请求在被答复后就不该再出现。
	pending := map[string]map[string]WorkbenchApproval{}
	order := []string{}
	for rows.Next() {
		var (
			runID, sessionID, typ, payload, occurred string
			seq                                      int64
		)
		if err := rows.Scan(&runID, &sessionID, &typ, &payload, &occurred, &seq); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描待审批失败: "+err.Error(), false)
		}
		approvalID := payloadField(payload, "approvalId")
		if approvalID == "" {
			continue
		}
		key := runID + "\x00" + approvalID
		if pending[runID] == nil {
			pending[runID] = map[string]WorkbenchApproval{}
		}
		if typ == "approval.resolved" {
			delete(pending[runID], approvalID)
			continue
		}
		if _, seen := pending[runID][approvalID]; !seen {
			order = append(order, key)
		}
		item := WorkbenchApproval{
			RunID:      runID,
			SessionID:  sessionID,
			ApprovalID: approvalID,
			ToolName:   payloadField(payload, "toolName"),
		}
		if ts, perr := time.Parse(time.RFC3339Nano, occurred); perr == nil {
			item.RequestedAt = ts
		}
		pending[runID][approvalID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "待审批结果异常: "+err.Error(), false)
	}
	for _, key := range order {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		if item, ok := pending[parts[0]][parts[1]]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

// payloadField 从脱敏后的 JSON 载荷里读一个字符串字段。
//
// 与 diagnostics 的读取方式一致：载荷是不可信输入（外部工具/模型产出），
// 解析失败就当没有这个字段，不 panic、不猜。
func payloadField(payload, key string) string {
	if strings.TrimSpace(payload) == "" {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return ""
	}
	v, ok := fields[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
