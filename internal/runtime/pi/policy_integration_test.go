package pi

// F04 的真实 Pi 集成验证（SEC02/SEC03/SEC08）。
//
// 与 policy/policybridge 的单测不同，这里跑的是**真实的 Pi 进程**：
// 模型（受控 fixture provider）真的要求执行 bash，Pi 真的走 tool_call 钩子，
// 我们的扩展真的通过 HTTP 去问策略端点。因此它能回答单测回答不了的问题：
// "策略层说 deny 之后，工具到底跑没跑"。
//
// 同时验证项目级资源隔离：工作区里放了会在加载时写 marker 的项目扩展、
// 会被观察到的项目设置、以及一段 AGENTS.md 注入。全部必须不生效。
//
// 开启方式：MYAGENT_PI_POLICY=1（需要 .tools/pi/v1.5.0 与 node）。

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/fixtures/openaisse"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
)

// evilExtensionSource 返回一个"一被加载就写 marker"的项目扩展。
//
// 这是 SEC08 的探针：如果 .pi/extensions 被加载，marker 就会出现在工作区里。
// 用真实副作用而不是检查配置布尔值——配置不加载不代表扩展没跑。
func evilExtensionSource(marker string) string {
	escaped, _ := json.Marshal(marker)
	return "import { writeFileSync } from \"node:fs\";\n" +
		"writeFileSync(" + string(escaped) + ", \"loaded\");\n" +
		"export default function () {}\n"
}

type policyRun struct {
	records []record
	stderr  string
	code    int
	pending int32 // 策略端点收到的待审批次数
}

// runScenario 描述一次真实 Pi 运行。
type runScenario struct {
	// scenario 是 fixture provider 的回放脚本名。
	scenario string
	// choice 是用例替用户给出的审批结论。
	choice policy.ApprovalChoice
	// harnessMCP 为 true 时用 -e builtin:mcp 重新启用 MCP（-ne 会连带关掉它）。
	harnessMCP bool
}

// runPolicyCase 在 work 目录上跑一次真实 Pi，策略结论由 sc.choice 决定。
func runPolicyCase(t *testing.T, root, work, agentDir string, sc runScenario) policyRun {
	t.Helper()

	guard, err := policy.NewPathGuard(work)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	svc := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: true, Network: false},
		PathGuard:   guard,
		ApprovalTTL: 60 * time.Second,
	})

	exec := contracts.ExecutionContext{
		WorkspaceID: "ws", SessionID: "sess-integration", RunID: "run-integration",
		ConfigRevision: 1, ControlGeneration: 1,
	}
	var pending int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 端点指针必须先声明：OnPending 回调里要用它提交结论，而端点又要靠这个回调启动。
	// 回调只在 Pi 发来请求时触发，那时 Start 早已返回，不存在空指针窗口。
	var srv *policybridge.Server
	srv, err = policybridge.Start(ctx, policybridge.Options{
		Service: svc,
		Binding: policybridge.Binding{
			SessionID: exec.SessionID, RunID: exec.RunID,
			ConfigRevision: exec.ConfigRevision, ControlGeneration: exec.ControlGeneration,
		},
		AskTimeout: 20 * time.Second,
		OnPending: func(p policy.ApprovalRequest) {
			atomic.AddInt32(&pending, 1)
			// 由用例指定的结论；用独立 goroutine 提交，避免阻塞 Pi 的请求。
			go func() {
				time.Sleep(50 * time.Millisecond)
				_ = resolveViaBridge(srv.Endpoint(), srv.Env(), exec.SessionID, p.ID, sc.choice)
			}()
		},
	})
	if err != nil {
		t.Fatalf("启动策略端点: %v", err)
	}
	defer func() { _ = srv.Close(context.Background()) }()

	spec := IsolationSpec{
		PolicyExtension: filepath.Join(root, "pi-extensions", "harness-policy.ts"),
		HarnessMCP:      sc.harnessMCP,
	}
	extra := spec.Args()

	launch := nodeLaunch(captureNodePath(t), piCLIPath(t, root))
	launch.env = append(launch.env, srv.Env()...)

	recs, stderr, code := runPiRPC(t, launch, work, agentDir,
		[][]string{{promptCmd("req-policy", sc.scenario)}}, 1, true, extra, 0, 120*time.Second)

	return policyRun{records: recs, stderr: stderr, code: code, pending: atomic.LoadInt32(&pending)}
}

// resolveViaBridge 从端点环境变量里取出 URL/令牌，提交人工结论。
func resolveViaBridge(ep policybridge.Endpoint, env []string, session contracts.SessionID, id string, choice policy.ApprovalChoice) error {
	var token string
	for _, kv := range env {
		if strings.HasPrefix(kv, "MYAGENT_POLICY_TOKEN=") {
			token = strings.TrimPrefix(kv, "MYAGENT_POLICY_TOKEN=")
		}
	}
	client := policybridge.NewClient(ep, token, session, 10*time.Second)
	return client.Resolve(context.Background(), id, choice)
}

// TestRealPiPolicyBlocksToolAndProjectResources 是 F04 的关键证据。
func TestRealPiPolicyBlocksToolAndProjectResources(t *testing.T) {
	if os.Getenv("MYAGENT_PI_POLICY") != "1" {
		t.Skip("设置 MYAGENT_PI_POLICY=1 才执行真实 Pi 的策略集成验证")
	}
	if runtime.GOOS != "windows" {
		t.Skip("当前集成脚本按 Windows 路径编写")
	}

	root := repoRoot(t)
	work := t.TempDir()
	agentDir := filepath.Join(work, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("建 agentDir: %v", err)
	}
	recDir := filepath.Join(work, "requests")
	srv := httptest.NewServer(openaisse.NewHandler(recDir))
	defer srv.Close()
	writeFixtureModels(t, agentDir, srv.URL)

	// ── 项目级陷阱资源 ──
	evilDir := filepath.Join(work, ".pi", "extensions")
	if err := os.MkdirAll(evilDir, 0o755); err != nil {
		t.Fatalf("建项目扩展目录: %v", err)
	}
	evilMarker := filepath.Join(work, "evil-extension-ran.txt")
	if err := os.WriteFile(filepath.Join(evilDir, "evil.ts"), []byte(evilExtensionSource(evilMarker)), 0o644); err != nil {
		t.Fatalf("写项目扩展: %v", err)
	}
	// 项目设置：若被加载，codemode 会出现在工具列表里（默认不启用）。
	settings := []byte(`{"defaultTools":["+codemode"]}`)
	if err := os.WriteFile(filepath.Join(work, ".pi", "settings.json"), settings, 0o644); err != nil {
		t.Fatalf("写项目设置: %v", err)
	}
	// 上下文文件：若被加载，这段文字会出现在发往模型的请求里。
	const agentsMarker = "PROJECT-INSTRUCTION-LEAK-CANARY"
	if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte(agentsMarker+"\n"), 0o644); err != nil {
		t.Fatalf("写 AGENTS.md: %v", err)
	}

	// 副作用探针：bash 命令会写这个文件。断言落在文件系统上，
	// 因为工具参数字符串同样会出现在记录里，用字符串判断区分不了"跑了"和"被拦了"。
	toolMarker := filepath.Join(work, "tool-ran.txt")

	// ── 场景 1：拒绝 ──
	_ = os.Remove(toolMarker)
	denyRun := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-file", choice: policy.ApprovalDeny})
	allDeny := joinRecords(denyRun.records)
	t.Logf("拒绝场景：%d 条记录，退出码 %d，待审批 %d 次", len(denyRun.records), denyRun.code, denyRun.pending)
	if denyRun.stderr != "" {
		t.Logf("stderr（前 400 字节）：\n%s", truncate(denyRun.stderr, 400))
	}

	if denyRun.pending == 0 {
		t.Fatalf("策略端点没有收到任何待审批请求——Pi 侧扩展可能根本没加载，本用例不构成证据")
	}
	if _, err := os.Stat(toolMarker); err == nil {
		t.Fatal("策略拒绝后命令仍然执行了：work 目录出现 tool-ran.txt")
	} else if !os.IsNotExist(err) {
		t.Fatalf("检查副作用文件失败: %v", err)
	}
	if !strings.Contains(allDeny, "未获授权") && !strings.Contains(allDeny, "拒绝") {
		t.Fatalf("工具结果里应有拒绝原因，实际记录中没有；前 1200 字节：\n%s", truncate(allDeny, 1200))
	}

	// ── 场景 2：批准（正向对照） ──
	// 先记下拒绝场景的结论，再清掉文件跑正向对照（否则两场的结果会互相污染）。
	denyToolRan := fileExists(toolMarker)

	// 没有这一场，"没执行"可能只是模型没要求执行——那是最常见的假绿。
	_ = os.Remove(toolMarker)
	allowRun := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-file", choice: policy.ApprovalOnce})
	allAllow := joinRecords(allowRun.records)
	t.Logf("批准场景：%d 条记录，退出码 %d，待审批 %d 次", len(allowRun.records), allowRun.code, allowRun.pending)
	if _, err := os.Stat(toolMarker); err != nil {
		t.Fatalf("批准后命令应真实执行并写出 tool-ran.txt，正向对照失败（%v）；前 1200 字节：\n%s",
			err, truncate(allAllow, 1200))
	}

	// ── MCP 通道：自有/外部 MCP 工具同样必须执行前授权 ──
	//
	// 这条同时验证了一个实测结论：-ne 会把内置 MCP 一起关掉，要保留 MCP 必须
	// 再显式 -e builtin:mcp。没有这句，Pi 根本不会连接 MCP server，
	// "MCP 调用被拦"就会退化成"MCP 不存在"，是个假绿。
	mcpLog := filepath.Join(work, "mcp-requests.log")
	writeMCPConfigWithLog(t, root, agentDir, mcpLog)

	_ = os.Remove(mcpLog)
	mcpDeny := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-mcp", choice: policy.ApprovalDeny, harnessMCP: true})
	denyLog := readIfExists(mcpLog)
	t.Logf("MCP 拒绝场景：%d 条记录，待审批 %d 次", len(mcpDeny.records), mcpDeny.pending)
	if mcpDeny.pending == 0 {
		t.Fatalf("MCP 调用没有触发审批，无法证明该通道被执行前检查覆盖")
	}
	if strings.Contains(denyLog, `"tools/call"`) {
		t.Fatalf("被拒绝的 MCP 调用仍然到达 handler；MCP 日志：\n%s", truncate(denyLog, 600))
	}

	_ = os.Remove(mcpLog)
	mcpAllow := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-mcp", choice: policy.ApprovalOnce, harnessMCP: true})
	allowLog := readIfExists(mcpLog)
	t.Logf("MCP 批准场景：%d 条记录，待审批 %d 次", len(mcpAllow.records), mcpAllow.pending)
	if !strings.Contains(allowLog, `"tools/call"`) {
		t.Fatalf("批准后 MCP 调用应到达 handler（正向对照失败）；MCP 日志：\n%s", truncate(allowLog, 600))
	}

	// ── SEC08：项目级资源一律不得生效（两场都不行）──
	if _, err := os.Stat(evilMarker); err == nil {
		t.Fatal("项目扩展被执行了：工作区出现 evil-extension-ran.txt（-na/-ne 未生效）")
	} else if !os.IsNotExist(err) {
		t.Fatalf("检查项目扩展 marker 失败: %v", err)
	}

	if reqs := readRequests(t, recDir); strings.Contains(reqs, agentsMarker) {
		t.Fatal("AGENTS.md 被喂给了模型（-nc 未生效）")
	}
	if reqs := readRequests(t, recDir); strings.Contains(reqs, `"codemode"`) {
		t.Fatal("项目 .pi/settings.json 被应用了（工具列表出现 codemode）")
	}

	// 证据落盘：只记录结论与关键计数，不复制整份会话。
	evidence := map[string]any{
		"deny": map[string]any{
			"records": len(denyRun.records), "exitCode": denyRun.code,
			"pendingApprovals": denyRun.pending, "toolRan": denyToolRan,
		},
		"allow": map[string]any{
			"records": len(allowRun.records), "exitCode": allowRun.code,
			"pendingApprovals": allowRun.pending, "toolRan": fileExists(toolMarker),
		},
		"mcp": map[string]any{
			"deny":  map[string]any{"pendingApprovals": mcpDeny.pending, "handlerReached": strings.Contains(denyLog, `"tools/call"`)},
			"allow": map[string]any{"pendingApprovals": mcpAllow.pending, "handlerReached": strings.Contains(allowLog, `"tools/call"`)},
		},
		"projectExtensionLoaded": false,
		"agentsMdLeaked":         false,
		"projectSettingsApplied": false,
		"isolationFlags":         requiredIsolationFlags,
		// 实际运行时用的是仓库内扩展的绝对路径；证据里记相对路径，避免泄漏本机路径。
		"policyExtension": "pi-extensions/harness-policy.ts",
	}
	if b, err := json.MarshalIndent(evidence, "", "  "); err == nil {
		t.Logf("策略集成证据：\n%s", b)
		out := filepath.Join(root, "docs", "evidence", "raw", "f04-policy-integration.json")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err == nil {
			_ = os.WriteFile(out, b, 0o644)
		}
	}
}

// writeMCPConfigWithLog 构建受控 MCP server 并写入 agentDir/mcp.json，
// 让它把收到的每条请求写进 logPath —— 这就是"handler 是否真的被调用"的判据。
//
// mcp.json 放在 agentDir（托管配置目录）而不是工作区的 .pi/：工作区那份属于
// 项目级资源，按 SEC08 必须被忽略，放进来的话这个用例测的就不是 MCP 通道了。
func writeMCPConfigWithLog(t *testing.T, root, agentDir, logPath string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fixture-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./tests/fixtures/mcp")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 fixture MCP 失败: %v\n%s", err, out)
	}
	doc := map[string]any{
		"mcpServers": map[string]any{
			"fixture": map[string]any{
				"command":     bin,
				"args":        []string{"-log", logPath},
				"description": "受控 fixture MCP server（F04 通道验证）",
				"exposure":    "direct",
			},
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("序列化 mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "mcp.json"), b, 0o644); err != nil {
		t.Fatalf("写 mcp.json: %v", err)
	}
}

// readIfExists 读文件内容，不存在时返回空串（用于"日志根本没生成"这种情形）。
func readIfExists(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// fileExists 报告路径是否作为文件存在。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func joinRecords(recs []record) string {
	var b strings.Builder
	for _, r := range recs {
		b.WriteString(r.Line)
		b.WriteString("\n")
	}
	return b.String()
}

// readRequests 汇总 fixture provider 收到的全部请求体，用于判断"模型看到了什么"。
func readRequests(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr == nil {
			b.Write(data)
		}
		return nil
	})
	return b.String()
}
