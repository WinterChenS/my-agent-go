package browser

// B06：下载管理（BR17）。
//
// 基线口径是"下载管理"，而"管理"的核心不是把文件存下来，而是三条边界：
//  1. **用户没选目标之前，不往任何用户目录/工作区目录写字节**。下载先落到本应用
//     受管的下载根里的 staging（`<root>/staging/<id>.part`），那既不是用户目录也不是
//     工作区目录；证据里用"工作区树 + 家目录树前后一致"来证明。
//  2. **确认目标后才产生最终文件，且 hash 必须对得上**。写入用同目录临时文件 +
//     rename，写完**重新读一遍磁盘**算 sha256 与 staging 登记值比较；不一致就把
//     最终文件删掉并返回 VERIFICATION_FAILED——不允许"看起来保存成功"的半份文件。
//  3. **取消就没有最终文件**。取消时删除 staging 并置 canceled；saved 之后不允许
//     "取消"（那已经是一个用户可见的文件，需要显式删除语义，不在本轮实现）。
//
// 还有两条与产品安全直接相关的规则：
//   - 目标路径必须落在该 workspace 的工作区根内。网页/模型不能把文件写到任意本机
//     路径；同时"落盘即登记为产物（C07）"也要求文件在工作区内。
//   - 建议文件名必须清理：`../../etc/passwd`、`C:\Windows\x.exe`、Windows 设备名
//     (`con.txt`) 之类都不能变成真实路径。清理在**下载开始时**做，作业里存的是
//     清理后的名字，用户看到的也是这个名字。
//
// 明确不做（不伪装完成）：程序化上传。file input 只能由用户通过原生文件选择器手动
// 指定；本轮没有任何"注入文件路径"的入口。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// 下载作业状态。
//
// 取值进数据库列与 JSON，改名等于改协议（前端按 InlineState 分支）。
const (
	// DownloadAwaitingDestination：内容已在 staging，等用户选目标。此时**没有**
	// 最终文件，也没有任何产物行。
	DownloadAwaitingDestination DownloadState = "awaiting_destination"
	// DownloadSaved：最终文件已写入并核对，产物已登记。
	DownloadSaved DownloadState = "saved"
	// DownloadCanceled：用户取消；staging 与最终文件都不存在。
	DownloadCanceled DownloadState = "canceled"
	// DownloadFailed：保存过程中发现不一致（hash/登记失败）或内容抓取失败。
	DownloadFailed DownloadState = "failed"
)

// 下载专用错误码（contracts 尚未登记的按 B05 的惯例先在本包定义，
// 收进 contracts 时必须保持字符串值不变）。
const (
	// CodeDownloadFailed 表示下载内容没有取到（HTTP 非 2xx、网络错误、超限）。
	CodeDownloadFailed = "DOWNLOAD_FAILED"
	// CodeDownloadState 表示当前状态不允许该操作（例如对已保存的作业再取消）。
	CodeDownloadState = "DOWNLOAD_STATE_CONFLICT"
)

const (
	// DefaultMaxDownloadBytes 是单次下载的大小上限（超过即失败，不截断）。
	DefaultMaxDownloadBytes int64 = 64 << 20
	// MaxSuggestedNameLength 是清理后文件名的长度上限（含扩展名）。
	MaxSuggestedNameLength = 120
)

// DownloadState 是一个下载作业的档位。
type DownloadState string

// AllDownloadStates 是全部状态（测试与前端枚举共用）。
var AllDownloadStates = []DownloadState{
	DownloadAwaitingDestination, DownloadSaved, DownloadCanceled, DownloadFailed,
}

// DownloadJob 是一个下载作业。
//
// SHA256 是**内容身份**：staging 时是已下载字节的 hash，保存后仍然必须是同一个值
// （保存路径会重新读盘核对）。ArtifactID 只有 saved 才有值。
type DownloadJob struct {
	ID            string                `json:"id"`
	TabID         TabID                 `json:"tabId,omitempty"`
	URL           string                `json:"url"`
	SuggestedName string                `json:"suggestedName"`
	State         DownloadState         `json:"state"`
	Size          int64                 `json:"size"`
	ArtifactID    string                `json:"artifactId,omitempty"`
	SHA256        string                `json:"sha256,omitempty"`
	FinalPath     string                `json:"finalPath,omitempty"`
	SessionID     contracts.SessionID   `json:"sessionId"`
	WorkspaceID   contracts.WorkspaceID `json:"workspaceId"`
	ErrorCode     string                `json:"errorCode,omitempty"`
	ErrorMessage  string                `json:"errorMessage,omitempty"`
}

// DownloadRequest 是原生宿主报上来的下载开始事件。
//
// SuggestedName 来自引擎（Content-Disposition / 链接 download 属性），属于不可信
// 输入：它只被当作"候选名"，Go 侧重新清理后才写进作业与磁盘路径。
type DownloadRequest struct {
	TabID         TabID  `json:"tabId,omitempty"`
	URL           string `json:"url"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

// ArtifactRequest 是一次"把已落盘文件登记为产物"的请求。
//
// 刻意不直接依赖 internal/artifact：下载域只需要这一个动作，适配器放在装配层
// （应用或测试），浏览器包不必把产物域的导出/预览/媒体类型全拖进依赖图。
type ArtifactRequest struct {
	Path        string                `json:"path"`
	WorkspaceID contracts.WorkspaceID `json:"workspaceId"`
	RunID       contracts.RunID       `json:"runId,omitempty"`
	SessionID   contracts.SessionID   `json:"sessionId,omitempty"`
	DisplayName string                `json:"displayName,omitempty"`
}

// ArtifactRef 是登记结果的摘要（C07 的 artifact.Service 能给出这三个值）。
type ArtifactRef struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ArtifactRegistrar 是产物登记的最小接口。
type ArtifactRegistrar interface {
	RegisterFile(ctx context.Context, req ArtifactRequest) (ArtifactRef, error)
}

// CookieProvider 提供某个 workspace 的登录态（下载是**非引擎** HTTP，需要自己带）。
type CookieProvider interface {
	CookieHeader(ctx context.Context, ws contracts.WorkspaceID) (string, error)
}

// DownloadOptions 是 DownloadManager 的装配参数。
type DownloadOptions struct {
	// Root 是本应用受管的下载根；staging 在 <Root>/staging 下，不是用户目录。
	Root string
	// DB 是作业状态的权威存储（重启后仍可解释"那次下载怎么了"）。
	DB *storage.DB
	// Cookies 提供 workspace 登录态；nil 表示不带 cookie。
	Cookies CookieProvider
	// Artifacts 负责把最终文件登记成产物；nil 表示保存后无法登记——此时保存会
	// 明确失败（不生成"有文件但不可列举"的产物），而不是静默跳过。
	Artifacts ArtifactRegistrar
	// WorkspaceRoot 把 workspace 解析成工作区根；目标路径必须落在它里面。
	WorkspaceRoot func(contracts.WorkspaceID) (string, error)
	// HTTPClient 可注入（测试用带 cookie 的 client）；nil 用带超时的默认 client。
	HTTPClient *http.Client
	// MaxBytes 覆盖单次下载上限（<=0 用 DefaultMaxDownloadBytes）。
	MaxBytes int64
	// Now 注入时钟。
	Now func() time.Time
}

// DownloadManager 是下载域的执行面。并发安全。
type DownloadManager struct {
	root      string
	db        *storage.DB
	cookies   CookieProvider
	artifacts ArtifactRegistrar
	wsRoot    func(contracts.WorkspaceID) (string, error)
	client    *http.Client
	maxBytes  int64
	now       func() time.Time
}

// NewDownloadManager 校验装配参数。
//
// Artifacts/WorkspaceRoot 缺失直接判配置错误：BR17 要求"保存后登记为产物"，
// 没有登记通道就等于这一条不可能满足——那样应该在启动时报错，而不是在用户
// 点"保存"之后才发现文件不可列举。
func NewDownloadManager(opts DownloadOptions) (*DownloadManager, error) {
	if opts.DB == nil {
		return nil, appErr(contracts.CodeConfigMissing, "下载管理缺少数据库句柄：作业状态必须可查询")
	}
	if opts.Artifacts == nil {
		return nil, appErr(contracts.CodeConfigMissing,
			"下载管理缺少产物登记通道：保存后必须能登记为 artifact")
	}
	if opts.WorkspaceRoot == nil {
		return nil, appErr(contracts.CodeConfigMissing,
			"下载管理缺少工作区根解析：目标路径必须限定在工作区内")
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, appErr(contracts.CodeConfigMissing, "下载受管根为空")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "解析下载根失败: "+err.Error())
	}
	abs = filepath.Clean(abs)
	if err := os.MkdirAll(filepath.Join(abs, "staging"), 0o700); err != nil {
		return nil, appErr(contracts.CodeInternal, "创建下载根失败: "+abs+" : "+err.Error())
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDownloadBytes
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &DownloadManager{
		root: abs, db: opts.DB, cookies: opts.Cookies, artifacts: opts.Artifacts,
		wsRoot: opts.WorkspaceRoot, client: client, maxBytes: maxBytes, now: now,
	}, nil
}

// Root 返回受管下载根（证据/诊断用）。
func (m *DownloadManager) Root() string {
	if m == nil {
		return ""
	}
	return m.root
}

// Start 抓取一次下载，落到 staging 并进入 awaiting_destination。
//
// 关键顺序：先抓取（只写受管 staging）→ 再登记作业。反过来的话，一个抓取失败的
// 作业会以 "awaiting_destination" 出现在 UI 里，用户点保存却得到一个空文件。
func (m *DownloadManager) Start(ctx context.Context, exec contracts.ExecutionContext, req DownloadRequest) (DownloadJob, error) {
	if m == nil || m.db == nil {
		return DownloadJob{}, appErr(contracts.CodeInternal, "下载管理未初始化")
	}
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return DownloadJob{}, appErr(contracts.CodeUnauthorized, "缺少会话身份：下载必须绑定会话")
	}
	if strings.TrimSpace(string(exec.WorkspaceID)) == "" {
		return DownloadJob{}, appErr(contracts.CodeUnauthorized, "缺少工作区归属：下载必须绑定 workspace")
	}
	raw := strings.TrimSpace(req.URL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return DownloadJob{}, appErr(contracts.CodeUnsupported, "下载只接受 http(s) URL: "+raw)
	}

	cookieHeader := ""
	if m.cookies != nil {
		header, cerr := m.cookies.CookieHeader(ctx, exec.WorkspaceID)
		if cerr != nil {
			return DownloadJob{}, cerr
		}
		cookieHeader = header
	}

	id, err := newDownloadID()
	if err != nil {
		return DownloadJob{}, err
	}
	staged := m.stagedPath(id)
	// 抓取：只写受管 staging。用户目录/工作区目录此刻一个字节都不会被写。
	meta, err := m.fetch(ctx, raw, cookieHeader, staged)
	if err != nil {
		_ = os.Remove(staged)
		return DownloadJob{}, err
	}

	suggested := SanitizeSuggestedName(firstNonEmpty(req.SuggestedName, meta.filename, path.Base(parsed.Path)))
	now := m.ts()
	job := DownloadJob{
		ID: id, TabID: req.TabID, URL: raw, SuggestedName: suggested,
		State: DownloadAwaitingDestination, Size: meta.size, SHA256: meta.sha256,
		SessionID: exec.SessionID, WorkspaceID: exec.WorkspaceID,
	}
	if _, err := m.db.SQL().ExecContext(ctx, `
		INSERT INTO browser_downloads(id, session_id, workspace_id, tab_id, url, suggested_name,
		                              state, size_bytes, sha256, staged_path, final_path,
		                              artifact_id, error_code, error_message, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', ?, ?)`,
		job.ID, string(job.SessionID), string(job.WorkspaceID), string(job.TabID), job.URL,
		job.SuggestedName, string(job.State), job.Size, job.SHA256, staged, now, now); err != nil {
		_ = os.Remove(staged)
		return DownloadJob{}, appErr(contracts.CodeInternal, "写入下载作业失败: "+err.Error())
	}
	return job, nil
}

// SaveDownload 把 staging 的内容写到用户确认的目标，核对 hash，并登记产物。
//
// 目标必须是绝对路径且落在该 workspace 的工作区根内：这是"网页不能写任意本机
// 路径"的实际执行点，也是"落盘即登记产物"的前提。
func (m *DownloadManager) SaveDownload(ctx context.Context, exec contracts.ExecutionContext, downloadID, destination string) (DownloadJob, error) {
	job, err := m.load(ctx, exec, downloadID)
	if err != nil {
		return DownloadJob{}, err
	}
	if job.State != DownloadAwaitingDestination {
		return DownloadJob{}, appErr(CodeDownloadState, fmt.Sprintf(
			"下载 %s 当前状态 %q：只有 %q 可以保存", job.ID, job.State, DownloadAwaitingDestination))
	}
	if strings.TrimSpace(destination) == "" {
		return DownloadJob{}, appErr(contracts.CodeInvalidInput, "保存下载需要目标路径")
	}
	wsRoot, err := m.workspaceRoot(exec)
	if err != nil {
		return DownloadJob{}, err
	}
	dest, err := resolveDownloadTarget(wsRoot, destination)
	if err != nil {
		return DownloadJob{}, err
	}

	staged := m.stagedPath(job.ID)
	if size, sha, herr := hashFile(staged); herr != nil {
		return DownloadJob{}, m.fail(ctx, job, CodeDownloadFailed,
			"staging 内容不可读: "+herr.Error())
	} else if size != job.Size || sha != job.SHA256 {
		return DownloadJob{}, m.fail(ctx, job, contracts.CodeVerificationFailed, fmt.Sprintf(
			"staging 内容与开始下载时不一致（size %d→%d, sha %s→%s）：拒绝保存", job.Size, size, job.SHA256, sha))
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return DownloadJob{}, appErr(contracts.CodeUnauthorized, "创建目标目录失败: "+err.Error())
	}
	// 同目录临时文件 + rename：中途失败不会留下半个"看起来对"的文件。
	tmp := dest + ".anvil-tmp"
	if err := copyFile(staged, tmp); err != nil {
		_ = os.Remove(tmp)
		return DownloadJob{}, m.fail(ctx, job, CodeDownloadFailed, "写入目标失败: "+err.Error())
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return DownloadJob{}, m.fail(ctx, job, CodeDownloadFailed, "替换目标失败: "+err.Error())
	}

	// 写完**重新读一遍磁盘**：核对对象是磁盘上的最终文件，不是我们以为写进去的字节。
	finalSize, finalSHA, err := hashFile(dest)
	switch {
	case err != nil:
		_ = os.Remove(dest)
		return DownloadJob{}, m.fail(ctx, job, CodeDownloadFailed, "读取目标失败: "+err.Error())
	case finalSHA != job.SHA256 || finalSize != job.Size:
		_ = os.Remove(dest)
		return DownloadJob{}, m.fail(ctx, job, contracts.CodeVerificationFailed, fmt.Sprintf(
			"目标文件 hash 不匹配（size %d→%d, sha %s→%s）：已删除目标文件", job.Size, finalSize, job.SHA256, finalSHA))
	}

	ref, err := m.artifacts.RegisterFile(ctx, ArtifactRequest{
		Path: dest, WorkspaceID: job.WorkspaceID, RunID: exec.RunID,
		SessionID: job.SessionID, DisplayName: job.SuggestedName,
	})
	if err != nil {
		// 登记失败时不留"有文件但列不出来"的产物：删文件、标失败、把原因交出去。
		_ = os.Remove(dest)
		return DownloadJob{}, m.fail(ctx, job, contracts.CodeVerificationFailed,
			"登记产物失败（已删除目标文件）: "+err.Error())
	}
	if ref.SHA256 != job.SHA256 || ref.Size != job.Size {
		_ = os.Remove(dest)
		return DownloadJob{}, m.fail(ctx, job, contracts.CodeVerificationFailed, fmt.Sprintf(
			"产物登记值与内容不一致（artifact sha %s size %d）：已删除目标文件", ref.SHA256, ref.Size))
	}

	job.State = DownloadSaved
	job.FinalPath = dest
	job.ArtifactID = ref.ID
	job.SHA256 = finalSHA
	job.Size = finalSize
	if err := m.update(ctx, job); err != nil {
		return DownloadJob{}, err
	}
	_ = os.Remove(staged)
	return job, nil
}

// CancelDownload 取消一个还没保存的下载。
//
// 已保存的作业不能"取消"：那已经是一个用户可见的文件，删除是另一个动作
// （需要显式确认），本轮不实现也不假装实现。
func (m *DownloadManager) CancelDownload(ctx context.Context, exec contracts.ExecutionContext, downloadID string) (DownloadJob, error) {
	job, err := m.load(ctx, exec, downloadID)
	if err != nil {
		return DownloadJob{}, err
	}
	switch job.State {
	case DownloadCanceled:
		// 幂等：重复取消不是错误，且不会让状态在 canceled/saved 之间跳。
		return job, nil
	case DownloadSaved:
		return DownloadJob{}, appErr(CodeDownloadState,
			"该下载已保存为文件与产物：取消不会删除用户文件，需要显式的删除动作（本轮未实现）")
	case DownloadFailed:
		return DownloadJob{}, appErr(CodeDownloadState,
			"该下载已失败：没有最终文件需要取消")
	}
	_ = os.Remove(m.stagedPath(job.ID))
	if _, statErr := os.Stat(m.stagedPath(job.ID)); !os.IsNotExist(statErr) {
		return DownloadJob{}, appErr(contracts.CodeCleanupFailed, "取消后 staging 仍然存在: "+m.stagedPath(job.ID))
	}
	job.State = DownloadCanceled
	job.ErrorCode = ""
	job.ErrorMessage = ""
	if err := m.update(ctx, job); err != nil {
		return DownloadJob{}, err
	}
	return job, nil
}

// Job 返回一个作业（跨 session 一律拒绝，不区分"不存在"与"不是你的"）。
func (m *DownloadManager) Job(ctx context.Context, exec contracts.ExecutionContext, downloadID string) (DownloadJob, error) {
	return m.load(ctx, exec, downloadID)
}

// Jobs 返回该会话的下载作业（按创建顺序）。
func (m *DownloadManager) Jobs(ctx context.Context, exec contracts.ExecutionContext) ([]DownloadJob, error) {
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return nil, appErr(contracts.CodeUnauthorized, "缺少会话身份：下载必须绑定会话")
	}
	rows, err := m.db.SQL().QueryContext(ctx,
		selectDownloadColumns+` WHERE session_id = ? ORDER BY created_at, id`, string(exec.SessionID))
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "读取下载列表失败: "+err.Error())
	}
	defer rows.Close()
	var out []DownloadJob
	for rows.Next() {
		job, scanErr := scanDownload(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, appErr(contracts.CodeInternal, "遍历下载列表失败: "+err.Error())
	}
	return out, nil
}

const selectDownloadColumns = `SELECT id, session_id, workspace_id, tab_id, url, suggested_name,
	state, size_bytes, sha256, final_path, artifact_id, error_code, error_message
	FROM browser_downloads`

func (m *DownloadManager) load(ctx context.Context, exec contracts.ExecutionContext, downloadID string) (DownloadJob, error) {
	if m == nil || m.db == nil {
		return DownloadJob{}, appErr(contracts.CodeInternal, "下载管理未初始化")
	}
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return DownloadJob{}, appErr(contracts.CodeUnauthorized, "缺少会话身份：下载必须绑定会话")
	}
	id := strings.TrimSpace(downloadID)
	if id == "" {
		return DownloadJob{}, appErr(contracts.CodeInvalidInput, "缺少下载 id")
	}
	row := m.db.SQL().QueryRowContext(ctx, selectDownloadColumns+` WHERE id = ?`, id)
	job, err := scanDownload(row)
	if err != nil {
		return DownloadJob{}, err
	}
	if job.SessionID != exec.SessionID {
		// 稳定拒绝：不区分"不存在"与"不是你的"，否则一次探测就能枚举别人的下载。
		return DownloadJob{}, appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"下载 %s 不属于当前会话（会话 %s）", id, exec.SessionID))
	}
	return job, nil
}

func (m *DownloadManager) update(ctx context.Context, job DownloadJob) error {
	_, err := m.db.SQL().ExecContext(ctx, `
		UPDATE browser_downloads SET state = ?, size_bytes = ?, sha256 = ?, final_path = ?,
		       artifact_id = ?, error_code = ?, error_message = ?, updated_at = ?
		 WHERE id = ?`,
		string(job.State), job.Size, job.SHA256, job.FinalPath, job.ArtifactID,
		job.ErrorCode, job.ErrorMessage, m.ts(), job.ID)
	if err != nil {
		return appErr(contracts.CodeInternal, "更新下载作业失败: "+err.Error())
	}
	return nil
}

// fail 把作业标成 failed 并返回对应错误（调用方一个字节都不留）。
func (m *DownloadManager) fail(ctx context.Context, job DownloadJob, code, message string) error {
	_ = os.Remove(m.stagedPath(job.ID))
	job.State = DownloadFailed
	job.ErrorCode = code
	job.ErrorMessage = message
	_ = m.update(ctx, job)
	return appErr(code, message)
}

func (m *DownloadManager) workspaceRoot(exec contracts.ExecutionContext) (string, error) {
	root, err := m.wsRoot(exec.WorkspaceID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(root) == "" {
		return "", appErr(contracts.CodeConfigMissing, "工作区根为空: "+string(exec.WorkspaceID))
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", appErr(contracts.CodeInternal, "解析工作区根失败: "+err.Error())
	}
	return filepath.Clean(abs), nil
}

func (m *DownloadManager) stagedPath(id string) string {
	return filepath.Join(m.root, "staging", id+".part")
}

func (m *DownloadManager) ts() string { return m.now().UTC().Format(tsLayout) }

// downloadMeta 是一次抓取的事实。
type downloadMeta struct {
	size     int64
	sha256   string
	filename string
}

// fetch 把 URL 的内容写到 staged（受管 staging），同时算 size/sha256。
//
// 三个细节都是刻意的：
//   - 响应体按 maxBytes+1 读：超限是**失败**，不是截断；
//   - 非 2xx 也读掉错误体再失败（避免连接悬挂），但绝不把它当文件内容；
//   - 写完 fsync 再关：后续读回核对的是同一份磁盘事实。
func (m *DownloadManager) fetch(ctx context.Context, raw, cookieHeader, staged string) (downloadMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return downloadMeta{}, appErr(CodeDownloadFailed, "构造下载请求失败: "+err.Error())
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return downloadMeta{}, appErr(CodeDownloadFailed, "下载请求失败: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return downloadMeta{}, appErr(CodeDownloadFailed, fmt.Sprintf(
			"下载返回 HTTP %d（可能需要该 workspace 的登录态，或该 URL 不可下载）", resp.StatusCode))
	}

	f, err := os.OpenFile(staged, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return downloadMeta{}, appErr(CodeDownloadFailed, "创建 staging 失败: "+err.Error())
	}
	hasher := sha256.New()
	buf := make([]byte, 128<<10)
	var total int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > m.maxBytes {
				_ = f.Close()
				_ = os.Remove(staged)
				return downloadMeta{}, appErr(CodeDownloadFailed, fmt.Sprintf(
					"下载超过上限 %d 字节：已中止且没有留下部分文件", m.maxBytes))
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = f.Close()
				_ = os.Remove(staged)
				return downloadMeta{}, appErr(CodeDownloadFailed, "写入 staging 失败: "+werr.Error())
			}
			_, _ = hasher.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = f.Close()
			_ = os.Remove(staged)
			return downloadMeta{}, appErr(CodeDownloadFailed, "读取下载内容失败: "+readErr.Error())
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(staged)
		return downloadMeta{}, appErr(CodeDownloadFailed, "落盘 staging 失败: "+err.Error())
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(staged)
		return downloadMeta{}, appErr(CodeDownloadFailed, "关闭 staging 失败: "+err.Error())
	}
	return downloadMeta{
		size:     total,
		sha256:   hex.EncodeToString(hasher.Sum(nil)),
		filename: filenameFromDisposition(resp.Header.Get("Content-Disposition")),
	}, nil
}

// resolveDownloadTarget 把用户给的目标解析成"工作区根内、逐段都不是链接的文件路径"。
func resolveDownloadTarget(wsRoot, destination string) (string, error) {
	raw := strings.TrimSpace(destination)
	if strings.ContainsRune(raw, 0) {
		return "", appErr(contracts.CodeUnauthorized, "目标路径包含 NUL 字节")
	}
	if !filepath.IsAbs(raw) {
		return "", appErr(contracts.CodeInvalidInput, "保存目标必须是绝对路径: "+raw)
	}
	abs := filepath.Clean(raw)
	if !pathInside(wsRoot, abs) {
		return "", appErr(contracts.CodeUnauthorized,
			"保存目标必须落在工作区内（产物登记与写入边界都要求）: "+abs)
	}
	// 逐段查链接：链接会让"看起来在工作区内"的路径实际写到别处。
	rel, err := filepath.Rel(filepath.Clean(wsRoot), abs)
	if err != nil {
		return "", appErr(contracts.CodeUnauthorized, "目标路径无法解析: "+raw)
	}
	current := filepath.Clean(wsRoot)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				break
			}
			return "", appErr(contracts.CodeUnauthorized, "目标路径不可访问: "+current)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", appErr(contracts.CodeUnauthorized, "目标路径经过符号链接/目录联接: "+current)
		}
	}
	return abs, nil
}

// SanitizeSuggestedName 把引擎给的建议名清理成一个安全文件名。
//
// 规则按"最坏输入"设计：路径穿越、Windows 设备名、控制字符、非法字符、超长、
// 只剩点号。清理失败时返回 "download"（而不是空串：空串会让调用方拼出一个
// 目录路径）。
func SanitizeSuggestedName(raw string) string {
	name := strings.TrimSpace(raw)
	// 1) 去掉控制字符（含 NUL）：它们在多数文件系统上非法，且常被用来截断路径。
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	// 2) 统一分隔符后只取最后一段：`..\..\x`、`C:\Windows\x`、`a/b/c` 都收敛成文件名。
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)
	// 3) 去掉前导点号（`.`/`..`/`....` 都不是文件名），再做首尾裁剪。
	name = strings.TrimLeft(name, ".")
	name = strings.TrimSpace(name)
	// 4) 非法字符替换成下划线；去掉尾随点/空格（Windows 会静默吃掉它们）。
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '<' || r == '>' || r == ':' || r == '"' || r == '|' || r == '?' || r == '*':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.TrimRight(b.String(), ". ")
	// 5) Windows 保留设备名（con/nul/com1…）不能当文件名。
	if isReservedDeviceName(name) {
		name = "_" + name
	}
	// 6) 长度上限：保留扩展名，避免把 `.csv` 这类"能不能打开"的信息截掉。
	name = clampNameLength(name, MaxSuggestedNameLength)
	if name == "" {
		return "download"
	}
	return name
}

func isReservedDeviceName(name string) bool {
	if name == "" {
		return false
	}
	base := strings.ToUpper(strings.TrimSuffix(name, path.Ext(name)))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func clampNameLength(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	ext := path.Ext(name)
	if len(ext) >= limit {
		return name[:limit]
	}
	return name[:limit-len(ext)] + ext
}

// filenameFromDisposition 从 Content-Disposition 里取文件名（取不到返回空串）。
func filenameFromDisposition(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["filename"])
}

// hashFile 打开文件读一遍，返回实际读到的字节数与 sha256。
//
// 返回的是**读到的事实**（total），不是 Stat 里的大小：两者不一致本身就是
// "读的时候文件正在变"的证据，调用方拿到的是它核对过的那些字节。
func hashFile(filePath string) (int64, string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	hasher := sha256.New()
	buf := make([]byte, 128<<10)
	var total int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			total += int64(n)
			_, _ = hasher.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, "", readErr
		}
	}
	return total, hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func newDownloadID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", appErr(contracts.CodeInternal, "生成下载 id 失败: "+err.Error())
	}
	return "dl-" + hex.EncodeToString(buf), nil
}

// scanDownload 把一行（或 QueryRow）扫描成作业。
func scanDownload(row interface {
	Scan(dest ...any) error
}) (DownloadJob, error) {
	var job DownloadJob
	var state string
	err := row.Scan(&job.ID, &job.SessionID, &job.WorkspaceID, &job.TabID, &job.URL,
		&job.SuggestedName, &state, &job.Size, &job.SHA256, &job.FinalPath,
		&job.ArtifactID, &job.ErrorCode, &job.ErrorMessage)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DownloadJob{}, appErr(contracts.CodeNotFound, "下载作业不存在")
		}
		return DownloadJob{}, appErr(contracts.CodeInternal, "读取下载作业失败: "+err.Error())
	}
	job.State = DownloadState(state)
	return job, nil
}
