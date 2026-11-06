package app

// C07 产物绑定层：产物登记/核对/预览/导出/按 run 列举。
//
// 四条纪律：
//   - 业务判断（工作区内、逐段无链接、重新核对 hash/size）全在 internal/artifact，
//     这里不重写一遍，否则两个地方的说法迟早不一致；
//   - 产物索引与 run usage/事件共用 <appRoot>/sessions.db 的 007 迁移（同一张库），
//     不另建第二套存储；
//   - 数据库句柄复用会话目录那一个（d.sessionDB）：迁移在这里只做一次，
//     产物页可能在用户还没开过会话时就被打开，不能指望"有人先跑过 run"；
//   - 跨边界错误一律结构化返回（CallError），前端按 Code 分支。
//
// 这里没有任何"按任意本机路径打开文件"的入口：预览/导出只接受产物 id，
// 实际路径来自索引行并重新证明仍在登记时的工作区内。

import (
	"context"
	"path/filepath"
	"strings"

	"my-agent-go/internal/artifact"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// ArtifactListQuery 是前端传来的产物列举条件（零值 = 全部工作区的最近产物）。
type ArtifactListQuery struct {
	WorkspaceID string `json:"workspaceId"`
	RunID       string `json:"runId"`
	SessionID   string `json:"sessionId"`
	Limit       int    `json:"limit"`
}

// ArtifactListResult 是一页产物索引。
type ArtifactListResult struct {
	Items []artifact.Artifact `json:"items"`
	Error *CallError          `json:"error,omitempty"`
}

// ArtifactResult 是单个产物操作的结果。
//
// GetArtifact 会带上 Verification（这次读的核对结论）；列表与导入不带，
// 因为"没核对"不能显示成"已核对"。
type ArtifactResult struct {
	Item  artifact.Artifact `json:"item"`
	Error *CallError        `json:"error,omitempty"`
}

// ArtifactExportResult 是一次产物导出的结果。
type ArtifactExportResult struct {
	ArtifactID string     `json:"artifactId"`
	Path       string     `json:"path"`
	Bytes      int64      `json:"bytes"`
	SHA256     string     `json:"sha256"`
	Error      *CallError `json:"error,omitempty"`
}

// ArtifactPreviewResult 是一次产物预览的结果。
type ArtifactPreviewResult struct {
	Preview artifact.Preview `json:"preview"`
	Error   *CallError       `json:"error,omitempty"`
}

// indexDB 返回会话/产物/诊断共用的索引库（已迁移到最新 schema）。
//
// 复用会话目录的懒加载入口：它负责打开 <appRoot>/sessions.db 并跑迁移。
func (d *Desktop) indexDB() (*storage.DB, error) {
	if _, err := d.sessionCatalog(); err != nil {
		return nil, err
	}
	d.svcMu.Lock()
	defer d.svcMu.Unlock()
	if d.sessionDB == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "索引库未就绪", false)
	}
	return d.sessionDB, nil
}

// artifactService 装配产物服务。
//
// 每次调用现装配：服务本身无状态（状态在库与文件系统里），守卫只做一次目录检查。
// 导出根与 C06e 的会话导出共用 <appRoot>/exports——用户点"导出"拿到的是导出目录下的
// 一个文件，而不是任意路径。
func (d *Desktop) artifactService() (*artifact.Service, error) {
	db, err := d.indexDB()
	if err != nil {
		return nil, err
	}
	return artifact.New(db, artifact.Options{
		ExportRoot:    filepath.Join(d.root, "exports"),
		WorkspaceRoot: d.artifactWorkspaceRoot,
	})
}

// artifactWorkspaceRoot 把工作区 id 解析成真实根路径。
//
// 权威在 C01 的工作区配置（CanonicalPath 已解析掉链接）；这里只读取，
// 不复制"什么算同一个目录"的判定逻辑。
func (d *Desktop) artifactWorkspaceRoot(id contracts.WorkspaceID) (string, error) {
	ws, _, err := d.coreServices()
	if err != nil {
		return "", err
	}
	item, err := ws.Get(context.Background(), id)
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(item.CanonicalPath)
	if root == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"工作区 "+string(id)+" 没有真实路径，无法判定产物范围", false)
	}
	return root, nil
}

// ListArtifacts 列出产物索引（不做磁盘核对）。
func (d *Desktop) ListArtifacts(q ArtifactListQuery) ArtifactListResult {
	svc, err := d.artifactService()
	if err != nil {
		return ArtifactListResult{Items: []artifact.Artifact{}, Error: toCallError(err)}
	}
	items, err := svc.ListArtifacts(context.Background(), artifact.Query{
		WorkspaceID: contracts.WorkspaceID(strings.TrimSpace(q.WorkspaceID)),
		RunID:       contracts.RunID(strings.TrimSpace(q.RunID)),
		SessionID:   contracts.SessionID(strings.TrimSpace(q.SessionID)),
		Limit:       q.Limit,
	})
	if err != nil {
		return ArtifactListResult{Items: []artifact.Artifact{}, Error: toCallError(err)}
	}
	if items == nil {
		items = []artifact.Artifact{}
	}
	return ArtifactListResult{Items: items}
}

// ListRunArtifacts 列出一个 run 登记的产物。
func (d *Desktop) ListRunArtifacts(runID string) ArtifactListResult {
	svc, err := d.artifactService()
	if err != nil {
		return ArtifactListResult{Items: []artifact.Artifact{}, Error: toCallError(err)}
	}
	items, err := svc.ListRunArtifacts(context.Background(), runID)
	if err != nil {
		return ArtifactListResult{Items: []artifact.Artifact{}, Error: toCallError(err)}
	}
	if items == nil {
		items = []artifact.Artifact{}
	}
	return ArtifactListResult{Items: items}
}

// GetArtifact 读一条产物，并重新核对磁盘（drift/missing 如实返回）。
func (d *Desktop) GetArtifact(id string) ArtifactResult {
	svc, err := d.artifactService()
	if err != nil {
		return ArtifactResult{Error: toCallError(err)}
	}
	item, err := svc.GetArtifact(context.Background(), id)
	if err != nil {
		return ArtifactResult{Error: toCallError(err)}
	}
	return ArtifactResult{Item: item}
}

// ImportArtifact 登记一个工作区内的文件为产物。
//
// path 是工具/用户给出的候选定位串，Go 侧会重新解析（越界/链接/目录一律拒绝）。
// runID 可为空；给了就用于"按 run 列产物"与诊断定位。
func (d *Desktop) ImportArtifact(path, workspaceID, runID string) ArtifactResult {
	svc, err := d.artifactService()
	if err != nil {
		return ArtifactResult{Error: toCallError(err)}
	}
	item, err := svc.ImportArtifact(context.Background(), artifact.ImportRequest{
		Path:        path,
		WorkspaceID: contracts.WorkspaceID(strings.TrimSpace(workspaceID)),
		RunID:       contracts.RunID(strings.TrimSpace(runID)),
	})
	if err != nil {
		return ArtifactResult{Error: toCallError(err)}
	}
	return ArtifactResult{Item: item}
}

// ExportArtifact 把产物导出到导出目录下的 target。
//
// 目标为空/相对路径时落在 <appRoot>/exports 下；越界/链接/目录目标由
// internal/artifact 结构化拒绝（不是绑定层自己再判一遍）。
func (d *Desktop) ExportArtifact(id, target string) ArtifactExportResult {
	svc, err := d.artifactService()
	if err != nil {
		return ArtifactExportResult{ArtifactID: strings.TrimSpace(id), Error: toCallError(err)}
	}
	res, err := svc.ExportArtifact(context.Background(), id, target)
	if err != nil {
		return ArtifactExportResult{ArtifactID: strings.TrimSpace(id), Error: toCallError(err)}
	}
	return ArtifactExportResult{
		ArtifactID: res.ArtifactID,
		Path:       res.Path,
		Bytes:      res.Bytes,
		SHA256:     res.SHA256,
	}
}

// PreviewArtifact 预览产物（MIME 白名单，核对之后才返回内容）。
func (d *Desktop) PreviewArtifact(id string) ArtifactPreviewResult {
	svc, err := d.artifactService()
	if err != nil {
		return ArtifactPreviewResult{Error: toCallError(err)}
	}
	preview, err := svc.PreviewArtifact(context.Background(), id)
	if err != nil {
		return ArtifactPreviewResult{Error: toCallError(err)}
	}
	return ArtifactPreviewResult{Preview: preview}
}
