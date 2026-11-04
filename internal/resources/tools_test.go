package resources

// 统一工具目录的单元测试（C04 / RES04）。
//
// 断言的是最容易骗人的三件事：
//   - 来源可区分（builtin / extension / mcp / harness），不混成一张无名表；
//   - 禁用的服务、hidden 暴露、诊断失败都显示为不可用，不能显示成可用；
//   - 未知 MCP 工具默认审批（不信任 readOnlyHint）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

func toolByName(tools []ToolDescriptor, name string) (ToolDescriptor, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return ToolDescriptor{}, false
}

func TestListToolsDistinguishesSources(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "fixture", State: "connected", Exposure: "direct", Tools: []string{"fixture_echo"},
	}}}}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()

	if _, err := svc.SaveMCP(ctx, 0, stdioDef("fixture", "npx", "-y", "fixture-mcp")); err != nil {
		t.Fatalf("保存 MCP: %v", err)
	}
	// 第三方扩展：启用后目录里应出现一条 source=extension 的未知工具行。
	extDir := filepath.Join(svc.layout.ExtensionsDir())
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("建扩展目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "demo-ext.ts"), []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写扩展: %v", err)
	}
	set, err := svc.ListExtensions(ctx)
	if err != nil {
		t.Fatalf("列扩展: %v", err)
	}
	if _, err := svc.SetExtensionEnabled(ctx, "demo-ext", set.Revision, true); err != nil {
		t.Fatalf("启用扩展: %v", err)
	}

	svc.SetHarnessTools([]ToolDescriptor{{
		Name: "harness_probe", Description: "自有 MCP 探针",
		InputSchema: map[string]any{"type": "object"},
		Enabled:     true,
	}})

	tools, err := svc.ListTools(ctx, contracts.SessionID("sess-1"))
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}

	for _, name := range []string{"read", "write", "edit", "bash"} {
		tool, ok := toolByName(tools, name)
		if !ok {
			t.Fatalf("缺少内置工具 %s: %+v", name, tools)
		}
		if tool.Source != ToolSourceBuiltin {
			t.Fatalf("%s 来源应为 builtin，实际 %s", name, tool.Source)
		}
		if tool.Policy != ToolPolicyApproval {
			t.Fatalf("当前策略对每次调用都要审批，%s 的 policy 应为 approval，实际 %s", name, tool.Policy)
		}
	}
	if tool, ok := toolByName(tools, "mcp__fixture__fixture_echo"); !ok {
		t.Fatalf("缺少 MCP 工具: %+v", tools)
	} else {
		if tool.Source != ToolSourceMCP || tool.Server != "fixture" {
			t.Fatalf("MCP 工具的来源/服务不对: %+v", tool)
		}
		if !tool.Enabled || tool.CapabilityState != ToolAvailable {
			t.Fatalf("direct 暴露的 MCP 工具应可用: %+v", tool)
		}
		if tool.Policy != ToolPolicyApproval {
			t.Fatalf("未知 MCP 工具默认审批，实际 %s", tool.Policy)
		}
	}
	if tool, ok := toolByName(tools, "harness_probe"); !ok {
		t.Fatalf("缺少自有 MCP 工具: %+v", tools)
	} else if tool.Source != ToolSourceHarness || !tool.Enabled {
		t.Fatalf("自有 MCP 工具应可用且来源为 harness: %+v", tool)
	}
	if tool, ok := toolByName(tools, "demo-ext"); !ok {
		t.Fatalf("缺少扩展来源条目: %+v", tools)
	} else if tool.Source != ToolSourceExtension || tool.CapabilityState != ToolUnknown {
		t.Fatalf("扩展工具清单不可静态枚举，应标 unknown: %+v", tool)
	}
}

func TestListToolsDisabledServerIsNotAvailable(t *testing.T) {
	h := &diagnoseHarness{}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	def := stdioDef("off", "npx")
	def.Enabled = false
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}

	tools, err := svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}
	tool, ok := toolByName(tools, "mcp__off__*")
	if !ok {
		t.Fatalf("禁用的服务应出现在目录里并标不可用: %+v", tools)
	}
	if tool.Enabled || tool.CapabilityState != ToolDisabled {
		t.Fatalf("禁用服务不能显示为可用: %+v", tool)
	}
	if h.calls != 0 {
		t.Fatalf("只有禁用服务时不应启动诊断进程，实际 %d 次", h.calls)
	}
}

// TestListToolsHiddenExposureIsUnreachable 钉住"连上 ≠ 可用"：
// hidden 的服务 connected，但工具不可达。
func TestListToolsHiddenExposureIsUnreachable(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "hidden", State: "connected", Exposure: "hidden", Tools: []string{"fixture_echo"},
	}}}}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	def := stdioDef("hidden", "npx")
	def.Exposure = MCPExposureHidden
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	tools, err := svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}
	tool, ok := toolByName(tools, "mcp__hidden__fixture_echo")
	if !ok {
		t.Fatalf("hidden 服务的工具仍应列出（标为不可达）: %+v", tools)
	}
	if tool.Enabled || tool.CapabilityState != ToolHidden {
		t.Fatalf("hidden 暴露必须是不可达: %+v", tool)
	}
}

func TestListToolsDeferredAddsToolSearch(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "big", State: "connected", Exposure: "deferred", Tools: []string{"search"},
	}}}}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	def := stdioDef("big", "npx")
	def.Exposure = MCPExposureDeferred
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	tools, err := svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}
	if _, ok := toolByName(tools, "tool_search"); !ok {
		t.Fatalf("deferred 暴露会激活 Pi 的 tool_search，目录应如实列出: %+v", tools)
	}
	if tool, ok := toolByName(tools, "mcp__big__search"); !ok || tool.CapabilityState != ToolDeferred {
		t.Fatalf("deferred 工具应标 deferred: %+v", tool)
	}
}

func TestListToolsDiagnosisFailureIsUnknownNotAvailable(t *testing.T) {
	h := &diagnoseHarness{err: contracts.NewAppError(contracts.CodeConfigMissing, "pi 未配置", false)}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("fixture", "npx")); err != nil {
		t.Fatalf("保存: %v", err)
	}
	tools, err := svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("诊断失败不应让整个目录返回错误: %v", err)
	}
	tool, ok := toolByName(tools, "mcp__fixture__*")
	if !ok {
		t.Fatalf("诊断失败应逐服务标 unknown: %+v", tools)
	}
	if tool.Enabled || tool.CapabilityState != ToolUnknown {
		t.Fatalf("诊断失败不能猜成可用: %+v", tool)
	}
	if tool.Description == "" {
		t.Fatal("未知工具行必须带原因")
	}
}

// TestListToolsShowsSkippedServerAsUnavailable 证明"启用了但凭证缺失"不会从目录里消失。
func TestListToolsShowsSkippedServerAsUnavailable(t *testing.T) {
	h := &diagnoseHarness{}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	// 直接写配置制造凭证缺失（保存路径会拒绝）。
	doc, err := svc.store.Load(ctx, "config-user")
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	fields := map[string]json.RawMessage{"schemaVersion": json.RawMessage(`1`)}
	items := []map[string]any{{
		"id": "broken", "transport": "stdio", "command": "npx",
		"envRefs": map[string]string{"TOKEN": "missing"}, "enabled": true, "exposure": "direct",
	}}
	raw, _ := json.Marshal(items)
	fields["mcp"] = raw
	body, _ := json.Marshal(fields)
	if _, err := svc.store.CompareAndSwap(ctx, "config-user", doc.Revision, body); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	tools, err := svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}
	tool, ok := toolByName(tools, "mcp__broken__*")
	if !ok {
		t.Fatalf("凭证缺失的服务应从目录里可见并标不可用: %+v", tools)
	}
	if tool.Enabled || tool.CapabilityState != ToolUnavailable {
		t.Fatalf("不可用状态不对: %+v", tool)
	}
}

// TestListToolsReadsDisabledStateFromConfig 证明目录的状态来自当前配置而不是缓存。
func TestListToolsReadsDisabledStateFromConfig(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "fixture", State: "connected", Exposure: "direct", Tools: []string{"fixture_echo"},
	}}}}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("fixture", "npx")); err != nil {
		t.Fatalf("保存: %v", err)
	}
	tools, err := svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}
	if tool, ok := toolByName(tools, "mcp__fixture__fixture_echo"); !ok || !tool.Enabled {
		t.Fatalf("启用时工具应可用: %+v", tool)
	}

	rev := planRevision(t, svc)
	if _, err := svc.SetMCPEnabled(ctx, "fixture", rev, false); err != nil {
		t.Fatalf("禁用: %v", err)
	}
	tools, err = svc.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("列工具: %v", err)
	}
	if _, ok := toolByName(tools, "mcp__fixture__fixture_echo"); ok {
		t.Fatalf("禁用后旧工具行不应继续显示为可用: %+v", tools)
	}
	tool, ok := toolByName(tools, "mcp__fixture__*")
	if !ok || tool.Enabled || tool.CapabilityState != ToolDisabled {
		t.Fatalf("禁用后应显示为不可用: %+v", tools)
	}
}

// TestMCPDefinitionJSONShape 钉住保存到配置里的字段名（前端与证据都依赖它）。
func TestMCPDefinitionJSONShape(t *testing.T) {
	raw, err := json.Marshal(MCPDefinition{ID: "x", Transport: MCPTransportStdio, Command: "npx",
		Args: []string{"-y"}, EnvRefs: map[string]string{"A": "r"}, Enabled: true, Exposure: MCPExposureDirect})
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	for _, key := range []string{`"id"`, `"transport"`, `"command"`, `"args"`, `"envRefs"`, `"enabled"`, `"exposure"`} {
		if !containsJSONKey(raw, key) {
			t.Fatalf("MCPDefinition 缺少字段 %s: %s", key, raw)
		}
	}
	_ = config.KeyUser
}

func containsJSONKey(raw []byte, key string) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	_, ok := doc[string(key[1:len(key)-1])]
	return ok
}
