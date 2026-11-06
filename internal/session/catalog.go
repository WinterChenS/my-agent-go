// Package session 是 C06 的会话目录域：会话的创建、改名、置顶、归档、软删除，
// 以及按工作区分组的稳定分页列举。
//
// 权威边界（基线 §5 / 001_initial.sql 头部声明）：
//   - SQLite 是会话索引的权威存储，但它不复制 Pi transcript 的内容：
//     会话行只保存定位键（session id），transcript 本身仍是 Pi 的独立权威，
//     本包从不打开、更不写它；
//   - 删除会话是**软删除标记**：行还在，所以 transcript 定位键还在，
//     runs/产物等子行也不会被 ON DELETE CASCADE 连带删掉。
//
// 并发约定：所有变更都是 revisions 上的 CAS。读-改-写在一个事务里完成，
// 并且 UPDATE 语句本身带 `WHERE revision = ?`——两个写者拿着同一个 revision
// 提交时，数据库层面只有一个能成功，失败者拿到 CONFIG_CONFLICT，
// 不存在"两边都写了一半"的中间态。
package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// KindChat 是普通会话的 kind。
//
// 目录只列普通会话：自动化/调度（C09）写自己的 kind，它们的行不应该出现在
// 用户的会话列表里，也不应该靠前端过滤来"假装不存在"。
const KindChat = "chat"

const (
	// DefaultPageSize 是未指定 limit 时的每页条数。
	DefaultPageSize = 30
	// MaxPageSize 是单页上限：再大就等于"一次拉全量"，分页也就没意义了。
	MaxPageSize = 200
)

// tsLayout 是 updated_at 的固定宽度格式。
//
// 顺序（pinned DESC, updated_at DESC, id DESC）依赖 TEXT 比较，所以宽度必须固定：
// RFC3339Nano 会裁掉尾随 0，"…:00Z" 与 "…:00.5Z" 的字典序与时间序相反。
// 读取侧仍按 RFC3339Nano 解析，兼容 005 之前由 history.RecordRun 写下的旧值。
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Session 是目录里的一条会话。
//
// Title 为空表示用户还没起名；本地化的"未命名会话"由前端显示（U01），
// 核心层不写入任何语言的占位文案——否则搜索与导出里会混进界面语言。
type Session struct {
	ID          contracts.SessionID   `json:"id"`
	WorkspaceID contracts.WorkspaceID `json:"workspaceId"`
	Title       string                `json:"title"`
	Pinned      bool                  `json:"pinned"`
	Archived    bool                  `json:"archived"`
	Kind        string                `json:"kind"`
	CreatedAt   time.Time             `json:"createdAt"`
	UpdatedAt   time.Time             `json:"updatedAt"`
	// Revision 是 CAS 令牌：每次被接受的变更 +1。
	Revision uint64 `json:"revision"`
}

// SessionQuery 是目录列举条件。
type SessionQuery struct {
	// WorkspaceID 为空表示跨工作区列举。
	WorkspaceID contracts.WorkspaceID
	// IncludeArchived 为 true 时归档会话也返回（默认不返回）。
	IncludeArchived bool
	// Limit 是每页条数；<=0 用 DefaultPageSize，超过 MaxPageSize 会被压到上限。
	Limit int
	// Cursor 是上一页返回的 NextCursor；空表示第一页。
	Cursor string
}

// SessionGroup 是同一工作区的会话，顺序即分页顺序。
type SessionGroup struct {
	WorkspaceID contracts.WorkspaceID `json:"workspaceId"`
	Sessions    []Session             `json:"sessions"`
}

// SessionPageResult 是一页目录。
//
// 分组是**在本页内**按工作区归并：跨页拼一个"完整分组"会把分页变成
// "每个工作区各自分页"，两种语义混在一起最容易漏行。前端要全量时应翻完所有页。
type SessionPageResult struct {
	Groups []SessionGroup `json:"groups"`
	// NextCursor 仅在 HasMore 为真时有值。
	NextCursor string `json:"nextCursor,omitempty"`
	HasMore    bool   `json:"hasMore"`
}

// Catalog 是会话目录服务。并发安全（状态全在 SQLite 里）。
type Catalog struct {
	db  *storage.DB
	now func() time.Time
	// workspaceRoot 让绑定层把工作区真实根路径写进索引行。
	// 为空时写空串：root_path 只是索引字段，history.RecordRun 会在首次 run 时覆盖。
	workspaceRoot func(contracts.WorkspaceID) (string, error)
}

// New 构造目录服务。数据库由 storage 的迁移负责建表，本包不建表。
func New(db *storage.DB) (*Catalog, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "会话目录缺少数据库句柄", false)
	}
	return &Catalog{db: db, now: time.Now}, nil
}

// SetClock 注入时钟，仅用于测试确定性。
func (c *Catalog) SetClock(now func() time.Time) {
	if now != nil {
		c.now = now
	}
}

// SetWorkspaceRoot 注入工作区根路径解析（绑定层用工作区服务实现）。
func (c *Catalog) SetWorkspaceRoot(fn func(contracts.WorkspaceID) (string, error)) {
	c.workspaceRoot = fn
}

const selectColumns = `id, workspace_id, title, pinned, archived, kind, created_at, updated_at, revision`

// ListSessions 按工作区分组列出会话。
//
// 顺序是 pinned DESC, updated_at DESC, id DESC：置顶优先，其余按最近活动倒序，
// 同一时刻用 id 兜底，因此这个顺序是**全序**。
//
// 分页用 keyset（(pinned, updated_at, id) < 游标）而不是 OFFSET：翻页期间有新会话
// 插入时，OFFSET 会把整页往后挤，导致漏行或重复行；keyset 的边界是"上一页最后
// 一行的位置"，插入只可能落在游标之前（更新的 updated_at 排前面），不会重排已翻过的部分。
func (c *Catalog) ListSessions(ctx context.Context, q SessionQuery) (SessionPageResult, error) {
	if err := ctx.Err(); err != nil {
		return SessionPageResult{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}

	where := []string{`deleted_at = ''`, `kind = ?`}
	args := []any{KindChat}
	if !q.IncludeArchived {
		where = append(where, `archived = 0`)
	}
	if id := strings.TrimSpace(string(q.WorkspaceID)); id != "" {
		where = append(where, `workspace_id = ?`)
		args = append(args, id)
	}
	if strings.TrimSpace(q.Cursor) != "" {
		cur, err := decodeCursor(q.Cursor)
		if err != nil {
			return SessionPageResult{}, err
		}
		// 全列倒序 ⇒ "在游标之后"就是元组更小。
		where = append(where, `(pinned, updated_at, id) < (?, ?, ?)`)
		args = append(args, cur.Pinned, cur.UpdatedAt, cur.ID)
	}
	args = append(args, limit+1) // 多取一条只为判断 HasMore，不返回给调用方

	query := `SELECT ` + selectColumns + ` FROM sessions WHERE ` +
		strings.Join(where, ` AND `) +
		` ORDER BY pinned DESC, updated_at DESC, id DESC LIMIT ?`
	rows, err := c.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return SessionPageResult{}, contracts.NewAppError(contracts.CodeInternal, "读取会话目录失败: "+err.Error(), true)
	}
	defer rows.Close()

	items := make([]Session, 0, limit+1)
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return SessionPageResult{}, err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return SessionPageResult{}, contracts.NewAppError(contracts.CodeInternal, "会话目录结果异常: "+err.Error(), true)
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	res := SessionPageResult{Groups: []SessionGroup{}, HasMore: hasMore}
	groupIndex := map[contracts.WorkspaceID]int{}
	for _, s := range items {
		i, ok := groupIndex[s.WorkspaceID]
		if !ok {
			i = len(res.Groups)
			groupIndex[s.WorkspaceID] = i
			res.Groups = append(res.Groups, SessionGroup{WorkspaceID: s.WorkspaceID, Sessions: []Session{}})
		}
		res.Groups[i].Sessions = append(res.Groups[i].Sessions, s)
	}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		res.NextCursor = encodeCursor(pageCursor{
			Pinned:    boolToInt(last.Pinned),
			UpdatedAt: formatTS(last.UpdatedAt),
			ID:        string(last.ID),
		})
	}
	return res, nil
}

// GetSession 读一条未删除的会话。
func (c *Catalog) GetSession(ctx context.Context, id contracts.SessionID) (Session, error) {
	sid, err := requireSessionID(id)
	if err != nil {
		return Session{}, err
	}
	row := c.db.SQL().QueryRowContext(ctx,
		`SELECT `+selectColumns+` FROM sessions WHERE id = ? AND deleted_at = ''`, sid)
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, notFound(sid)
	}
	if err != nil {
		return Session{}, err
	}
	return s, nil
}

// CreateSession 在工作区里新建一个普通会话。
//
// workspaces 是索引表的父行（sessions.workspace_id 是外键），所以这里先确保它存在：
// 用户可能在工作区还没有任何 run 的时候就想开会话。
func (c *Catalog) CreateSession(ctx context.Context, workspaceID contracts.WorkspaceID) (Session, error) {
	wid := strings.TrimSpace(string(workspaceID))
	if wid == "" {
		return Session{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少工作区 ID，无法创建会话", false)
	}
	sid, err := newSessionID()
	if err != nil {
		return Session{}, err
	}
	root := ""
	if c.workspaceRoot != nil {
		root, err = c.workspaceRoot(workspaceID)
		if err != nil {
			return Session{}, err
		}
	}
	now := formatTS(c.now().UTC())

	tx, err := c.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "开启会话事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces(id, root_path, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`, wid, root, now); err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "写入工作区索引失败: "+err.Error(), true)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions(id, workspace_id, title, pinned, archived, kind, created_at, updated_at, revision)
		 VALUES(?, ?, '', 0, 0, ?, ?, ?, 1)`, sid, wid, KindChat, now, now); err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "创建会话失败: "+err.Error(), true)
	}
	row := tx.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM sessions WHERE id = ?`, sid)
	s, err := scanSession(row)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "提交会话事务失败: "+err.Error(), true)
	}
	return s, nil
}

// RenameSession 改标题（CAS）。
func (c *Catalog) RenameSession(ctx context.Context, id contracts.SessionID, expectedRevision uint64, title string) (Session, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return Session{}, contracts.NewAppError(contracts.CodeConfigMissing, "会话标题不能为空", false)
	}
	return c.mutate(ctx, id, expectedRevision, func(time.Time) ([]string, []any) {
		return []string{`title = ?`}, []any{trimmed}
	})
}

// PinSession 设置/取消置顶（CAS）。
func (c *Catalog) PinSession(ctx context.Context, id contracts.SessionID, expectedRevision uint64, pinned bool) (Session, error) {
	return c.mutate(ctx, id, expectedRevision, func(time.Time) ([]string, []any) {
		return []string{`pinned = ?`}, []any{boolToInt(pinned)}
	})
}

// ArchiveSession 归档/取消归档（CAS）。归档不是删除：行、transcript、产物都还在。
func (c *Catalog) ArchiveSession(ctx context.Context, id contracts.SessionID, expectedRevision uint64, archived bool) (Session, error) {
	return c.mutate(ctx, id, expectedRevision, func(time.Time) ([]string, []any) {
		return []string{`archived = ?`}, []any{boolToInt(archived)}
	})
}

// DeleteSession 软删除一条会话（CAS）。
//
// 它不删行、不删 transcript、不删产物：只是打上 deleted_at 并推进 revision。
// 因此"删除"之后历史仍然定位得到（用于诊断与审计），而目录与新建 run 都不再接受它。
func (c *Catalog) DeleteSession(ctx context.Context, id contracts.SessionID, expectedRevision uint64) error {
	_, err := c.mutate(ctx, id, expectedRevision, func(next time.Time) ([]string, []any) {
		return []string{`deleted_at = ?`}, []any{formatTS(next)}
	})
	return err
}

// mutate 是所有变更的唯一入口：读当前行 → CAS 检查 → 单条 UPDATE 推进。
//
// 三条不变量：
//   - 只有 expectedRevision 等于库里 revision 时才写入，否则 CONFIG_CONFLICT；
//   - updated_at/revision 由这里统一推进，调用方无法伪造时间或跳过版本；
//   - UPDATE 自身再带一次 `WHERE revision = ?`：读与写之间即使被插入别的写者，
//     也只有一方能成功，不会出现"两边都改了一点"。
func (c *Catalog) mutate(
	ctx context.Context,
	id contracts.SessionID,
	expectedRevision uint64,
	buildSet func(next time.Time) ([]string, []any),
) (Session, error) {
	sid, err := requireSessionID(id)
	if err != nil {
		return Session{}, err
	}
	tx, err := c.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "开启会话事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		curRevision int64
		curUpdated  string
		curDeleted  string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT revision, updated_at, deleted_at FROM sessions WHERE id = ?`, sid).
		Scan(&curRevision, &curUpdated, &curDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, notFound(sid)
	}
	if err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "读取会话失败: "+err.Error(), true)
	}
	if curDeleted != "" {
		return Session{}, notFound(sid)
	}
	if uint64(curRevision) != expectedRevision {
		return Session{}, conflict(sid, expectedRevision, uint64(curRevision))
	}

	// 严格单调：时钟粗（Windows 上很常见）时也不能让两次变更落到同一时刻，
	// 否则排序与游标会出现"地位相同但内容不同"的行。
	next := c.now().UTC()
	if prev, perr := parseTS(curUpdated); perr == nil && !next.After(prev) {
		next = prev.Add(time.Nanosecond)
	} else if perr != nil && strings.TrimSpace(curUpdated) != "" {
		return Session{}, contracts.NewAppError(contracts.CodeInternal,
			"会话 updated_at 无法解析（疑似被外部改写）: "+sid+" : "+perr.Error(), false)
	}

	set, args := buildSet(next)
	set = append(set, `updated_at = ?`, `revision = revision + 1`)
	args = append(args, formatTS(next), sid, expectedRevision)
	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET `+strings.Join(set, `, `)+
			` WHERE id = ? AND revision = ? AND deleted_at = ''`, args...)
	if err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "更新会话失败: "+err.Error(), true)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// 读与写之间有人抢先改了：结论仍是冲突，不是"没找到"。
		return Session{}, conflict(sid, expectedRevision, 0)
	}
	row := tx.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM sessions WHERE id = ?`, sid)
	s, err := scanSession(row)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "提交会话事务失败: "+err.Error(), true)
	}
	return s, nil
}

// pageCursor 是不透明游标的载荷。字段是位置信息，不是"第几页"。
type pageCursor struct {
	Pinned    int    `json:"p"`
	UpdatedAt string `json:"u"`
	ID        string `json:"i"`
}

func encodeCursor(cur pageCursor) string {
	raw, err := json.Marshal(cur)
	if err != nil {
		// 只有字符串/整数字段，编码失败不可能发生；真发生了就退化成空游标，
		// 由 HasMore/NextCursor 的一致性测试兜住。
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(raw string) (pageCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return pageCursor{}, badCursor()
	}
	var cur pageCursor
	if err := json.Unmarshal(decoded, &cur); err != nil {
		return pageCursor{}, badCursor()
	}
	if cur.ID == "" || cur.UpdatedAt == "" {
		return pageCursor{}, badCursor()
	}
	if _, err := parseTS(cur.UpdatedAt); err != nil {
		return pageCursor{}, badCursor()
	}
	return cur, nil
}

func badCursor() error {
	return contracts.NewAppError(contracts.CodeConfigMissing, "分页游标无法解析（必须原样使用上一页返回的 nextCursor）", false)
}

func scanSession(sc interface {
	Scan(dest ...any) error
}) (Session, error) {
	var (
		s         Session
		id, title string
		workspace string
		kind      string
		created   string
		updated   string
		pinned    int
		archived  int
		revision  int64
	)
	if err := sc.Scan(&id, &workspace, &title, &pinned, &archived, &kind, &created, &updated, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, err
		}
		return Session{}, contracts.NewAppError(contracts.CodeInternal, "解析会话行失败: "+err.Error(), false)
	}
	createdAt, err := parseTS(created)
	if err != nil {
		return Session{}, contracts.NewAppError(contracts.CodeInternal,
			"会话 created_at 无法解析（疑似被外部改写）: "+id+" : "+err.Error(), false)
	}
	// 005 之前的行（history.RecordRun 只写 created_at）在迁移里已回填；
	// 但其它包将来若插入不带 updated_at 的行，这里退化成 created_at 而不是报错，
	// 因为"读不出来"不该让整个目录打不开。
	updatedAt := createdAt
	if strings.TrimSpace(updated) != "" {
		parsed, uerr := parseTS(updated)
		if uerr != nil {
			return Session{}, contracts.NewAppError(contracts.CodeInternal,
				"会话 updated_at 无法解析（疑似被外部改写）: "+id+" : "+uerr.Error(), false)
		}
		updatedAt = parsed
	}
	s = Session{
		ID:          contracts.SessionID(id),
		WorkspaceID: contracts.WorkspaceID(workspace),
		Title:       title,
		Pinned:      pinned != 0,
		Archived:    archived != 0,
		Kind:        kind,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		Revision:    uint64(revision),
	}
	return s, nil
}

func parseTS(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
}

func formatTS(t time.Time) string { return t.UTC().Format(tsLayout) }

func requireSessionID(id contracts.SessionID) (string, error) {
	sid := strings.TrimSpace(string(id))
	if sid == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "缺少会话 ID", false)
	}
	return sid, nil
}

func notFound(id string) error {
	return contracts.NewAppError(contracts.CodeNotFound, "会话不存在或已删除: "+id, false)
}

func conflict(id string, expected, current uint64) error {
	msg := fmt.Sprintf("会话修订号已过期: %s 期望 %d", id, expected)
	if current != 0 {
		msg += fmt.Sprintf("，当前 %d", current)
	}
	// 可重试：前端拿最新 revision 重读后可以再次提交（不是数据损坏）。
	return contracts.NewAppError(contracts.CodeConfigConflict, msg, true)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func newSessionID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成会话 ID 失败: "+err.Error(), false)
	}
	return "ses-" + hex.EncodeToString(buf), nil
}
