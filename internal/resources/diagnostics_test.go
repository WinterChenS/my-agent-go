package resources

// MCP 诊断的单元测试（C04 / MCP02 / MCP05）。
//
// 这里用注入的 runner 固定 Pi 的输出，验证"状态映射"本身：
// 连不上不能显示 connected、鉴权失败要单独分类、禁用的服务不启动任何进程。
// 真实 Pi 的 diagnosis 在 mcp_runtime_integration_test.go 里跑。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// diagnoseHarness 记录 runner 被调用的次数与最后一次参数。
type diagnoseHarness struct {
	calls int
	last  mcpCommand
	// mcpJSON 是诊断进程实际会读到的那份 mcp.json（临时目录里的副本）。
	mcpJSON string
	out     mcpListOutput
	err     error
}

func (h *diagnoseHarness) run(ctx context.Context, spec mcpCommand) (mcpListOutput, error) {
	h.calls++
	h.last = spec
	if data, err := os.ReadFile(filepath.Join(spec.Dir, "mcp.json")); err == nil {
		h.mcpJSON = string(data)
	}
	if h.err != nil {
		return mcpListOutput{}, h.err
	}
	return h.out, nil
}

func newDiagnoseService(t *testing.T, h *diagnoseHarness) (*Service, string) {
	t.Helper()
	svc, _, _ := newMCPService(t)
	dir := t.TempDir()
	svc.SetMCPDiagnoseEnv(MCPDiagnoseEnv{Executable: "pi-fixture"})
	svc.SetMCPDiagnoseRunner(h.run)
	return svc, dir
}

func TestDiagnoseMCPMapsConnectedWithToolExposure(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "fixture", State: "connected", Exposure: "direct", Tools: []string{"fixture_echo"},
	}}}}
	svc, dir := newDiagnoseService(t, h)
	ctx := context.Background()

	def := stdioDef("fixture", "npx", "-y", "fixture-mcp")
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	st, err := svc.DiagnoseMCP(ctx, "fixture")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State != MCPStateConnected {
		t.Fatalf("应判定 connected，实际 %s（%s）", st.State, st.Detail)
	}
	if len(st.Tools) != 1 || st.Tools[0].Name != "fixture_echo" {
		t.Fatalf("工具清单不对: %+v", st.Tools)
	}
	if !st.Tools[0].Enabled {
		t.Fatal("direct 暴露的工具应可达")
	}
	if st.OAuthSupported {
		t.Fatal("V1 没有实现 MCP OAuth，不能声称支持")
	}
	if h.calls != 1 {
		t.Fatalf("应只启动一次诊断进程，实际 %d", h.calls)
	}
	// 诊断读的是临时目录里的当前配置，不改写运行中的 agentDir。
	if !strings.Contains(h.mcpJSON, `"fixture"`) {
		t.Fatalf("诊断用的 mcp.json 应包含当前配置: %s", h.mcpJSON)
	}
	if _, err := os.Stat(filepath.Join(dir, "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("诊断不应改写运行中的 mcp.json（%s）", dir)
	}
	if h.last.Dir == dir {
		t.Fatal("诊断应使用临时 agentDir，而不是运行中的目录")
	}
}

// TestDiagnoseMCPMapsAuthFailureWithoutSideEffects 是 MCP02 的鉴权负例：
// 状态必须是 auth_required（不是 connected），消息要说清 OAuth 未支持。
func TestDiagnoseMCPMapsAuthFailureWithoutSideEffects(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "remote", State: "failed", Exposure: "direct",
		Error: "MCP server requires authentication",
	}}}}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()

	def := MCPDefinition{ID: "remote", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
		Enabled: true, Exposure: MCPExposureDirect}
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	st, err := svc.DiagnoseMCP(ctx, "remote")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State == MCPStateConnected {
		t.Fatal("鉴权失败不能显示 connected")
	}
	if st.State != MCPStateAuthRequired {
		t.Fatalf("应判定 auth_required，实际 %s", st.State)
	}
	if st.Error == nil || st.Error.Code != contracts.CodeUnauthorized {
		t.Fatalf("应返回 UNAUTHORIZED: %+v", st.Error)
	}
	if !strings.Contains(st.Error.Message, "OAuth") || !strings.Contains(st.Error.Message, "headerRefs") {
		t.Fatalf("消息应说明 OAuth 未支持与替代方案: %s", st.Error.Message)
	}
}

func TestDiagnoseMCPMapsTimeoutAndUnreachable(t *testing.T) {
	cases := []struct {
		name  string
		piErr string
		state string
		code  string
		msg   string
	}{
		{"连接被拒", "ECONNREFUSED: Unable to connect", MCPStateFailed, contracts.CodeProviderUnreachable, "连接失败"},
		{"超时", "request timed out after 60s", MCPStateTimeout, contracts.CodeProviderUnreachable, "超时"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
				Name: "remote", State: "failed", Exposure: "direct", Error: tc.piErr,
			}}}}
			svc, _ := newDiagnoseService(t, h)
			ctx := context.Background()
			if _, err := svc.SaveMCP(ctx, 0, MCPDefinition{ID: "remote", Transport: MCPTransportHTTP,
				URL: "http://127.0.0.1:1/mcp", Enabled: true, Exposure: MCPExposureDirect}); err != nil {
				t.Fatalf("保存: %v", err)
			}
			st, err := svc.DiagnoseMCP(ctx, "remote")
			if err != nil {
				t.Fatalf("诊断: %v", err)
			}
			if st.State != tc.state {
				t.Fatalf("状态期望 %s，实际 %s", tc.state, st.State)
			}
			if st.Error == nil || st.Error.Code != tc.code {
				t.Fatalf("错误码期望 %s，实际 %+v", tc.code, st.Error)
			}
			if !strings.Contains(st.Error.Message, tc.msg) {
				t.Fatalf("消息应包含 %q，实际 %s", tc.msg, st.Error.Message)
			}
		})
	}
}

func TestDiagnoseMCPMapsUnknownStateToFailed(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "remote", State: "weird", Exposure: "direct",
	}}}}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	if _, err := svc.SaveMCP(ctx, 0, MCPDefinition{ID: "remote", Transport: MCPTransportHTTP,
		URL: "http://127.0.0.1:1/mcp", Enabled: true, Exposure: MCPExposureDirect}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	st, err := svc.DiagnoseMCP(ctx, "remote")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State != MCPStateFailed {
		t.Fatalf("未知状态不能当成 connected，实际 %s", st.State)
	}
}

func TestDiagnoseMCPDisabledDoesNotStartProcess(t *testing.T) {
	h := &diagnoseHarness{}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	def := stdioDef("off", "npx")
	def.Enabled = false
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	st, err := svc.DiagnoseMCP(ctx, "off")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State != MCPStateDisabled {
		t.Fatalf("禁用服务应报 disabled，实际 %s", st.State)
	}
	if h.calls != 0 {
		t.Fatalf("禁用服务不应启动 PI 诊断进程，实际 %d 次", h.calls)
	}
}

func TestDiagnoseMCPSkippedCredentialDoesNotStartProcess(t *testing.T) {
	h := &diagnoseHarness{}
	svc, _ := newDiagnoseService(t, h)
	ctx := context.Background()
	// 直接写配置制造"凭证缺失"的状态（保存路径会拒绝）。
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

	st, err := svc.DiagnoseMCP(ctx, "broken")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State != MCPStateUnavailable {
		t.Fatalf("凭证缺失应报 unavailable，实际 %s", st.State)
	}
	if st.Error == nil || st.Error.Code != contracts.CodeDependencyMissing {
		t.Fatalf("应报 DEPENDENCY_MISSING: %+v", st.Error)
	}
	if h.calls != 0 {
		t.Fatalf("不可用的服务不应启动诊断进程，实际 %d 次", h.calls)
	}
}

func TestDiagnoseMCPRequiresPiConfiguration(t *testing.T) {
	svc, _, _ := newMCPService(t)
	svc.SetMCPDiagnoseEnv(MCPDiagnoseEnv{})
	ctx := context.Background()
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("fixture", "npx")); err != nil {
		t.Fatalf("保存: %v", err)
	}
	_, err := svc.DiagnoseMCP(ctx, "fixture")
	requireCode(t, err, contracts.CodeConfigMissing)
}

// TestDiagnoseMCPInjectsSecretsOnlyIntoProcessEnv 证明秘密不会写进 mcp.json，
// 而是作为诊断子进程的环境变量传入。
func TestDiagnoseMCPInjectsSecretsOnlyIntoProcessEnv(t *testing.T) {
	h := &diagnoseHarness{out: mcpListOutput{Servers: []piMCPServer{{
		Name: "remote", State: "connected", Exposure: "direct", Tools: []string{"x"},
	}}}}
	svc, dir := newDiagnoseService(t, h)
	ctx := context.Background()
	const secret = "Bearer very-secret"
	if err := svc.SaveMCPCredential(ctx, "mcp-secret", secret); err != nil {
		t.Fatalf("保存凭证: %v", err)
	}
	def := MCPDefinition{ID: "remote", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-secret"},
		Enabled:    true, Exposure: MCPExposureDirect}
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	if _, err := svc.DiagnoseMCP(ctx, "remote"); err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if strings.Contains(h.mcpJSON, secret) {
		t.Fatalf("诊断用的 mcp.json 里出现明文秘密: %s", h.mcpJSON)
	}
	if !strings.Contains(h.mcpJSON, "${"+mcpSecretEnvName("mcp-secret")+"}") {
		t.Fatalf("mcp.json 应使用 ${ENV} 引用: %s", h.mcpJSON)
	}
	envHasSecret := false
	for _, kv := range h.last.Env {
		if strings.HasSuffix(kv, "="+secret) {
			envHasSecret = true
		}
	}
	if !envHasSecret {
		t.Fatalf("秘密应只经诊断进程环境注入，实际 env=%v", h.last.Env)
	}
	if !strings.Contains(strings.Join(h.last.Env, "\n"), "PI_CODING_AGENT_DIR="+h.last.Dir) {
		t.Fatalf("诊断进程必须指向自己的临时 agentDir（%s）", h.last.Dir)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "mcp.json")); !os.IsNotExist(statErr) {
		t.Fatal("诊断不应在运行中的 agentDir 留下 mcp.json")
	}
}
