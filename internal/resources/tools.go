// 统一工具目录（C04 / RES04）。
//
// 一张表要同时回答三件事：这个工具从哪来（builtin / extension / mcp / harness）、
// 现在到底可不可用（enabled + capabilityState）、调用它要不要审批（policy）。
// 三者混成"已启用"，用户就分不清"没生效"是配置、连接还是策略的问题。
//
// 数据来源刻意不统一，因为可信度不同：
//   - builtin：Pi 1.5.0 的默认工具集（read/write/edit/write）+ 因 MCP 暴露策略
//     自动激活的 tool_search。它们是确定存在的，标 available。
//   - harness：自有 MCP 的工具清单由绑定层注入（harnessmcp 的注册表），
//     不在这里复制一份。
//   - mcp：只有 Pi 的诊断输出能证明"确实有这个工具"，所以走 DiagnoseMCP 的
//     同一套 `pi mcp list --json`；拿不到时标 unknown，绝不猜成可用。
//   - extension：扩展在 Pi 进程内注册工具，产品无法静态枚举；只列出扩展本身
//     并标 unknown，不编造工具名。
//
// 策略口径：F04 的策略服务对**每一次**工具调用都要审批（除配置硬拒绝），
// 所以这里统一标 approval；不把 readOnlyHint 当免审批理由（基线 §6）。
package resources

import (
	"context"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

// 工具来源。
const (
	ToolSourceBuiltin   = "builtin"
	ToolSourceExtension = "extension"
	ToolSourceMCP       = "mcp"
	ToolSourceHarness   = "harness"
)

// 工具的策略口径。
const (
	// ToolPolicyApproval：调用前需要逐次审批（当前策略对 MCP/扩展/写/shell 的默认）。
	ToolPolicyApproval = "approval"
	// ToolPolicyAuto：当前配置下无需逐次审批（预留给"已审查的只读工具"）。
	ToolPolicyAuto = "auto"
	// ToolPolicyDenied：当前配置下硬拒绝（例如未授予 shell 权限时的 bash）。
	ToolPolicyDenied = "denied"
)

// 工具能力状态。
const (
	ToolAvailable   = "available"
	ToolDeferred    = "deferred"
	ToolHidden      = "hidden"
	ToolUnknown     = "unknown"
	ToolUnavailable = "unavailable"
	ToolDisabled    = "disabled"
)

// ToolDescriptor 是工具目录里的一行。
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Source      string         `json:"source"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	// Enabled 表示"模型当前可达且未被禁用"；hidden/disabled/failed 一律 false。
	Enabled bool `json:"enabled"`
	// Policy 是调用发生的授权口径（approval/auto/denied）。
	Policy string `json:"policy"`
	// CapabilityState 说明能力状态：available/deferred/hidden/unknown/unavailable/disabled。
	CapabilityState string `json:"capabilityState"`
	// Server 是 MCP 服务名（source=mcp 时有值）。
	Server string `json:"server,omitempty"`
}

// SetHarnessTools 注入自有 MCP 的工具清单（source 会被强制改成 harness）。
func (s *Service) SetHarnessTools(tools []ToolDescriptor) {
	out := make([]ToolDescriptor, 0, len(tools))
	for _, t := range tools {
		t.Source = ToolSourceHarness
		if t.Policy == "" {
			t.Policy = ToolPolicyApproval
		}
		if t.CapabilityState == "" {
			t.CapabilityState = ToolAvailable
		}
		out = append(out, t)
	}
	s.mcpMu.Lock()
	s.harnessTools = out
	s.mcpMu.Unlock()
}

// ListTools 返回统一工具目录。
//
// sessionID 预留给会话级授权（session_scope）：当前策略在调用时判定，
// 目录只展示"默认口径"。不要在展示层另算一份授权结论——两次判定分叉时，
// 用户看到的就是"目录说可用、实际被拒"。
func (s *Service) ListTools(ctx context.Context, sessionID contracts.SessionID) ([]ToolDescriptor, error) {
	_ = sessionID
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	plan, err := s.CompileMCP(ctx, "")
	if err != nil {
		return nil, err
	}
	out := builtinToolDescriptors(plan)

	// extension：只列加载的第三方扩展，工具清单标 unknown。
	set, err := s.ListExtensions(ctx)
	if err != nil {
		return nil, err
	}
	for _, ext := range set.Items {
		if ext.System || !ext.Enabled {
			continue
		}
		out = append(out, ToolDescriptor{
			Name:   ext.ID,
			Source: ToolSourceExtension,
			Description: "扩展在 Pi 进程内注册工具，产品无法静态枚举；" +
				"实际调用以运行记录里的工具事件为准。",
			Enabled:         true,
			Policy:          ToolPolicyApproval,
			CapabilityState: ToolUnknown,
		})
	}

	// harness：自有 MCP 的注册表（绑定层注入）。
	s.mcpMu.RLock()
	harness := append([]ToolDescriptor{}, s.harnessTools...)
	s.mcpMu.RUnlock()
	out = append(out, harness...)

	// mcp：能诊断就取真实工具名；诊断不了就逐服务标 unknown，不伪造工具。
	mcpTools := s.listMCPToolDescriptors(ctx, plan)
	out = append(out, mcpTools...)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return toolSourceOrder(out[i].Source) < toolSourceOrder(out[j].Source)
		}
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func toolSourceOrder(source string) int {
	switch source {
	case ToolSourceBuiltin:
		return 0
	case ToolSourceExtension:
		return 1
	case ToolSourceMCP:
		return 2
	case ToolSourceHarness:
		return 3
	default:
		return 4
	}
}

// builtinToolDescriptors 是 Pi 实际会声明的内置工具。
//
// 与隔离/运行参数同源：worker 启动时不传 --tools，Pi 的默认集就是
// read/bash/edit/write（cli.md）。deferred 暴露会激活 tool_search，
// 这是 Pi 的自动行为，目录要如实列出来，否则用户会以为工具凭空出现。
func builtinToolDescriptors(plan MCPPlan) []ToolDescriptor {
	out := []ToolDescriptor{
		{Name: "read", Source: ToolSourceBuiltin, Description: "读取工作区文件（只读）",
			Enabled: true, Policy: ToolPolicyApproval, CapabilityState: ToolAvailable},
		{Name: "write", Source: ToolSourceBuiltin, Description: "写入工作区文件",
			Enabled: true, Policy: ToolPolicyApproval, CapabilityState: ToolAvailable},
		{Name: "edit", Source: ToolSourceBuiltin, Description: "编辑工作区文件",
			Enabled: true, Policy: ToolPolicyApproval, CapabilityState: ToolAvailable},
		{Name: "bash", Source: ToolSourceBuiltin, Description: "执行 shell 命令（受权限与审批约束）",
			Enabled: true, Policy: ToolPolicyApproval, CapabilityState: ToolAvailable},
	}
	for _, cfg := range plan.Servers {
		if cfg.Exposure == MCPExposureDeferred {
			out = append(out, ToolDescriptor{
				Name: "tool_search", Source: ToolSourceBuiltin,
				Description: "Pi 因 deferred 暴露的 MCP 服务自动激活：搜索并声明未声明的工具",
				Enabled:     true, Policy: ToolPolicyApproval, CapabilityState: ToolAvailable,
			})
			break
		}
	}
	return out
}

// listMCPToolDescriptors 生成外部 MCP 的工具行。
func (s *Service) listMCPToolDescriptors(ctx context.Context, plan MCPPlan) []ToolDescriptor {
	set, err := s.ListMCP(ctx)
	if err != nil {
		return nil
	}
	if len(set.Items) == 0 {
		return nil
	}

	skipped := map[string]string{}
	for _, sk := range plan.Skipped {
		skipped[sk.ID] = sk.Reason
	}
	disabled := map[string]MCPStatus{}
	statuses := map[string]MCPStatus{}
	if len(plan.Servers) > 0 {
		all, err := s.diagnoseMCPPlan(ctx, plan, set.Items)
		if err == nil {
			for _, st := range all {
				statuses[st.ID] = st
			}
		} else {
			// 诊断不可用（Pi 未配置/启动失败）：不返回错误把整个目录打挂，
			// 而是逐条标 unknown，并把原因写进描述。
			reason := messageOf(err)
			for _, def := range set.Items {
				if !def.Enabled {
					continue
				}
				disabled[def.ID] = MCPStatus{
					ID: def.ID, Enabled: true,
					Error: contracts.NewAppError(contracts.CodeDependencyMissing, reason, false),
				}
			}
		}
	}

	out := make([]ToolDescriptor, 0, len(set.Items))
	for _, def := range set.Items {
		st, ok := statuses[def.ID]
		if !ok {
			switch {
			case skipped[def.ID] != "":
				// 已启用但凭证缺失：配置有效、运行时不可用，必须出现在目录里。
				out = append(out, wildcardMCPTool(def, ToolUnavailable, skipped[def.ID]))
			case disabled[def.ID].ID != "":
				out = append(out, wildcardMCPTool(def, ToolUnknown, "无法诊断："+messageOf(disabled[def.ID].Error)))
			case !def.Enabled:
				out = append(out, wildcardMCPTool(def, ToolDisabled, "已禁用：不会出现在 mcp.json，Pi 不会连接"))
			}
			continue
		}
		switch st.State {
		case MCPStateConnected:
			for _, tool := range st.Tools {
				enabled := tool.Enabled
				cap := tool.CapabilityState
				if cap == "" {
					cap = capabilityForExposure(def.Exposure)
				}
				out = append(out, ToolDescriptor{
					Name:            piMCPToolName(def.ID, tool.Name),
					Source:          ToolSourceMCP,
					Server:          def.ID,
					Description:     "外部 MCP 工具（" + def.ID + "）；未知 MCP 工具默认逐次审批，不信任 readOnlyHint",
					Enabled:         enabled,
					Policy:          ToolPolicyApproval,
					CapabilityState: cap,
				})
			}
		case MCPStateDisabled:
			out = append(out, wildcardMCPTool(def, ToolDisabled, "已禁用：不会出现在 mcp.json，Pi 不会连接"))
		case MCPStateUnavailable:
			out = append(out, wildcardMCPTool(def, ToolUnavailable, messageOf(st.Error)))
		default:
			reason := st.Detail
			if st.Error != nil {
				reason = st.Error.Message
			}
			out = append(out, wildcardMCPTool(def, ToolUnknown, reason))
		}
	}
	return out
}

func wildcardMCPTool(def MCPDefinition, capability, reason string) ToolDescriptor {
	return ToolDescriptor{
		Name:            "mcp__" + sanitizeMCPToolSegment(def.ID) + "__*",
		Source:          ToolSourceMCP,
		Server:          def.ID,
		Description:     "服务 " + def.ID + " 的工具清单未知：" + reason,
		Enabled:         false,
		Policy:          ToolPolicyApproval,
		CapabilityState: capability,
	}
}

// piMCPToolName 与 Pi 的工具命名规则一致：mcp__<server>__<tool>，
// 非字母/数字/下划线的字符替换成下划线。
func piMCPToolName(server, tool string) string {
	return "mcp__" + sanitizeMCPToolSegment(server) + "__" + sanitizeMCPToolSegment(tool)
}

func sanitizeMCPToolSegment(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
