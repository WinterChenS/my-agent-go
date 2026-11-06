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
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
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
