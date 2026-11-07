package scheduler

// C09 的 SQLite 存储层（迁移 004_schedules.sql）。
//
// 两条硬约束在这里落地，而不是靠调用方自觉：
//   - occurrence 的唯一键 (schedule_id, scheduled_at)：BeginOccurrence 用
//     `ON CONFLICT DO NOTHING` 插入，插不进去就是"这一次已经发生过"；
//   - 用户可见编辑是 CAS（UPDATE ... WHERE revision = ?），调度器自己的记账
//     （last/next/current/failures）走不带 revision 的路径——否则用户每打开一次
//     编辑框都可能被后台 tick 判成冲突。
//
// 时间列一律固定宽度 UTC 文本：scheduled_at 既参与唯一键也参与排序，
// 宽度不固定会让字典序不等于时间序（RFC3339Nano 会裁掉尾随 0）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// tsLayout 是固定宽度的 UTC 时间格式（与 005 的会话目录同一口径）。
const tsLayout = "2006-01-02T15:04:05.000000000Z"

// formatTS 把时间写成固定宽度的 UTC 文本；零值写空串。
func formatTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(tsLayout)
}

// parseTS 解析固定宽度 UTC 文本；空串与不可解析都返回零值。
//
// 不可解析不报错：库里不会出现坏值（写侧只有 formatTS），而"读到一个坏值就让
// 整个任务列表失败"比"这一条的时间显示为未设置"更糟。
func parseTS(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Store 是任务定义与 occurrence 的权威存储。
type Store struct {
	db *storage.DB
}

// NewStore 构造存储。数据库由调用方打开并迁移（迁移编号 004）。
func NewStore(db *storage.DB) (*Store, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "调度器缺少数据库句柄", false)
	}
	return &Store{db: db}, nil
}

// DB 返回底层句柄（诊断/测试用）。
func (s *Store) DB() *storage.DB { return s.db }

const scheduleColumns = `id, name, cron, time_zone, workspace_id, profile_id, prompt, permission_ref,
	output_target, misfire_policy, catch_up_limit, enabled, revision, last_scheduled_at,
	next_scheduled_at, current_session_id, current_run_id, consecutive_failures,
	created_at, updated_at, deleted_at`

func scanSchedule(sc interface {
	Scan(dest ...any) error
}) (Schedule, error) {
	var (
		sch      Schedule
		enabled  int
		revision int64
		last     string
		next     string
		created  string
		updated  string
		deleted  string
	)
	err := sc.Scan(
		&sch.ID, &sch.Name, &sch.Cron, &sch.TimeZone, &sch.WorkspaceID, &sch.ProfileID,
		&sch.Prompt, &sch.PermissionRef, &sch.OutputTarget, &sch.MisfirePolicy,
		&sch.CatchUpLimit, &enabled, &revision, &last, &next,
		&sch.CurrentSessionID, &sch.CurrentRunID, &sch.ConsecutiveFailures,
		&created, &updated, &deleted,
	)
	if err != nil {
		return Schedule{}, err
	}
	sch.Enabled = enabled == 1
	sch.Revision = uint64(revision)
	sch.LastScheduledAt = parseTS(last)
	sch.NextScheduledAt = parseTS(next)
	sch.CreatedAt = parseTS(created)
	sch.UpdatedAt = parseTS(updated)
	sch.DeletedAt = parseTS(deleted)
	return sch, nil
}

// Create 插入一条新任务（revision 从 1 开始）。
func (s *Store) Create(ctx context.Context, sch Schedule) (Schedule, error) {
	if _, err := s.Get(ctx, sch.ID); err == nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeConfigConflict,
			"任务 ID 已存在: "+sch.ID, false)
	} else if !isNotFound(err) {
		return Schedule{}, err
	}
	if sch.CreatedAt.IsZero() {
		sch.CreatedAt = time.Now().UTC()
	}
	sch.Revision = 1
	_, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO schedules(id, name, cron, time_zone, workspace_id, profile_id, prompt,
			permission_ref, output_target, misfire_policy, catch_up_limit, enabled, revision,
			last_scheduled_at, next_scheduled_at, current_session_id, current_run_id,
			consecutive_failures, created_at, updated_at, deleted_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		sch.ID, sch.Name, sch.Cron, sch.TimeZone, sch.WorkspaceID, sch.ProfileID, sch.Prompt,
		sch.PermissionRef, sch.OutputTarget, string(sch.MisfirePolicy), sch.CatchUpLimit,
		boolToInt(sch.Enabled), sch.Revision,
		formatTS(sch.LastScheduledAt), formatTS(sch.NextScheduledAt),
		sch.CurrentSessionID, sch.CurrentRunID, sch.ConsecutiveFailures,
		formatTS(sch.CreatedAt), formatTS(sch.UpdatedAt))
	if err != nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeInternal, "创建任务失败: "+err.Error(), true)
	}
	return s.Get(ctx, sch.ID)
}

// Get 读取一条未删除的任务。
func (s *Store) Get(ctx context.Context, id string) (Schedule, error) {
	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM schedules WHERE id = ? AND deleted_at = ''`, id)
	sch, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, notFoundSchedule(id)
	}
	if err != nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeInternal, "读取任务失败: "+err.Error(), false)
	}
	return sch, nil
}

// List 返回任务列表（默认不含已删除）。
func (s *Store) List(ctx context.Context, includeDeleted bool) ([]Schedule, error) {
	query := `SELECT ` + scheduleColumns + ` FROM schedules`
	if !includeDeleted {
		query += ` WHERE deleted_at = ''`
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.db.SQL().QueryContext(ctx, query)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "列举任务失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]Schedule, 0, 8)
	for rows.Next() {
		sch, err := scanSchedule(rows)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取任务行失败: "+err.Error(), false)
		}
		out = append(out, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "遍历任务失败: "+err.Error(), false)
	}
	return out, nil
}

// Update 是用户可见编辑的 CAS：只有 revision 对得上才落库，成功则 revision+1。
//
// apply 在事务内被调用，可以按需改动任务（例如改 cron 后重算 next）。
func (s *Store) Update(ctx context.Context, id string, expected uint64, apply func(*Schedule) error, now time.Time) (Schedule, error) {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeInternal, "开启任务事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	sch, err := s.getTx(ctx, tx, id)
	if err != nil {
		return Schedule{}, err
	}
	if sch.Revision != expected {
		return Schedule{}, conflictSchedule(id, expected, sch.Revision)
	}
	if err := apply(&sch); err != nil {
		return Schedule{}, err
	}
	sch.Revision = expected + 1
	sch.UpdatedAt = now.UTC()

	res, err := tx.ExecContext(ctx, `
		UPDATE schedules SET name = ?, cron = ?, time_zone = ?, workspace_id = ?, profile_id = ?,
			prompt = ?, permission_ref = ?, output_target = ?, misfire_policy = ?,
			catch_up_limit = ?, enabled = ?, next_scheduled_at = ?, revision = ?, updated_at = ?
		WHERE id = ? AND revision = ? AND deleted_at = ''`,
		sch.Name, sch.Cron, sch.TimeZone, sch.WorkspaceID, sch.ProfileID,
		sch.Prompt, sch.PermissionRef, sch.OutputTarget, string(sch.MisfirePolicy),
		sch.CatchUpLimit, boolToInt(sch.Enabled), formatTS(sch.NextScheduledAt),
		sch.Revision, formatTS(sch.UpdatedAt), id, expected)
	if err != nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeInternal, "更新任务失败: "+err.Error(), true)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Schedule{}, conflictSchedule(id, expected, 0)
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeInternal, "提交任务失败: "+err.Error(), true)
	}
	return s.Get(ctx, id)
}

// Delete 软删除任务（行保留：它已经产生的 run/会话/产物不随之消失）。
func (s *Store) Delete(ctx context.Context, id string, expected uint64, now time.Time) error {
	res, err := s.db.SQL().ExecContext(ctx, `
		UPDATE schedules SET deleted_at = ?, enabled = 0, revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ? AND deleted_at = ''`,
		formatTS(now), formatTS(now), id, expected)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "删除任务失败: "+err.Error(), true)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// 区分"不存在"与"revision 过期"，前端处理不同（重读 vs 提示已删除）。
		if _, gerr := s.Get(ctx, id); gerr != nil {
			return gerr
		}
		return conflictSchedule(id, expected, 0)
	}
	return nil
}

// bookkeeping 是调度器自己的记账字段：**不**推进 revision。
type bookkeeping struct {
	last        time.Time
	next        time.Time
	hasLast     bool
	hasNext     bool
	sessionID   contracts.SessionID
	hasSession  bool
	runID       contracts.RunID
	hasRun      bool
	failures    int
	hasFailures bool
}

// Bookkeeping 更新记账字段（不参与 CAS，也不推进 revision）。
func (s *Store) Bookkeeping(ctx context.Context, id string, b bookkeeping, now time.Time) error {
	sets := []string{"updated_at = ?"}
	args := []any{formatTS(now)}
	if b.hasLast {
		sets = append(sets, "last_scheduled_at = ?")
		args = append(args, formatTS(b.last))
	}
	if b.hasNext {
		sets = append(sets, "next_scheduled_at = ?")
		args = append(args, formatTS(b.next))
	}
	if b.hasSession {
		sets = append(sets, "current_session_id = ?")
		args = append(args, b.sessionID)
	}
	if b.hasRun {
		sets = append(sets, "current_run_id = ?")
		args = append(args, b.runID)
	}
	if b.hasFailures {
		sets = append(sets, "consecutive_failures = ?")
		args = append(args, b.failures)
	}
	args = append(args, id)
	query := "UPDATE schedules SET " + joinComma(sets) + " WHERE id = ? AND deleted_at = ''"
	if _, err := s.db.SQL().ExecContext(ctx, query, args...); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "更新任务记账失败: "+err.Error(), true)
	}
	return nil
}

// PauseForFailures 是"连续失败自动暂停"的 CAS 落地。
//
// 条件里带 revision：并发编辑赢了的时候不能把用户刚保存的状态覆盖掉，
// 所以这里读-改-写重试，而不是无条件 UPDATE。成功时 revision+1（这是调度器
// 唯一被允许推进 revision 的地方，因为它改的是 enabled 这个用户可见字段）。
func (s *Store) PauseForFailures(ctx context.Context, id string, failures int, now time.Time) (Schedule, bool, error) {
	for attempt := 0; attempt < 4; attempt++ {
		sch, err := s.Get(ctx, id)
		if err != nil {
			return Schedule{}, false, err
		}
		if !sch.Enabled {
			return sch, false, nil
		}
		res, err := s.db.SQL().ExecContext(ctx, `
			UPDATE schedules SET enabled = 0, consecutive_failures = ?, revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND enabled = 1 AND deleted_at = ''`,
			failures, formatTS(now), id, sch.Revision)
		if err != nil {
			return Schedule{}, false, contracts.NewAppError(contracts.CodeInternal,
				"自动暂停任务失败: "+err.Error(), true)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			updated, gerr := s.Get(ctx, id)
			if gerr != nil {
				return Schedule{}, false, gerr
			}
			return updated, true, nil
		}
	}
	return Schedule{}, false, contracts.NewAppError(contracts.CodeConfigConflict,
		"自动暂停时任务被并发修改，已放弃（下次 tick 重试）: "+id, true)
}

// BeginOccurrence 尝试为一次计划时刻建 occurrence 行。
//
// created=false 表示这一跳已经存在（唯一键命中）：调用方必须把它当成
// "已经发生过"，既不派发也不重放。
func (s *Store) BeginOccurrence(ctx context.Context, scheduleID string, at, now time.Time) (bool, error) {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return false, contracts.NewAppError(contracts.CodeInternal, "开启 occurrence 事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO schedule_occurrences(schedule_id, scheduled_at, state, reason, session_id,
			run_id, attempts, created_at, updated_at)
		VALUES(?, ?, ?, '', '', '', 0, ?, ?)
		ON CONFLICT(schedule_id, scheduled_at) DO NOTHING`,
		scheduleID, formatTS(at), StateQueued, formatTS(now), formatTS(now))
	if err != nil {
		return false, contracts.NewAppError(contracts.CodeInternal, "记录 occurrence 失败: "+err.Error(), true)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if err := appendOccurrenceEvent(ctx, tx, scheduleID, at, now, StateQueued, "", "", "", ""); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, contracts.NewAppError(contracts.CodeInternal, "提交 occurrence 失败: "+err.Error(), true)
	}
	return true, nil
}

// occurrencePatch 是状态更新时一并要写的绑定字段。
type occurrencePatch struct {
	sessionID     contracts.SessionID
	hasSession    bool
	runID         contracts.RunID
	hasRun        bool
	attemptsDelta int
}

// SetOccurrenceState 迁移 occurrence 状态并追加轨迹。
//
// 返回 changed=false 的两种情况都被当成"无事发生"：
//   - 已经是终态（先写者赢：approval_timeout 不会被随后的 cancelled 覆盖）；
//   - 迁移不在表内（例如 run 的 stopping 不是 occurrence 的一个状态）。
func (s *Store) SetOccurrenceState(ctx context.Context, scheduleID string, at time.Time, state, reason string,
	patch occurrencePatch, now time.Time) (Occurrence, bool, error) {
	return s.setOccurrenceStateFrom(ctx, scheduleID, at, "", state, reason, patch, now)
}

// setOccurrenceStateFrom 是带"期望当前状态"的版本。
//
// expectFrom 只用于派发路径：派发用的是**admission 时的快照**，而在快照与写库之间，
// 真实事件可能已经把这一跳推进到 waiting_approval。此时再用旧快照写 running 就是
// 把真实结论改回中间态（实测可复现）。所以派发只允许从 queued 出发写状态，
// 库里已经不是 queued 时只补绑定字段（run/session/attempts），不动状态。
func (s *Store) setOccurrenceStateFrom(ctx context.Context, scheduleID string, at time.Time,
	expectFrom, state, reason string, patch occurrencePatch, now time.Time) (Occurrence, bool, error) {

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Occurrence{}, false, contracts.NewAppError(contracts.CodeInternal, "开启 occurrence 事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := getOccurrenceTx(ctx, tx, scheduleID, at)
	if err != nil {
		return Occurrence{}, false, err
	}
	if IsTerminalOccurrence(cur.State) {
		return cur, false, nil
	}
	// 状态没变但带了新的绑定信息（run/session/attempts）时仍要落库：
	// 队列里跑起来的 run 一开始就是 queued，此时“queued → queued”并不是迁移，
	// 但把 run_id 丢掉会让这一跳永远无法从事件里找回自己。
	if expectFrom != "" && cur.State != expectFrom {
		state = cur.State
	}
	changedState := cur.State != state
	if changedState && !canTransitionOccurrence(cur.State, state) {
		return cur, false, nil
	}

	sets := []string{"reason = ?", "updated_at = ?", "attempts = attempts + ?"}
	args := []any{reason, formatTS(now), patch.attemptsDelta}
	if changedState {
		sets = append(sets, "state = ?")
		args = append(args, state)
	}
	if patch.hasSession {
		sets = append(sets, "session_id = ?")
		args = append(args, patch.sessionID)
	}
	if patch.hasRun {
		sets = append(sets, "run_id = ?")
		args = append(args, patch.runID)
	}
	args = append(args, scheduleID, formatTS(at))
	if _, err := tx.ExecContext(ctx,
		"UPDATE schedule_occurrences SET "+joinComma(sets)+" WHERE schedule_id = ? AND scheduled_at = ?",
		args...); err != nil {
		return Occurrence{}, false, contracts.NewAppError(contracts.CodeInternal, "更新 occurrence 失败: "+err.Error(), true)
	}
	if changedState {
		if err := appendOccurrenceEvent(ctx, tx, scheduleID, at, now, state, cur.State, reason, cur.SessionID, cur.RunID); err != nil {
			return Occurrence{}, false, err
		}
	}
	updated, err := getOccurrenceTx(ctx, tx, scheduleID, at)
	if err != nil {
		return Occurrence{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Occurrence{}, false, contracts.NewAppError(contracts.CodeInternal, "提交 occurrence 失败: "+err.Error(), true)
	}
	return updated, true, nil
}

// Occurrence 读取一条 occurrence。
func (s *Store) Occurrence(ctx context.Context, scheduleID string, at time.Time) (Occurrence, error) {
	return getOccurrenceTx(ctx, s.db.SQL(), scheduleID, at)
}

// Occurrences 按时间顺序列出某个任务的 occurrence（limit<=0 用默认 50）。
func (s *Store) Occurrences(ctx context.Context, scheduleID string, limit int) ([]Occurrence, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT schedule_id, scheduled_at, state, reason, session_id, run_id, attempts, created_at, updated_at
		FROM schedule_occurrences WHERE schedule_id = ?
		ORDER BY scheduled_at LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 occurrence 列表失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]Occurrence, 0, 8)
	for rows.Next() {
		occ, err := scanOccurrence(rows)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取 occurrence 行失败: "+err.Error(), false)
		}
		out = append(out, occ)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "遍历 occurrence 失败: "+err.Error(), false)
	}
	return out, nil
}

// OccurrenceEvents 返回一条 occurrence 的状态轨迹（按 seq 升序）。
func (s *Store) OccurrenceEvents(ctx context.Context, scheduleID string, at time.Time) ([]OccurrenceEvent, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT seq, schedule_id, scheduled_at, at, state, previous, reason, session_id, run_id
		FROM schedule_occurrence_events WHERE schedule_id = ? AND scheduled_at = ?
		ORDER BY seq`, scheduleID, formatTS(at))
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 occurrence 轨迹失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]OccurrenceEvent, 0, 4)
	for rows.Next() {
		var (
			ev          OccurrenceEvent
			scheduledAt string
			atRaw       string
		)
		if err := rows.Scan(&ev.Seq, &ev.ScheduleID, &scheduledAt, &atRaw, &ev.State,
			&ev.Previous, &ev.Reason, &ev.SessionID, &ev.RunID); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取轨迹行失败: "+err.Error(), false)
		}
		ev.ScheduledAt = parseTS(scheduledAt)
		ev.At = parseTS(atRaw)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "遍历轨迹失败: "+err.Error(), false)
	}
	return out, nil
}

// ActiveOccurrences 返回所有未到终态的 occurrence（重启对账用）。
func (s *Store) ActiveOccurrences(ctx context.Context) ([]Occurrence, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT schedule_id, scheduled_at, state, reason, session_id, run_id, attempts, created_at, updated_at
		FROM schedule_occurrences
		WHERE state IN ('queued', 'running', 'waiting_approval')
		ORDER BY schedule_id, scheduled_at`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取未决 occurrence 失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]Occurrence, 0, 8)
	for rows.Next() {
		occ, err := scanOccurrence(rows)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取未决 occurrence 行失败: "+err.Error(), false)
		}
		out = append(out, occ)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "遍历未决 occurrence 失败: "+err.Error(), false)
	}
	return out, nil
}

// OtherActiveOccurrence 找出同一任务里"除自己之外"仍在跑的那一条（不重入判据）。
func (s *Store) OtherActiveOccurrence(ctx context.Context, scheduleID string, exclude time.Time) (Occurrence, bool, error) {
	row := s.db.SQL().QueryRowContext(ctx, `
		SELECT schedule_id, scheduled_at, state, reason, session_id, run_id, attempts, created_at, updated_at
		FROM schedule_occurrences
		WHERE schedule_id = ? AND scheduled_at <> ?
		  AND state IN ('queued', 'running', 'waiting_approval')
		ORDER BY scheduled_at LIMIT 1`, scheduleID, formatTS(exclude))
	occ, err := scanOccurrence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Occurrence{}, false, nil
	}
	if err != nil {
		return Occurrence{}, false, contracts.NewAppError(contracts.CodeInternal, "读取在跑 occurrence 失败: "+err.Error(), false)
	}
	return occ, true, nil
}

// OccurrenceByRun 反查 run 对应的 occurrence（事件投影用）。
func (s *Store) OccurrenceByRun(ctx context.Context, runID contracts.RunID) (Occurrence, bool, error) {
	row := s.db.SQL().QueryRowContext(ctx, `
		SELECT schedule_id, scheduled_at, state, reason, session_id, run_id, attempts, created_at, updated_at
		FROM schedule_occurrences WHERE run_id = ?
		ORDER BY scheduled_at DESC LIMIT 1`, string(runID))
	occ, err := scanOccurrence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Occurrence{}, false, nil
	}
	if err != nil {
		return Occurrence{}, false, contracts.NewAppError(contracts.CodeInternal, "按 run 查 occurrence 失败: "+err.Error(), false)
	}
	return occ, true, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

func (s *Store) getTx(ctx context.Context, tx *sql.Tx, id string) (Schedule, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM schedules WHERE id = ? AND deleted_at = ''`, id)
	sch, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, notFoundSchedule(id)
	}
	if err != nil {
		return Schedule{}, contracts.NewAppError(contracts.CodeInternal, "读取任务失败: "+err.Error(), false)
	}
	return sch, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getOccurrenceTx(ctx context.Context, q queryRower, scheduleID string, at time.Time) (Occurrence, error) {
	row := q.QueryRowContext(ctx, `
		SELECT schedule_id, scheduled_at, state, reason, session_id, run_id, attempts, created_at, updated_at
		FROM schedule_occurrences WHERE schedule_id = ? AND scheduled_at = ?`, scheduleID, formatTS(at))
	occ, err := scanOccurrence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Occurrence{}, contracts.NewAppError(contracts.CodeNotFound,
			fmt.Sprintf("occurrence 不存在: %s @ %s", scheduleID, formatTS(at)), false)
	}
	if err != nil {
		return Occurrence{}, contracts.NewAppError(contracts.CodeInternal, "读取 occurrence 失败: "+err.Error(), false)
	}
	return occ, nil
}

func scanOccurrence(sc interface {
	Scan(dest ...any) error
}) (Occurrence, error) {
	var (
		occ         Occurrence
		scheduledAt string
		created     string
		updated     string
	)
	err := sc.Scan(&occ.ScheduleID, &scheduledAt, &occ.State, &occ.Reason,
		&occ.SessionID, &occ.RunID, &occ.Attempts, &created, &updated)
	if err != nil {
		return Occurrence{}, err
	}
	occ.ScheduledAt = parseTS(scheduledAt)
	occ.CreatedAt = parseTS(created)
	occ.UpdatedAt = parseTS(updated)
	return occ, nil
}

func appendOccurrenceEvent(ctx context.Context, tx *sql.Tx, scheduleID string, at, now time.Time,
	state, previous, reason string, sessionID contracts.SessionID, runID contracts.RunID) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO schedule_occurrence_events(schedule_id, scheduled_at, at, state, previous, reason, session_id, run_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		scheduleID, formatTS(at), formatTS(now), state, previous, reason, sessionID, runID)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入 occurrence 轨迹失败: "+err.Error(), true)
	}
	return nil
}

func notFoundSchedule(id string) error {
	return contracts.NewAppError(contracts.CodeNotFound, "任务不存在或已删除: "+id, false)
}

func conflictSchedule(id string, expected, current uint64) error {
	msg := fmt.Sprintf("任务修订号已过期: %s 期望 %d", id, expected)
	if current != 0 {
		msg += fmt.Sprintf("，当前 %d", current)
	}
	// 可重试：前端拿最新 revision 重读后可以再次提交（不是数据损坏）。
	return contracts.NewAppError(contracts.CodeConfigConflict, msg, true)
}

func isNotFound(err error) bool {
	var appErr *contracts.AppError
	return errors.As(err, &appErr) && appErr != nil && appErr.Code == contracts.CodeNotFound
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func joinComma(items []string) string { return strings.Join(items, ", ") }
