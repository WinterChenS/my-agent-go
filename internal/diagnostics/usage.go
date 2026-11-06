// Package diagnostics 是 C07 的观测域：usage/预算汇总、run 详情与诊断导出。
//
// 权威边界
//   - usage/预算/失败结论的来源是 run 快照（由 Recorder 实现 run.HistorySink 落库），
//     不是本包自己算的估算值；tokens 有而价格没有时，tokens 照报、成本 unavailable。
//   - 事件日志（run_events）只保存产品事件（不含 message.delta），载荷在写入与
//     导出两侧都走 internal/redact；诊断包是给人定位故障用的，不是给机器重放的。
//   - 诊断包不默认上传任何地方：它只是写到本机磁盘上的一个文件。
//
// 三态纪律（RUN06 / ART03）
//
//   - 没有 usage 记录 → availability=unavailable，JSON 里**没有** tokens/cost 键；
//
//   - 有 tokens、没有价格 → tokens 照报，cost availability=unavailable、没有 costUsd；
//
//   - 两者都有 → 数字 + priceSource + estimated。
//
//     把"不知道"写成 0 是本域最严重的错误：用户会据此以为"这一轮没花钱"。
package diagnostics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

// tsLayout 是索引时间列的固定宽度 UTC 格式（与 007 / internal/session 同口径）。
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Options 是诊断服务的装配参数。
type Options struct {
	// Root 是诊断包导出根（生产为 <appRoot>/diagnostics）。目标必须落在它里面。
	Root string
	// Version 是产品版本，写进诊断包（版本是定位故障的第一要素）。
	Version string
	// Now 注入时钟，仅用于测试确定性。
	Now func() time.Time
}

// Service 是诊断服务（usage 汇总 / 诊断导出 / run 详情）。并发安全。
type Service struct {
	db      *storage.DB
	root    string
	guard   *policy.PathGuard
	version string
	now     func() time.Time
}

// New 构造诊断服务。数据库由 storage 的迁移（007）建表，本包不建表。
func New(db *storage.DB, opts Options) (*Service, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "诊断服务缺少数据库句柄", false)
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "诊断包导出根目录为空", false)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "诊断包导出根无法绝对化: "+err.Error(), false)
	}
	abs = filepath.Clean(abs)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建诊断包导出根失败: "+abs+" : "+err.Error(), false)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, contracts.NewAppError(contracts.CodeInternal, "诊断包导出根不是目录: "+abs, false)
	}
	guard, err := policy.NewPathGuard(abs)
	if err != nil {
		return nil, err
	}
	svc := &Service{db: db, root: abs, guard: guard, version: strings.TrimSpace(opts.Version), now: opts.Now}
	if svc.now == nil {
		svc.now = time.Now
	}
	if svc.version == "" {
		svc.version = "unknown"
	}
	return svc, nil
}

// SetClock 注入时钟，仅用于测试确定性。
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Root 返回诊断包导出根（只读定位，供绑定层与验收断言使用）。
func (s *Service) Root() string { return s.root }

// UsageRange 是 usage 汇总的范围。
//
// 零值表示"全部记录"；From/To 是含端点的闭区间（按 run 的最后更新时间）。
type UsageRange struct {
	// From/To 用指针：零值时间会被序列化成 "0001-01-01T00:00:00Z"，
	// 那看起来像一个真实的时间边界，而它的实际含义是"没有下界"。
	From        *time.Time `json:"from,omitempty"`
	To          *time.Time `json:"to,omitempty"`
	WorkspaceID string     `json:"workspaceId,omitempty"`
	SessionID   string     `json:"sessionId,omitempty"`
	RunIDs      []string   `json:"runIds,omitempty"`
}

// TokenUsage 是 token 汇总。指针为 nil 表示"不知道"，序列化时键不出现。
type TokenUsage struct {
	Availability string `json:"availability"`
	InputTokens  *int64 `json:"inputTokens,omitempty"`
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	TotalTokens  *int64 `json:"totalTokens,omitempty"`
	// ReportedRuns/UnreportedRuns 说明这份合计覆盖了多少个 run：
	// 合计本身只包含"报了的"那些，未知的数量必须显式给出来。
	ReportedRuns   int `json:"reportedRuns"`
	UnreportedRuns int `json:"unreportedRuns"`
}

// CostUsage 是费用汇总。
//
// 只要有一个"报了 tokens 的 run"没有价格，合计费用就是 unavailable：
// 把已知的那几笔加起来当成总额，是在用一个偏小的数字冒充事实。
type CostUsage struct {
	Availability string   `json:"availability"`
	USD          *float64 `json:"costUsd,omitempty"`
	PriceSource  string   `json:"priceSource,omitempty"`
	Estimated    *bool    `json:"estimated,omitempty"`
	KnownRuns    int      `json:"knownRuns"`
	UnknownRuns  int      `json:"unknownRuns"`
}

// UsageSummary 是一段范围内的 usage 结论。
type UsageSummary struct {
	Availability   string     `json:"availability"`
	Range          UsageRange `json:"range"`
	Runs           int        `json:"runs"`
	ReportedRuns   int        `json:"reportedRuns"`
	UnreportedRuns int        `json:"unreportedRuns"`
	Tokens         TokenUsage `json:"tokens"`
	Cost           CostUsage  `json:"cost"`
	// Note 只在结论需要一句解释时出现（例如"有 3 个 run 没有用量记录"）。
	Note string `json:"note,omitempty"`
}

// runUsageRow 是 run_usage 的一行（本包内部的读取形状）。
type runUsageRow struct {
	RunID        string
	SessionID    string
	WorkspaceID  string
	State        string
	PiVersion    string
	Provider     string
	Model        string
	ConfigRev    uint64
	LastSeq      uint64
	Availability string
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
	CostUSD      *float64
	PriceSource  string
	Estimated    bool
	MaxToolCalls int
	MaxDuration  int64
	ToolCalls    int
	BudgetOver   bool
	BudgetReason string
	ErrorCode    string
	ErrorMsg     string
	ErrorRetry   bool
	StartedAt    *time.Time
	UpdatedAt    time.Time
}

const runUsageColumns = `run_id, session_id, workspace_id, state, pi_version, provider, model,
	config_revision, last_seq, usage_availability, input_tokens, output_tokens, total_tokens,
	cost_usd, price_source, estimated, max_tool_calls, max_duration_ms, tool_calls,
	budget_exceeded, budget_exceeded_reason, error_code, error_message, error_retryable,
	started_at, updated_at`

// UsageSummary 汇总范围内的 usage。
func (s *Service) UsageSummary(ctx context.Context, r UsageRange) (UsageSummary, error) {
	if s == nil {
		return UsageSummary{}, contracts.NewAppError(contracts.CodeInternal, "诊断服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return UsageSummary{}, err
	}
	rows, err := s.readRunUsage(ctx, r)
	if err != nil {
		return UsageSummary{}, err
	}
	return summarizeUsage(r, rows), nil
}

// summarizeUsage 把行聚合成结论；纯函数，便于逐条核对三态。
func summarizeUsage(r UsageRange, rows []runUsageRow) UsageSummary {
	out := UsageSummary{
		Availability: contracts.UsageUnavailable,
		Range:        r,
		Runs:         len(rows),
		Tokens:       TokenUsage{Availability: contracts.UsageUnavailable},
		Cost:         CostUsage{Availability: contracts.UsageUnavailable},
	}
	var (
		input, output, total int64
		costSum              float64
		costKnown            int
		costUnknown          int
		sawCatalog           bool
		sawUnknownSource     bool
	)
	for _, row := range rows {
		// "报了但数字缺失"也算没报：无法并入合计的量，不能按 0 计入。
		if row.Availability != contracts.UsageReported ||
			row.InputTokens == nil || row.OutputTokens == nil || row.TotalTokens == nil {
			out.UnreportedRuns++
			continue
		}
		out.ReportedRuns++
		input += *row.InputTokens
		output += *row.OutputTokens
		total += *row.TotalTokens
		if row.CostUSD == nil {
			costUnknown++
			continue
		}
		costKnown++
		costSum += *row.CostUSD
		if row.Estimated || strings.EqualFold(row.PriceSource, "catalog") {
			sawCatalog = true
		}
		if row.PriceSource == "" {
			sawUnknownSource = true
		}
	}

	if out.ReportedRuns == 0 {
		// 没有一条可用用量：结论就是"不知道"，其余字段保持缺席。
		if out.Runs > 0 {
			out.Note = fmt.Sprintf("%d 个 run 有记录，但都没有可用的用量报告", out.Runs)
		}
		return out
	}

	out.Availability = contracts.UsageReported
	out.Tokens = TokenUsage{
		Availability:   contracts.UsageReported,
		InputTokens:    &input,
		OutputTokens:   &output,
		TotalTokens:    &total,
		ReportedRuns:   out.ReportedRuns,
		UnreportedRuns: out.UnreportedRuns,
	}
	out.Cost = CostUsage{
		Availability: contracts.UsageUnavailable,
		KnownRuns:    costKnown,
		UnknownRuns:  costUnknown,
	}
	if costKnown > 0 && costUnknown == 0 {
		// 每一条都带价格：合计才是事实。
		usd := costSum
		estimated := false
		source := "provider"
		switch {
		case sawCatalog:
			estimated = true
			source = "catalog"
		case sawUnknownSource:
			// 至少有一笔没写来源：不能替它声称是提供方账单。
			source = ""
		}
		out.Cost.Availability = contracts.UsageReported
		out.Cost.USD = &usd
		out.Cost.PriceSource = source
		out.Cost.Estimated = &estimated
	} else if costUnknown > 0 {
		out.Note = joinNotes(out.Note, fmt.Sprintf(
			"%d 个 run 有 tokens 但没有价格，合计费用不可知", costUnknown))
	}
	if out.UnreportedRuns > 0 {
		out.Note = joinNotes(out.Note, fmt.Sprintf(
			"%d 个 run 没有可用的用量报告（不计入合计）", out.UnreportedRuns))
	}
	return out
}

func joinNotes(current, extra string) string {
	if current == "" {
		return extra
	}
	return current + "；" + extra
}

// readRunUsage 按范围读 run_usage 行。
func (s *Service) readRunUsage(ctx context.Context, r UsageRange) ([]runUsageRow, error) {
	where := []string{"1 = 1"}
	args := []any{}
	if r.From != nil {
		where = append(where, "updated_at >= ?")
		args = append(args, formatTS(*r.From))
	}
	if r.To != nil {
		where = append(where, "updated_at <= ?")
		args = append(args, formatTS(*r.To))
	}
	if id := strings.TrimSpace(r.WorkspaceID); id != "" {
		where = append(where, "workspace_id = ?")
		args = append(args, id)
	}
	if id := strings.TrimSpace(r.SessionID); id != "" {
		where = append(where, "session_id = ?")
		args = append(args, id)
	}
	if ids := cleanIDs(r.RunIDs); len(ids) > 0 {
		where = append(where, "run_id IN ("+placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}

	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT `+runUsageColumns+` FROM run_usage WHERE `+strings.Join(where, " AND ")+
			` ORDER BY updated_at DESC, run_id DESC`, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 usage 记录失败: "+err.Error(), true)
	}
	defer rows.Close()

	out := make([]runUsageRow, 0, 16)
	for rows.Next() {
		row, err := scanRunUsage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "usage 记录结果异常: "+err.Error(), true)
	}
	return out, nil
}

// getRunUsage 读单个 run 的记录；没有返回 (nil, nil)——"没有记录"不是错误。
func (s *Service) getRunUsage(ctx context.Context, runID string) (*runUsageRow, error) {
	key := strings.TrimSpace(runID)
	if key == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 run id", false)
	}
	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT `+runUsageColumns+` FROM run_usage WHERE run_id = ?`, key)
	rec, err := scanRunUsage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func scanRunUsage(sc interface{ Scan(dest ...any) error }) (runUsageRow, error) {
	var (
		row                    runUsageRow
		input, output, total   sql.NullInt64
		cost                   sql.NullFloat64
		estimated, over, retry int
		configRev, lastSeq     int64
		maxTool, toolCalls     int
		maxDuration            int64
		startedAt, updatedAt   string
	)
	if err := sc.Scan(&row.RunID, &row.SessionID, &row.WorkspaceID, &row.State,
		&row.PiVersion, &row.Provider, &row.Model, &configRev, &lastSeq,
		&row.Availability, &input, &output, &total, &cost, &row.PriceSource, &estimated,
		&maxTool, &maxDuration, &toolCalls, &over, &row.BudgetReason,
		&row.ErrorCode, &row.ErrorMsg, &retry, &startedAt, &updatedAt); err != nil {
		return runUsageRow{}, err
	}
	if configRev > 0 {
		row.ConfigRev = uint64(configRev)
	}
	if lastSeq > 0 {
		row.LastSeq = uint64(lastSeq)
	}
	if input.Valid {
		v := input.Int64
		row.InputTokens = &v
	}
	if output.Valid {
		v := output.Int64
		row.OutputTokens = &v
	}
	if total.Valid {
		v := total.Int64
		row.TotalTokens = &v
	}
	if cost.Valid {
		v := cost.Float64
		row.CostUSD = &v
	}
	row.Estimated = estimated != 0
	row.MaxToolCalls = maxTool
	row.MaxDuration = maxDuration
	row.ToolCalls = toolCalls
	row.BudgetOver = over != 0
	row.ErrorRetry = retry != 0
	if strings.TrimSpace(startedAt) != "" {
		if ts, err := parseTS(startedAt); err == nil {
			row.StartedAt = &ts
		}
	}
	if ts, err := parseTS(updatedAt); err == nil {
		row.UpdatedAt = ts
	}
	return row, nil
}

func parseTS(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
}

func formatTS(t time.Time) string { return t.UTC().Format(tsLayout) }

func cleanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return out
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
