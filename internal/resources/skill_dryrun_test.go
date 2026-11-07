package resources

// 隔离试运行的沙箱与工具策略测试。
//
// 这里断言的是"试运行不可能碰到用户数据、也不可能拿到外部工具"：
//   - agentDir 与工作区都是临时目录，工作区是空的；
//   - 只挂载待测技能的一份副本，且没有别的技能/扩展/MCP；
//   - 只读策略下 write/edit/bash/网络/浏览器一律 deny，
//     需要审批的连接器/外部 MCP 工具在试运行里没有审批人，必须按 deny 处理；
//   - 结束后临时目录消失，用户 agentDir/工作区/数据根 hash 不变。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// fixtureDryRunSkill 造一个带脚本的技能目录，返回目录。
func fixtureDryRunSkill(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("建技能目录: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: 试运行测试\n---\n\n按步骤执行。\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("写 SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("写脚本: %v", err)
	}
	return dir
}

func beginDryRunForTest(t *testing.T) (*DryRunSandbox, string, string) {
	t.Helper()
	root := t.TempDir()
	userAgentDir := filepath.Join(root, "agent")
	userWorkspace := filepath.Join(root, "work")
	for _, dir := range []string{userAgentDir, userWorkspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("建用户目录: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(userAgentDir, "settings.json"), []byte(`{"user":true}`), 0o600); err != nil {
		t.Fatalf("写用户配置: %v", err)
	}
	src := fixtureDryRunSkill(t, "dry-skill")
	sb, err := BeginDryRun(root, "dry-skill", src)
	if err != nil {
		t.Fatalf("创建沙箱失败: %v", err)
	}
	t.Cleanup(func() { _ = sb.Cleanup() })
	return sb, userAgentDir, userWorkspace
}

// TestBeginDryRunUsesTempAgentDirAndEmptyWorkspace 守"沙箱就是临时目录"。
func TestBeginDryRunUsesTempAgentDirAndEmptyWorkspace(t *testing.T) {
	sb, userAgentDir, userWorkspace := beginDryRunForTest(t)

	for name, dir := range map[string]string{"agentDir": sb.AgentDir(), "workspace": sb.WorkspaceDir()} {
		if !isWithin(sb.Root(), dir) {
			t.Fatalf("%s 不在沙箱根内: %s", name, dir)
		}
		if isWithin(userAgentDir, dir) || isWithin(userWorkspace, dir) {
			t.Fatalf("%s 落到了用户目录里: %s", name, dir)
		}
	}
	entries, err := os.ReadDir(sb.WorkspaceDir())
	if err != nil {
		t.Fatalf("工作区不可读: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("试运行工作区必须是空的，实际 %d 项", len(entries))
	}
	if _, err := os.Stat(sb.SkillPath()); err != nil {
		t.Fatalf("沙箱里没有技能副本: %v", err)
	}
	// 技能目录里的其他文件也要复制过来（否则技能会表现成"缺文件"）。
	if _, err := os.Stat(filepath.Join(filepath.Dir(sb.SkillPath()), "scripts", "run.sh")); err != nil {
		t.Fatalf("技能副本不完整: %v", err)
	}
}

// TestDryRunIsolationMountsOnlyTheTestedSkill 守"只挂载待测技能"。
func TestDryRunIsolationMountsOnlyTheTestedSkill(t *testing.T) {
	sb, _, _ := beginDryRunForTest(t)
	ext := filepath.Join(t.TempDir(), "harness-policy.ts")
	if err := os.WriteFile(ext, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展: %v", err)
	}
	iso := sb.Isolation(ext)
	args := iso.Args()
	if len(iso.Skills) != 1 || iso.Skills[0] != sb.SkillPath() {
		t.Fatalf("只应挂载沙箱里的那一份技能: %v", iso.Skills)
	}
	if iso.HarnessMCP {
		t.Fatal("试运行不得启用自有 MCP（会带进记忆/工具通道）")
	}
	if len(iso.ExtraExtensions) != 0 {
		t.Fatalf("试运行不得加载额外扩展: %v", iso.ExtraExtensions)
	}
	joined := joinArgs(args)
	if !containsToken(args, "--no-mcp") {
		t.Fatalf("隔离参数缺少 --no-mcp: %s", joined)
	}
	if containsToken(args, "builtin:mcp") {
		t.Fatalf("隔离参数不应重新启用内置 MCP: %s", joined)
	}
	if err := iso.Validate(); err != nil {
		t.Fatalf("沙箱隔离参数不成立: %v", err)
	}
	// 没有策略扩展时不能启动：没有执行前授权就等于没有只读约束。
	if err := sb.Isolation("").Validate(); err == nil {
		t.Fatal("缺少策略扩展时必须拒绝启动")
	}
}

// TestDryRunPolicyNeverAllowsUnsafeTools 是试运行的安全边界。
func TestDryRunPolicyNeverAllowsUnsafeTools(t *testing.T) {
	sb, _, _ := beginDryRunForTest(t)
	pol, err := sb.Policy()
	if err != nil {
		t.Fatalf("构造只读策略失败: %v", err)
	}
	perm := sb.Permissions()
	if perm.Filesystem != "read" {
		t.Fatalf("试运行文件系统权限必须是 read，实际 %q", perm.Filesystem)
	}
	if perm.Network || perm.Shell {
		t.Fatalf("试运行不得有 network/shell 权限: %+v", perm)
	}
	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-dry", SessionID: "sess-dry", RunID: "run-dry",
		ConfigRevision: 1, ControlGeneration: 1,
	}
	cases := []struct {
		name   string
		call   policy.ToolCall
		reason string
	}{
		{"write", policy.ToolCall{Name: "write", Source: policy.SourceBuiltin, ResourceScope: filepath.Join(sb.WorkspaceDir(), "x.txt")}, "写文件"},
		{"edit", policy.ToolCall{Name: "edit", Source: policy.SourceBuiltin, ResourceScope: filepath.Join(sb.WorkspaceDir(), "SKILL.md")}, "编辑"},
		{"shell", policy.ToolCall{Name: "bash", Source: policy.SourceBuiltin}, "shell"},
		{"network", policy.ToolCall{Name: "fetch_url", Source: policy.SourceHarnessMCP}, "网络"},
		{"browser", policy.ToolCall{Name: "browser_navigate", Source: policy.SourceBuiltin}, "浏览器"},
		{"connector", policy.ToolCall{Name: "connector_github_issue", Source: policy.SourceHarnessMCP}, "连接器"},
		{"external-mcp", policy.ToolCall{Name: "mcp__remote__query", Source: policy.SourceExternalMCP}, "外部 MCP"},
	}
	for _, tc := range cases {
		res, err := pol.Authorize(context.Background(), exec, tc.call)
		if err != nil {
			t.Fatalf("%s: 授权调用报错: %v", tc.name, err)
		}
		guard := sb.GuardedDecision(res)
		if guard != policy.DecisionDeny {
			t.Fatalf("%s（%s）在试运行里必须被拒绝，实际 %s", tc.name, tc.reason, guard)
		}
	}
}

// TestDryRunDeniedCapabilitiesAreListed 让界面能逐项说明"试运行没有做什么"。
func TestDryRunDeniedCapabilitiesAreListed(t *testing.T) {
	sb, _, _ := beginDryRunForTest(t)
	want := []string{
		DryRunCapabilityWrite, DryRunCapabilityShell, DryRunCapabilityNetwork,
		DryRunCapabilityBrowser, DryRunCapabilityConnectors, DryRunCapabilityExternalMCP,
	}
	got := map[string]string{}
	for _, c := range sb.DeniedCapabilities() {
		got[c.Name] = c.Rule
	}
	for _, name := range want {
		if got[name] == "" {
			t.Fatalf("缺少被禁用能力的说明: %s（实际 %v）", name, got)
		}
	}
}

// TestDryRunCleanupRemovesSandboxAndIsIdempotent。
func TestDryRunCleanupRemovesSandboxAndIsIdempotent(t *testing.T) {
	sb, _, _ := beginDryRunForTest(t)
	root := sb.Root()
	if err := sb.Cleanup(); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("清理后沙箱仍存在: %v", err)
	}
	if !sb.Cleaned() {
		t.Fatal("清理后 Cleaned() 应为 true")
	}
	if err := sb.Cleanup(); err != nil {
		t.Fatalf("重复清理应当无害: %v", err)
	}
}

// TestDryRunUserStateStaysUnchanged 守"用户数据 hash 不变"这条可验证的断言。
func TestDryRunUserStateStaysUnchanged(t *testing.T) {
	sb, userAgentDir, userWorkspace := beginDryRunForTest(t)
	root := filepath.Dir(userAgentDir)
	before, err := TreeDigest(userAgentDir, userWorkspace, filepath.Join(root, "memory.db"))
	if err != nil {
		t.Fatalf("计算 hash 失败: %v", err)
	}
	if err := sb.VerifyUserStateUnchanged(before); err != nil {
		t.Fatalf("未执行任何动作时不应有变化: %v", err)
	}
	// 用户改动一次真实配置：验证这套 hash 真的能发现变化。
	if err := os.WriteFile(filepath.Join(userAgentDir, "settings.json"), []byte(`{"user":false}`), 0o600); err != nil {
		t.Fatalf("改用户配置: %v", err)
	}
	if err := sb.VerifyUserStateUnchanged(before); err == nil {
		t.Fatal("用户配置被改动后必须能检测出来")
	}
}

// TestDryRunSandboxSkillCopyIsReadOnlyCopy 守"改的是副本，不是托管目录"。
func TestDryRunSandboxSkillCopyIsReadOnlyCopy(t *testing.T) {
	sb, _, _ := beginDryRunForTest(t)
	before, err := sb.SkillDigest()
	if err != nil {
		t.Fatalf("计算技能 hash 失败: %v", err)
	}
	if err := os.WriteFile(sb.SkillPath(), []byte("被改过"), 0o600); err != nil {
		t.Fatalf("写副本: %v", err)
	}
	after, err := sb.SkillDigest()
	if err != nil {
		t.Fatalf("再次计算失败: %v", err)
	}
	if before == after {
		t.Fatal("技能副本被改但 hash 没变")
	}
}

// TestTreeDigestMarksMissingPaths 让"不可用"与"空"可区分。
func TestTreeDigestMarksMissingPaths(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "a")
	if err := os.MkdirAll(present, 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(present, "f"), []byte("x"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	got, err := TreeDigest(present, filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("计算失败: %v", err)
	}
	if got[present] == "" || got[filepath.Join(dir, "missing")] != "" {
		t.Fatalf("缺失路径必须是空摘要（不是猜成空目录）: %v", got)
	}
}

// isWithin 判断 child 是否在 parent 之内（含自身）。
func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !startsWithDotDot(rel))
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}

// joinArgs/containsToken 只用于测试里读参数，不参与生产判定。
func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

func containsToken(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
