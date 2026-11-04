package app

// 本文件是 C03 的绑定层：技能与扩展的列出、导入、编辑、启停、删除。
//
// 与 C01/C02 一样，错误一律结构化返回，前端按 Code 分支。
// 另有一条本文件特有的责任：如实说明"改了但还没生效"。
// 技能是 Pi 启动时加载的，运行中改配置不会影响正在跑的那一轮；
// 界面必须说清楚，否则用户会以为技能已经生效。

import (
	"context"

	"my-agent-go/internal/resources"
)

// 工作区作用域的技能目前只服务于默认工作区（运行时固定 ws-default）。
// 用常量而不是散落的字面量，将来接入真实工作区时只有一个地方要改。
const defaultWorkspaceID = "ws-default"

// SkillListResult 是技能列表。
type SkillListResult struct {
	Items    []resources.SkillResource `json:"items"`
	Revision uint64                    `json:"revision"`
	// ActiveRun 表示当前是否有 run 在使用旧配置。
	ActiveRun bool `json:"activeRun"`
	// Pending 表示"配置里的技能集合与运行中的不一致"。
	// 有 run 在跑 → 当前 run 结束后生效；没有 run → 下次运行生效。
	Pending bool `json:"pending"`
	// RuntimeSkills 是运行中的 worker **显式加载的托管技能** ID（--skill）。
	// 不含 Pi 自己发现的技能——那份清单产品没有可信来源（见 Status.SkillAutoDiscovery）。
	RuntimeSkills []string   `json:"runtimeSkills"`
	Error         *CallError `json:"error,omitempty"`
}

// SkillResult 是单条技能操作的结果。
type SkillResult struct {
	Item     resources.SkillResource `json:"item"`
	Revision uint64                  `json:"revision"`
	Pending  bool                    `json:"pending"`
	Error    *CallError              `json:"error,omitempty"`
}

// SkillContentResult 是编辑用的内容。
type SkillContentResult struct {
	Item    resources.SkillResource `json:"item"`
	Content string                  `json:"content"`
	Error   *CallError              `json:"error,omitempty"`
}

// ExtensionListResult 是扩展列表。
type ExtensionListResult struct {
	Items    []resources.Extension `json:"items"`
	Revision uint64                `json:"revision"`
	Error    *CallError            `json:"error,omitempty"`
}

// ExtensionResult 是单条扩展操作的结果。
type ExtensionResult struct {
	Item     resources.Extension `json:"item"`
	Revision uint64              `json:"revision"`
	Error    *CallError          `json:"error,omitempty"`
}

// ListSkills 返回全部技能与"是否已生效"。
func (d *Desktop) ListSkills() SkillListResult {
	out := SkillListResult{Items: []resources.SkillResource{}}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	set, err := svc.ListSkills(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Items = set.Items
	out.Revision = set.Revision
	out.Pending, out.ActiveRun, out.RuntimeSkills = d.skillRuntimeState()
	return out
}

// ImportSkill 从本地目录导入一个技能。
//
// scope 为空时按 user 处理：用户点"导入"时最常想的是"给我自己用"。
func (d *Desktop) ImportSkill(path, scope, workspaceID string) SkillResult {
	out := SkillResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if scope == "" {
		scope = string(resources.ScopeUser)
	}
	if workspaceID == "" {
		workspaceID = defaultWorkspaceID
	}
	item, err := svc.ImportSkill(context.Background(), path, resources.ResourceScope(scope), workspaceID)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = item.Revision
	out.Pending, _, _ = d.skillRuntimeState()
	return out
}

// OverwriteSkill 用新内容覆盖一条技能（显式动作，需要当前 revision）。
func (d *Desktop) OverwriteSkill(id, path string, expectedRevision uint64) SkillResult {
	out := SkillResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	item, err := svc.OverwriteSkill(context.Background(), id, path, expectedRevision)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = item.Revision
	out.Pending, _, _ = d.skillRuntimeState()
	return out
}

// ReadSkill 读取一条技能的 SKILL.md 原文，供编辑器使用。
func (d *Desktop) ReadSkill(id string) SkillContentResult {
	out := SkillContentResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	item, err := svc.GetSkill(context.Background(), id)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	content, err := resources.ReadSkillContent(item.Path)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Content = content
	return out
}

// SaveSkill 保存编辑后的 SKILL.md。
func (d *Desktop) SaveSkill(id string, expectedRevision uint64, content string) SkillResult {
	out := SkillResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	item, err := svc.SaveSkillContent(context.Background(), id, expectedRevision, content)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = item.Revision
	out.Pending, _, _ = d.skillRuntimeState()
	return out
}

// SetSkillEnabled 启用/禁用一条技能。
func (d *Desktop) SetSkillEnabled(id string, expectedRevision uint64, enabled bool) SkillResult {
	out := SkillResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	item, err := svc.SetSkillEnabled(context.Background(), id, expectedRevision, enabled)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = item.Revision
	out.Pending, _, _ = d.skillRuntimeState()
	return out
}

// DeleteSkill 删除一条技能。
func (d *Desktop) DeleteSkill(expectedRevision uint64, id string) SkillResult {
	out := SkillResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if err := svc.DeleteSkill(context.Background(), expectedRevision, id); err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Revision = expectedRevision + 1
	out.Pending, _, _ = d.skillRuntimeState()
	return out
}

// ListExtensions 返回系统扩展与第三方扩展。
func (d *Desktop) ListExtensions() ExtensionListResult {
	out := ExtensionListResult{Items: []resources.Extension{}}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	// 系统扩展路径可能因为仓库布局变化而在启动后才解析成功，这里刷新一次。
	svc.SetSystemExtension(d.systemExtensionPath())
	set, err := svc.ListExtensions(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Items = set.Items
	out.Revision = set.Revision
	return out
}

// SetExtensionEnabled 启用/禁用一条第三方扩展。
//
// 系统扩展的关闭请求会在这里被拒绝——不是靠界面不显示按钮。
func (d *Desktop) SetExtensionEnabled(id string, expectedRevision uint64, enabled bool) ExtensionResult {
	out := ExtensionResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	item, err := svc.SetExtensionEnabled(context.Background(), id, expectedRevision, enabled)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = expectedRevision + 1
	return out
}

// skillRuntimeState 返回"运行中的技能"与"配置与运行是否不一致"。
func (d *Desktop) skillRuntimeState() (pending bool, active bool, ids []string) {
	d.mu.Lock()
	digest := d.runtimeSkillDigest
	ids = append([]string{}, d.runtimeSkills...)
	svc := d.svc
	d.mu.Unlock()

	if svc != nil {
		active = svc.ActiveCount() > 0 || svc.QueuedCount() > 0
	}
	if digest == "" {
		// 还没起过运行时：谈不上"等待生效"，下次运行自然会用新配置。
		return false, active, ids
	}
	plan, err := d.compileSkills()
	if err != nil {
		// 配置读不出来时不假装"已生效"，但也不能把列表整体打挂。
		return true, active, ids
	}
	return plan.Digest != digest, active, ids
}

// compileSkills 是"当前配置下应加载哪些技能"的唯一入口。
func (d *Desktop) compileSkills() (resources.SkillPlan, error) {
	svc, err := d.resourceServices()
	if err != nil {
		return resources.SkillPlan{}, err
	}
	return svc.CompileSkills(context.Background(), defaultWorkspaceID)
}
