package app

// 本文件是 C03 的绑定层：技能与扩展的列出、导入、编辑、启停、删除。
//
// 与 C01/C02 一样，错误一律结构化返回，前端按 Code 分支。
// 另有一条本文件特有的责任：如实说明"改了但还没生效"。
// 技能是 Pi 启动时加载的，运行中改配置不会影响正在跑的那一轮；
// 界面必须说清楚，否则用户会以为技能已经生效。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/resources"
	"my-agent-go/internal/run"
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
	// 链接导入的暂存副本在导入成功后清掉：它是"用户还没确认"的临时下载，
	// 留着只会把应用数据目录当缓存。只清理 skill-links 下的路径，别的一律不碰。
	_ = resources.SweepStagedLinks(d.root, path)
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

// ---------------------------------------------------------------- U04：链接导入与隔离试运行

// newSkillLinkInspector 构造链接检查器。
//
// 留成变量而不是直接 new：测试需要注入可控的解析器/拨号器来证明
// DNS rebinding、重定向逃逸与 zip bomb 都会拒绝（生产路径仍是系统解析器）。
var newSkillLinkInspector = func(root string) *resources.LinkInspector {
	return resources.NewLinkInspector(root)
}

// SkillLinkRejection 是一条可逐项展示的拒绝原因（规则 ID + 诊断参数）。
type SkillLinkRejection struct {
	Rule   string            `json:"rule"`
	Params map[string]string `json:"params,omitempty"`
}

// SkillLinkInspection 是"检查链接"的结果。
//
// 拒绝走 Rejections（逐条），Error 只表示检查过程本身出了故障。
// StagedPath 只在没有拒绝时有值：确认后才把它交给 ImportSkill。
type SkillLinkInspection struct {
	URL                    string               `json:"url"`
	FinalURL               string               `json:"finalUrl"`
	Form                   string               `json:"form"`
	Name                   string               `json:"name"`
	Description            string               `json:"description"`
	Requires               []string             `json:"requires"`
	DisableModelInvocation bool                 `json:"disableModelInvocation"`
	Hash                   string               `json:"hash"`
	Bytes                  int64                `json:"bytes"`
	Files                  int                  `json:"files"`
	StagedPath             string               `json:"stagedPath"`
	Redirects              []string             `json:"redirects"`
	Warnings               []string             `json:"warnings"`
	Rejections             []SkillLinkRejection `json:"rejections"`
	Error                  *CallError           `json:"error,omitempty"`
}

// InspectSkillLink 只取 HTTPS 上的单个 SKILL.md 或 zip/tar.gz，解析清单并暂存。
//
// 不执行任何脚本、不调用 git、不写托管目录；用户在界面上确认后才导入。
func (d *Desktop) InspectSkillLink(rawURL string) SkillLinkInspection {
	out := SkillLinkInspection{
		URL:        strings.TrimSpace(rawURL),
		Requires:   []string{},
		Redirects:  []string{},
		Warnings:   []string{},
		Rejections: []SkillLinkRejection{},
	}
	insp, err := newSkillLinkInspector(d.root).Inspect(context.Background(), rawURL)
	out.FinalURL = insp.FinalURL
	out.Form = insp.Form
	out.Name = insp.Name
	out.Description = insp.Description
	out.Requires = append(out.Requires, insp.Requires...)
	out.DisableModelInvocation = insp.DisableModelInvocation
	out.Hash = insp.Hash
	out.Bytes = insp.Bytes
	out.Files = insp.Files
	out.StagedPath = insp.StagedSkillPath
	out.Redirects = append(out.Redirects, insp.Redirects...)
	out.Warnings = append(out.Warnings, insp.Warnings...)
	for _, r := range insp.Rejections {
		out.Rejections = append(out.Rejections, SkillLinkRejection{Rule: r.Rule, Params: r.Params})
	}
	if err != nil {
		if _, isRule := resources.AsLinkRejections(err); !isRule {
			out.Error = toCallError(err)
			out.StagedPath = ""
		}
	}
	return out
}

// CreateSkill 在托管目录里新建一条本地技能。
func (d *Desktop) CreateSkill(name, scope, workspaceID string) SkillResult {
	out := SkillResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if strings.TrimSpace(scope) == "" {
		scope = string(resources.ScopeUser)
	}
	if strings.TrimSpace(workspaceID) == "" {
		workspaceID = defaultWorkspaceID
	}
	item, err := svc.CreateSkill(context.Background(), name, resources.ResourceScope(scope), workspaceID)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = item.Revision
	out.Pending, _, _ = d.skillRuntimeState()
	return out
}

// SkillDryRunPolicy 是试运行的权限事实（界面照实展示，不写"安全"两个字）。
type SkillDryRunPolicy struct {
	Filesystem         string   `json:"filesystem"`
	Network            bool     `json:"network"`
	Shell              bool     `json:"shell"`
	Approvals          string   `json:"approvals"`
	DeniedCapabilities []string `json:"deniedCapabilities"`
}

// SkillDryRunDenial 是试运行里被拒绝的一次工具调用。
type SkillDryRunDenial struct {
	ToolName string `json:"toolName"`
	Rule     string `json:"rule"`
}

// SkillDryRunResult 是一次隔离试运行的结果。
type SkillDryRunResult struct {
	ItemID             string              `json:"itemId"`
	State              string              `json:"state"`
	Output             string              `json:"output"`
	Policy             SkillDryRunPolicy   `json:"policy"`
	Denials            []SkillDryRunDenial `json:"denials"`
	IsolationArgs      []string            `json:"isolationArgs"`
	TempCleaned        bool                `json:"tempCleaned"`
	SkillUnchanged     bool                `json:"skillUnchanged"`
	UserStateUnchanged bool                `json:"userStateUnchanged"`
	DurationMs         int64               `json:"durationMs"`
	Limitations        []string            `json:"limitations"`
	Error              *CallError          `json:"error,omitempty"`
}

// dryRunLimitations 是试运行**做不到**的事，如实交给界面展示。
//
// Pi 的用户级技能自动发现保持打开（ADR 0003），试运行无法只关项目级；
// 因此"只挂载了这一份托管技能"说的是托管通道，不是 Pi 能看见的全部技能。
func dryRunLimitations() []string {
	return []string{"DRYRUN_LIMIT_USER_SKILL_DISCOVERY"}
}

// DryRunSkill 在隔离沙箱里跑一次试运行。
//
// 用户显式触发（并已知晓 prompt 与技能内容会发给 provider）；结束后无论成败
// 都清理临时目录，并核对托管技能与用户目录 hash 没有变化。
func (d *Desktop) DryRunSkill(id, prompt string) SkillDryRunResult {
	started := time.Now()
	out := SkillDryRunResult{
		ItemID:        strings.TrimSpace(id),
		State:         "blocked",
		Denials:       []SkillDryRunDenial{},
		IsolationArgs: []string{},
		Limitations:   dryRunLimitations(),
	}
	fail := func(err error) SkillDryRunResult {
		out.Error = toCallError(err)
		out.DurationMs = time.Since(started).Milliseconds()
		return out
	}
	if strings.TrimSpace(prompt) == "" {
		return fail(contracts.NewAppError(contracts.CodeUnsupported, "试运行需要一段 prompt", false))
	}
	svc, err := d.resourceServices()
	if err != nil {
		return fail(err)
	}
	item, err := svc.GetSkill(context.Background(), out.ItemID)
	if err != nil {
		return fail(err)
	}
	policyExt := d.systemExtensionPath()
	if policyExt == "" {
		return fail(contracts.NewAppError(contracts.CodeConfigMissing,
			"找不到策略扩展；没有执行前授权就不能试运行任何技能", false))
	}

	sandbox, err := resources.BeginDryRun(d.root, item.ID, filepath.Dir(item.Path))
	if err != nil {
		return fail(err)
	}
	defer func() { _ = sandbox.Cleanup() }()

	settings := d.Settings()
	userPaths := []string{d.agentDir(), settings.Workspace}
	before, err := resources.TreeDigest(userPaths...)
	if err != nil {
		return fail(err)
	}
	// 结果里始终回填策略事实：即使后面 blocked，也要能说清"试运行本来会禁用什么"。
	out.Policy = SkillDryRunPolicy{
		Filesystem: sandbox.Permissions().Filesystem,
		Network:    sandbox.Permissions().Network,
		Shell:      sandbox.Permissions().Shell,
		Approvals:  "deny",
		DeniedCapabilities: []string{
			resources.DryRunCapabilityWrite, resources.DryRunCapabilityShell,
			resources.DryRunCapabilityNetwork, resources.DryRunCapabilityBrowser,
			resources.DryRunCapabilityConnectors, resources.DryRunCapabilityExternalMCP,
		},
	}

	setup, err := d.resolveModelSetup(context.Background())
	if err != nil {
		return fail(err)
	}
	inst, err := d.resolveInstallation(settings)
	if err != nil {
		return fail(err)
	}
	if err := run.EnsureAgentDir(sandbox.AgentDir()); err != nil {
		return fail(err)
	}
	if err := resources.WritePiModels(sandbox.AgentDir(), resources.PiModels{JSON: setup.ModelsJSON}); err != nil {
		return fail(err)
	}
	pol, err := sandbox.Policy()
	if err != nil {
		return fail(err)
	}
	iso := sandbox.Isolation(policyExt)
	if err := iso.Validate(); err != nil {
		return fail(err)
	}
	out.IsolationArgs = iso.Args()

	d.mu.Lock()
	workerStarter := d.workerStarter
	d.mu.Unlock()

	perm := sandbox.Permissions()
	drySvc := run.New(run.Options{
		WorkspaceID:  "ws-dryrun",
		SessionID:    "sess-dryrun",
		WorkspaceDir: sandbox.WorkspaceDir(),
		AgentDir:     sandbox.AgentDir(),
		Installation: inst,
		Isolation:    iso,
		Provider:     setup.Provider,
		Model:        setup.Model,
		ModelEnv:     setup.Env,
		Policy:       pol,
		PolicyCeiling: run.PolicyCeiling{
			Permissions: perm,
			Ref:         "ceiling:dryrun",
		},
		PolicySnapshot: run.PolicySnapshot{Permissions: perm},
		// 试运行不落 run 索引：它不属于用户会话历史，混进去会让"最近会话"凭空多一条。
		History:        nil,
		StartWorker:    workerStarter,
		MaxToolCalls:   8,
		MaxRunDuration: 3 * time.Minute,
	})

	events := make(chan contracts.Event, 64)
	unsubscribe := drySvc.Subscribe(func(ev contracts.Event) {
		select {
		case events <- ev:
		default:
			// 订阅端不阻塞事件泵：试运行只需要"有没有 deny / 有没有文本"，
			// 丢一条文本不影响结论。
		}
	})
	defer unsubscribe()

	snap, err := drySvc.Start(context.Background(), run.StartRunRequest{
		Message:     prompt,
		SessionID:   "sess-dryrun",
		WorkspaceID: "ws-dryrun",
	})
	if err != nil {
		_ = drySvc.Close(context.Background())
		return fail(err)
	}

	var (
		output   strings.Builder
		state    = snap.State
		deadline = time.Now().Add(3 * time.Minute)
	)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	settled := false
	for !settled {
		select {
		case ev := <-events:
			switch ev.Type {
			case run.EventMessageDelta:
				var payload struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(ev.Payload, &payload); err == nil {
					output.WriteString(payload.Text)
				}
			case run.EventApprovalRequest:
				var payload struct {
					ApprovalID string `json:"approvalId"`
					ToolName   string `json:"toolName"`
				}
				if err := json.Unmarshal(ev.Payload, &payload); err == nil {
					out.Denials = append(out.Denials, SkillDryRunDenial{
						ToolName: payload.ToolName,
						Rule:     resources.DryRunRuleApprovalAbsent,
					})
					// 试运行没有审批人：需要人工批准的一律拒绝。
					_ = drySvc.ResolveApproval(context.Background(), snap.RunID, payload.ApprovalID, string(policy.ApprovalDeny))
				}
			}
			continue
		case <-ticker.C:
		}
		current, getErr := drySvc.Get(snap.RunID)
		if getErr == nil {
			state = current.State
			if runStateTerminal(state) {
				snap = current
				settled = true
			}
		}
		if !settled && time.Now().After(deadline) {
			_ = drySvc.Abort(context.Background(), snap.RunID)
			_ = drySvc.Close(context.Background())
			out.State = "timeout"
			out.Output = output.String()
			out.TempCleaned = sandboxCleanup(sandbox)
			out.SkillUnchanged = skillSourceUnchanged(item, sandbox)
			out.UserStateUnchanged = sandbox.VerifyUserStateUnchanged(before) == nil
			out.DurationMs = time.Since(started).Milliseconds()
			return out
		}
	}

	_ = drySvc.Close(context.Background())
	out.State = state
	out.Output = output.String()
	out.TempCleaned = sandboxCleanup(sandbox)
	out.SkillUnchanged = skillSourceUnchanged(item, sandbox)
	if verifyErr := sandbox.VerifyUserStateUnchanged(before); verifyErr != nil {
		out.UserStateUnchanged = false
		out.Error = toCallError(verifyErr)
	} else {
		out.UserStateUnchanged = true
	}
	if state == contracts.RunStateFailed && out.Error == nil {
		if snap.Error != nil {
			out.Error = toCallError(snap.Error)
		} else {
			out.Error = toCallError(contracts.NewAppError(contracts.CodeProviderError,
				"试运行以 failed 结束，但没有给出原因", false))
		}
	}
	out.DurationMs = time.Since(started).Milliseconds()
	return out
}

// sandboxCleanup 清理并返回"确实删掉了"。
func sandboxCleanup(sb *resources.DryRunSandbox) bool {
	_ = sb.Cleanup()
	return sb.Cleaned()
}

// skillSourceUnchanged 核对托管目录里的源技能没有被试运行改动。
func skillSourceUnchanged(item resources.SkillResource, sb *resources.DryRunSandbox) bool {
	dir := filepath.Dir(item.Path)
	digest, err := resources.TreeDigest(dir)
	if err != nil {
		return false
	}
	return digest[dir] == sb.SourceSkillDigest()
}

// runStateTerminal 判断 run 是否已经结束。
func runStateTerminal(state string) bool {
	switch state {
	case contracts.RunStateCompleted, contracts.RunStateFailed,
		contracts.RunStateCancelled, contracts.RunStateInterrupted:
		return true
	}
	return false
}
