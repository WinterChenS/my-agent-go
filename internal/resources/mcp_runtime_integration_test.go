package resources

// MCP 的真实 Pi 集成测试（C04 / MCP01–MCP03 / MCP05）。
//
// 只有设置 MYAGENT_PI_WORKER_INTEGRATION=1 才跑：每条会启动真实 Pi（30–90s）。
//
// 为什么必须真起 Pi：C04 要证明的不是"我们写出了 mcp.json"，而是
//   - Pi 真的发现了 fixture MCP 的工具（工具声明出现在发给模型的请求里）；
//   - Pi 真的调用了它（fixture 侧收到 tools/call，且模型收到结果）；
//   - Go 没有自己起第二个 MCP client（fixture 的父进程不是测试进程）；
//   - 禁用后下一次运行里工具真的不存在（模型声明里没有它，fixture 没被再启动）。
//
// fixture 的 -counter / -calls / -log 是判据来源：进程与副作用计数落在文件上，
// 不靠界面显示或 Go 侧日志推断。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/runtime/pi"
)

func requirePiIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("MYAGENT_PI_WORKER_INTEGRATION") != "1" {
		t.Skip("设置 MYAGENT_PI_WORKER_INTEGRATION=1 才跑真实 Pi 的 MCP 集成测试")
	}
}

// buildMCPFixture 构建 tests/fixtures/mcp 的可执行文件。
func buildMCPFixture(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fixture-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(repoRootForTest(t), "tests", "fixtures", "mcp")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fixture MCP 失败: %v\n%s", err, out)
	}
	return bin
}

// mcpProviderSim 是一个会调用 MCP 工具的可控模型端点。
// 第一次请求发 tool_call，收到 role:tool 之后回一句完成文本。
type mcpProviderSim struct {
	toolName string

	mu     sync.Mutex
	bodies []string
}

func (s *mcpProviderSim) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 8192)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	hasToolResult := strings.Contains(string(body), `"role":"tool"`)
	s.mu.Unlock()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	emit := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": "chatcmpl-mcp", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": "probe-model",
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	emit(chunk(map[string]any{"role": "assistant"}, nil))
	if !hasToolResult {
		emit(chunk(map[string]any{"tool_calls": []map[string]any{{
			"index": 0, "id": "call_mcp_1", "type": "function",
			"function": map[string]any{"name": s.toolName, "arguments": `{"text":"ping"}`},
		}}}, nil))
		emit(chunk(map[string]any{}, "tool_calls"))
	} else {
		emit(chunk(map[string]any{"content": "MCP_DONE"}, nil))
		emit(chunk(map[string]any{}, "stop"))
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *mcpProviderSim) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.bodies...)
}

// mcpRun 是一次真实 Pi 运行的可观测产物。
type mcpRun struct {
	Bodies []string
	Events []map[string]any
	// Argv 是实际启动 Pi 的命令行（证据用，不靠猜）。
	Argv []string
}

// startMCPRun 用给定的隔离参数起一次真实 Pi 会话，模型会尝试调用 toolName。
func startMCPRun(t *testing.T, toolName string, agentDir, work string, isoArgs, extraEnv []string) mcpRun {
	t.Helper()
	sim := &mcpProviderSim{toolName: toolName}
	srv := httptest.NewServer(sim)
	t.Cleanup(srv.Close)

	pm, err := CompileLegacy(ModelDefinition{
		ID: "m-mcp", ProviderID: "probe", ModelID: "probe-model",
		API: "openai-completions", BaseURL: srv.URL + "/v1",
		InputKinds: []string{"text"}, ContextWindow: 8192, MaxOutput: 1024,
	}, "")
	if err != nil {
		t.Fatalf("编译 models.json: %v", err)
	}
	if err := WritePiModels(agentDir, pm); err != nil {
		t.Fatalf("写 models.json: %v", err)
	}

	args := append([]string{"--mode", "rpc"}, isoArgs...)
	args = append(args, "--no-session", "--provider", "probe", "--model", "probe-model")
	inst := probeInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	w, err := pi.StartWorker(ctx, pi.WorkerSpec{
		Installation: inst,
		WorkingDir:   work,
		AgentDir:     agentDir,
		Env:          append(append([]string{}, pm.Env...), extraEnv...),
		Args:         args,
	})
	if err != nil {
		t.Fatalf("启动 Pi: %v", err)
	}
	defer func() {
		closeCtx, cc := context.WithTimeout(context.Background(), 25*time.Second)
		defer cc()
		_ = w.Close(closeCtx)
	}()

	if _, err := w.Call(ctx, json.RawMessage(`{"type":"set_auto_retry","enabled":false}`)); err != nil {
		t.Fatalf("关自动重试: %v", err)
	}
	msg, _ := json.Marshal(map[string]string{"type": "prompt", "message": "调用工具"})
	if _, err := w.Call(ctx, msg); err != nil {
		t.Fatalf("发 prompt: %v", err)
	}

	run := mcpRun{Argv: append(append([]string{inst.Executable}, inst.Args...), args...)}
	deadline := time.After(100 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("等不到 agent_settled；已收到事件 %d 条，请求 %d 份", len(run.Events), len(sim.requests()))
		case ev, ok := <-w.Events():
			if !ok {
				tail, _ := w.StderrTail()
				code, _ := w.ExitCode()
				t.Fatalf("Pi 提前退出（code=%d）: %s", code, clip(tail, 600))
			}
			var rec map[string]any
			if err := json.Unmarshal(ev.Record, &rec); err == nil {
				run.Events = append(run.Events, rec)
			}
			if ev.Type == "agent_settled" {
				run.Bodies = sim.requests()
				return run
			}
		}
	}
}

func toolExecutionEvents(run mcpRun) []map[string]any {
	out := []map[string]any{}
	for _, ev := range run.Events {
		if ev["type"] == "tool_execution_end" {
			out = append(out, ev)
		}
	}
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读 %s: %v", path, err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func countStarts(lines []string) (starts, exits int, parentPIDs []string) {
	for _, line := range lines {
		if strings.HasPrefix(line, "start ") {
			starts++
			for _, field := range strings.Fields(line) {
				if strings.HasPrefix(field, "ppid=") {
					parentPIDs = append(parentPIDs, strings.TrimPrefix(field, "ppid="))
				}
			}
		}
		if strings.HasPrefix(line, "exit ") {
			exits++
		}
	}
	return
}

// mcpTestService 造一个带隔离 agentDir 的资源服务与工作区。
func mcpTestService(t *testing.T, work string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	svc := New(store, credentials.NewMemStore(), WithLayout(NewLayout(root)))
	svc.SetWorkspaceRoot(work)
	return svc, filepath.Join(root, "agent")
}

func configureDiagnose(t *testing.T, svc *Service) {
	t.Helper()
	inst := probeInstallation(t)
	svc.SetMCPDiagnoseEnv(MCPDiagnoseEnv{
		Executable: inst.Executable,
		Args:       inst.Args,
		Timeout:    60 * time.Second,
	})
}

func isolationArgsForMCP(t *testing.T) []string {
	t.Helper()
	// -ne 会连带关掉内置 MCP，HarnessMCP=true 会显式 -e builtin:mcp。
	// 这里不加载策略扩展：本测试测的是 Pi 的 MCP 发现与调用，审批由 F04 覆盖。
	spec := pi.IsolationSpec{HarnessMCP: true}
	return spec.Args()
}

// TestRealPiStdioMCPDiscoveryAndCall 是 MCP01 的 stdio 一半。
func TestRealPiStdioMCPDiscoveryAndCall(t *testing.T) {
	requirePiIntegration(t)
	bin := buildMCPFixture(t)
	work := t.TempDir()
	agentDir := t.TempDir()
	svc, _ := mcpTestService(t, work)
	svc.SetMCPDiagnoseEnv(MCPDiagnoseEnv{})
	ctx := context.Background()

	counter := filepath.Join(t.TempDir(), "counter.txt")
	calls := filepath.Join(t.TempDir(), "calls.txt")
	trace := filepath.Join(t.TempDir(), "requests.jsonl")

	def := stdioDef("fixture", bin,
		"-counter", counter, "-calls", calls, "-log", trace)
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存 MCP: %v", err)
	}
	plan, err := svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if err := ApplyMCPPlan(agentDir, plan); err != nil {
		t.Fatalf("写 mcp.json: %v", err)
	}
	// 保存/编译/写盘都不能启动 MCP 进程：进程只应由 Pi 拥有。
	if lines := readLines(t, counter); len(lines) != 0 {
		t.Fatalf("配置阶段就启动了 MCP 进程（Go 不应自建 MCP client）: %v", lines)
	}

	run := startMCPRun(t, "mcp__fixture__fixture_echo", agentDir, work, isolationArgsForMCP(t), nil)
	t.Logf("pi argv: %s", strings.Join(run.Argv, " "))
	t.Logf("source recompiled mcp.json: %s", strings.Join(readLines(t, filepath.Join(agentDir, "mcp.json")), " "))
	t.Logf("first-request MCP tool declarations: %v", mcpDeclarations(run.Bodies[0]))

	if len(run.Bodies) < 2 {
		t.Fatalf("至少应有两次模型请求（工具调用前/后），实际 %d", len(run.Bodies))
	}
	if !strings.Contains(run.Bodies[0], "mcp__fixture__fixture_echo") {
		t.Fatalf("Pi 没有把 fixture 工具声明给模型；首份请求:\n%s", clip(run.Bodies[0], 1600))
	}
	toolSeen := false
	for _, body := range run.Bodies[1:] {
		if strings.Contains(body, `"role":"tool"`) && strings.Contains(body, "echo: ping") {
			toolSeen = true
		}
	}
	if !toolSeen {
		t.Fatalf("模型没有收到 MCP 工具结果（期望 echo: ping）；请求数 %d", len(run.Bodies))
	}

	// 服务端证据：真的收到了 tools/call。
	rawTrace, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读 fixture 请求日志: %v", err)
	}
	if !strings.Contains(string(rawTrace), "tools/call") || !strings.Contains(string(rawTrace), "fixture_echo") {
		t.Fatalf("fixture 没收到 tools/call:\n%s", clip(string(rawTrace), 1200))
	}
	if got := readLines(t, calls); len(got) != 1 || !strings.Contains(got[0], "call fixture_echo") {
		t.Fatalf("副作用计数应为 1 次 fixture_echo，实际 %v", got)
	} else {
		t.Logf("fixture calls: %v", got)
	}

	// 进程树证据：唯一的 fixture 进程由 Pi 启动，不是 Go 测试进程。
	lines := readLines(t, counter)
	t.Logf("fixture counter: %v", lines)
	starts, exits, ppids := countStarts(lines)
	if starts != 1 {
		t.Fatalf("fixture 应只被启动一次，实际 %d 次: %v", starts, lines)
	}
	self := fmt.Sprintf("%d", os.Getpid())
	for _, ppid := range ppids {
		if ppid == self {
			t.Fatalf("fixture 的父进程是 Go 测试进程（%s）——说明 Go 自己起了 MCP client", ppid)
		}
	}
	// worker 关闭后进程应被清理（MCP05）。
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, exits, _ = countStarts(readLines(t, counter))
		if exits >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if exits < 1 {
		t.Fatalf("Pi 关闭后 fixture 进程未记录退出（owned 进程未清理）: %v", readLines(t, counter))
	}
	// MCP01/MCP05 的外部观察：fixture 进程自己写下的调用/请求/进程记录。
	recordMCPFixtureObservation(t, "stdio-discovery-call", calls, trace, counter, map[string]any{
		"toolDeclarations":  mcpDeclarations(run.Bodies[0]),
		"modelRequestCount": len(run.Bodies),
		"piArgv":            run.Argv,
	})
}

// startHTTPFixture 启动 fixture 的 Streamable HTTP 模式，返回地址与进程句柄。
func startHTTPFixture(t *testing.T, bin string, args ...string) (addr string, calls string, stop func()) {
	t.Helper()
	addrFile := filepath.Join(t.TempDir(), "addr.txt")
	calls = filepath.Join(t.TempDir(), "calls.txt")
	base := []string{"-http", "127.0.0.1:0", "-addr-file", addrFile, "-calls", calls}
	cmd := exec.Command(bin, append(base, args...)...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 HTTP fixture: %v", err)
	}
	stopOnce := sync.Once{}
	stop = func() {
		stopOnce.Do(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(addrFile); err == nil && strings.TrimSpace(string(data)) != "" {
			return strings.TrimSpace(string(data)), calls, stop
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP fixture 未写出监听地址")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRealPiHTTPMCPDiscoveryAndCall 是 MCP01 的 HTTP 一半（Streamable HTTP + header 引用）。
func TestRealPiHTTPMCPDiscoveryAndCall(t *testing.T) {
	requirePiIntegration(t)
	bin := buildMCPFixture(t)
	work := t.TempDir()
	agentDir := t.TempDir()
	svc, _ := mcpTestService(t, work)
	ctx := context.Background()

	const token = "fixture-token-9f2c"
	addr, calls, _ := startHTTPFixture(t, bin, "-auth", token)
	if err := svc.SaveMCPCredential(ctx, "mcp-fixture-token", "Bearer "+token); err != nil {
		t.Fatalf("保存凭证: %v", err)
	}
	def := MCPDefinition{
		ID: "web", Transport: MCPTransportHTTP, URL: "http://" + addr + "/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-fixture-token"},
		Enabled:    true, Exposure: MCPExposureDirect,
	}
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存 http MCP: %v", err)
	}
	plan, err := svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if err := ApplyMCPPlan(agentDir, plan); err != nil {
		t.Fatalf("写 mcp.json: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(agentDir, "mcp.json"))
	if strings.Contains(string(raw), "Bearer "+token) {
		t.Fatalf("mcp.json 出现明文令牌:\n%s", raw)
	}

	run := startMCPRun(t, "mcp__web__fixture_echo", agentDir, work, isolationArgsForMCP(t), plan.Env)
	t.Logf("pi argv: %s", strings.Join(run.Argv, " "))
	t.Logf("mcp.json has env reference only: %s", redactFixtureSecret(string(raw), "Bearer "+token))
	t.Logf("first-request MCP tool declarations: %v", mcpDeclarations(run.Bodies[0]))
	if !strings.Contains(run.Bodies[0], "mcp__web__fixture_echo") {
		t.Fatalf("Pi 没有发现 HTTP MCP 工具；首份请求:\n%s", clip(run.Bodies[0], 1600))
	}
	toolSeen := false
	for _, body := range run.Bodies[1:] {
		if strings.Contains(body, `"role":"tool"`) && strings.Contains(body, "echo: ping") {
			toolSeen = true
		}
	}
	if !toolSeen {
		t.Fatalf("HTTP MCP 工具结果没回到模型（期望 echo: ping）")
	}
	if got := readLines(t, calls); len(got) != 1 || !strings.Contains(got[0], "call fixture_echo") {
		t.Fatalf("HTTP 副作用计数应为 1，实际 %v", got)
	} else {
		t.Logf("fixture calls: %v", got)
	}
	// HTTP 装置的观察值：fixture HTTP 服务端自己写下的调用行（没有 stdio 的进程记录）。
	recordMCPFixtureObservation(t, "http-discovery-call", calls, "", "", map[string]any{
		"toolDeclarations":  mcpDeclarations(run.Bodies[0]),
		"modelRequestCount": len(run.Bodies),
		"piArgv":            run.Argv,
	})
}

// TestDisabledMCPToolCannotExecute 是 MCP03 / RES04 的核心：
// 禁用后下一次运行里工具真的不存在，fixture 不再被启动。
func TestDisabledMCPToolCannotExecute(t *testing.T) {
	requirePiIntegration(t)
	bin := buildMCPFixture(t)
	work := t.TempDir()
	svc, agentDir := mcpTestService(t, work)
	ctx := context.Background()

	counter := filepath.Join(t.TempDir(), "counter.txt")
	calls := filepath.Join(t.TempDir(), "calls.txt")
	def := stdioDef("fixture", bin, "-counter", counter, "-calls", calls)
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	plan, err := svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if err := ApplyMCPPlan(agentDir, plan); err != nil {
		t.Fatalf("写 mcp.json: %v", err)
	}

	// 第一轮：启用 → 真的能调用。
	first := startMCPRun(t, "mcp__fixture__fixture_echo", agentDir, work, isolationArgsForMCP(t), nil)
	if !strings.Contains(first.Bodies[0], "mcp__fixture__fixture_echo") {
		t.Fatalf("启用时工具应被声明")
	}
	if got := readLines(t, calls); len(got) != 1 {
		t.Fatalf("启用时应有 1 次调用，实际 %v", got)
	} else {
		t.Logf("first run calls: %v", got)
	}

	// 禁用 → 重新编译 → mcp.json 里不再有该服务。
	set, err := svc.ListMCP(ctx)
	if err != nil {
		t.Fatalf("列配置: %v", err)
	}
	if _, err := svc.SetMCPEnabled(ctx, "fixture", set.Revision, false); err != nil {
		t.Fatalf("禁用: %v", err)
	}
	plan, err = svc.CompileMCP(ctx, "ws-default")
	if err != nil {
		t.Fatalf("重新编译: %v", err)
	}
	if len(plan.Servers) != 0 {
		t.Fatalf("禁用后不应有服务参与运行: %+v", plan.Servers)
	}
	if err := ApplyMCPPlan(agentDir, plan); err != nil {
		t.Fatalf("重新写 mcp.json: %v", err)
	}
	servers := readMCPFile(t, agentDir)["mcpServers"].(map[string]any)
	if _, ok := servers["fixture"]; ok {
		t.Fatalf("禁用后 mcp.json 仍有该服务: %v", servers)
	}

	// 第二轮：模型仍然尝试调用同名工具，但 Pi 没有这个工具，fixture 也不应被启动。
	before := readLines(t, counter)
	second := startMCPRun(t, "mcp__fixture__fixture_echo", agentDir, work, isolationArgsForMCP(t), nil)
	t.Logf("after disabling, MCP tool declarations: %v", mcpDeclarations(second.Bodies[0]))
	t.Logf("after disabling, mcp.json servers: %v", mcpServerNamesOf(readMCPFile(t, agentDir)))
	if strings.Contains(second.Bodies[0], "mcp__fixture__fixture_echo") {
		t.Fatalf("禁用后 Pi 仍声明了该工具:\n%s", clip(second.Bodies[0], 1200))
	}
	events := toolExecutionEvents(second)
	failed := false
	for _, ev := range events {
		name, _ := ev["toolName"].(string)
		isErr, _ := ev["isError"].(bool)
		if name == "mcp__fixture__fixture_echo" && isErr {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("禁用后对同名工具的调用应失败（isError=true），实际事件: %v", events)
	}
	if got := readLines(t, calls); len(got) != 1 {
		t.Fatalf("禁用后仍产生了 MCP 副作用: %v", got)
	} else {
		t.Logf("after disabling, fixture calls (unchanged): %v", got)
	}
	after := readLines(t, counter)
	t.Logf("counter before second run: %v", before)
	t.Logf("counter after second run: %v", after)
	if len(after) != len(before) {
		t.Fatalf("禁用后仍启动了 MCP 进程:\n前: %v\n后: %v", before, after)
	}
	// MCP03 的外部观察：禁用前后的 fixture 调用行与进程启动次数（应保持不变）。
	recordMCPFixtureObservation(t, "stdio-after-disable", calls, "", counter, map[string]any{
		"enabledRunDeclarations":  mcpDeclarations(first.Bodies[0]),
		"disabledRunDeclarations": mcpDeclarations(second.Bodies[0]),
		"disabledCallErrored":     failed,
		"disabledServerNames":     mcpServerNamesOf(readMCPFile(t, agentDir)),
	})
}

// TestRealPiDiagnoseStdioAndToolListChange 覆盖诊断与工具列表变化。
func TestRealPiDiagnoseStdioAndToolListChange(t *testing.T) {
	requirePiIntegration(t)
	bin := buildMCPFixture(t)
	work := t.TempDir()
	svc, _ := mcpTestService(t, work)
	configureDiagnose(t, svc)
	ctx := context.Background()

	def := stdioDef("fixture", bin, "-tools", "fixture_echo")
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}
	st, err := svc.DiagnoseMCP(ctx, "fixture")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State != MCPStateConnected {
		t.Fatalf("fixture 应 connected，实际 %s（%s）", st.State, st.Detail)
	}
	if !hasTool(st.Tools, "fixture_echo") {
		t.Fatalf("诊断工具清单缺少 fixture_echo: %+v", st.Tools)
	}
	if hasTool(st.Tools, "fixture_extra") {
		t.Fatalf("未开启的工具不应出现: %+v", st.Tools)
	}
	t.Logf("diagnose(before tool change): state=%s tools=%v", st.State, toolNames(st.Tools))
	recordMCPDiagnoseObservation(t, "before-tool-change", st)

	// 切换工具集合：同一个 ID，只改 args（模拟服务端工具列表变化）。
	set, err := svc.ListMCP(ctx)
	if err != nil {
		t.Fatalf("列配置: %v", err)
	}
	def.Args = []string{"-tools", "fixture_echo,fixture_extra"}
	if _, err := svc.SaveMCP(ctx, set.Revision, def); err != nil {
		t.Fatalf("更新: %v", err)
	}
	st, err = svc.DiagnoseMCP(ctx, "fixture")
	if err != nil {
		t.Fatalf("再诊断: %v", err)
	}
	if !hasTool(st.Tools, "fixture_extra") {
		t.Fatalf("工具列表变化没有反映到诊断: %+v", st.Tools)
	}
	t.Logf("diagnose(after tool change): state=%s tools=%v", st.State, toolNames(st.Tools))
	recordMCPDiagnoseObservation(t, "after-tool-change", st)

	tools, err := svc.ListTools(ctx, "sess-mcp")
	if err != nil {
		t.Fatalf("工具目录: %v", err)
	}
	if _, ok := toolByName(tools, "mcp__fixture__fixture_extra"); !ok {
		t.Fatalf("工具目录没有跟随服务端变化: %+v", tools)
	}
}

// TestRealPiDiagnoseHTTPAuthFailureAndReconnect 覆盖鉴权失败、不可达与重连。
func TestRealPiDiagnoseHTTPAuthFailureAndReconnect(t *testing.T) {
	requirePiIntegration(t)
	bin := buildMCPFixture(t)
	work := t.TempDir()
	svc, _ := mcpTestService(t, work)
	configureDiagnose(t, svc)
	ctx := context.Background()

	const token = "fixture-token-auth-1"
	addr, calls, stop := startHTTPFixture(t, bin, "-auth", token)
	if err := svc.SaveMCPCredential(ctx, "mcp-wrong-token", "Bearer wrong-token"); err != nil {
		t.Fatalf("保存错误凭证: %v", err)
	}
	def := MCPDefinition{
		ID: "web", Transport: MCPTransportHTTP, URL: "http://" + addr + "/mcp",
		HeaderRefs: map[string]string{"Authorization": "mcp-wrong-token"},
		Enabled:    true, Exposure: MCPExposureDirect,
	}
	if _, err := svc.SaveMCP(ctx, 0, def); err != nil {
		t.Fatalf("保存: %v", err)
	}

	// 1) 错误凭证：状态必须是 auth_required，且没有调用副作用。
	st, err := svc.DiagnoseMCP(ctx, "web")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State != MCPStateAuthRequired {
		t.Fatalf("错误凭证应判定 auth_required，实际 %s（%s）", st.State, st.Detail)
	}
	if st.OAuthSupported {
		t.Fatal("V1 不支持 MCP OAuth，不能声称支持")
	}
	t.Logf("diagnose(wrong token): state=%s error=%s", st.State, st.Error)
	recordMCPDiagnoseObservation(t, "wrong-token", st)
	if got := readLines(t, calls); len(got) != 0 {
		t.Fatalf("鉴权失败的调用产生了副作用: %v", got)
	} else {
		t.Logf("calls with wrong token: %v (file absent or empty)", got)
	}

	// 2) 换正确凭证：connected。
	if err := svc.SaveMCPCredential(ctx, "mcp-right-token", "Bearer "+token); err != nil {
		t.Fatalf("保存正确凭证: %v", err)
	}
	set, err := svc.ListMCP(ctx)
	if err != nil {
		t.Fatalf("列配置: %v", err)
	}
	def.HeaderRefs = map[string]string{"Authorization": "mcp-right-token"}
	if _, err := svc.SaveMCP(ctx, set.Revision, def); err != nil {
		t.Fatalf("更新: %v", err)
	}
	st, err = svc.DiagnoseMCP(ctx, "web")
	if err != nil {
		t.Fatalf("再诊断: %v", err)
	}
	if st.State != MCPStateConnected {
		t.Fatalf("正确凭证应 connected，实际 %s（%s）", st.State, st.Detail)
	}
	t.Logf("diagnose(correct token): state=%s tools=%v", st.State, toolNames(st.Tools))
	recordMCPDiagnoseObservation(t, "correct-token", st)

	// 3) 服务断开：状态必须是 failed（不能保留 connected）。
	stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err = svc.DiagnoseMCP(ctx, "web")
		if err != nil {
			t.Fatalf("断开后诊断: %v", err)
		}
		if st.State != MCPStateConnected || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st.State == MCPStateConnected {
		t.Fatal("服务已断开却仍显示 connected")
	}
	t.Logf("diagnose(after stop): state=%s detail=%s", st.State, st.Detail)
	recordMCPDiagnoseObservation(t, "after-stop", st)

	// 4) 原地址重启：状态恢复 connected（Pi 重新连接，不需要改配置）。
	cmd := exec.Command(bin, "-http", addr, "-auth", token)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("重启 fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline = time.Now().Add(15 * time.Second)
	for {
		st, err = svc.DiagnoseMCP(ctx, "web")
		if err != nil {
			t.Fatalf("重连后诊断: %v", err)
		}
		if st.State == MCPStateConnected || time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if st.State != MCPStateConnected {
		t.Fatalf("服务恢复后应重新 connected，实际 %s（%s）", st.State, st.Detail)
	}
	t.Logf("diagnose(after restart): state=%s tools=%v", st.State, toolNames(st.Tools))
	recordMCPDiagnoseObservation(t, "after-restart", st)
	// 鉴权失败期间不应有副作用：HTTP fixture 自己的调用行是空的。
	recordMCPFixtureObservation(t, "http-diagnose", calls, "", "", nil)
}

// TestRealPiDiagnoseUnreachable 覆盖"服务不可达时诊断状态准确、不显示 connected"。
func TestRealPiDiagnoseUnreachable(t *testing.T) {
	requirePiIntegration(t)
	work := t.TempDir()
	svc, _ := mcpTestService(t, work)
	configureDiagnose(t, svc)
	ctx := context.Background()

	missing := filepath.Join(t.TempDir(), "does-not-exist.exe")
	if _, err := svc.SaveMCP(ctx, 0, stdioDef("broken", missing)); err != nil {
		t.Fatalf("保存: %v", err)
	}
	st, err := svc.DiagnoseMCP(ctx, "broken")
	if err != nil {
		t.Fatalf("诊断: %v", err)
	}
	if st.State == MCPStateConnected {
		t.Fatal("不可达的服务显示成了 connected")
	}
	if st.State != MCPStateFailed {
		t.Fatalf("应判定 failed，实际 %s（%s）", st.State, st.Detail)
	}
	if st.Error == nil || st.Error.Code != contracts.CodeProviderUnreachable {
		t.Fatalf("应报 PROVIDER_UNREACHABLE: %+v", st.Error)
	}
	t.Logf("diagnose(unreachable command): state=%s code=%s detail=%s", st.State, st.Error.Code, st.Detail)
	recordMCPDiagnoseObservation(t, "unreachable-command", st)
}

func mcpDeclarations(body string) []string {
	out := []string{}
	for _, m := range regexp.MustCompile(`"name":"(mcp__[^"]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func toolNames(tools []MCPToolStatus) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func mcpServerNamesOf(root map[string]any) []string {
	out := []string{}
	servers, _ := root["mcpServers"].(map[string]any)
	for name := range servers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func redactFixtureSecret(s, secret string) string {
	return strings.ReplaceAll(s, secret, "<redacted>")
}

func hasTool(tools []MCPToolStatus, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
