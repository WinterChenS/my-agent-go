package app

// C04 的绑定层测试：MCP 保存/禁用/诊断与"禁用必须落到运行时"。
//
// 这里用 stub worker（不启动真实 Pi），聚焦两条容易骗人的路径：
//   - 有 run 在跑时改 MCP：旧 run 必须保持旧 mcp.json，状态报 pending；
//   - 空闲时改 MCP：下一次 ensureRuntime 重建，mcp.json 里真的移除该服务。
//
// 真实 Pi 的发现/调用/禁用不可调用在 internal/resources/mcp_runtime_integration_test.go。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/resources"
)

func readAgentMCP(t *testing.T, d *Desktop) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.agentDir(), "mcp.json"))
	if err != nil {
		t.Fatalf("读 mcp.json: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("mcp.json 不是合法 JSON: %v\n%s", err, data)
	}
	return root
}

func mcpServerNames(root map[string]any) map[string]bool {
	out := map[string]bool{}
	servers, _ := root["mcpServers"].(map[string]any)
	for name := range servers {
		out[name] = true
	}
	return out
}

func demoMCPDef() resources.MCPDefinition {
	return resources.MCPDefinition{
		ID:        "fixture-ext",
		Transport: resources.MCPTransportStdio,
		Command:   "npx",
		Args:      []string{"-y", "fixture-mcp"},
		Enabled:   true,
		Exposure:  resources.MCPExposureDirect,
	}
}

// TestMCPDisabledOnlyAtSafePoint 是"空闲时改配置 → 下一次 ensureRuntime 生效"。
func TestMCPDisabledOnlyAtSafePoint(t *testing.T) {
	d := prepareRuntimeDesktop(t)

	saved := d.SaveMCP(0, demoMCPDef())
	if saved.Error != nil {
		t.Fatalf("保存: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	if _, err := d.ensureRuntime(); err != nil {
		t.Fatalf("构建运行时: %v", err)
	}
	names := mcpServerNames(readAgentMCP(t, d))
	if !names["fixture-ext"] {
		t.Fatalf("启用后 mcp.json 应包含该服务: %v", names)
	}
	if !names["harness"] {
		t.Fatalf("自有 MCP 条目不应被覆盖: %v", names)
	}
	if st := d.Status(); len(st.RuntimeMCP) != 1 || st.RuntimeMCP[0] != "fixture-ext" || st.MCPPending {
		t.Fatalf("运行状态不对: runtimeMCP=%v pending=%v", st.RuntimeMCP, st.MCPPending)
	}

	// 禁用：配置立即变，但运行时还没到安全点。
	disabled := d.SetMCPEnabled("fixture-ext", saved.Revision, false)
	if disabled.Error != nil {
		t.Fatalf("禁用: %s: %s", disabled.Error.Code, disabled.Error.Message)
	}
	if !disabled.Pending {
		t.Fatal("禁用后必须报 pending（还没有重建运行时）")
	}
	if names := mcpServerNames(readAgentMCP(t, d)); !names["fixture-ext"] {
		t.Fatalf("空闲但尚未重建时不应提前改写旧 mcp.json: %v", names)
	}
	if !d.Status().MCPPending {
		t.Fatal("Status.MCPPending 应为 true")
	}

	// 下一次 ensureRuntime（空闲时）：重建并移除该服务。
	if _, err := d.ensureRuntime(); err != nil {
		t.Fatalf("重建: %v", err)
	}
	if names := mcpServerNames(readAgentMCP(t, d)); names["fixture-ext"] {
		t.Fatalf("禁用后 mcp.json 仍有该服务——禁用只改了菜单: %v", names)
	}
	st := d.Status()
	if len(st.RuntimeMCP) != 0 || st.MCPPending {
		t.Fatalf("重建后应无外部 MCP 且不再 pending: runtimeMCP=%v pending=%v", st.RuntimeMCP, st.MCPPending)
	}

	// CONN05 的外部观察：安全点之后 agentDir 里的 mcp.json 真的少了一条（从磁盘读）。
	observeTree(t, d.agentDir(), "mcp-agent-tree.json", map[string]any{
		"runtimeMCPCount": len(st.RuntimeMCP),
		"mcpPending":      st.MCPPending,
	})
}

// TestMCPChangeDuringRunKeepsOldRuntime 是"旧 run 不静默变化"。
func TestMCPChangeDuringRunKeepsOldRuntime(t *testing.T) {
	d := trustedDesktopWithStubWorker(t)
	saved := d.SaveMCP(0, demoMCPDef())
	if saved.Error != nil {
		t.Fatalf("保存: %s: %s", saved.Error.Code, saved.Error.Message)
	}

	snap, err := d.Start("你好")
	if err != nil {
		t.Fatalf("启动: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.agentDir(), "mcp.json")); err != nil {
		t.Fatalf("启动后应写出 mcp.json: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(d.agentDir(), "mcp.json"))
	if err != nil {
		t.Fatalf("读 mcp.json: %v", err)
	}

	// 运行中禁用：旧 run 的 mcp.json 不能被改写。
	disabled := d.SetMCPEnabled("fixture-ext", saved.Revision, false)
	if disabled.Error != nil {
		t.Fatalf("禁用: %s: %s", disabled.Error.Code, disabled.Error.Message)
	}
	if !disabled.Pending {
		t.Fatal("有 run 在跑时必须报 pending")
	}
	after, err := os.ReadFile(filepath.Join(d.agentDir(), "mcp.json"))
	if err != nil {
		t.Fatalf("读 mcp.json: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("运行中改 MCP 静默改写了旧 run 的 mcp.json:\n前: %s\n后: %s", before, after)
	}

	svc, err := d.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	d.mu.Lock()
	same := d.svc == svc
	d.mu.Unlock()
	if !same {
		t.Fatal("改 MCP 把正在跑的运行时换掉了")
	}
	if svc.ActiveCount() != 1 {
		t.Fatalf("活跃 run 数不应变化，实际 %d", svc.ActiveCount())
	}
	list := d.ListMCP()
	if !list.Pending || !list.ActiveRun {
		t.Fatalf("列表应同时报 pending 与 activeRun: %+v", list)
	}
	if len(list.RuntimeServers) != 1 || list.RuntimeServers[0] != "fixture-ext" {
		t.Fatalf("运行中的集合应保持旧快照: %v", list.RuntimeServers)
	}

	// 结束 run，让下一次 ensureRuntime 真正落到新集合。
	if err := d.Abort(string(snap.RunID)); err != nil {
		t.Fatalf("停止: %v", err)
	}
	if !eventuallyTrue(3*time.Second, func() bool { return svc.ActiveCount() == 0 }) {
		t.Fatalf("run 没有结束，活跃数 %d", svc.ActiveCount())
	}
	if _, err := d.ensureRuntime(); err != nil {
		t.Fatalf("空闲后重建: %v", err)
	}
	if names := mcpServerNames(readAgentMCP(t, d)); names["fixture-ext"] {
		t.Fatalf("空闲后重建仍未移除已禁用服务: %v", names)
	}
	if d.Status().MCPPending {
		t.Fatal("重建后不应再 pending")
	}

	// CONN05 的外部观察：旧 run 期间磁盘上的 mcp.json 未被改写，安全点之后才变。
	observeTree(t, d.agentDir(), "mcp-agent-tree.json", map[string]any{
		"changedDuringRun": string(before) != string(after),
	})
}

// TestMCPDeleteAndCreateThroughBindings 覆盖管理与删除。
func TestMCPDeleteAndCreateThroughBindings(t *testing.T) {
	d, _ := newTestDesktop(t)

	httpDef := resources.MCPDefinition{
		ID: "remote-docs", Transport: resources.MCPTransportHTTP,
		URL:     "https://docs.example.test/mcp",
		Enabled: true, Exposure: resources.MCPExposureDeferred,
	}
	saved := d.SaveMCP(0, httpDef)
	if saved.Error != nil {
		t.Fatalf("保存: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	list := d.ListMCP()
	if list.Error != nil {
		t.Fatalf("列举: %s", list.Error.Message)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "remote-docs" {
		t.Fatalf("列表不对: %+v", list.Items)
	}

	// 过期 revision 必须冲突。
	if res := d.SaveMCP(0, httpDef); res.Error == nil || res.Error.Code != "CONFIG_CONFLICT" {
		t.Fatalf("过期 revision 应 CONFIG_CONFLICT，实际 %+v", res.Error)
	}

	deleted := d.DeleteMCP(saved.Revision, "remote-docs")
	if deleted.Error != nil {
		t.Fatalf("删除: %s: %s", deleted.Error.Code, deleted.Error.Message)
	}
	if items := d.ListMCP().Items; len(items) != 0 {
		t.Fatalf("删除后仍有配置: %+v", items)
	}
}

// TestMCPCredentialNeverEchoed 确认绑定层不回传凭证值。
func TestMCPCredentialNeverEchoed(t *testing.T) {
	d, _ := newTestDesktop(t)
	const secret = "Bearer echo-test-token"
	res := d.SaveMCPCredential("mcp-test-ref", secret)
	if res.Error != nil {
		t.Fatalf("保存凭证: %s: %s", res.Error.Code, res.Error.Message)
	}
	if strings.Contains(res.Ref, "echo-test-token") {
		t.Fatalf("结果里回显了秘密: %+v", res)
	}
	def := resources.MCPDefinition{
		ID: "secure", Transport: resources.MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-test-ref"},
		Enabled:    true, Exposure: resources.MCPExposureDirect,
	}
	saved := d.SaveMCP(0, def)
	if saved.Error != nil {
		t.Fatalf("保存 MCP: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "echo-test-token") {
		t.Fatalf("MCP 结果里出现明文秘密: %s", raw)
	}
}

// TestDiagnoseMCPFailureIsNotConnected 确认诊断失败不会被当成 connected。
func TestDiagnoseMCPFailureIsNotConnected(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	// prepareRuntimeDesktop 的 Pi 是内容为 "stub" 的假文件：诊断会启动失败。
	if saved := d.SaveMCP(0, demoMCPDef()); saved.Error != nil {
		t.Fatalf("保存: %s", saved.Error.Message)
	}
	res := d.DiagnoseMCP("fixture-ext")
	if res.Error == nil {
		t.Fatalf("假 Pi 不应诊断成功，实际 %+v", res.Status)
	}
	if res.Status.State == resources.MCPStateConnected {
		t.Fatal("诊断失败却报告 connected")
	}
}

// TestListToolsIncludesHarnessAndBuiltin 是工具目录的绑定层冒烟测试。
func TestListToolsIncludesHarnessAndBuiltin(t *testing.T) {
	d, _ := newTestDesktop(t)
	res := d.ListTools()
	if res.Error != nil {
		t.Fatalf("列工具: %s: %s", res.Error.Code, res.Error.Message)
	}
	byName := map[string]resources.ToolDescriptor{}
	for _, item := range res.Items {
		byName[item.Name] = item
	}
	if _, ok := byName["read"]; !ok {
		t.Fatalf("缺少内置工具: %+v", res.Items)
	}
	probe, ok := byName["harness_probe"]
	if !ok {
		t.Fatalf("缺少自有 MCP 工具: %+v", res.Items)
	}
	if probe.Source != resources.ToolSourceHarness {
		t.Fatalf("harness_probe 来源应为 harness，实际 %s", probe.Source)
	}
	if probe.Policy != resources.ToolPolicyApproval {
		t.Fatalf("自有 MCP 也要审批，实际 %s", probe.Policy)
	}
}

// TestSaveMCPValidationThroughBindings 确认绑定层把校验错误原样带出。
func TestSaveMCPValidationThroughBindings(t *testing.T) {
	d, _ := newTestDesktop(t)
	bad := resources.MCPDefinition{
		ID: "shell", Transport: resources.MCPTransportStdio,
		Command: "npx -y fixture", Enabled: true, Exposure: resources.MCPExposureDirect,
	}
	res := d.SaveMCP(0, bad)
	if res.Error == nil || res.Error.Code != "UNSUPPORTED" {
		t.Fatalf("应返回 UNSUPPORTED，实际 %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "args") {
		t.Fatalf("错误应说明 command/args 分开: %s", res.Error.Message)
	}
}
