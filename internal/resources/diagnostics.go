// MCP 诊断（C04 / MCP02 / MCP05）。
//
// 诊断不自己连 MCP：连接的拥有者必须是 Pi（R05：Go 不重写 Pi 的 MCP client）。
// 这里启动一次短命的 `pi mcp list --json`，让它把每个服务的 state/tools/error
// 报回来。三点后果必须说清楚：
//
//   - 诊断会**启动一个 Pi 进程**（因此也会启动它自己的 stdio MCP 子进程）。
//     它不抢运行中 worker 的连接，但同一个服务会短暂出现第二个连接；
//     对无状态 MCP 无害，对有状态服务可能造成重复初始化。这是 V1 的已知限制。
//   - 诊断输出里没有 inputSchema（Pi 只列工具名），所以工具目录里 MCP 工具的
//     schema 标记为未知，不编造。
//   - 鉴权失败（401/requires authentication）必须显示为"需鉴权/未支持 OAuth"，
//     绝不能显示成 connected。
package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// MCP 诊断状态。
const (
	MCPStateConnected    = "connected"
	MCPStateFailed       = "failed"
	MCPStateTimeout      = "timeout"
	MCPStateAuthRequired = "auth_required"
	MCPStateDisabled     = "disabled"
	MCPStateUnavailable  = "unavailable"
)

// MCPToolStatus 是服务端实际列出的一个工具（来自 Pi 的诊断输出）。
type MCPToolStatus struct {
	Name string `json:"name"`
	// Exposure 是该服务的暴露策略（direct/deferred/hidden），不是 Pi 的 codemode。
	Exposure string `json:"exposure"`
	// Enabled 表示模型是否可达。hidden 为 false：服务连上了，但工具不可达。
	Enabled         bool   `json:"enabled"`
	CapabilityState string `json:"capabilityState"`
}

// MCPStatus 是一条服务的诊断结果。
type MCPStatus struct {
	ID         string `json:"id"`
	Configured bool   `json:"configured"`
	Transport  string `json:"transport"`
	Enabled    bool   `json:"enabled"`
	Exposure   string `json:"exposure"`
	// State 是实际连接状态（connected/failed/timeout/auth_required/disabled/unavailable）。
	State  string              `json:"state"`
	Tools  []MCPToolStatus     `json:"tools"`
	Error  *contracts.AppError `json:"error,omitempty"`
	Detail string              `json:"detail,omitempty"`
	// Source 说明这条状态从哪来，便于把"没检查"和"检查失败"分开。
	Source string `json:"source"`
	// OAuthSupported 恒为 false：V1 没有实现 MCP OAuth 登录流程。
	// 需要 OAuth 的服务必须显示"未支持/需外部配置"，不能显示 connected。
	OAuthSupported bool   `json:"oauthSupported"`
	CheckedAt      string `json:"checkedAt"`
}

// MCPDiagnoseEnv 是启动 `pi mcp list --json` 所需的上下文。
//
// 由绑定层填充（Pi 可执行文件可能经 node 启动，所以 Args 里可能有 .js 入口）。
type MCPDiagnoseEnv struct {
	Executable string
	Args       []string
	// Timeout 是整次诊断的上限，默认 90s（大于 Pi 的单请求 60s）。
	Timeout time.Duration
}

// 超时默认 90s：大于 Pi 自己的单请求超时（60s），让 Pi 先按它自己的语义失败并清理
// 它启动的 stdio 子进程；我们的超时只是兜底。
//
// 诊断不会改写运行中的 agentDir：它把当前配置编译到临时 agentDir 再跑
// `pi mcp list --json`。这样"诊断的是当前配置"与"旧 run 继续用旧集合"
// 同时成立，诊断也不会和 ensureRuntime 抢同一份 mcp.json。

// mcpCommand 是一次诊断进程的完整描述（测试可注入）。
type mcpCommand struct {
	Executable string
	Args       []string
	Dir        string
	Env        []string
	Timeout    time.Duration
}

// mcpListOutput 对应 `pi mcp list --json` 的顶层结构。
type mcpListOutput struct {
	Servers []piMCPServer `json:"servers"`
	Errors  []string      `json:"errors"`
}

type piMCPServer struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Source    string   `json:"source"`
	Enabled   bool     `json:"enabled"`
	Exposure  string   `json:"exposure"`
	Transport string   `json:"transport"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	Error     string   `json:"error"`
}

// SetMCPDiagnoseEnv 配置诊断用的 Pi 启动信息。
func (s *Service) SetMCPDiagnoseEnv(e MCPDiagnoseEnv) {
	s.mcpMu.Lock()
	s.mcpDiagnose = e
	s.mcpMu.Unlock()
}

// SetMCPDiagnoseRunner 替换真实进程执行（仅测试使用）。
func (s *Service) SetMCPDiagnoseRunner(fn func(ctx context.Context, spec mcpCommand) (mcpListOutput, error)) {
	s.mcpMu.Lock()
	s.mcpDiagnoseRun = fn
	s.mcpMu.Unlock()
}

// DiagnoseMCP 诊断一条配置。未启用的服务不启动任何进程，直接报 disabled。
func (s *Service) DiagnoseMCP(ctx context.Context, id string) (MCPStatus, error) {
	set, err := s.ListMCP(ctx)
	if err != nil {
		return MCPStatus{}, err
	}
	var target *MCPDefinition
	for i := range set.Items {
		if set.Items[i].ID == strings.TrimSpace(id) {
			target = &set.Items[i]
			break
		}
	}
	if target == nil {
		return MCPStatus{}, contracts.NewAppError(contracts.CodeNotFound, "MCP 服务不存在: "+id, false)
	}
	plan, err := s.CompileMCP(ctx, "")
	if err != nil {
		return MCPStatus{}, err
	}
	statuses, err := s.diagnoseMCPPlan(ctx, plan, []MCPDefinition{*target})
	if err != nil {
		return MCPStatus{}, err
	}
	return statuses[0], nil
}

// DiagnoseAllMCP 一次诊断全部配置（只启动一次 Pi）。
func (s *Service) DiagnoseAllMCP(ctx context.Context) ([]MCPStatus, error) {
	set, err := s.ListMCP(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.CompileMCP(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.diagnoseMCPPlan(ctx, plan, set.Items)
}

// diagnoseMCPPlan 是诊断的共用实现：先把当前配置写进隔离 agentDir，再调用 Pi。
func (s *Service) diagnoseMCPPlan(ctx context.Context, plan MCPPlan, defs []MCPDefinition) ([]MCPStatus, error) {
	s.mcpMu.RLock()
	env := s.mcpDiagnose
	runner := s.mcpDiagnoseRun
	s.mcpMu.RUnlock()

	skipped := map[string]string{}
	for _, sk := range plan.Skipped {
		skipped[sk.ID] = sk.Reason
	}
	statuses := make([]MCPStatus, 0, len(defs))
	var listed map[string]piMCPServer
	var listErrors []string

	needConnect := false
	for _, def := range defs {
		if def.Enabled && skipped[def.ID] == "" {
			needConnect = true
			break
		}
	}

	if needConnect {
		if strings.TrimSpace(env.Executable) == "" {
			return nil, contracts.NewAppError(contracts.CodeConfigMissing,
				"未配置 Pi 可执行文件，无法诊断 MCP；请先在设置里指定 Pi", false)
		}
		// 临时 agentDir：诊断只读自己的这份 mcp.json，绝不改写运行中的那一份。
		// 这也让"诊断的是当前配置"与"旧 run 继续用旧集合"同时成立。
		diagDir, err := os.MkdirTemp("", "myagent-mcp-diag-")
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "创建诊断临时目录失败: "+err.Error(), false)
		}
		defer os.RemoveAll(diagDir)
		if err := ApplyMCPPlan(diagDir, plan); err != nil {
			return nil, err
		}
		spec := mcpCommand{
			Executable: env.Executable,
			Args:       append(append([]string{}, env.Args...), "mcp", "list", "--json"),
			Dir:        diagDir,
			Env:        diagnoseEnv(diagDir, plan.Env),
			Timeout:    env.Timeout,
		}
		if spec.Timeout <= 0 {
			spec.Timeout = 90 * time.Second
		}
		run := runner
		if run == nil {
			run = runMCPList
		}
		out, err := run(ctx, spec)
		if err != nil {
			return nil, err
		}
		listErrors = out.Errors
		listed = make(map[string]piMCPServer, len(out.Servers))
		for _, srv := range out.Servers {
			listed[srv.Name] = srv
		}
	}

	for _, def := range defs {
		st := MCPStatus{
			ID:             def.ID,
			Configured:     true,
			Transport:      def.Transport,
			Enabled:        def.Enabled,
			Exposure:       def.Exposure,
			Source:         "pi mcp list --json",
			OAuthSupported: false,
			CheckedAt:      mcpNow().UTC().Format(time.RFC3339),
			Tools:          []MCPToolStatus{},
		}
		switch {
		case !def.Enabled:
			st.State = MCPStateDisabled
			st.Source = "config"
			st.Detail = "已禁用：不会出现在 mcp.json 里，Pi 不会连接它"
			statuses = append(statuses, st)
		case skipped[def.ID] != "":
			st.State = MCPStateUnavailable
			st.Source = "config"
			st.Error = contracts.NewAppError(contracts.CodeDependencyMissing, skipped[def.ID], false)
			statuses = append(statuses, st)
		default:
			srv, ok := listed[def.ID]
			if !ok {
				st.State = MCPStateFailed
				st.Detail = "Pi 未列出该服务"
				if len(listErrors) > 0 {
					st.Detail += "；" + strings.Join(listErrors, "；")
				}
				st.Error = contracts.NewAppError(contracts.CodeProviderUnreachable, st.Detail, false)
				statuses = append(statuses, st)
				continue
			}
			applyPiServerStatus(&st, srv)
			statuses = append(statuses, st)
		}
	}
	return statuses, nil
}

// applyPiServerStatus 把 Pi 的 state 映射成产品状态，并区分鉴权/超时/不可达。
func applyPiServerStatus(st *MCPStatus, srv piMCPServer) {
	st.Detail = strings.TrimSpace(srv.Error)
	switch strings.ToLower(strings.TrimSpace(srv.State)) {
	case "connected":
		st.State = MCPStateConnected
		// 连上了不等于工具可达：hidden 的服务照样 connected，但工具不可达。
		for _, name := range srv.Tools {
			st.Tools = append(st.Tools, MCPToolStatus{
				Name:            name,
				Exposure:        st.Exposure,
				Enabled:         st.Exposure != MCPExposureHidden,
				CapabilityState: capabilityForExposure(st.Exposure),
			})
		}
	case "failed", "error", "disconnected":
		lower := strings.ToLower(st.Detail)
		switch {
		case strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") ||
			strings.Contains(lower, "401") || strings.Contains(lower, "oauth") ||
			strings.Contains(lower, "sign in") || strings.Contains(lower, "sign-in"):
			st.State = MCPStateAuthRequired
			st.Error = contracts.NewAppError(contracts.CodeUnauthorized,
				"服务需要鉴权。V1 不支持 MCP OAuth 登录；请改用 headerRefs 引用已保存的凭证"+
					authDetail(st.Detail), false)
		case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") ||
			strings.Contains(lower, "deadline"):
			st.State = MCPStateTimeout
			st.Error = contracts.NewAppError(contracts.CodeProviderUnreachable,
				"连接超时，未建立连接"+authDetail(st.Detail), true)
		default:
			st.State = MCPStateFailed
			st.Error = contracts.NewAppError(contracts.CodeProviderUnreachable,
				"连接失败，未建立连接"+authDetail(st.Detail), false)
		}
	default:
		st.State = MCPStateFailed
		st.Error = contracts.NewAppError(contracts.CodeProviderUnreachable,
			"Pi 报告了未知连接状态: "+srv.State+authDetail(st.Detail), false)
	}
}

func authDetail(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	return "（Pi: " + detail + "）"
}

func capabilityForExposure(exposure string) string {
	switch exposure {
	case MCPExposureHidden:
		return "hidden"
	case MCPExposureDeferred:
		return "deferred"
	case MCPExposureDirect:
		return "available"
	default:
		return "unknown"
	}
}

// diagnoseEnv 构造诊断子进程的环境：剔除宿主 PI_*（避免读回用户真实配置目录），
// 固定 PI_CODING_AGENT_DIR，再注入本轮的凭证环境变量。
func diagnoseEnv(agentDir string, secrets []string) []string {
	env := make([]string, 0, len(os.Environ())+len(secrets)+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PI_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PI_CODING_AGENT_DIR="+agentDir)
	// 与 worker 一致：HOME/USERPROFILE 不在这里改，诊断只关心 mcp.json。
	env = append(env, secrets...)
	return env
}

// runMCPList 执行 `pi mcp list --json` 并解析结果。
//
// 退出码非 0 是正常的（任一服务连不上时 Pi 返回 1），因此不能把 exit code
// 当作"诊断失败"：真正的失败是输出不是 JSON（Pi 没启动、版本不对）。
func runMCPList(ctx context.Context, spec mcpCommand) (mcpListOutput, error) {
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return mcpListOutput{}, contracts.NewAppError(contracts.CodeProviderUnreachable,
			"MCP 诊断超时（"+spec.Timeout.String()+"）：Pi 没有在时限内返回 `mcp list --json`", true)
	}

	out := mcpListOutput{}
	trimmed := bytes.TrimSpace(stdout.Bytes())
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" && runErr != nil {
			detail = runErr.Error()
		}
		return mcpListOutput{}, contracts.NewAppError(contracts.CodeInternal,
			"MCP 诊断没有拿到合法 JSON（Pi 可能启动失败或版本不支持 mcp list --json）"+clipDetail(detail), false)
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return mcpListOutput{}, contracts.NewAppError(contracts.CodeInternal,
			"MCP 诊断输出解析失败: "+err.Error(), false)
	}
	// 退出码 1 仍可能是"有服务器连不上"，这在 out.Servers[].state 里表达；
	// 这里只记录进程错误文本，供 detail 使用。
	if runErr != nil && len(out.Servers) == 0 && len(out.Errors) == 0 {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = runErr.Error()
		}
		return mcpListOutput{}, contracts.NewAppError(contracts.CodeInternal,
			"`pi mcp list --json` 退出异常且没有返回任何服务状态"+clipDetail(detail), false)
	}
	return out, nil
}

func clipDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	const max = 600
	if len(detail) > max {
		detail = detail[:max] + "…"
	}
	return "（" + detail + "）"
}
