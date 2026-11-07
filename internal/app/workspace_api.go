package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/profile"
	"my-agent-go/internal/workspace"
)

// 本文件是 C01 的绑定层：工作区与 Profile。
//
// 跨边界错误一律结构化返回，而不是靠 Wails 把 error 转成字符串。
// 理由是前端必须按 Code 分支（冲突要展示差异、缺依赖要给修复路径），
// 而错误文案会随措辞调整变化，靠解析文案判断错误类型是迟早会坏的做法。

// CallError 是绑定层统一的错误载体。
//
// 用独立类型而不是直接返回 contracts.AppError：它是跨进程协议的一部分，
// 字段一旦要加（比如 hint），不应该跟着 contracts 一起动。
//
// Params/Operation/Cause 与 contracts.AppError 同构：跨边界时原样带出，
// 前端只按 Code + Params 取消息表，绝不解析 Message 文案。
type CallError struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable"`
	Params    map[string]string `json:"params,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Cause     string            `json:"cause,omitempty"`
}

func toCallError(err error) *CallError {
	if err == nil {
		return nil
	}
	var appErr *contracts.AppError
	if errors.As(err, &appErr) {
		return &CallError{
			Code:      appErr.Code,
			Message:   appErr.Message,
			Retryable: appErr.Retryable,
			Params:    appErr.Params,
			Operation: appErr.Operation,
			Cause:     appErr.Cause,
		}
	}
	// 非契约错误也要原样带出：吞成"操作失败"会让排查无从下手。
	return &CallError{Code: contracts.CodeInternal, Message: err.Error(), Retryable: false}
}

// WorkspaceListResult 是列举工作区的结果。
type WorkspaceListResult struct {
	Items []workspace.Workspace `json:"items"`
	Error *CallError            `json:"error,omitempty"`
}

// WorkspaceResult 是单个工作区操作的结果。
type WorkspaceResult struct {
	Item  workspace.Workspace `json:"item"`
	Error *CallError          `json:"error,omitempty"`
}

// ProfileListResult 是列举 Profile 的结果。
type ProfileListResult struct {
	Items []profile.Profile `json:"items"`
	Error *CallError        `json:"error,omitempty"`
}

// ProfileResult 是单个 Profile 操作的结果。
type ProfileResult struct {
	Item  profile.Profile `json:"item"`
	Error *CallError      `json:"error,omitempty"`
}

// ProfileFieldDiff 是一处字段差异。
type ProfileFieldDiff struct {
	Field  string `json:"field"`
	Mine   string `json:"mine"`
	Theirs string `json:"theirs"`
}

// ProfileConflictResult 是保存冲突时的现场：磁盘上的当前版本 + 与草稿的差异。
//
// 冲突只报"revision 不一致"没有用——用户需要看见哪几个字段被改了，
// 才能决定是放弃自己的改动还是另存一份。
type ProfileConflictResult struct {
	Current profile.Profile    `json:"current"`
	Diff    []ProfileFieldDiff `json:"diff"`
	Error   *CallError         `json:"error,omitempty"`
}

// coreServices 懒加载配置存储与两个服务。
//
// 不放在 Desktop 构造里做：没有 Pi、没有凭证时用户仍然要能打开设置和看历史
// （UI01），而配置存储本身不依赖 Pi 是否装好。
func (d *Desktop) coreServices() (*workspace.Service, *profile.Service, error) {
	d.svcMu.Lock()
	defer d.svcMu.Unlock()
	if d.wsSvc != nil && d.profileSvc != nil {
		return d.wsSvc, d.profileSvc, nil
	}
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return nil, nil, err
	}
	d.wsSvc = workspace.New(store)
	d.profileSvc = profile.New(store)
	return d.wsSvc, d.profileSvc, nil
}

// ListWorkspaces 返回已添加的工作区。
func (d *Desktop) ListWorkspaces() WorkspaceListResult {
	ws, _, err := d.coreServices()
	if err != nil {
		return WorkspaceListResult{Items: []workspace.Workspace{}, Error: toCallError(err)}
	}
	items, err := ws.List(context.Background())
	if err != nil {
		return WorkspaceListResult{Items: []workspace.Workspace{}, Error: toCallError(err)}
	}
	return WorkspaceListResult{Items: items}
}

// CreateWorkspace 添加工作区；同一真实路径重复添加返回已有条目。
func (d *Desktop) CreateWorkspace(path string) WorkspaceResult {
	ws, _, err := d.coreServices()
	if err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	item, err := ws.CreateWorkspace(context.Background(), path)
	if err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	return WorkspaceResult{Item: item}
}

// SetWorkspaceTrust 设置工作区信任状态。
func (d *Desktop) SetWorkspaceTrust(id string, trusted bool) WorkspaceResult {
	ws, _, err := d.coreServices()
	if err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	ctx := context.Background()
	if err := ws.SetTrust(ctx, contracts.WorkspaceID(id), trusted); err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	// 回读而不是把入参拼回去：CAS 成功不代表磁盘上是这个值（比如被并发改过）。
	item, err := ws.Get(ctx, contracts.WorkspaceID(id))
	if err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	return WorkspaceResult{Item: item}
}

// CurrentWorkspace 返回设置里的工作区（必要时登记一次）。
//
// 前端要判断"左栏里哪一项是当前项目"，直接比对路径字符串是不行的：
// 设置里存的是用户输入的原样路径（可能带尾斜杠、大小写不同、是软链接），
// 而列表里是规范化后的真实路径。让后端做一次规范化再返回，
// 比在前端复刻一套路径规则可靠得多。
//
// 注意它不检查信任状态：未信任也是"当前工作区"，只是不能运行。
func (d *Desktop) CurrentWorkspace() WorkspaceResult {
	d.mu.Lock()
	path := strings.TrimSpace(d.settings.Workspace)
	d.mu.Unlock()
	if path == "" {
		return WorkspaceResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeNotFound, "尚未设置工作区", false))}
	}
	ws, _, err := d.coreServices()
	if err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	item, err := ws.CreateWorkspace(context.Background(), path)
	if err != nil {
		return WorkspaceResult{Error: toCallError(err)}
	}
	return WorkspaceResult{Item: item}
}

// ListProfiles 返回全部 Profile。
func (d *Desktop) ListProfiles() ProfileListResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileListResult{Items: []profile.Profile{}, Error: toCallError(err)}
	}
	items, err := ps.List(context.Background())
	if err != nil {
		return ProfileListResult{Items: []profile.Profile{}, Error: toCallError(err)}
	}
	return ProfileListResult{Items: items}
}

// GetProfile 读取单个 Profile。
func (d *Desktop) GetProfile(id string) ProfileResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	item, err := ps.Get(context.Background(), contracts.ProfileID(id))
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	return ProfileResult{Item: item}
}

// SaveProfile 以 CAS 保存 Profile。
//
// expectedRevision 由前端从"读到的那一版"带上；冲突时返回 CONFIG_CONFLICT，
// 前端再调 ProfileConflict 拿差异。
func (d *Desktop) SaveProfile(id, workspaceID string, expectedRevision uint64, p profile.Profile) ProfileResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	item, err := ps.Save(context.Background(), contracts.ProfileID(id), contracts.WorkspaceID(workspaceID), expectedRevision, p)
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	return ProfileResult{Item: item}
}

// ProfileConflict 返回磁盘上的当前版本与草稿的逐字段差异。
//
// 只在保存冲突之后调用：它读的是"现在磁盘上是什么"，
// 所以必须在冲突发生后重新读，不能拿保存前的快照充数。
func (d *Desktop) ProfileConflict(id string, draft profile.Profile) ProfileConflictResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileConflictResult{Error: toCallError(err)}
	}
	current, err := ps.Get(context.Background(), contracts.ProfileID(id))
	if err != nil {
		return ProfileConflictResult{Error: toCallError(err)}
	}
	return ProfileConflictResult{Current: current, Diff: diffProfiles(draft, current)}
}

// diffProfiles 逐字段比较草稿与当前版本。
//
// 只比较用户可编辑的语义字段，不比 Revision：revision 必然不同，
// 把它列进差异里只会淹没真正重要的字段。
func diffProfiles(mine, theirs profile.Profile) []ProfileFieldDiff {
	out := []ProfileFieldDiff{}
	add := func(field, a, b string) {
		if a != b {
			out = append(out, ProfileFieldDiff{Field: field, Mine: a, Theirs: b})
		}
	}
	add("name", mine.Name, theirs.Name)
	add("modelRef", mine.ModelRef, theirs.ModelRef)
	add("skillIDs", strings.Join(mine.SkillIDs, ","), strings.Join(theirs.SkillIDs, ","))
	add("mcpIDs", strings.Join(mine.MCPIDs, ","), strings.Join(theirs.MCPIDs, ","))
	add("toolPolicy",
		describeToolPolicy(mine.ToolPolicy),
		describeToolPolicy(theirs.ToolPolicy))
	add("memoryPolicy",
		describeMemoryPolicy(mine.MemoryPolicy),
		describeMemoryPolicy(theirs.MemoryPolicy))
	return out
}

func describeToolPolicy(t profile.ToolPolicy) string {
	return "filesystem=" + t.Filesystem +
		", network=" + boolText(t.Network) +
		", shell=" + boolText(t.Shell)
}

func describeMemoryPolicy(m profile.MemoryPolicy) string {
	return "enabled=" + boolText(m.Enabled) + ", maxEntries=" + strconv.Itoa(m.MaxEntries)
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// AssertProfileRunnable 供 Start 之前调用：工作区未信任或模型缺失时拒绝启动。
//
// 放在绑定层而不是 UI 里：UI 可以被绕过，而"不信任不执行"必须是数据层不变量。
func (d *Desktop) assertRunnable(ctx context.Context, workspaceID, profileID string) error {
	ws, ps, err := d.coreServices()
	if err != nil {
		return err
	}
	if err := ws.AssertRunnable(ctx, contracts.WorkspaceID(workspaceID)); err != nil {
		return err
	}
	return ps.AssertRunnable(ctx, contracts.ProfileID(profileID), contracts.WorkspaceID(workspaceID))
}
