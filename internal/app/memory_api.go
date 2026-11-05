package app

// 本文件是 C05 的绑定层：记忆的列表/保存/确认/删除/检索/导出/按 scope 关闭，
// 以及 run 启动时的受控注入。
//
// 三条纪律：
//   - 检索与注入只走 memory.Service：scope 过滤、secret 拒绝、6000 bytes 预算
//     不能在这个文件里再写一份。
//   - 注入是"参考数据块 + 用户原文"，不改写用户消息、不升级成系统提示。
//   - 记忆不可用（数据库打不开）不阻断聊天；错误在记忆页可见，而不是
//     "看着像没有记忆"。这属于功能不可用，不是把 FTS 降级成 LIKE。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/memory"
)

// MemoryScopeState 是某个 scope 的注入开关。
type MemoryScopeState struct {
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId"`
	Enabled     bool   `json:"enabled"`
}

// MemoryListResult 是记忆列表 + scope 开关。
type MemoryListResult struct {
	Items  []memory.Memory    `json:"items"`
	Scopes []MemoryScopeState `json:"scopes"`
	Error  *CallError         `json:"error,omitempty"`
}

// MemoryResult 是单条记忆操作的结果。
type MemoryResult struct {
	Item  memory.Memory `json:"item"`
	Error *CallError    `json:"error,omitempty"`
}

// MemorySearchResult 是检索结果（只含 active 且未过期）。
type MemorySearchResult struct {
	Items []memory.Memory `json:"items"`
	Error *CallError      `json:"error,omitempty"`
}

// MemoryExportResult 是导出的可读 JSON 文本（不默认上传）。
type MemoryExportResult struct {
	JSON      string     `json:"json"`
	ItemCount int        `json:"itemCount"`
	Error     *CallError `json:"error,omitempty"`
}

// MemoryScopeResult 是按 scope 关闭/打开的结果。
type MemoryScopeResult struct {
	State MemoryScopeState `json:"state"`
	Error *CallError       `json:"error,omitempty"`
}

// memoryService 返回应用级记忆服务（懒加载，数据库固定在应用数据根下）。
func (d *Desktop) memoryService() (*memory.Service, error) {
	d.svcMu.Lock()
	defer d.svcMu.Unlock()
	if d.memSvc != nil {
		return d.memSvc, nil
	}
	svc, err := memory.Open(context.Background(), filepath.Join(d.root, "memory.db"))
	if err != nil {
		return nil, err
	}
	d.memSvc = svc
	return svc, nil
}

// memoryWorkspaceID 返回设置里当前工作区的稳定 ID。
//
// 用工作区服务计算的 ID，而不是固定的 "ws-default"：换目录就是换项目，
// 否则 A 项目的记忆会出现在 B 项目里（MEM02 最怕的那种泄漏）。
func (d *Desktop) memoryWorkspaceID(ctx context.Context) (string, error) {
	path := strings.TrimSpace(d.Settings().Workspace)
	if path == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"尚未选择工作区：记忆需要知道当前项目才能隔离", false)
	}
	ws, _, err := d.coreServices()
	if err != nil {
		return "", err
	}
	item, err := ws.CreateWorkspace(ctx, path)
	if err != nil {
		return "", err
	}
	return string(item.ID), nil
}

// memoryScopeStates 读取两个 scope 的开关状态。
func (d *Desktop) memoryScopeStates(ctx context.Context, svc *memory.Service, workspaceID string) ([]MemoryScopeState, error) {
	userEnabled, err := svc.ScopeEnabled(ctx, memory.ScopeUser, "")
	if err != nil {
		return nil, err
	}
	wsEnabled, err := svc.ScopeEnabled(ctx, memory.ScopeWorkspace, workspaceID)
	if err != nil {
		return nil, err
	}
	return []MemoryScopeState{
		{Scope: string(memory.ScopeUser), WorkspaceID: "", Enabled: userEnabled},
		{Scope: string(memory.ScopeWorkspace), WorkspaceID: workspaceID, Enabled: wsEnabled},
	}, nil
}

// ListMemories 返回当前项目可见的记忆（含候选，不含已删除）。
func (d *Desktop) ListMemories() MemoryListResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemoryListResult{Items: []memory.Memory{}, Error: toCallError(err)}
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return MemoryListResult{Items: []memory.Memory{}, Error: toCallError(err)}
	}
	items, err := svc.List(ctx, workspaceID)
	if err != nil {
		return MemoryListResult{Items: []memory.Memory{}, Error: toCallError(err)}
	}
	scopes, err := d.memoryScopeStates(ctx, svc, workspaceID)
	if err != nil {
		return MemoryListResult{Items: items, Error: toCallError(err)}
	}
	return MemoryListResult{Items: items, Scopes: scopes}
}

// SaveMemory 显式保存一条记忆（直接 active）。
//
// sourceSessionID 记录"是在哪个 run 里记下的"（没有 run 时为空，UI 显示"未记录"）。
// 显式保存不触发任何模型调用。
func (d *Desktop) SaveMemory(content, kind, scope string) MemoryResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	draft := memory.MemoryDraft{
		Scope:           memory.Scope(strings.TrimSpace(scope)),
		Kind:            kind,
		Content:         content,
		SourceSessionID: d.memorySessionID(),
	}
	if draft.Scope == "" {
		draft.Scope = memory.ScopeWorkspace
	}
	if draft.Scope == memory.ScopeWorkspace {
		draft.WorkspaceID = workspaceID
	}
	item, err := svc.Save(ctx, draft)
	if err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	return MemoryResult{Item: item}
}

// ConfirmMemory 把候选记忆确认为 active。
func (d *Desktop) ConfirmMemory(id string) MemoryResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	if err := svc.Confirm(ctx, id); err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	item, err := svc.Get(ctx, id)
	if err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	return MemoryResult{Item: item}
}

// DeleteMemory 删除一条记忆；下一次 run 的注入不再包含它。
//
// 它不能撤回已经发给模型的历史消息——UI 必须如实说明这一点。
func (d *Desktop) DeleteMemory(id string) MemoryResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	item, getErr := svc.Get(ctx, id)
	if getErr != nil {
		return MemoryResult{Error: toCallError(getErr)}
	}
	if err := svc.Delete(ctx, id); err != nil {
		return MemoryResult{Error: toCallError(err)}
	}
	item.Status = memory.StatusDeleted
	return MemoryResult{Item: item}
}

// SearchMemories 检索当前项目可见的 active 记忆。
//
// 少于 3 个 Unicode 字符返回 QUERY_TOO_SHORT（前端按码分支，不能渲染成空结果）。
func (d *Desktop) SearchMemories(query string) MemorySearchResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemorySearchResult{Items: []memory.Memory{}, Error: toCallError(err)}
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return MemorySearchResult{Items: []memory.Memory{}, Error: toCallError(err)}
	}
	items, err := svc.Search(ctx, memory.MemoryQuery{
		WorkspaceID: workspaceID,
		Text:        query,
		Limit:       memory.MaxContextEntries,
	})
	if err != nil {
		return MemorySearchResult{Items: []memory.Memory{}, Error: toCallError(err)}
	}
	return MemorySearchResult{Items: items}
}

// ExportMemories 返回可读 JSON。只在本地查看/另存，不默认上传。
func (d *Desktop) ExportMemories() MemoryExportResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemoryExportResult{Error: toCallError(err)}
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return MemoryExportResult{Error: toCallError(err)}
	}
	doc, err := svc.Export(ctx, workspaceID)
	if err != nil {
		return MemoryExportResult{Error: toCallError(err)}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return MemoryExportResult{Error: toCallError(err)}
	}
	return MemoryExportResult{JSON: string(raw), ItemCount: len(doc.Items)}
}

// SetMemoryScopeEnabled 按 scope 关闭/打开注入；只影响检索与注入，不删条目。
func (d *Desktop) SetMemoryScopeEnabled(scope string, enabled bool) MemoryScopeResult {
	ctx := context.Background()
	svc, err := d.memoryService()
	if err != nil {
		return MemoryScopeResult{Error: toCallError(err)}
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return MemoryScopeResult{Error: toCallError(err)}
	}
	s := memory.Scope(strings.TrimSpace(scope))
	wsKey := ""
	if s == memory.ScopeWorkspace {
		wsKey = workspaceID
	}
	if err := svc.SetScopeEnabled(ctx, s, wsKey, enabled); err != nil {
		return MemoryScopeResult{Error: toCallError(err)}
	}
	return MemoryScopeResult{State: MemoryScopeState{Scope: string(s), WorkspaceID: wsKey, Enabled: enabled}}
}

// memorySessionID 返回最近一次 run 的 session ID（没有则为空）。
func (d *Desktop) memorySessionID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runtimeSessionID
}

// withMemoryContext 为一次 run 构造受控注入。
//
// 失败一律退化为"没有注入"：记忆是参考数据，不能因为它读不出来就拦住用户发消息。
// QueryTooShort 也走这条路——短消息没有可检索的关键词，不该变成一次报错。
func (d *Desktop) withMemoryContext(ctx context.Context, message string) string {
	svc, err := d.memoryService()
	if err != nil {
		return message
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return message
	}
	mc, err := svc.BuildContext(ctx, memory.MemoryQuery{
		WorkspaceID: workspaceID,
		Text:        message,
		Limit:       memory.MaxContextEntries,
	})
	if err != nil {
		return message
	}
	return memory.AugmentPrompt(message, mc)
}

// memoryToolsForRuntime 返回（记忆服务，当前工作区 ID）；任一不可用则服务为 nil。
//
// 单独一个函数让"运行时注册工具"和"工具目录展示"读同一份解析结果，
// 不会出现目录里有 memory_save、实际 MCP 没注册的偏差。
func (d *Desktop) memoryToolsForRuntime(ctx context.Context) (*memory.Service, string) {
	svc, err := d.memoryService()
	if err != nil {
		return nil, ""
	}
	workspaceID, err := d.memoryWorkspaceID(ctx)
	if err != nil {
		return nil, ""
	}
	return svc, workspaceID
}
