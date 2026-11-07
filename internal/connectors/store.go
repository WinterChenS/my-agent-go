package connectors

// 定义与审计的持久化。
//
// 定义放在 SQLite 里（迁移 003_connectors.sql），而不是另发明一份 JSON 配置：
//   - "配置里只有 credential ref"需要一个可被测试直接扫的落点，
//     数据库文件字节就是最直接的证据；
//   - revision 需要 CAS，文件覆盖写做不到"两个写者只有一个成功"。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// DefinitionStore 是连接器定义的读写接口。
//
// Put 的语义是 CAS：Revision 为 0 表示新建，否则必须与已存版本一致，
// 不一致返回 STALE_REVISION。并发编辑不能靠"后写者赢"。
type DefinitionStore interface {
	List(ctx context.Context) ([]Definition, error)
	Get(ctx context.Context, id string) (Definition, error)
	Put(ctx context.Context, def Definition) (Definition, error)
	Delete(ctx context.Context, id string) error
}

// SQLStore 用共享索引库承载连接器定义与审计。
type SQLStore struct {
	db *storage.DB
}

// NewSQLStore 绑定一个已迁移的数据库，并确认所需表存在。
//
// 表不存在时立刻失败：延迟到第一次写才报错会让连接器页面变成"加载中"。
func NewSQLStore(db *storage.DB) (*SQLStore, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "连接器存储缺少数据库句柄", false)
	}
	ctx := context.Background()
	for _, table := range []string{"connector_definitions", "connector_audit"} {
		var name string
		err := db.SQL().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			return nil, contracts.NewAppError(
				contracts.CodeInternal,
				"连接器存储缺少表 "+table+"（迁移 003_connectors.sql 未应用）: "+err.Error(),
				false,
			)
		}
	}
	return &SQLStore{db: db}, nil
}

// List 返回全部定义（按 ID 排序，保证输出稳定）。
func (s *SQLStore) List(ctx context.Context) ([]Definition, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, kind, display_name, credential_ref, enabled, capabilities,
		       allowed_resources, rate_limit, endpoints, revision, updated_at
		FROM connector_definitions ORDER BY id`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取连接器定义失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := []Definition{}
	for rows.Next() {
		def, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取连接器定义失败: "+err.Error(), false)
	}
	return out, nil
}

// Get 读取单条定义；不存在返回 NOT_FOUND。
func (s *SQLStore) Get(ctx context.Context, id string) (Definition, error) {
	row := s.db.SQL().QueryRowContext(ctx, `
		SELECT id, kind, display_name, credential_ref, enabled, capabilities,
		       allowed_resources, rate_limit, endpoints, revision, updated_at
		FROM connector_definitions WHERE id = ?`, strings.TrimSpace(id))
	def, err := scanDefinition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Definition{}, contracts.NewAppError(contracts.CodeNotFound, "连接器不存在: "+id, false)
	}
	return def, err
}

// Put 以 CAS 语义保存定义。
func (s *SQLStore) Put(ctx context.Context, def Definition) (Definition, error) {
	if err := def.Validate(); err != nil {
		return Definition{}, err
	}
	now := time.Now().UTC()
	stored, err := s.Get(ctx, def.ID)
	created := false
	switch {
	case err == nil:
		if def.Revision != stored.Revision {
			return Definition{}, contracts.NewAppError(
				contracts.CodeStaleRevision,
				"连接器定义已被修改（期望版本 "+itoa(stored.Revision)+"，提交 "+itoa(def.Revision)+"）",
				false,
			)
		}
		def.Revision = stored.Revision + 1
	case isNotFound(err):
		if def.Revision != 0 {
			return Definition{}, contracts.NewAppError(contracts.CodeStaleRevision, "连接器定义不存在，无法按旧版本保存", false)
		}
		def.Revision = 1
		created = true
	default:
		return Definition{}, err
	}

	caps, _ := json.Marshal(def.Capabilities)
	resources, _ := json.Marshal(def.AllowedResources)
	rate, _ := json.Marshal(def.RateLimit)
	endpoints, _ := json.Marshal(def.Endpoints)
	def.UpdatedAt = now

	var execErr error
	if created {
		_, execErr = s.db.SQL().ExecContext(ctx, `
			INSERT INTO connector_definitions(id, kind, display_name, credential_ref, enabled,
				capabilities, allowed_resources, rate_limit, endpoints, revision, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			def.ID, string(def.Kind), def.DisplayName, def.CredentialRef, boolInt(def.Enabled),
			string(caps), string(resources), string(rate), string(endpoints),
			int64(def.Revision), now.Format(time.RFC3339Nano))
	} else {
		res, uerr := s.db.SQL().ExecContext(ctx, `
			UPDATE connector_definitions SET kind = ?, display_name = ?, credential_ref = ?,
				enabled = ?, capabilities = ?, allowed_resources = ?, rate_limit = ?,
				endpoints = ?, revision = ?, updated_at = ?
			WHERE id = ? AND revision = ?`,
			string(def.Kind), def.DisplayName, def.CredentialRef, boolInt(def.Enabled),
			string(caps), string(resources), string(rate), string(endpoints),
			int64(def.Revision), now.Format(time.RFC3339Nano), def.ID, int64(stored.Revision))
		execErr = uerr
		if execErr == nil {
			if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
				execErr = contracts.NewAppError(contracts.CodeStaleRevision, "连接器定义已被并发修改", false)
			}
		}
	}
	if execErr != nil {
		return Definition{}, contracts.NewAppError(contracts.CodeInternal, "保存连接器定义失败: "+execErr.Error(), false)
	}
	return def, nil
}

// Delete 删除定义（幂等）。
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.SQL().ExecContext(ctx, `DELETE FROM connector_definitions WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "删除连接器定义失败: "+err.Error(), false)
	}
	return nil
}

// Append 追加一条审计。
func (s *SQLStore) Append(ctx context.Context, e AuditEntry) error {
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO connector_audit(at, connector_id, action_id, kind, decision, outcome, reason,
			credential_ref, idempotency_key, approval_id, session_id, run_id, tool_name, detail)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.UTC().Format(time.RFC3339Nano), e.ConnectorID, e.ActionID, string(e.Kind), e.Decision,
		e.Outcome, e.Reason, e.CredentialRef, e.IdempotencyKey, e.ApprovalID, e.SessionID,
		e.RunID, e.ToolName, e.Detail)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写连接器审计失败: "+err.Error(), false)
	}
	return nil
}

// ListAudits 读回审计（按追加顺序；limit<=0 表示全部）。
func (s *SQLStore) ListAudits(ctx context.Context, connectorID string, limit int) ([]AuditEntry, error) {
	query := `SELECT seq, at, connector_id, action_id, kind, decision, outcome, reason,
		credential_ref, idempotency_key, approval_id, session_id, run_id, tool_name, detail
		FROM connector_audit`
	args := []any{}
	if strings.TrimSpace(connectorID) != "" {
		query += ` WHERE connector_id = ?`
		args = append(args, connectorID)
	}
	query += ` ORDER BY seq`
	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取连接器审计失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var (
			e         AuditEntry
			at        string
			kind      string
			credRef   sql.NullString
			idem      sql.NullString
			approval  sql.NullString
			sessionID sql.NullString
			runID     sql.NullString
			toolName  sql.NullString
			reason    sql.NullString
			detail    sql.NullString
		)
		if err := rows.Scan(&e.Seq, &at, &e.ConnectorID, &e.ActionID, &kind, &e.Decision, &e.Outcome,
			&reason, &credRef, &idem, &approval, &sessionID, &runID, &toolName, &detail); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "解析连接器审计失败: "+err.Error(), false)
		}
		e.Kind = Kind(kind)
		e.Reason = reason.String
		e.CredentialRef = credRef.String
		e.IdempotencyKey = idem.String
		e.ApprovalID = approval.String
		e.SessionID = sessionID.String
		e.RunID = runID.String
		e.ToolName = toolName.String
		e.Detail = detail.String
		if parsed, perr := time.Parse(time.RFC3339Nano, at); perr == nil {
			e.At = parsed
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取连接器审计失败: "+err.Error(), false)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// rowScanner 让 List/Get 共用同一段扫描逻辑。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDefinition(row rowScanner) (Definition, error) {
	var (
		def       Definition
		kind      string
		caps      string
		resources string
		rate      string
		endpoints string
		enabled   int
		revision  int64
		updatedAt string
	)
	if err := row.Scan(&def.ID, &kind, &def.DisplayName, &def.CredentialRef, &enabled,
		&caps, &resources, &rate, &endpoints, &revision, &updatedAt); err != nil {
		return Definition{}, err
	}
	def.Kind = Kind(kind)
	def.Enabled = enabled != 0
	def.Revision = uint64(revision)
	if strings.TrimSpace(caps) != "" {
		if err := json.Unmarshal([]byte(caps), &def.Capabilities); err != nil {
			return Definition{}, contracts.NewAppError(contracts.CodeInternal, "连接器能力字段损坏: "+err.Error(), false)
		}
	}
	if strings.TrimSpace(resources) != "" {
		if err := json.Unmarshal([]byte(resources), &def.AllowedResources); err != nil {
			return Definition{}, contracts.NewAppError(contracts.CodeInternal, "连接器资源范围字段损坏: "+err.Error(), false)
		}
	}
	if strings.TrimSpace(rate) != "" {
		if err := json.Unmarshal([]byte(rate), &def.RateLimit); err != nil {
			return Definition{}, contracts.NewAppError(contracts.CodeInternal, "连接器限流字段损坏: "+err.Error(), false)
		}
	}
	if strings.TrimSpace(endpoints) != "" {
		if err := json.Unmarshal([]byte(endpoints), &def.Endpoints); err != nil {
			return Definition{}, contracts.NewAppError(contracts.CodeInternal, "连接器端点字段损坏: "+err.Error(), false)
		}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		def.UpdatedAt = parsed
	}
	return def, nil
}

func isNotFound(err error) bool {
	var appErr *contracts.AppError
	return errors.As(err, &appErr) && appErr != nil && appErr.Code == contracts.CodeNotFound
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// DefaultMailDefinition 返回一条尚未配置的 mail 定义。
//
// 默认不启用：没配端点/没授权之前，"已启用但什么也做不了"会让 UI 显示成一个
// 坏掉的功能，而不是"待配置"。
func DefaultMailDefinition(id, credentialRef string) Definition {
	return Definition{
		ID:            id,
		Kind:          KindMail,
		DisplayName:   "邮箱",
		CredentialRef: credentialRef,
		Enabled:       false,
		Capabilities:  []Capability{CapabilityMailRead, CapabilityMailSend},
		RateLimit:     RateLimit{Requests: 30, PerSeconds: 60},
		Revision:      0,
	}
}
