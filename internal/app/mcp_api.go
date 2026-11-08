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

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/harnessmcp"
	"my-agent-go/internal/memory"
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
	mem, memoryWS := d.memoryToolsForRuntime(context.Background())
	svc.SetHarnessTools(d.harnessToolDescriptors(workspaceOf(s), mem, memoryWS))
	env := resources.MCPDiagnoseEnv{}
	if inst, ierr := d.resolveInstallation(s); ierr == nil {
		env.Executable = inst.Executable
		env.Args = inst.Args
	}
	svc.SetMCPDiagnoseEnv(env)
	return svc, nil
}

func workspaceOf(s Settings) string { return strings.TrimSpace(s.Workspace) }

// harnessTools 是自有 MCP 工具注册表的构造核心（探针 + 记忆 + 传入的额外工具）。
//
// 生产装配不直接调它：ensureRuntime（真的启动服务）与 mcpService（工具目录）都走
// harnessToolset / harnessToolDescriptors 这两个方法，两处各写一份会出现
// "目录里有、实际没注册"（或反过来）的偏差。
//
// extra 是"只在能力真的可用时才存在"的工具（当前是浏览器）。它们由调用方按
// 装配事实决定要不要给，本函数不猜。
func harnessTools(workspace string, mem *memory.Service, memoryWorkspaceID string, extra ...harnessmcp.Tool) []harnessmcp.Tool {
	tools := []harnessmcp.Tool{harnessmcp.ProbeTool(workspace)}
	// 记忆不可用（无服务或无工作区 ID）时不注册记忆工具：
	// 工具目录与实际注册必须一致，不能"目录里有、调用必然失败"。
	if mem != nil && strings.TrimSpace(memoryWorkspaceID) != "" {
		tools = append(tools, harnessmcp.MemoryTools(mem, memoryWorkspaceID)...)
	}
	tools = append(tools, extra...)
	return tools
}

// harnessToolset 是这台 Desktop 此刻应注册的全部自有 MCP 工具。
//
// ensureRuntime（真的启动服务）与 mcpService（工具目录）都走它：一处决定
// "注册什么"，两处看到同一份结果。
func (d *Desktop) harnessToolset(workspace string, mem *memory.Service, memoryWorkspaceID string) []harnessmcp.Tool {
	return harnessTools(workspace, mem, memoryWorkspaceID, d.harnessBrowserTools()...)
}

func (d *Desktop) harnessToolDescriptors(workspace string, mem *memory.Service, memoryWorkspaceID string) []resources.ToolDescriptor {
	return harnessToolDescriptors(workspace, mem, memoryWorkspaceID, d.harnessBrowserTools()...)
}

func harnessToolDescriptors(workspace string, mem *memory.Service, memoryWorkspaceID string, extra ...harnessmcp.Tool) []resources.ToolDescriptor {
	tools := harnessTools(workspace, mem, memoryWorkspaceID, extra...)
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

// ---------- 浏览器工具（M1：注册进自有 MCP） ----------

// harnessBrowserTools 返回此刻应注册进自有 MCP 的浏览器工具。
//
// 两个条件同时成立才注册，缺一条就一个都不注册（不是"注册了再返回失败"）：
//   - 桥接层里有真的 *browser.Service —— 原生宿主没装上的平台，工具必然失败；
//   - Desktop 能给出真的授权器 —— 装配失败时 browser.Service 会拒绝所有写动作，
//     browser_act 就成了一个注定失败的工具。
//
// 目录（ListTools）与实际注册（ensureRuntime → harnessmcp.Start）读的都是它，
// 因此不会出现"目录里有、实际没注册"的分叉。
func (d *Desktop) harnessBrowserTools() []harnessmcp.Tool {
	if d.browserBridge().Service() == nil {
		return nil
	}
	if d.BrowserAuthorizer() == nil {
		// 装配失败的原因由 BrowserAuthorizer 写进 lastErr（界面可见），这里只做不注册。
		return nil
	}
	return harnessmcp.BrowserTools(browserMCPController{desktop: d}, harnessmcp.BrowserToolsOptions{})
}

// browserMCPController 是自有 MCP 的浏览器工具控制器。
//
// 为什么不直接把 *browser.Service 交给 harnessmcp.BrowserTools：那个构造函数在**注册时**
// 把执行身份（contracts.ExecutionContext）捕获进每个工具的闭包，而自有 MCP 服务是
// 应用级单例（桌面宿主只启动一次），注册时捕获的身份到后面就不是"此刻的身份"了——
// tab 的归属按会话过滤，身份一旧，面板和模型看到的就不是同一批 tab。
//
// 所以这里把工具闭包里那份可能过期的身份**忽略**，每次调用按服务端事实重新解析：
//   - 服务来自 Desktop 当前的桥接层（装配发生在窗口挂载之后，晚于工具注册）；
//   - 身份来自 d.BrowserExecutionContext()，与右侧面板同一份来源。
//
// 模型改不了这两项：工具参数里出现 sessionId/runId/approvalId/confirmed 之类的键是
// 硬错误（browser_tools.go 的 forbiddenArgKeys），前端也拿不到这份身份。
//
// 已知限制（M1 报告里写明）：自有 MCP 传输层没有 per-run 的执行身份通道，一次 MCP
// 调用无法被归到某一个 run；这里用的是运行时会话（runtimeSessionID），与面板一致。
// 正确的形状是让工具在调用时拿到本次 run 的 ExecutionBinding（harnessmcp 的
// BrowserToolsOptions 增加一个 ExecProvider），那要改 internal/harnessmcp，不在 M1 的
// 改动范围内，因此不在这里发明第三种身份来源。
type browserMCPController struct {
	desktop *Desktop
}

// resolve 返回这次调用要用的服务与执行身份。
func (c browserMCPController) resolve() (*browser.Service, contracts.ExecutionContext, error) {
	svc := c.desktop.browserBridge().Service()
	if svc == nil {
		return nil, contracts.ExecutionContext{}, contracts.NewAppError(contracts.CodeUnsupported,
			"没有装配原生浏览器宿主：浏览器工具此刻不可用（本次没有派发任何动作）", false)
	}
	return svc, c.desktop.BrowserExecutionContext(), nil
}

func (c browserMCPController) Capabilities() browser.CapabilitySet {
	if svc := c.desktop.browserBridge().Service(); svc != nil {
		return svc.Capabilities()
	}
	return browser.CapabilitySet{}
}

func (c browserMCPController) OpenTab(ctx context.Context, _ contracts.ExecutionContext, opts browser.TabOptions) (browser.TabID, error) {
	svc, exec, err := c.resolve()
	if err != nil {
		return "", err
	}
	return svc.OpenTab(ctx, exec, opts)
}

func (c browserMCPController) Observe(ctx context.Context, _ contracts.ExecutionContext, tab browser.TabID, opts browser.ObserveOptions) (browser.Snapshot, error) {
	svc, exec, err := c.resolve()
	if err != nil {
		return browser.Snapshot{}, err
	}
	return svc.Observe(ctx, exec, tab, opts)
}

func (c browserMCPController) Execute(ctx context.Context, _ contracts.ExecutionContext, tab browser.TabID, action browser.Action) (browser.ActionResult, error) {
	svc, exec, err := c.resolve()
	if err != nil {
		return browser.ActionResult{}, err
	}
	return svc.Execute(ctx, exec, tab, action)
}

func (c browserMCPController) CloseTab(ctx context.Context, _ contracts.ExecutionContext, tab browser.TabID) error {
	svc, exec, err := c.resolve()
	if err != nil {
		return err
	}
	return svc.CloseTab(ctx, exec, tab)
}

func (c browserMCPController) TabInfo(_ contracts.ExecutionContext, tab browser.TabID) (browser.TabInfo, error) {
	svc, exec, err := c.resolve()
	if err != nil {
		return browser.TabInfo{}, err
	}
	return svc.TabInfo(exec, tab)
}

func (c browserMCPController) Control(_ contracts.ExecutionContext, tab browser.TabID) (browser.ControlState, error) {
	svc, exec, err := c.resolve()
	if err != nil {
		return browser.ControlState{}, err
	}
	return svc.Control(exec, tab)
}

// Tabs 让 browser_tabs 工具能看到 tab 列表（harnessmcp 用类型断言探测这个能力）。
func (c browserMCPController) Tabs(_ contracts.ExecutionContext) []browser.TabID {
	svc, exec, err := c.resolve()
	if err != nil {
		return nil
	}
	return svc.Tabs(exec)
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
