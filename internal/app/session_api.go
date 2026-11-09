package app

// 本文件是 C06 的会话目录绑定层：会话的列举/创建/改名/置顶/归档/删除。
//
// 它只是薄绑定，三条纪律：
//   - 业务判断（CAS、分页、软删除、普通会话过滤）全在 internal/session，
//     这里不重写一遍，否则两个地方的说法迟早不一致；
//   - 会话库固定在 <appRoot>/sessions.db，与 C07 的产物索引、run 索引共用同一个
//     索引库（001 的 workspaces/sessions/runs 就在这张库里），不另建第二套存储；
//   - 跨边界错误一律结构化返回（CallError），前端按 Code 分支：
//     CONFIG_CONFLICT 要显示"已被别处修改"并让用户重读，不是弹一个字符串。
//
// 错误策略：会话目录不可用（库打不开）不阻断聊天，但必须让调用方看见错误——
// 空列表 + 无声无息会把"读不出来"伪装成"没有会话"。

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/history"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/run"
	"my-agent-go/internal/session"
	"my-agent-go/internal/storage"
)

// SessionListQuery 是前端传来的列举条件。
//
// WorkspaceID 为空表示跨工作区列举（历史侧栏按工作区分组显示，U01）。
type SessionListQuery struct {
	WorkspaceID     string `json:"workspaceId"`
	IncludeArchived bool   `json:"includeArchived"`
	Limit           int    `json:"limit"`
	Cursor          string `json:"cursor"`
}

// SessionListResult 是一页会话目录。
type SessionListResult struct {
	Groups     []session.SessionGroup `json:"groups"`
	NextCursor string                 `json:"nextCursor,omitempty"`
	HasMore    bool                   `json:"hasMore"`
	Error      *CallError             `json:"error,omitempty"`
}

// SessionResult 是单个会话操作的结果。
type SessionResult struct {
	Item  session.Session `json:"item"`
	Error *CallError      `json:"error,omitempty"`
}

// SessionOpResult 是只需要成败的操作结果（删除）。
type SessionOpResult struct {
	Error *CallError `json:"error,omitempty"`
}

// sessionCatalog 返回应用级会话目录（懒加载，库固定在应用数据根下）。
func (d *Desktop) sessionCatalog() (*session.Catalog, error) {
	d.svcMu.Lock()
	defer d.svcMu.Unlock()
	if d.sessionCat != nil {
		return d.sessionCat, nil
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(d.root, "sessions.db"))
	if err != nil {
		return nil, err
	}
	// 迁移在这里做：会话页可能在用户还没跑过任何 run 时就被打开，
	// 不能指望"有人先跑一次历史索引"把表建出来。
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	cat, err := session.New(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	cat.SetWorkspaceRoot(d.sessionWorkspaceRoot)
	d.sessionDB = db
	d.sessionCat = cat
	return cat, nil
}

// sessionWorkspaceRoot 把工作区真实根路径交给目录做索引行。
//
// 工作区的权威在 C01 的配置文档里；这里只读取它的 CanonicalPath，不复制判定逻辑。
func (d *Desktop) sessionWorkspaceRoot(id contracts.WorkspaceID) (string, error) {
	ws, _, err := d.coreServices()
	if err != nil {
		return "", err
	}
	item, err := ws.Get(context.Background(), id)
	if err != nil {
		return "", err
	}
	return item.CanonicalPath, nil
}

// currentWorkspaceID 返回设置里当前工作区的稳定 ID。
//
// 与记忆的 memoryWorkspaceID 同一条口径：用工作区服务算出的 ID，而不是固定的
// "ws-default"，否则换目录就是换项目，会话会串到别的项目里。
func (d *Desktop) currentWorkspaceID(ctx context.Context) (contracts.WorkspaceID, error) {
	path := strings.TrimSpace(d.Settings().Workspace)
	if path == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"尚未选择工作区：会话需要知道归属项目才能创建", false)
	}
	ws, _, err := d.coreServices()
	if err != nil {
		return "", err
	}
	item, err := ws.CreateWorkspace(ctx, path)
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

// ListSessions 列出一页会话目录（按工作区分组，普通会话）。
func (d *Desktop) ListSessions(q SessionListQuery) SessionListResult {
	cat, err := d.sessionCatalog()
	if err != nil {
		return SessionListResult{Groups: []session.SessionGroup{}, Error: toCallError(err)}
	}
	res, err := cat.ListSessions(context.Background(), session.SessionQuery{
		WorkspaceID:     contracts.WorkspaceID(strings.TrimSpace(q.WorkspaceID)),
		IncludeArchived: q.IncludeArchived,
		Limit:           q.Limit,
		Cursor:          q.Cursor,
	})
	if err != nil {
		return SessionListResult{Groups: []session.SessionGroup{}, Error: toCallError(err)}
	}
	return SessionListResult{Groups: res.Groups, NextCursor: res.NextCursor, HasMore: res.HasMore}
}

// CreateSession 在工作区里新建会话；workspaceID 为空表示当前工作区。
func (d *Desktop) CreateSession(workspaceID string) SessionResult {
	ctx := context.Background()
	cat, err := d.sessionCatalog()
	if err != nil {
		return SessionResult{Error: toCallError(err)}
	}
	wid := contracts.WorkspaceID(strings.TrimSpace(workspaceID))
	if wid == "" {
		wid, err = d.currentWorkspaceID(ctx)
		if err != nil {
			return SessionResult{Error: toCallError(err)}
		}
	}
	item, err := cat.CreateSession(ctx, wid)
	if err != nil {
		return SessionResult{Error: toCallError(err)}
	}
	return SessionResult{Item: item}
}

// RenameSession 改标题。expectedRevision 是用户看到的那一版，过期返回 CONFIG_CONFLICT。
func (d *Desktop) RenameSession(id string, expectedRevision uint64, title string) SessionResult {
	return d.sessionMutation(func(cat *session.Catalog) (session.Session, error) {
		return cat.RenameSession(context.Background(), contracts.SessionID(id), expectedRevision, title)
	})
}

// PinSession 置顶/取消置顶。
func (d *Desktop) PinSession(id string, expectedRevision uint64, pinned bool) SessionResult {
	return d.sessionMutation(func(cat *session.Catalog) (session.Session, error) {
		return cat.PinSession(context.Background(), contracts.SessionID(id), expectedRevision, pinned)
	})
}

// ArchiveSession 归档/取消归档（不是删除：行、transcript、产物都还在）。
func (d *Desktop) ArchiveSession(id string, expectedRevision uint64, archived bool) SessionResult {
	return d.sessionMutation(func(cat *session.Catalog) (session.Session, error) {
		return cat.ArchiveSession(context.Background(), contracts.SessionID(id), expectedRevision, archived)
	})
}

// DeleteSession 软删除会话。
func (d *Desktop) DeleteSession(id string, expectedRevision uint64) SessionOpResult {
	cat, err := d.sessionCatalog()
	if err != nil {
		return SessionOpResult{Error: toCallError(err)}
	}
	if err := cat.DeleteSession(context.Background(), contracts.SessionID(id), expectedRevision); err != nil {
		return SessionOpResult{Error: toCallError(err)}
	}
	return SessionOpResult{}
}

// sessionMutation 统一取服务、执行、把结果包成绑定层结构。
func (d *Desktop) sessionMutation(fn func(*session.Catalog) (session.Session, error)) SessionResult {
	cat, err := d.sessionCatalog()
	if err != nil {
		return SessionResult{Error: toCallError(err)}
	}
	item, err := fn(cat)
	if err != nil {
		return SessionResult{Error: toCallError(err)}
	}
	return SessionResult{Item: item}
}

// ---------------------------------------------------------------------------
// C06c：普通会话附件（app-owned blob store + 只读 broker）
// ---------------------------------------------------------------------------

// AttachmentsResult 是一次附件接收的绑定层结果。
//
// Items 与 Rejected 共同构成完整答案：Error 只表示"整批无法处理"
// （会话不存在/不是普通会话/存储不可用），单个文件的问题走 Rejected——
// 前端必须逐条显示原因，不能把一批文件里的一个坏文件变成"全都失败"。
type AttachmentsResult struct {
	SessionID string                        `json:"sessionId"`
	Items     []session.AttachmentMeta      `json:"items"`
	Rejected  []session.AttachmentRejection `json:"rejected"`
	Error     *CallError                    `json:"error,omitempty"`
}

// attachmentStore 返回应用级附件存储（懒加载，blob 固定在 <appRoot>/blobs）。
//
// 能力登记表必须是单例：ResolveAttachments 发出去的 ID 与 OpenAttachment 认的
// ID 必须来自同一张表，否则 run 拿到的能力永远读不到。
func (d *Desktop) attachmentStore() (*session.AttachmentStore, error) {
	d.attachMu.Lock()
	defer d.attachMu.Unlock()
	if d.attachStore != nil {
		return d.attachStore, nil
	}
	cat, err := d.sessionCatalog()
	if err != nil {
		return nil, err
	}
	blobRoot := filepath.Join(d.root, "blobs")
	if err := d.assertBlobStoreOutsideWorkspace(blobRoot); err != nil {
		return nil, err
	}
	store, err := session.NewAttachmentStore(cat, blobRoot)
	if err != nil {
		return nil, err
	}
	d.attachStore = store
	return store, nil
}

// assertBlobStoreOutsideWorkspace 证明 blob store 不在 run 被授予的 workspace 里。
//
// 这不是"再检查一遍用户输入"，而是装配时的不变量：一旦 blob 落进 workspace，
// Pi 就有了写附件的路径，broker 的只读承诺立刻失效。证明不了就不装配（fail closed），
// 而不是"先跑起来再说"。
func (d *Desktop) assertBlobStoreOutsideWorkspace(blobRoot string) error {
	wsPath := strings.TrimSpace(d.Settings().Workspace)
	if wsPath == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing,
			"尚未选择工作区：无法证明附件存储位于 run 的权限边界之外", false)
	}
	guard, err := policy.NewPathGuard(wsPath)
	if err != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"工作区无法解析，附件存储的隔离位置无法证明: "+err.Error(), false)
	}
	dec, err := guard.Check(blobRoot)
	if err == nil {
		return contracts.NewAppError(contracts.CodeConfigConflict,
			"附件存储位于工作区内（"+dec.Real+"），拒绝装配：run 不能拥有可写附件路径", false)
	}
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr.Code == contracts.CodeUnauthorized {
		return nil // 越界＝在 workspace 之外，正是要的结论
	}
	return err
}

// ResolveAttachments 接收用户通过原生选择器选中的附件。
//
// 只收路径：Go 重新解析路径、拒绝链接/越界，把字节复制进 app 托管的
// content-addressed blob store，然后返回只读能力 ID。源文件不会被回写，
// 结果里也不含任何可写路径。
func (d *Desktop) ResolveAttachments(sessionID string, reqs []session.AttachmentRequest) AttachmentsResult {
	out := AttachmentsResult{
		SessionID: strings.TrimSpace(sessionID),
		Items:     []session.AttachmentMeta{},
		Rejected:  []session.AttachmentRejection{},
	}
	store, err := d.attachmentStore()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	res, err := store.ResolveAttachments(sessionID, reqs)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if res.Items != nil {
		out.Items = res.Items
	}
	if res.Rejected != nil {
		out.Rejected = res.Rejected
	}
	out.SessionID = res.SessionID
	return out
}

// ---------------------------------------------------------------------------
// C06e：会话导出（html | markdown | json）
// ---------------------------------------------------------------------------

// SessionExportResult 是一次会话导出的绑定层结果。
//
// 它只报告"写到哪里、写了多少、脱敏几处"：导出内容本身不经过前端，
// 前端拿到的只是路径与指纹，避免把 transcript 正文再抄一遍。
type SessionExportResult struct {
	SessionID  string     `json:"sessionId"`
	Format     string     `json:"format"`
	Path       string     `json:"path"`
	Bytes      int        `json:"bytes"`
	SHA256     string     `json:"sha256"`
	Records    int        `json:"records"`
	Messages   int        `json:"messages"`
	Redactions int        `json:"redactions"`
	WrittenAt  time.Time  `json:"writtenAt"`
	Error      *CallError `json:"error,omitempty"`
}

// exportService 返回导出服务。
//
// 导出根固定在 <appRoot>/exports：用户点"导出"给的是**文件名**，不是任意路径，
// 这样"导出到别处"必须是一次显式的、可审阅的决定（C07 的产物导出另走一条路）。
//
// 每次调用现装配：history.Service 只是对已有会话库句柄的只读包装（不起 goroutine、
// 不建表），Exporter 也无状态，因此不需要第三个应用级单例，也就不必动 desktop.go。
func (d *Desktop) exportService() (*session.Exporter, error) {
	// 先走一次会话目录：它负责打开并迁移 sessions.db，导出复用同一个句柄。
	if _, err := d.sessionCatalog(); err != nil {
		return nil, err
	}
	d.svcMu.Lock()
	db := d.sessionDB
	d.svcMu.Unlock()
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "会话库未就绪，无法导出", false)
	}
	hist, err := history.Open(context.Background(), db, history.Options{
		AgentDir:      d.agentDir(),
		WorkspaceID:   "ws-default",
		WorkspaceRoot: strings.TrimSpace(d.Settings().Workspace),
	})
	if err != nil {
		return nil, err
	}
	return session.NewExporter(hist, filepath.Join(d.root, "exports"))
}

// ExportSession 把会话导出到导出目录下的 target。
//
// format ∈ html|markdown|json；target 为空或相对路径时落在 <appRoot>/exports 下，
// 越界/链接/目录目标由 internal/session 结构化拒绝（不是绑定层自己再判一遍）。
func (d *Desktop) ExportSession(sessionID, format, target string) SessionExportResult {
	exp, err := d.exportService()
	if err != nil {
		return SessionExportResult{SessionID: strings.TrimSpace(sessionID), Format: format, Error: toCallError(err)}
	}
	res, err := exp.ExportSession(context.Background(), sessionID, format, target)
	if err != nil {
		return SessionExportResult{SessionID: strings.TrimSpace(sessionID), Format: format, Error: toCallError(err)}
	}
	return SessionExportResult{
		SessionID:  res.SessionID,
		Format:     res.Format,
		Path:       res.Path,
		Bytes:      res.Bytes,
		SHA256:     res.SHA256,
		Records:    res.Records,
		Messages:   res.Messages,
		Redactions: res.Redactions,
		WrittenAt:  res.WrittenAt,
	}
}

// ---------------------------------------------------------------------------
// C06f：附件投递 seam 与澄清桥的装配
// ---------------------------------------------------------------------------

// desktopAttachmentReader 把 run 侧的窄接口接到 app 托管的附件存储上。
//
// run 只认识 OpenAttachment 这一个入口，不认识 blob 路径、不认识表结构；
// 存储打不开时返回结构化错误（run 侧 fail closed，不会"给个空附件"）。
type desktopAttachmentReader struct {
	d *Desktop
}

// OpenAttachment 返回已经校验过 size/sha256 的字节。
//
// 校验发生在 internal/session（同一个打开句柄上先验后发），这里只做转接：
// 绑定层不再自己读一遍文件——两份校验逻辑迟早会分叉。
func (r desktopAttachmentReader) OpenAttachment(id string) (io.ReadCloser, error) {
	if r.d == nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "附件读取缺少桌面宿主", false)
	}
	store, err := r.d.attachmentStore()
	if err != nil {
		return nil, err
	}
	body, _, err := store.OpenAttachment(id)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// clarificationOptions 返回澄清桥的装配参数。
//
// Enabled 只在产品自带的澄清扩展真的存在时为真：找不到扩展就没有"产品身份的
// 澄清工具"，这时既不建立澄清桥，也不能把任何同名工具当成"本地无副作用"。
func (d *Desktop) clarificationOptions() run.ClarificationOptions {
	ext, ok := findProductExtension("anvil-clarification.ts", productExtensionEnvKey("anvil-clarification.ts"))
	if !ok {
		return run.ClarificationOptions{}
	}
	return run.ClarificationOptions{Enabled: true, ExtensionPath: ext}
}

// ProductExtensions 返回产品自带、不可关闭的系统扩展路径（诊断/测试可见）。
//
// 它回答"这一轮 Pi 会额外加载哪几条产品扩展"，与隔离参数读同一份定位逻辑。
func (d *Desktop) ProductExtensions() []string { return productExtensionPaths() }

// AnswerClarification 提交一次内容澄清答复。
//
// 它只走澄清桥：不经过 ResolveApproval，不创建 approval、session grant 或
// 任何工具/站点权限（C06 的明确要求）。会话归属由 run 侧校验：
// 一个会话不能替另一个会话回答内容问题。
func (d *Desktop) AnswerClarification(sessionID, requestID string, response run.ClarificationResponse) error {
	svc, err := d.runtime()
	if err != nil {
		return err
	}
	return svc.AnswerClarification(context.Background(), contracts.SessionID(strings.TrimSpace(sessionID)), requestID, response)
}

// StartSessionRun 是会话级提交入口：消息 + 已选附件。
//
// 附件只以 capability ID 的形式进来，绑定层在这里把它解析成冻结的引用：
// 过期/不存在的 ID 直接拒绝（而不是带一个永远读不到的引用进 run）。
func (d *Desktop) StartSessionRun(sessionID, message string, attachmentIDs []string) (contracts.RunSnapshot, error) {
	ctx := context.Background()
	workspaceID, err := d.assertWorkspaceReady(ctx)
	if err != nil {
		d.setLastErr(err.Error())
		return contracts.RunSnapshot{}, err
	}
	refs, err := d.attachmentRefs(attachmentIDs)
	if err != nil {
		d.setLastErr(err.Error())
		return contracts.RunSnapshot{}, err
	}
	message = d.withMemoryContext(ctx, message)
	svc, err := d.ensureRuntime()
	if err != nil {
		d.setLastErr(err.Error())
		return contracts.RunSnapshot{}, err
	}

	d.mu.Lock()
	runtimeSession := contracts.SessionID(d.runtimeSessionID)
	workspaceDir := strings.TrimSpace(d.settings.Workspace)
	d.mu.Unlock()

	// 会话必须显式给出时才用它：空值沿用运行时默认会话（兼容旧入口）。
	sid := contracts.SessionID(strings.TrimSpace(sessionID))
	if sid == "" {
		sid = runtimeSession
	}

	snap, err := svc.Start(ctx, run.StartRunRequest{
		Message:      message,
		SessionID:    sid,
		WorkspaceID:  workspaceID,
		WorkspaceDir: workspaceDir,
		Attachments:  refs,
		// 与 Desktop.Start 同一口径：档位在受理时读一次，进 ExecutionBinding 快照。
		PermissionMode: d.permissionModeForAttendedRun(),
	})
	if err != nil {
		d.setLastErr(err.Error())
		return snap, err
	}
	return snap, nil
}

// attachmentRefs 把 capability ID 解析成冻结的附件引用。
//
// 三件事在这里一次做完：ID 必须仍然有效（Lookup）、产品必须能读（Attachment 扩展
// 已加载）、引用只带元数据（没有路径）。
func (d *Desktop) attachmentRefs(ids []string) ([]run.AttachmentRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if _, ok := findProductExtension("anvil-attachment.ts", productExtensionEnvKey("anvil-attachment.ts")); !ok {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing,
			"找不到附件读取扩展 pi-extensions/anvil-attachment.ts：带附件的 run 无法把内容交给模型，拒绝提交", false)
	}
	store, err := d.attachmentStore()
	if err != nil {
		return nil, err
	}
	refs := make([]run.AttachmentRef, 0, len(ids))
	for _, id := range ids {
		meta, err := store.Lookup(strings.TrimSpace(id))
		if err != nil {
			return nil, err
		}
		refs = append(refs, run.AttachmentRef{
			ID:          meta.ID,
			DisplayName: meta.DisplayName,
			Size:        meta.Size,
			SHA256:      meta.SHA256,
			MediaType:   meta.MediaType,
		})
	}
	return refs, nil
}
