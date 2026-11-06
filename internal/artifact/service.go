// Package artifact 是 C07 的产物域：登记、核对、预览、导出工作区里产出的文件。
//
// 权威边界（基线 §5 / 001_initial.sql 头部声明）
//   - 产物**内容**的权威是工作区里的那个文件本身；SQLite 只存索引
//     （定位路径 + 登记时的 sha256/size + 媒体类型 + 归属 run/会话），
//     不复制字节，也不做第二份存储。
//   - 因此"登记的 hash"不等于"现在的内容"：GetArtifact 每次都重新打开文件、
//     重新算 hash 与大小，并把索引值与实测值分开报告（verified / drifted /
//     missing）。拿旧 hash 冒充现状是这一层最不该犯的错——它会让用户以为
//     自己看到的就是当时那一份。
//
// 边界（ART01）
//   - 只登记工作区内、逐段都不是链接、确实是普通文件的路径；
//     `..`、UNC/设备路径、驱动器相对路径、备用数据流、越界绝对路径、
//     符号链接/目录联接、消失的文件、目录/管道一律结构化拒绝，
//     且拒绝发生在读字节与写索引之前。
//   - 读侧不提供"按任意本机路径打开"的入口：ExportArtifact/PreviewArtifact
//     只接受产物 id，路径来自索引行，并在每次读取时重新证明"仍在登记时那个
//     工作区根内"。
//
// 不做的事：这里不是 OS 沙箱。工具可以绕过本包直接写工作区；本包保证的是
// "被登记的产物是它声称的那一份"，而不是"工作区里只有登记过的文件"。
package artifact

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

// 核对结论（Artifact.Verification.Status）。
const (
	// StatusVerified：磁盘上的内容与登记时完全一致（size 与 sha256 都对上）。
	StatusVerified = "verified"
	// StatusDrifted：文件还在，但内容/大小与登记时不符，或路径现在经过链接/越界。
	StatusDrifted = "drifted"
	// StatusMissing：文件（或它的工作区根）已经不可用，一个字节都没读到。
	StatusMissing = "missing"
)

const (
	// DefaultMaxArtifactBytes 是单个产物的大小上限。导入要完整读一遍算 hash，
	// 没有上限时一个异常大的文件就能把进程撑爆；超限是拒绝，不是截断。
	DefaultMaxArtifactBytes int64 = 256 << 20

	// DefaultListLimit / MaxListLimit 是列举条数的默认值与上限。
	DefaultListLimit = 100
	MaxListLimit     = 500

	// tsLayout 是索引时间列的固定宽度 UTC 格式。固定宽度让 TEXT 比较
	// 与时间顺序一致（RFC3339Nano 会裁掉尾随 0）。
	tsLayout = "2006-01-02T15:04:05.000000000Z07:00"
)

// Options 是产物服务的装配参数。
type Options struct {
	// ExportRoot 是导出根（生产为 <appRoot>/exports）。导出目标必须落在它里面。
	ExportRoot string

	// WorkspaceRoot 把工作区 id 解析成真实根路径。为 nil 时读
	// workspaces.root_path（由 C06 的会话目录 / RUN07 的索引写入）。
	WorkspaceRoot func(contracts.WorkspaceID) (string, error)

	// MaxBytes 覆盖单产物大小上限（<=0 用 DefaultMaxArtifactBytes）。
	MaxBytes int64

	// Now 注入时钟，仅用于测试确定性。
	Now func() time.Time
}

// Artifact 是一条产物索引行。
//
// CanonicalPath 是**定位信息**，不是内容证明：它说明"当时登记的是这个路径"。
// 当前内容是否还对得上，看 Verification（只有 GetArtifact 等"读"路径才填）。
type Artifact struct {
	ID            string                `json:"id"`
	WorkspaceID   contracts.WorkspaceID `json:"workspaceId"`
	RunID         contracts.RunID       `json:"runId,omitempty"`
	SessionID     contracts.SessionID   `json:"sessionId,omitempty"`
	DisplayName   string                `json:"displayName"`
	CanonicalPath string                `json:"canonicalPath"`
	SHA256        string                `json:"sha256"`
	Size          int64                 `json:"size"`
	MediaType     string                `json:"mediaType"`
	CreatedAt     time.Time             `json:"createdAt"`
	UpdatedAt     time.Time             `json:"updatedAt"`
	Verification  *Verification         `json:"verification,omitempty"`
}

// Verification 是一次磁盘核对的结果。
//
// 三个字段必须分开：SHA256 是**登记时**的 hash，ObservedSHA256 是**这次读到的**
// hash。 drifted 时两者都非空且不相等；missing 时 ObservedSHA256 为空
// （一个字节都没读到，不能编一个值出来）。
type Verification struct {
	Status         string    `json:"status"`
	ObservedSHA256 string    `json:"observedSha256,omitempty"`
	ObservedSize   *int64    `json:"observedSize,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	CheckedAt      time.Time `json:"checkedAt"`
}

// Query 是产物列举条件。零值表示"全部工作区的最近产物"。
type Query struct {
	WorkspaceID contracts.WorkspaceID
	RunID       contracts.RunID
	SessionID   contracts.SessionID
	Limit       int
}

// ImportRequest 是一次产物登记。
//
// Path 由用户/工具给出，Go 侧重新解析：它只被当作"候选定位串"，不是可信路径。
// RunID/SessionID 是归属信息（可为空），用于按 run 列产物与诊断定位。
type ImportRequest struct {
	Path        string                `json:"path"`
	WorkspaceID contracts.WorkspaceID `json:"workspaceId"`
	RunID       contracts.RunID       `json:"runId"`
	SessionID   contracts.SessionID   `json:"sessionId"`
	DisplayName string                `json:"displayName"`
}

// ExportResult 是一次产物导出的结果。
type ExportResult struct {
	ArtifactID string    `json:"artifactId"`
	Path       string    `json:"path"`
	Bytes      int64     `json:"bytes"`
	SHA256     string    `json:"sha256"`
	WrittenAt  time.Time `json:"writtenAt"`
}

// Service 是产物服务。并发安全（状态全在 SQLite 与文件系统里）。
type Service struct {
	db          *storage.DB
	exportRoot  string
	exportGuard *policy.PathGuard
	workspaceFn func(contracts.WorkspaceID) (string, error)
	now         func() time.Time
	maxBytes    int64
}

// New 构造产物服务。数据库由 storage 的迁移负责建表（007），本包不建表。
func New(db *storage.DB, opts Options) (*Service, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "产物服务缺少数据库句柄", false)
	}
	root := strings.TrimSpace(opts.ExportRoot)
	if root == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "产物导出根目录为空", false)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "产物导出根无法绝对化: "+err.Error(), false)
	}
	abs = filepath.Clean(abs)
	guard, err := ensureRoot(abs)
	if err != nil {
		return nil, err
	}
	svc := &Service{
		db:          db,
		exportRoot:  abs,
		exportGuard: guard,
		workspaceFn: opts.WorkspaceRoot,
		now:         opts.Now,
		maxBytes:    opts.MaxBytes,
	}
	if svc.now == nil {
		svc.now = time.Now
	}
	if svc.maxBytes <= 0 {
		svc.maxBytes = DefaultMaxArtifactBytes
	}
	return svc, nil
}

// ensureRoot 保证根目录存在且是一个真实目录，并给出落在它里面的路径守卫。
func ensureRoot(abs string) (*policy.PathGuard, error) {
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建目录失败: "+abs+" : "+err.Error(), false)
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return nil, contracts.NewAppError(contracts.CodeInternal, "不是目录: "+abs, false)
	}
	guard, err := policy.NewPathGuard(abs)
	if err != nil {
		return nil, err
	}
	return guard, nil
}

// SetClock 注入时钟，仅用于测试确定性。
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// ExportRoot 返回导出根（只读定位，供绑定层与验收断言使用）。
func (s *Service) ExportRoot() string { return s.exportRoot }

const artifactColumns = `id, workspace_id, run_id, session_id, display_name,
	canonical_path, sha256, size_bytes, media_type, created_at, updated_at`

// GetArtifact 读一条产物，并**重新核对磁盘**。
//
// 这是本包的核心承诺：返回的 Verification 说明这次读到的内容与登记时是否一致。
// 文件被改 → drifted（同时给索引 hash 与实测 hash）；被删 → missing；
// 路径现在经过链接或越界 → drifted（原因写明），不会跟随链接去读别处的文件。
func (s *Service) GetArtifact(ctx context.Context, id string) (Artifact, error) {
	if s == nil {
		return Artifact{}, contracts.NewAppError(contracts.CodeInternal, "产物服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	item, err := s.load(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	ver := s.verify(ctx, item)
	item.Verification = &ver
	return item, nil
}

// ListArtifacts 列出产物索引行（**不做磁盘核对**）。
//
// 不核对就不填 Verification：列表说"登记过这一份"，而不是"现在还是这一份"。
// 需要当前事实的调用方必须逐条 GetArtifact。
func (s *Service) ListArtifacts(ctx context.Context, q Query) ([]Artifact, error) {
	if s == nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "产物服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	where := []string{"1 = 1"}
	args := []any{}
	if id := strings.TrimSpace(string(q.WorkspaceID)); id != "" {
		where = append(where, "workspace_id = ?")
		args = append(args, id)
	}
	if id := strings.TrimSpace(string(q.RunID)); id != "" {
		where = append(where, "run_id = ?")
		args = append(args, id)
	}
	if id := strings.TrimSpace(string(q.SessionID)); id != "" {
		where = append(where, "session_id = ?")
		args = append(args, id)
	}
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE `+strings.Join(where, " AND ")+
			` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取产物索引失败: "+err.Error(), true)
	}
	defer rows.Close()

	out := make([]Artifact, 0, limit)
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "产物索引结果异常: "+err.Error(), true)
	}
	return out, nil
}

// ListRunArtifacts 列出一个 run 登记的产物。
//
// 与 ListArtifacts 一样不做磁盘核对：run 详情页要显示"这一轮产出了哪些文件"，
// 那些文件现在还在不在，由每一条的 GetArtifact 回答。
func (s *Service) ListRunArtifacts(ctx context.Context, runID string) ([]Artifact, error) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 run id，无法列出产物", false)
	}
	return s.ListArtifacts(ctx, Query{RunID: contracts.RunID(id)})
}

// load 读一条索引行（不核对磁盘）。
func (s *Service) load(ctx context.Context, id string) (Artifact, error) {
	key := strings.TrimSpace(id)
	if key == "" {
		return Artifact{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少产物 id", false)
	}
	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE id = ?`, key)
	item, err := scanArtifact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, contracts.NewAppError(contracts.CodeNotFound, "产物不存在: "+key, false)
	}
	if err != nil {
		return Artifact{}, err
	}
	return item, nil
}

func scanArtifact(sc interface{ Scan(dest ...any) error }) (Artifact, error) {
	var (
		item                 Artifact
		id, workspace, run   string
		session              string
		display, canonical   string
		sha, mediaType       string
		size                 int64
		createdAt, updatedAt string
	)
	if err := sc.Scan(&id, &workspace, &run, &session, &display, &canonical, &sha,
		&size, &mediaType, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, err
		}
		return Artifact{}, contracts.NewAppError(contracts.CodeInternal, "解析产物行失败: "+err.Error(), false)
	}
	created, err := parseTS(createdAt)
	if err != nil {
		return Artifact{}, contracts.NewAppError(contracts.CodeInternal,
			"产物 created_at 无法解析（疑似被外部改写）: "+id+" : "+err.Error(), false)
	}
	updated := created
	if strings.TrimSpace(updatedAt) != "" {
		parsed, uerr := parseTS(updatedAt)
		if uerr != nil {
			return Artifact{}, contracts.NewAppError(contracts.CodeInternal,
				"产物 updated_at 无法解析（疑似被外部改写）: "+id+" : "+uerr.Error(), false)
		}
		updated = parsed
	}
	item = Artifact{
		ID:            id,
		WorkspaceID:   contracts.WorkspaceID(workspace),
		RunID:         contracts.RunID(run),
		SessionID:     contracts.SessionID(session),
		DisplayName:   display,
		CanonicalPath: canonical,
		SHA256:        sha,
		Size:          size,
		MediaType:     mediaType,
		CreatedAt:     created,
		UpdatedAt:     updated,
	}
	return item, nil
}

func parseTS(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
}

func formatTS(t time.Time) string { return t.UTC().Format(tsLayout) }

// workspaceRootFor 解析工作区真实根路径。
//
// 优先用装配时注入的解析器（绑定层用工作区服务实现），否则读索引行——
// workspaces.root_path 由会话目录与 run 索引写入，是同一份事实的落库副本。
func (s *Service) workspaceRootFor(ctx context.Context, id contracts.WorkspaceID) (string, error) {
	key := strings.TrimSpace(string(id))
	if key == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "缺少工作区 id，无法判定产物范围", false)
	}
	if s.workspaceFn != nil {
		return s.workspaceFn(id)
	}
	var root string
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT root_path FROM workspaces WHERE id = ?`, key).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+key, false)
	}
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "读取工作区根失败: "+err.Error(), true)
	}
	if strings.TrimSpace(root) == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"工作区 "+key+" 没有登记根路径，无法判定产物范围", false)
	}
	return root, nil
}

// pathKey 是路径唯一性判定用的折叠键。
//
// Windows 与默认大小写不敏感的 macOS 上，同一个文件可以有多种大小写写法；
// 只按原样比较会让"重新导入同一个文件"变成两条索引行。
func pathKey(p string) string {
	clean := filepath.Clean(p)
	if runtimeIsCaseInsensitive() {
		return strings.ToLower(clean)
	}
	return clean
}
