package app

// U03 的绑定层：单个 Agent/Profile 的产品编辑面。
//
// 这里补的是 C01 领域之外、但"产品化编辑"必须有的四件事：
//
//  1. 上限比较：profile 只能收紧系统与工作区上限。领域层（internal/profile）
//     没有这道检查——`config.Merge` 对越界取值是**静默夹回**，于是"我在 UI 上
//     打开了 shell"会变成"实际没打开"，而且没有任何提示。所以在写入前比较，
//     越界直接结构化拒绝，并带上足够解释的 params。
//  2. 来源：每个引用值来自哪一层（default/user/workspace）要能查；查不到就是
//     `unavailable`，不能悄悄回落到"看起来有值"。
//  3. 复制：只复制引用与策略，不复制 revision、不复制任何像秘密的字段。
//  4. 删除：按 revision 确认（不是"点一下就没了"），且只删 profile 文档本身，
//     绝不连带删除它引用的模型/技能/MCP。
//
// 纪律：
//   - 秘密、引用解析、CAS 仍由 internal/profile 负责，这里不重写一遍；
//   - 跨边界错误一律 toCallError，带 Code + Params，前端按 code 分支取文案；
//   - 这里不做任何"乐观成功"：所有返回值都来自磁盘回读或服务返回。

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/profile"
)

// agentUnavailableFields 是 V1 领域模型还不承载、界面必须显示 unavailable 的字段。
//
// 为什么不是"先画上去、等以后再接"：`profile.Profile` 没有这些字段，
// 前端写进 JSON 后会被 `json.Unmarshal` 静默丢掉——用户会以为保存成功了。
// 显示 unavailable 至少说的是实话；真正补齐要改 internal/profile（本轮不在
// 写入范围，已在报告里登记为缺口）。
var agentUnavailableFields = []string{"description", "systemPrompt", "reasoningLevel", "toolIDs"}

// AgentCeiling 是 profile 可用的权限上限（系统 ∩ 工作区）。
//
// Sources 逐字段给出"这个上限是谁定的"：`system`（应用策略）或某个配置层。
// Available=false 表示没有任何配置层显式设置过权限，上限完全来自应用策略。
type AgentCeiling struct {
	Filesystem string            `json:"filesystem"`
	Network    bool              `json:"network"`
	Shell      bool              `json:"shell"`
	Sources    map[string]string `json:"sources"`
	Available  bool              `json:"available"`
}

// AgentSource 是一个值的来源。
type AgentSource struct {
	// Field 是逻辑字段名：modelRef / skillIDs / mcpIDs。
	Field string `json:"field"`
	// Value 是被引用的资源 ID。
	Value string `json:"value"`
	// Source 取值：system / default / user / workspace / unavailable。
	Source string `json:"source"`
	// Available=false 表示该引用现在解析不到（被删除或禁用）。
	Available bool `json:"available"`
}

// AgentMissingRef 是一条解析不到的引用。
type AgentMissingRef struct {
	Kind string `json:"kind"` // model / skill / mcp
	ID   string `json:"id"`
}

// AgentEditorState 是编辑器打开时需要的一份事实：当前文档、上限、来源、缺口。
type AgentEditorState struct {
	Profile           profile.Profile   `json:"profile"`
	Ceiling           AgentCeiling      `json:"ceiling"`
	Sources           []AgentSource     `json:"sources"`
	Missing           []AgentMissingRef `json:"missing,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	UnavailableFields []string          `json:"unavailableFields"`
	Error             *CallError        `json:"error,omitempty"`
}

// lowerConfig 是"不含本 profile"的下层配置快照（default/user/workspace）。
//
// 刻意不用 `Store.Compile(ProfileID:"")`：Compile 用 `ProfileKey("")` 得到的
// key 是 `config-profile-default`，也就是说一个恰好叫 default 的 profile 会
// 混进"上限"里——上限被被检查的对象改写，检查就失去意义。
type lowerConfig struct {
	values     config.Values
	provenance map[string]string
	warnings   []string
	layers     int
}

func (d *Desktop) lowerConfig(ctx context.Context, wsID contracts.WorkspaceID) (lowerConfig, error) {
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return lowerConfig{}, err
	}
	specs := []struct {
		name config.LayerName
		key  string
	}{
		{config.LayerDefault, config.KeyDefault},
		{config.LayerUser, config.KeyUser},
		{config.LayerWorkspace, config.WorkspaceKey(wsID)},
	}
	layers := make([]config.Layer, 0, len(specs))
	for _, spec := range specs {
		doc, loadErr := store.Load(ctx, spec.key)
		if loadErr != nil {
			return lowerConfig{}, loadErr
		}
		if doc.Revision == 0 || len(doc.Body) == 0 {
			continue // 该层没有文档 = 没说话
		}
		layers = append(layers, config.Layer{Name: spec.name, Body: doc.Body})
	}
	merged, err := config.Merge(layers)
	if err != nil {
		return lowerConfig{}, err
	}
	return lowerConfig{
		values:     merged.Values,
		provenance: merged.Provenance,
		warnings:   merged.Warnings,
		layers:     len(layers),
	}, nil
}

// agentCeiling 计算 profile 的上限：应用策略与配置层取更严的一侧。
func (d *Desktop) agentCeiling(lower lowerConfig) AgentCeiling {
	system := effectivePermissions(d.Settings())
	out := AgentCeiling{
		Filesystem: system.Filesystem,
		Network:    system.Network,
		Shell:      system.Shell,
		Sources: map[string]string{
			"permissions.filesystem": "system",
			"permissions.network":    "system",
			"permissions.shell":      "system",
		},
	}
	// 只有**确实有层显式说过话**的字段才参与收紧。
	// Merge 对没人设置的字段用"未设置哨兵"起算，那是最严默认值而不是上限：
	// 照它比较会让 filesystem 永远停在 none，等于把整个权限编辑锁死。
	if layer, ok := lower.provenance["permissions.filesystem"]; ok {
		out.Filesystem = stricterFilesystem(system.Filesystem, lower.values.Permissions.Filesystem)
		out.Sources["permissions.filesystem"] = layer
		out.Available = true
	}
	if layer, ok := lower.provenance["permissions.network"]; ok {
		out.Network = system.Network && lower.values.Permissions.Network
		out.Sources["permissions.network"] = layer
		out.Available = true
	}
	if layer, ok := lower.provenance["permissions.shell"]; ok {
		out.Shell = system.Shell && lower.values.Permissions.Shell
		out.Sources["permissions.shell"] = layer
		out.Available = true
	}
	return out
}

// filesystemRank 给出文件系统权限的宽严序。
//
// 与 config.Merge 内部的 rank 必须一致（那里是私有的）。
// `TestAgentFilesystemOrderMatchesMerge` 用 Merge 的实际行为逐对校验这张表，
// 防止两边分叉后出现"UI 认为合规、Merge 静默收紧"。
func filesystemRank(v string) int {
	name, ok := config.NormalizeFilesystem(v)
	if !ok {
		return 0 // 不认识的一律按最严处理（与 Merge 的安全默认一致）
	}
	switch name {
	case "unrestricted":
		return 3
	case "workspace":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

// filesystemAtMost 判断草稿是否不比上限更宽。
func filesystemAtMost(draft, ceiling string) bool {
	return filesystemRank(draft) <= filesystemRank(ceiling)
}

func stricterFilesystem(a, b string) string {
	na, _ := config.NormalizeFilesystem(a)
	nb, _ := config.NormalizeFilesystem(b)
	if filesystemRank(na) <= filesystemRank(nb) {
		return na
	}
	return nb
}

// ceilingViolation 把"越界"变成结构化错误：字段名、请求值、上限值、上限来源。
//
// 只说"没有权限"是不够的：用户需要知道是哪一项、为什么不行、该去哪里改。
// 文案本身在前端（catalog），这里只给 Code + Params。
func ceilingViolation(field, requested, ceiling, source string) error {
	return contracts.NewAppError(
		contracts.CodeInvalidInput,
		"profile 的 "+field+" 超过上限：请求 "+requested+"，上限 "+ceiling+"（来源 "+source+"）；"+
			"profile 只能收紧权限，放开请改上层配置或应用策略",
		false,
	).WithParams(map[string]string{
		"field":     field,
		"requested": requested,
		"ceiling":   ceiling,
		"source":    source,
	}).WithOperation("agent.save")
}

// assertAgentCeiling 在写入前比较上限。
func (d *Desktop) assertAgentCeiling(ctx context.Context, wsID contracts.WorkspaceID, draft profile.Profile) error {
	lower, err := d.lowerConfig(ctx, wsID)
	if err != nil {
		return err
	}
	ceiling := d.agentCeiling(lower)

	requested, ok := config.NormalizeFilesystem(draft.ToolPolicy.Filesystem)
	if !ok {
		return contracts.NewAppError(
			contracts.CodeInvalidInput,
			"toolPolicy.filesystem 非法: "+draft.ToolPolicy.Filesystem+"（允许 none/read/workspace/unrestricted）",
			false,
		).WithParams(map[string]string{
			"field":     "permissions.filesystem",
			"requested": draft.ToolPolicy.Filesystem,
			"ceiling":   ceiling.Filesystem,
			"source":    ceiling.Sources["permissions.filesystem"],
		}).WithOperation("agent.save")
	}
	if !filesystemAtMost(requested, ceiling.Filesystem) {
		return ceilingViolation("permissions.filesystem", requested, ceiling.Filesystem,
			ceiling.Sources["permissions.filesystem"])
	}
	if draft.ToolPolicy.Network && !ceiling.Network {
		return ceilingViolation("permissions.network", "true", boolText(ceiling.Network),
			ceiling.Sources["permissions.network"])
	}
	if draft.ToolPolicy.Shell && !ceiling.Shell {
		return ceilingViolation("permissions.shell", "true", boolText(ceiling.Shell),
			ceiling.Sources["permissions.shell"])
	}
	return nil
}

// AgentCeilingResult 是上限查询的结果。
//
// 新建 profile 时还读不到文档，但界面已经需要知道"能选到哪一档"，
// 所以上限单独可查，而不是只能从某一份已存在的 profile 反推。
type AgentCeilingResult struct {
	Ceiling           AgentCeiling `json:"ceiling"`
	UnavailableFields []string     `json:"unavailableFields"`
	Error             *CallError   `json:"error,omitempty"`
}

// AgentProfileCeiling 单独返回上限（新建时也需要的同一份事实）。
func (d *Desktop) AgentProfileCeiling(workspaceID string) AgentCeilingResult {
	lower, err := d.lowerConfig(context.Background(), contracts.WorkspaceID(workspaceID))
	if err != nil {
		return AgentCeilingResult{UnavailableFields: agentUnavailableFields, Error: toCallError(err)}
	}
	return AgentCeilingResult{
		Ceiling:           d.agentCeiling(lower),
		UnavailableFields: agentUnavailableFields,
	}
}

// AgentEditorState 读取一份可编辑的现场：文档 + 上限 + 来源 + 缺口。
func (d *Desktop) AgentEditorState(id, workspaceID string) AgentEditorState {
	_, ps, err := d.coreServices()
	if err != nil {
		return AgentEditorState{UnavailableFields: agentUnavailableFields, Error: toCallError(err)}
	}
	ctx := context.Background()
	item, err := ps.Get(ctx, contracts.ProfileID(strings.TrimSpace(id)))
	if err != nil {
		return AgentEditorState{UnavailableFields: agentUnavailableFields, Error: toCallError(err)}
	}
	lower, err := d.lowerConfig(ctx, contracts.WorkspaceID(workspaceID))
	if err != nil {
		return AgentEditorState{Profile: item, UnavailableFields: agentUnavailableFields, Error: toCallError(err)}
	}
	out := AgentEditorState{
		Profile:           item,
		Ceiling:           d.agentCeiling(lower),
		Warnings:          lower.warnings,
		UnavailableFields: agentUnavailableFields,
	}
	out.Sources, out.Missing = agentSources(item, lower)
	return out
}

// agentSources 给出每个引用值的来源与缺失项。
func agentSources(p profile.Profile, lower lowerConfig) ([]AgentSource, []AgentMissingRef) {
	sources := make([]AgentSource, 0, 2+len(p.SkillIDs)+len(p.MCPIDs))
	missing := make([]AgentMissingRef, 0)

	add := func(field, value, kind, provenanceKey string) {
		source, ok := lower.provenance[provenanceKey]
		if !ok {
			sources = append(sources, AgentSource{Field: field, Value: value, Source: "unavailable", Available: false})
			missing = append(missing, AgentMissingRef{Kind: kind, ID: value})
			return
		}
		sources = append(sources, AgentSource{Field: field, Value: value, Source: source, Available: true})
	}

	if strings.TrimSpace(p.ModelRef) != "" {
		add("modelRef", p.ModelRef, "model", "models."+p.ModelRef)
	}
	for _, id := range p.SkillIDs {
		add("skillIDs", id, "skill", "skills."+id)
	}
	for _, id := range p.MCPIDs {
		add("mcpIDs", id, "mcp", "mcp."+id)
	}
	sort.SliceStable(missing, func(i, j int) bool {
		if missing[i].Kind != missing[j].Kind {
			return missing[i].Kind < missing[j].Kind
		}
		return missing[i].ID < missing[j].ID
	})
	return sources, missing
}

// SaveAgentProfile 在通过上限检查后以 CAS 保存 profile。
//
// 秘密字段、引用解析与 revision 冲突仍由 profile.Service 判定；这里只额外
// 守住"不能比上限更宽"，并让拒绝带上可解释的 params。
func (d *Desktop) SaveAgentProfile(id, workspaceID string, expectedRevision uint64, draft profile.Profile) ProfileResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ProfileResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeInvalidInput, "profile ID 不能为空", false).
			WithParams(map[string]string{"field": "id"}).WithOperation("agent.save"))}
	}
	ctx := context.Background()
	if err := d.assertAgentCeiling(ctx, contracts.WorkspaceID(workspaceID), draft); err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	item, err := ps.Save(ctx, contracts.ProfileID(id), contracts.WorkspaceID(workspaceID), expectedRevision, draft)
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	return ProfileResult{Item: item}
}

// DuplicateAgentProfile 以新 ID 复制一份 profile。
//
// 复制的是**引用与策略**：模型、技能、MCP、权限上限与记忆设置。revision 从 0
// 开始（新文档），源文档里出现的任何秘密形状字段都不会被带过来——它们本来
// 就不在领域模型里，重建文档时只会写入领域字段。
func (d *Desktop) DuplicateAgentProfile(sourceID, newID, workspaceID string) ProfileResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	sourceID = strings.TrimSpace(sourceID)
	newID = strings.TrimSpace(newID)
	if newID == "" {
		return ProfileResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeInvalidInput, "复制目标 ID 不能为空", false).
			WithParams(map[string]string{"field": "id"}).WithOperation("agent.duplicate"))}
	}
	if newID == sourceID {
		return ProfileResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeInvalidInput, "复制目标 ID 不能与源相同", false).
			WithParams(map[string]string{"field": "id", "value": newID}).WithOperation("agent.duplicate"))}
	}
	ctx := context.Background()
	source, err := ps.Get(ctx, contracts.ProfileID(sourceID))
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	draft := source
	draft.ID = contracts.ProfileID(newID)
	draft.Revision = 0
	if err := d.assertAgentCeiling(ctx, contracts.WorkspaceID(workspaceID), draft); err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	item, err := ps.Save(ctx, contracts.ProfileID(newID), contracts.WorkspaceID(workspaceID), 0, draft)
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	return ProfileResult{Item: item}
}

// RenameAgentProfile 以 CAS 改名。
//
// 改名不改策略：这里**不**做上限检查——上限可能在保存之后被上层收紧，
// 那不该让"改个名字"失败；越界由编辑器的权限摘要提示，由用户显式收紧。
func (d *Desktop) RenameAgentProfile(id, workspaceID string, expectedRevision uint64, name string) ProfileResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProfileResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeInvalidInput, "profile 名称不能为空", false).
			WithParams(map[string]string{"field": "name"}).WithOperation("agent.rename"))}
	}
	ctx := context.Background()
	current, err := ps.Get(ctx, contracts.ProfileID(strings.TrimSpace(id)))
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	current.Name = name
	current.Revision = 0
	item, err := ps.Save(ctx, contracts.ProfileID(strings.TrimSpace(id)),
		contracts.WorkspaceID(workspaceID), expectedRevision, current)
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	return ProfileResult{Item: item}
}

// DeleteAgentProfile 按 revision 删除 profile 文档。
//
// 两步而不是一步：
//  1. 用一次 CAS（把当前 body 原样写回，revision+1）占住这一版——其余写者
//     手上的旧 revision 从此会冲突，不会在删除的同时把内容写回来；
//  2. 删除文件。第二步失败时文档仍停在 revision+1，数据没丢，只报失败。
//
// 只删 profile 这一份文档：它引用的模型/技能/MCP 定义属于别的层，删 profile
// 不得连带删除（否则"删一个 Agent"会静默毁掉资源库里还在被别处引用的定义）。
func (d *Desktop) DeleteAgentProfile(id string, expectedRevision uint64) ProfileResult {
	_, ps, err := d.coreServices()
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	ctx := context.Background()
	pid := contracts.ProfileID(strings.TrimSpace(id))
	current, err := ps.Get(ctx, pid)
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	if current.Revision != expectedRevision {
		return ProfileResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeConfigConflict,
			"profile "+string(pid)+" revision 冲突：期望 "+strconv.FormatUint(expectedRevision, 10)+
				"，实际 "+strconv.FormatUint(current.Revision, 10)+"；请重新读取后再删除",
			false,
		).WithParams(map[string]string{"field": "revision"}).WithOperation("agent.delete"))}
	}

	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	key := config.ProfileKey(pid)
	doc, err := store.Load(ctx, key)
	if err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	if _, err := store.CompareAndSwap(ctx, key, doc.Revision, doc.Body); err != nil {
		return ProfileResult{Error: toCallError(err)}
	}
	path := filepath.Join(store.Root(), key+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ProfileResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeInternal, "删除 profile 文档失败: "+err.Error(), false).
			WithOperation("agent.delete"))}
	}
	return ProfileResult{Item: current}
}
