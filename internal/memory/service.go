// Package memory 是 C05 的长期记忆：存储、中文 trigram 检索与受控注入。
//
// 权威边界（基线 §5/§7）：
//   - SQLite 是记忆的权威存储，FTS5 trigram 是检索索引；两者都由本包写入。
//   - 检索只返回授权 scope 内 status=active 且未过期的条目；
//     candidate/deleted/expired 一律不参与注入。
//   - 每次注入最多 5 条、总计最多 6000 UTF-8 bytes；不把 bytes 冒充 tokens。
//   - 显式保存直接 active；V1 不做后台自动抽取，因此这里没有任何模型/网络调用。
//
// 「不降级」原则：FTS5 trigram 不可用时 Open 直接失败，绝不退化成 LIKE 全表扫描
// （那会让"中文检索通过"变成一句无法验证的话）。
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// Scope 是记忆的可见范围。
type Scope string

const (
	// ScopeUser 跨项目共享（workspace_id 恒为空）。
	ScopeUser Scope = "user"
	// ScopeWorkspace 只属于一个项目。
	ScopeWorkspace Scope = "workspace"
)

// Status 是记忆的生命周期状态。
type Status string

const (
	StatusCandidate Status = "candidate"
	StatusActive    Status = "active"
	StatusDeleted   Status = "deleted"
)

// Kind 是记忆的语义分类。取值是稳定契约，前端与导出都按它显示。
const (
	KindFact       = "fact"
	KindPreference = "preference"
	KindConvention = "convention"
	KindNote       = "note"
)

// 基线固定值（§7）。
const (
	// MaxContextEntries 是每次注入的条数上限。
	MaxContextEntries = 5
	// MaxContextBytes 是每次注入的 UTF-8 字节上限。
	MaxContextBytes = 6000
	// MinQueryRunes 是允许检索的最短查询长度（按 Unicode 码点计）。
	MinQueryRunes = 3
)

// Memory 是一条已落库的记忆。
type Memory struct {
	ID              string     `json:"id"`
	Scope           Scope      `json:"scope"`
	WorkspaceID     string     `json:"workspaceId"`
	Kind            string     `json:"kind"`
	Content         string     `json:"content"`
	SourceSessionID string     `json:"sourceSessionId"`
	Status          Status     `json:"status"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
}

// MemoryDraft 是待保存的记忆。
//
// Status 为空表示"用户显式保存"，直接 active；自主提取路径必须显式传 candidate。
type MemoryDraft struct {
	Scope           Scope
	WorkspaceID     string
	Kind            string
	Content         string
	SourceSessionID string
	Status          Status
	ExpiresAt       *time.Time
}

// MemoryQuery 是一次检索/注入请求。
type MemoryQuery struct {
	WorkspaceID string
	Text        string
	// Limit 默认 5，且无论如何不超过 5（基线固定值）。
	Limit int
	// Scope 为空表示 user + 当前 workspace 都查。
	Scope Scope
}

// ContextEntry 是一条注入条目及其来源。
type ContextEntry struct {
	MemoryID        string `json:"memoryId"`
	Scope           Scope  `json:"scope"`
	WorkspaceID     string `json:"workspaceId"`
	SourceSessionID string `json:"sourceSessionId"`
	// Content 是按 rune 边界截断后真正进入 prompt 的内容。
	Content string `json:"content"`
	// Bytes 是该条内容的实际 UTF-8 字节数（不是 token 数）。
	Bytes int `json:"bytes"`
	// Truncated 表示这条内容为了预算被裁剪过。
	Truncated bool `json:"truncated"`
}

// MemoryContext 是受控注入块。
//
// Bytes 是 Text 的真实 UTF-8 字节数；Provenance 与 Entries 一一对应，
// 便于"这条内容从哪来"的审计，而不是只给模型一段无来源的文本。
type MemoryContext struct {
	Entries    []ContextEntry `json:"entries"`
	Bytes      int            `json:"bytes"`
	Provenance []string       `json:"provenance"`
	Text       string         `json:"text"`
}

// ExportDocument 是导出文件的内容。导出只落在本地，不上传。
type ExportDocument struct {
	SchemaVersion int       `json:"schemaVersion"`
	ExportedAt    time.Time `json:"exportedAt"`
	WorkspaceID   string    `json:"workspaceId"`
	Items         []Memory  `json:"items"`
}

// Service 是记忆服务。它持有一个真实文件数据库，不提供内存库降级。
type Service struct {
	db  *storage.DB
	now func() time.Time
}

// Open 打开（必要时创建）记忆数据库，应用迁移，并真实探测 FTS5 trigram。
//
// 探测失败即返回错误：宁可记忆不可用并明确报错，也不静默退回全表扫描。
func Open(ctx context.Context, path string) (*Service, error) {
	db, err := storage.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	svc := New(db)
	if err := svc.VerifyTrigram(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return svc, nil
}

// New 用已迁移的数据库构造服务。
func New(db *storage.DB) *Service {
	return &Service{db: db, now: time.Now}
}

// DB 暴露底层数据库（同包测试与诊断用）。
func (s *Service) DB() *storage.DB { return s.db }

// Close 关闭数据库。
func (s *Service) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Save 保存一条记忆。显式保存默认 active；自主提取必须显式传 candidate。
func (s *Service) Save(ctx context.Context, draft MemoryDraft) (Memory, error) {
	scope, workspaceID, err := normalizeScope(draft.Scope, draft.WorkspaceID)
	if err != nil {
		return Memory{}, err
	}
	content := strings.TrimSpace(draft.Content)
	if content == "" {
		return Memory{}, contracts.NewAppError(contracts.CodeUnsupported, "记忆内容不能为空", false)
	}
	if !utf8.ValidString(content) {
		return Memory{}, contracts.NewAppError(contracts.CodeUnsupported, "记忆内容不是合法 UTF-8", false)
	}
	kind, err := normalizeKind(draft.Kind)
	if err != nil {
		return Memory{}, err
	}
	status := draft.Status
	switch status {
	case "":
		status = StatusActive
	case StatusActive, StatusCandidate:
	default:
		return Memory{}, contracts.NewAppError(contracts.CodeUnsupported,
			"记忆状态只能是 active 或 candidate（deleted 由删除操作设置）", false)
	}
	// 秘密检测在这里而不是 UI：MCP 工具、绑定、未来的导入都可能直接调到 Save。
	if reason := DetectSecret(content); reason != "" {
		return Memory{}, contracts.NewAppError(contracts.CodeUnsupported,
			"拒绝保存为记忆：疑似凭证（"+reason+"）。请把密钥放进凭证库；不要把整段工具日志或密钥原文存成记忆。",
			false)
	}

	id, err := newMemoryID()
	if err != nil {
		return Memory{}, err
	}
	now := s.now().UTC()
	expires := ""
	if draft.ExpiresAt != nil {
		expires = formatTime(*draft.ExpiresAt)
	}

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, contracts.NewAppError(contracts.CodeInternal, "开启保存事务失败: "+err.Error(), false)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memories(id, scope, workspace_id, kind, content, source_session_id, status, created_at, updated_at, expires_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(scope), workspaceID, kind, content, draft.SourceSessionID, string(status),
		formatTime(now), formatTime(now), expires); err != nil {
		return Memory{}, contracts.NewAppError(contracts.CodeInternal, "写入记忆失败: "+err.Error(), false)
	}
	// FTS 行与本体在同一个事务里写入：不存在"本体已写、索引没写"的窗口。
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_fts(rowid, content)
		SELECT rowid, content FROM memories WHERE id = ?`, id); err != nil {
		return Memory{}, contracts.NewAppError(contracts.CodeInternal, "写入检索索引失败: "+err.Error(), false)
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, contracts.NewAppError(contracts.CodeInternal, "提交记忆失败: "+err.Error(), false)
	}
	return s.Get(ctx, id)
}

// Get 按 ID 读取一条未被删除的记忆。
func (s *Service) Get(ctx context.Context, id string) (Memory, error) {
	row := s.db.SQL().QueryRowContext(ctx, selectMemoryColumns+` WHERE m.id = ? AND m.status <> 'deleted'`, id)
	m, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, contracts.NewAppError(contracts.CodeNotFound, "记忆不存在或已删除: "+id, false)
	}
	if err != nil {
		return Memory{}, contracts.NewAppError(contracts.CodeInternal, "读取记忆失败: "+err.Error(), false)
	}
	return m, nil
}

// Confirm 把候选记忆确认为 active（显式确认路径，不涉及模型调用）。
func (s *Service) Confirm(ctx context.Context, id string) error {
	return s.transition(ctx, id, StatusActive)
}

// Delete 软删除一条记忆，并从检索索引里移除；下一次注入不会再包含它。
//
// 它不会、也无法撤回已经发给模型的历史消息（MEM05 的诚实边界）。
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.transition(ctx, id, StatusDeleted)
}

func (s *Service) transition(ctx context.Context, id string, to Status) error {
	if strings.TrimSpace(id) == "" {
		return contracts.NewAppError(contracts.CodeNotFound, "缺少记忆 ID", false)
	}
	now := formatTime(s.now().UTC())
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "开启记忆状态事务失败: "+err.Error(), false)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE memories SET status = ?, updated_at = ? WHERE id = ? AND status <> 'deleted'`,
		string(to), now, id)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "更新记忆状态失败: "+err.Error(), false)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return contracts.NewAppError(contracts.CodeNotFound, "记忆不存在或已删除: "+id, false)
	}
	switch to {
	case StatusDeleted:
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memory_fts WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)`, id); err != nil {
			return contracts.NewAppError(contracts.CodeInternal, "移除检索索引失败: "+err.Error(), false)
		}
	case StatusActive:
		// 候选可能是在一次 RebuildSearchIndex（只索引 active）之后被确认的，
		// 这里补一次 upsert，保证 active 条目一定可检索。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memory_fts WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)`, id); err != nil {
			return contracts.NewAppError(contracts.CodeInternal, "重建检索索引失败: "+err.Error(), false)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_fts(rowid, content) SELECT rowid, content FROM memories WHERE id = ?`, id); err != nil {
			return contracts.NewAppError(contracts.CodeInternal, "写入检索索引失败: "+err.Error(), false)
		}
	}
	if err := tx.Commit(); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "提交记忆状态失败: "+err.Error(), false)
	}
	return nil
}

// List 返回当前项目可见的候选与生效记忆（不含 deleted）。
//
// 列表不受 scope 开关影响：关闭注入不等于把已有条目藏起来，
// 用户需要看见"哪些被关掉了"才能重新打开。
func (s *Service) List(ctx context.Context, workspaceID string) ([]Memory, error) {
	rows, err := s.db.SQL().QueryContext(ctx, selectMemoryColumns+`
		WHERE m.status <> 'deleted'
		  AND (m.scope = 'user' OR (m.scope = 'workspace' AND m.workspace_id = ?))
		ORDER BY m.created_at DESC, m.id DESC`, workspaceID)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "列举记忆失败: "+err.Error(), false)
	}
	defer rows.Close()
	out := make([]Memory, 0)
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取记忆失败: "+err.Error(), false)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "记忆列表异常: "+err.Error(), false)
	}
	return out, nil
}

// ScopeEnabled 返回某个 scope 的注入开关（默认开启）。
func (s *Service) ScopeEnabled(ctx context.Context, scope Scope, workspaceID string) (bool, error) {
	normalized, wsKey, err := normalizeScope(scope, workspaceID)
	if err != nil {
		return false, err
	}
	var enabled int
	err = s.db.SQL().QueryRowContext(ctx,
		`SELECT enabled FROM memory_scope_settings WHERE scope = ? AND workspace_id = ?`,
		string(normalized), wsKey).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, contracts.NewAppError(contracts.CodeInternal, "读取记忆范围开关失败: "+err.Error(), false)
	}
	return enabled != 0, nil
}

// SetScopeEnabled 按 scope 关闭/打开注入。关闭只影响检索与注入，不删除条目。
func (s *Service) SetScopeEnabled(ctx context.Context, scope Scope, workspaceID string, enabled bool) error {
	normalized, wsKey, err := normalizeScope(scope, workspaceID)
	if err != nil {
		return err
	}
	v := 0
	if enabled {
		v = 1
	}
	if _, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO memory_scope_settings(scope, workspace_id, enabled, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(scope, workspace_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		string(normalized), wsKey, v, formatTime(s.now().UTC())); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "保存记忆范围开关失败: "+err.Error(), false)
	}
	return nil
}

// Export 生成可读的导出文档（不删除、不上传）。
func (s *Service) Export(ctx context.Context, workspaceID string) (ExportDocument, error) {
	items, err := s.List(ctx, workspaceID)
	if err != nil {
		return ExportDocument{}, err
	}
	return ExportDocument{
		SchemaVersion: 1,
		ExportedAt:    s.now().UTC(),
		WorkspaceID:   workspaceID,
		Items:         items,
	}, nil
}

// VerifyTrigram 用真实 SQLite 探测 FTS5 trigram（DB02 口径）：
// 编译选项 + 现有索引定义 + 一次中文子串命中。不可用就明确报错。
func (s *Service) VerifyTrigram(ctx context.Context) error {
	var enabled int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&enabled); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "无法查询 SQLite 编译选项: "+err.Error(), false)
	}
	if enabled != 1 {
		return contracts.NewAppError(contracts.CodeUnsupported, "当前 SQLite 构建未启用 FTS5，记忆检索不可用", false)
	}

	var schema sql.NullString
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'memory_fts'`).Scan(&schema); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"缺少 memory_fts 索引表（迁移未完成？）: "+err.Error(), false)
	}
	if !strings.Contains(strings.ToLower(schema.String), "tokenize") ||
		!strings.Contains(strings.ToLower(schema.String), "trigram") {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"memory_fts 未使用 trigram 分词器，拒绝以其它方式检索: "+schema.String, false)
	}

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "开启 trigram 探测事务失败: "+err.Error(), false)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS temp.memory_trigram_probe`); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "清理探测表失败: "+err.Error(), false)
	}
	if _, err := tx.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.memory_trigram_probe USING fts5(content, tokenize='trigram')`); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "FTS5 trigram 分词器不可用: "+err.Error(), false)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO temp.memory_trigram_probe(content) VALUES('数据库连接池上限为二十')`); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "trigram 探测写入失败: "+err.Error(), false)
	}
	var hits int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM temp.memory_trigram_probe WHERE memory_trigram_probe MATCH '连接池上'`).Scan(&hits); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "trigram 探测查询失败: "+err.Error(), false)
	}
	if hits == 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "trigram 未命中中文子串，视为不可用", false)
	}
	if err := tx.Rollback(); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "清理探测事务失败: "+err.Error(), false)
	}
	return nil
}

const selectMemoryColumns = `
	SELECT m.id, m.scope, COALESCE(m.workspace_id, ''), COALESCE(m.kind, 'fact'), m.content,
	       COALESCE(m.source_session_id, ''), m.status, m.created_at, COALESCE(m.updated_at, ''), m.expires_at
	FROM memories m`

// rowScanner 让 *sql.Row 与 *sql.Rows 共用同一套扫描逻辑。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemory(row rowScanner) (Memory, error) {
	var (
		m         Memory
		created   string
		updated   string
		expiresAt sql.NullString
	)
	if err := row.Scan(&m.ID, &m.Scope, &m.WorkspaceID, &m.Kind, &m.Content,
		&m.SourceSessionID, &m.Status, &created, &updated, &expiresAt); err != nil {
		return Memory{}, err
	}
	var err error
	if m.CreatedAt, err = parseStoredTime(created); err != nil {
		return Memory{}, err
	}
	if m.UpdatedAt, err = parseStoredTime(updated); err != nil {
		return Memory{}, err
	}
	if expiresAt.Valid && strings.TrimSpace(expiresAt.String) != "" {
		t, err := parseStoredTime(expiresAt.String)
		if err != nil {
			return Memory{}, err
		}
		m.ExpiresAt = &t
	}
	return m, nil
}

// normalizeScope 校验 scope 并归一 workspace 键：
// user 记忆的 workspace_id 恒为空，否则"用户偏好"会被绑在某个项目上。
func normalizeScope(scope Scope, workspaceID string) (Scope, string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	switch scope {
	case ScopeUser:
		return ScopeUser, "", nil
	case ScopeWorkspace:
		if workspaceID == "" {
			return "", "", contracts.NewAppError(contracts.CodeUnsupported, "项目级记忆必须绑定 workspaceId", false)
		}
		return ScopeWorkspace, workspaceID, nil
	default:
		return "", "", contracts.NewAppError(contracts.CodeUnsupported,
			"记忆 scope 只能是 user 或 workspace，收到 "+string(scope), false)
	}
}

func normalizeKind(kind string) (string, error) {
	switch strings.TrimSpace(kind) {
	case "":
		return KindFact, nil
	case KindFact, KindPreference, KindConvention, KindNote:
		return strings.TrimSpace(kind), nil
	default:
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"记忆 kind 只能是 fact/preference/convention/note，收到 "+kind, false)
	}
}

func newMemoryID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成记忆 ID 失败: "+err.Error(), false)
	}
	return "mem-" + hex.EncodeToString(buf), nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseStoredTime 兼容产品写入（RFC3339）与旧行/手工 SQL（datetime('now')）。
func parseStoredTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q", s)
}
