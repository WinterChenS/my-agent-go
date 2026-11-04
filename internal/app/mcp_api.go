package app

// 本文件是 C04 的绑定层：MCP 配置、诊断、统一工具目录。
//
// 三条纪律：
//   - 保存配置不启动任何东西（不执行 command、不访问 url）；诊断才连接，
//     而且连接由 Pi 拥有（`pi mcp list --json`），Go 不自建 MCP client。
//   - 禁用/删除必须落到运行时：ensureRuntime 会重新编译 mcp.json，
//     禁用的服务从文件里移除；运行中的旧 run 不会被静默改写，状态里报 pending。
//   - 需要 OAuth 的服务显示"需鉴权/未支持"，不能显示 connected。

import (
	"context"
	"strings"

	"my-agent-go/internal/harnessmcp"
	"my-agent-go/internal/resources"
)

// MCPListResult 是 MCP 配置列表 + 运行中的生效状态。
type MCPListResult struct {
	Items    []resources.MCPDefinition `json:"items"`
	Revision uint64                    `json:"revision"`
	// RuntimeServers 是运行中的 worker 实际拿到的外部 MCP 服务名（agentDir/mcp.json）。
	RuntimeServers []string `json:"runtimeServers"`
	// Pending 表示配置里的集合与运行中的不一致：有 run 在跑时本轮继续用旧集合，
	// 空闲后的下一次运行（ensureRuntime 重建）才生效。
	Pending   bool `json:"pending"`
	ActiveRun bool `json:"activeRun"`
	// DiagnoseReady 表示是否具备启动 Pi 诊断的条件（Pi 可执行文件可用）。
	DiagnoseReady bool       `json:"diagnoseReady"`
	Error         *CallError `json:"error,omitempty"`
}

// MCPResult 是单条 MCP 操作的结果。
type MCPResult struct {
	Item     resources.MCPDefinition `json:"item"`
	Revision uint64                  `json:"revision"`
	Pending  bool                    `json:"pending"`
	Error    *CallError              `json:"error,omitempty"`
}

// MCPStatusResult 是单条诊断结果。
type MCPStatusResult struct {
	Status resources.MCPStatus `json:"status"`
	Error  *CallError          `json:"error,omitempty"`
}

// MCPStatusListResult 是全部诊断结果。
type MCPStatusListResult struct {
	Items []resources.MCPStatus `json:"items"`
	Error *CallError            `json:"error,omitempty"`
}

// MCPCredentialResult 是保存 MCP 凭证的结果（只回 ref，不回值）。
type MCPCredentialResult struct {
	Ref   string     `json:"ref"`
	Error *CallError `json:"error,omitempty"`
}

// ToolListResult 是统一工具目录。
type ToolListResult struct {
	Items []resources.ToolDescriptor `json:"items"`
	Error *CallError                 `json:"error,omitempty"`
}

// mcpService 返回配置了工作区根与诊断环境的资源服务。
//
// 工作区根参与 workingDir 校验：不在这里刷新的话，设置里换了工作区后
// 旧根仍然被当成允许范围。
func (d *Desktop) mcpService() (*resources.Service, error) {
	svc, err := d.resourceServices()
	if err != nil {
		return nil, err
	}
	s := d.Settings()
	svc.SetWorkspaceRoot(s.Workspace)
	svc.SetHarnessTools(harnessToolDescriptors(workspaceOf(s)))
	env := resources.MCPDiagnoseEnv{}
	if inst, ierr := d.resolveInstallation(s); ierr == nil {
		env.Executable = inst.Executable
		env.Args = inst.Args
	}
	svc.SetMCPDiagnoseEnv(env)
	return svc, nil
}

func workspaceOf(s Settings) string { return strings.TrimSpace(s.Workspace) }

// harnessTools 是自有 MCP 的工具注册表。
//
// 唯一的注册点：ensureRuntime 用它启动服务，工具目录用它生成条目。
// 两处各写一份会出现"目录里有、实际没注册"（或反过来）的偏差。
func harnessTools(workspace string) []harnessmcp.Tool {
	return []harnessmcp.Tool{harnessmcp.ProbeTool(workspace)}
}

func harnessToolDescriptors(workspace string) []resources.ToolDescriptor {
	tools := harnessTools(workspace)
	out := make([]resources.ToolDescriptor, 0, len(tools))
	for _, t := range tools {
		out = append(out, resources.ToolDescriptor{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Enabled:     true,
		})
	}
	return out
}

// compileMCP 是"当前配置下应写进 mcp.json 的服务"的唯一入口。
func (d *Desktop) compileMCP() (resources.MCPPlan, error) {
	svc, err := d.resourceServices()
	if err != nil {
		return resources.MCPPlan{}, err
	}
	s := d.Settings()
	svc.SetWorkspaceRoot(s.Workspace)
	return svc.CompileMCP(context.Background(), defaultWorkspaceID)
}

// ListMCP 返回全部 MCP 配置与生效状态。
func (d *Desktop) ListMCP() MCPListResult {
	out := MCPListResult{Items: []resources.MCPDefinition{}}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	set, err := svc.ListMCP(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if set.Items != nil {
		out.Items = set.Items
	}
	out.Revision = set.Revision
	out.Pending, out.ActiveRun, out.RuntimeServers = d.mcpRuntimeState()
	_, instErr := d.resolveInstallation(d.Settings())
	out.DiagnoseReady = instErr == nil
	return out
}

// SaveMCP 保存一条 MCP 配置（CAS）。保存不执行任何脚本、不连接任何端点。
func (d *Desktop) SaveMCP(expectedRevision uint64, def resources.MCPDefinition) MCPResult {
	out := MCPResult{}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	doc, err := svc.SaveMCP(context.Background(), expectedRevision, def)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item, out.Error = d.getMCP(svc, def.ID)
	out.Revision = doc.Revision
	out.Pending, _, _ = d.mcpRuntimeState()
	return out
}

// DeleteMCP 删除一条配置（CAS）。
func (d *Desktop) DeleteMCP(expectedRevision uint64, id string) MCPResult {
	out := MCPResult{}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	doc, err := svc.DeleteMCP(context.Background(), expectedRevision, id)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Revision = doc.Revision
	out.Pending, _, _ = d.mcpRuntimeState()
	return out
}

// SetMCPEnabled 启用/禁用一条服务（CAS）。
func (d *Desktop) SetMCPEnabled(id string, expectedRevision uint64, enabled bool) MCPResult {
	out := MCPResult{}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	doc, err := svc.SetMCPEnabled(context.Background(), id, expectedRevision, enabled)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item, out.Error = d.getMCP(svc, id)
	out.Revision = doc.Revision
	out.Pending, _, _ = d.mcpRuntimeState()
	return out
}

// SaveMCPCredential 把 MCP 使用的凭证写进 OS 凭证库。
//
// secret 不回流、不落配置：MCP 配置里只有 ref（见 SaveMCP 的 EnvRefs/HeaderRefs）。
func (d *Desktop) SaveMCPCredential(ref, secret string) MCPCredentialResult {
	out := MCPCredentialResult{Ref: strings.TrimSpace(ref)}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if err := svc.SaveMCPCredential(context.Background(), ref, secret); err != nil {
		out.Error = toCallError(err)
		return out
	}
	return out
}

// DiagnoseMCP 诊断一条服务。连接由 Pi 拥有（短命的 `pi mcp list --json`）。
func (d *Desktop) DiagnoseMCP(id string) MCPStatusResult {
	out := MCPStatusResult{}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	st, err := svc.DiagnoseMCP(context.Background(), id)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Status = st
	return out
}

// DiagnoseAllMCP 一次诊断全部服务（只启动一次 Pi）。
func (d *Desktop) DiagnoseAllMCP() MCPStatusListResult {
	out := MCPStatusListResult{Items: []resources.MCPStatus{}}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	items, err := svc.DiagnoseAllMCP(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if items != nil {
		out.Items = items
	}
	return out
}

// ListTools 返回统一工具目录（builtin / extension / mcp / harness）。
func (d *Desktop) ListTools() ToolListResult {
	out := ToolListResult{Items: []resources.ToolDescriptor{}}
	svc, err := d.mcpService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	// 目录不属于某一次 run：session 级授权在调用时由策略服务判定，
	// 这里不复制一份结论，因此 sessionID 留空。
	items, err := svc.ListTools(context.Background(), "")
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if items != nil {
		out.Items = items
	}
	return out
}

func (d *Desktop) getMCP(svc *resources.Service, id string) (resources.MCPDefinition, *CallError) {
	item, err := svc.GetMCP(context.Background(), id)
	if err != nil {
		return resources.MCPDefinition{}, toCallError(err)
	}
	return item, nil
}

// mcpRuntimeState 返回"运行中的 MCP 集合"与"配置与运行是否不一致"。
func (d *Desktop) mcpRuntimeState() (pending bool, active bool, ids []string) {
	d.mu.Lock()
	digest := d.runtimeMCPDigest
	ids = append([]string{}, d.runtimeMCP...)
	svc := d.svc
	d.mu.Unlock()

	if svc != nil {
		active = svc.ActiveCount() > 0 || svc.QueuedCount() > 0
	}
	if digest == "" {
		// 还没起过运行时：谈不上"等待生效"，下次运行自然会用新配置。
		return false, active, ids
	}
	plan, err := d.compileMCP()
	if err != nil {
		// 配置读不出来时不假装"已生效"，但也不把列表整体打挂。
		return true, active, ids
	}
	return plan.Digest != digest, active, ids
}
