package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/fixtures/openaisse"
)

// captureNodePath 返回用于启动 Pi 的 node 解释器。
// 必须优先使用基线 node：PATH 首个 node 可能是别的版本（微信开发者工具/托管运行时），
// 会让捕获结果不可复现。
func captureNodePath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		`D:\Program Files\nodejs\node.exe`,
	}
	if p, err := exec.LookPath("node"); err == nil {
		candidates = append(candidates, p)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Fatalf("找不到 node 解释器，无法启动真实 Pi")
	return ""
}

// repoRoot 从测试所在目录回溯到仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	// internal/runtime/pi -> 仓库根
	root := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("定位仓库根失败（%s 下没有 go.mod）: %v", root, err)
	}
	return root
}

// piCLIPath 返回隔离安装的 Pi 1.5.0 CLI 入口。
func piCLIPath(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, ".tools", "pi", "v1.5.0", "node_modules",
		"@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("未找到隔离安装的 Pi 1.5.0（%s）。先执行：\n"+
			`npm --prefix .tools/pi/v1.5.0 install @earendil-works/pi-coding-agent@1.5.0`, p)
	}
	return p
}

// writeFixtureModels 在隔离 agentDir 里写 models.json，把 Pi 指向本地 fixture 端点。
// 绝不写入用户真实 ~/.pi/agent。
func writeFixtureModels(t *testing.T, agentDir, baseURL string) {
	t.Helper()
	doc := map[string]any{
		"$schema": "https://pi.dev/schemas/models.schema.json",
		"providers": map[string]any{
			"fixture": map[string]any{
				"baseUrl": baseURL + "/v1",
				"api":     "openai-completions",
				"apiKey":  "fixture-key",
				"models": []map[string]any{
					{
						"id":            "fixture-model",
						"name":          "Fixture",
						"input":         []string{"text"},
						"contextWindow": 8000,
						"maxTokens":     2048,
						"reasoning":     false,
						"cost": map[string]int{
							"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0,
						},
					},
				},
			},
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("序列化 models.json 失败: %v", err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("创建 agentDir 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), b, 0o644); err != nil {
		t.Fatalf("写 models.json 失败: %v", err)
	}
}

// closeGrace 是 agent_settled 之后、关闭 stdin 之前的等待时间。
// 可用 MYAGENT_PI_CLOSE_GRACE 覆盖，用于验证关闭时序对退出稳定性的影响。
var closeGrace = func() time.Duration {
	if v := os.Getenv("MYAGENT_PI_CLOSE_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 800 * time.Millisecond
}()

// record 是一条原始 RPC 记录（未解析成具体类型，保持字节原样）。
type record struct {
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	ID      string          `json:"id"`
	Raw     json.RawMessage `json:"-"`
	Line    string          `json:"-"`
}

// runPiRPC 启动真实 Pi 的 RPC 模式，按批次下发命令，收集所有 stdout 记录。
//
// batches 的语义：第 0 批在启动窗口后立即发送，之后每观察到一次 agent_settled
// 就发送下一批。这样需要"上一轮结束后才允许下发"的命令（如 compact）不会与之竞争。
func runPiRPC(t *testing.T, nodePath, cliPath, workDir, agentDir string, batches [][]string, settleCount int, allowExtensions bool, extraArgs []string, linger, timeout time.Duration) ([]record, string, int) {
	t.Helper()

	args := []string{
		cliPath,
		"--mode", "rpc",
		// 用隔离的临时 session-dir 而不是 --no-session：
		// --no-session 下没有可压缩的持久化条目，compact 一律返回
		// "Nothing to compact (session too small)"，无法作为压缩证据。
		"--session-dir", filepath.Join(workDir, "sessions"),
		"--provider", "fixture",
		"--model", "fixture-model",
	}
	// 默认禁用扩展发现以保证可复现。
	// 注意：实测 --no-extensions 会连带不加载 MCP server（文档只提到扩展与内置扩展），
	// 因此需要验证 MCP 的场景必须放开这个开关。
	if !allowExtensions {
		args = append(args, "--no-extensions")
	}
	args = append(args, extraArgs...)
	cmd := exec.Command(nodePath, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PI_CODING_AGENT_DIR="+agentDir,
		"PI_OFFLINE=1",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("取 stdin 失败: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("取 stdout 失败: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 Pi 失败: %v", err)
	}

	// 逐行读取，仅在 LF 处切分；不把 Unicode 行分隔符当记录边界（见 rpc.md 分帧要求）。
	// 记录逐条推入 channel，供主循环实时判定 agent_settled。
	recCh := make(chan record, 8192)
	eofCh := make(chan struct{}, 1)
	go func() {
		r := bufio.NewReaderSize(stdout, 1<<20)
		for {
			line, err := r.ReadString('\n')
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line != "" {
				var rec record
				if json.Unmarshal([]byte(line), &rec) == nil {
					rec.Line = line
					rec.Raw = json.RawMessage(line)
					recCh <- rec
				}
			}
			if err != nil {
				eofCh <- struct{}{}
				return
			}
		}
	}()

	// 命令分批下发：第一批在启动窗口后发出，之后每观察到一次 agent_settled
	// 就发下一批。所有写入都在主循环里完成，避免与读取 goroutine 竞争 stdin。
	// 启动窗口需要足够长：MCP server 连接是异步的，过短会让首个请求看不到 MCP 工具。
	startDelay := 4 * time.Second
	if v := os.Getenv("MYAGENT_PI_START_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			startDelay = d
		}
	}
	startTimer := time.After(startDelay)
	nextBatch := 0
	flushBatch := func() {
		if nextBatch >= len(batches) {
			return
		}
		for _, c := range batches[nextBatch] {
			_, _ = stdin.Write([]byte(c + "\n"))
		}
		nextBatch++
	}

	var recs []record
	deadline := time.After(timeout)
	settled := 0
	var lingerCh <-chan time.Time

loop:
	for {
		select {
		case <-startTimer:
			flushBatch()
		case rec := <-recCh:
			recs = append(recs, rec)
			if rec.Type == "agent_settled" {
				settled++
				// 先发下一批，再判断是否收尾：compact 这类命令正需要在上轮结束后下发。
				flushBatch()
				if settled >= settleCount && lingerCh == nil {
					if linger <= 0 {
						break loop
					}
					// 压缩等操作不产生新的 agent_settled，用固定窗口继续收记录。
					lingerCh = time.After(linger)
				}
			}
		case <-lingerCh:
			break loop
		case <-eofCh:
			break loop
		case <-deadline:
			t.Logf("等待超时（%s），已收到 %d 条记录、%d 次 settled", timeout, len(recs), settled)
			break loop
		}
	}

	// 收尾宽限期：agent_settled 只表示 agent 层不再自动继续，
	// 不代表 stdout 写入已完全落盘。实测大输出/工具场景若立刻关 stdin，
	// Pi 会在退出阶段触发 libuv 断言崩溃（0xC0000409），因此先留出余量。
	time.Sleep(closeGrace)
	_ = stdin.Close()
	waitCh := make(chan struct{}, 1)
	go func() {
		_ = cmd.Wait()
		waitCh <- struct{}{}
	}()
	select {
	case <-waitCh:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waitCh
	}

	// 排干残留记录，避免丢尾部。
	for {
		select {
		case rec := <-recCh:
			recs = append(recs, rec)
		default:
			goto drained
		}
	}
drained:

	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if settled < settleCount {
		t.Logf("settled 次数不足：期望 %d，实际 %d", settleCount, settled)
	}
	return recs, stderr.String(), code
}

// writeFixtureMCP 在隔离 agentDir 里注册 fixture MCP server。
// Pi 从 $PI_CODING_AGENT_DIR/mcp.json 读取 user-level MCP 配置。
func writeFixtureMCP(t *testing.T, root, agentDir string) {
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
				"args":        []string{},
				"description": "受控 fixture MCP server（F02）",
				// 默认 exposure 是 codemode：工具既不声明给模型，也不写进 codemode 描述，
				// 只能被脚本 searchTools() 找到。要验证"工具是否暴露给模型"必须用 direct。
				"exposure": "direct",
			},
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("序列化 mcp.json 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "mcp.json"), b, 0o644); err != nil {
		t.Fatalf("写 mcp.json 失败: %v", err)
	}
}

// promptCmd 构造一条 prompt 命令，场景标记放在消息里由 fixture 解析。
func promptCmd(id, scenario string) string {
	msg := "请回答：[[scenario:" + scenario + "]]"
	b, _ := json.Marshal(map[string]string{"id": id, "type": "prompt", "message": msg})
	return string(b)
}

// normalizeLine 把本机绝对路径替换成占位符，使 testdata 可作为可移植基准。
//
// 这只做脱敏，不改变协议语义。原始未脱敏记录另存 docs/evidence/raw/。
// 同时校验：临时工作目录若出现在记录里，说明捕获把本机环境泄漏进了 fixture。
func normalizeLine(line, root string) string {
	escaped := strings.ReplaceAll(root, `\`, `\\`) // JSON 里的转义形态
	line = strings.ReplaceAll(line, escaped, "<REPO_ROOT>")
	line = strings.ReplaceAll(line, root, "<REPO_ROOT>")
	return line
}

// captureCase 描述一个捕获场景。
type captureCase struct {
	name string
	// batches 分批下发：第 0 批在启动窗口后发送，之后每次 agent_settled 发下一批。
	batches [][]string
	// settles 期望观察到的 agent_settled 次数，达到即认为该场景可以收尾。
	settles int
	// linger>0 时，达标后不立刻退出，而是继续收记录一段时间
	// （用于不产生 agent_settled 的后续命令，如 compact）。
	linger  time.Duration
	timeout time.Duration
	// requireSettle=false 用于预期失败、可能不进入 idle 的场景（如 provider 报错）。
	requireSettle bool
	// wantContains 要求至少一条记录包含指定子串，用于确认该场景真的诱发了目标行为。
	wantContains string
	// withMCP=true 时在隔离 agentDir 注册 fixture MCP server 后再启动 Pi。
	withMCP bool
	// wantToolInRequest 要求 Pi 发给 provider 的 tools 数组里出现该工具名。
	// MCP 工具不会出现在 stdout 事件里，只能从请求 side 核对。
	wantToolInRequest string
	// extraArgs 追加到 Pi 命令行，例如 -e <extension>。
	extraArgs []string
	// acceptFailure 允许 prompt 响应为 success=false（用于错误场景）。
	acceptFailure bool
}

// TestCaptureRealPiProtocol 用真实 Pi 1.5.0 + 受控 fixture provider 捕获真实协议记录。
// 需要显式开启：MYAGENT_PI_CAPTURE=1。默认跳过，避免常规测试依赖本机安装。
//
// 捕获到的记录写入 internal/runtime/pi/testdata/v1.5.0/<scene>.jsonl。
// 这些是真实进程产出的原始记录，仅做了路径脱敏；provider 是受控 fixture，
// 因此不代表任何真实云模型的行为（见 docs/evidence/pi-compatibility.md）。
func TestCaptureRealPiProtocol(t *testing.T) {
	if os.Getenv("MYAGENT_PI_CAPTURE") != "1" {
		t.Skip("设置 MYAGENT_PI_CAPTURE=1 才执行真实 Pi 协议捕获")
	}
	if runtime.GOOS != "windows" {
		t.Skip("当前捕获脚本按 Windows 路径编写")
	}

	root := repoRoot(t)
	nodePath := captureNodePath(t)
	cliPath := piCLIPath(t, root)

	outDir := filepath.Join(root, "internal", "runtime", "pi", "testdata", "v1.5.0")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("创建 testdata 目录失败: %v", err)
	}

	stateCmd := `{"id":"req-state","type":"get_state"}`
	compactCmd := `{"id":"req-compact","type":"compact"}`

	cases := []captureCase{
		{
			name:          "text",
			batches:       [][]string{{stateCmd, promptCmd("req-prompt", "text")}},
			settles:       1,
			timeout:       40 * time.Second,
			requireSettle: true,
			wantContains:  `"text_delta"`,
		},
		{
			name:          "unicode",
			batches:       [][]string{{promptCmd("req-prompt", "unicode")}},
			settles:       1,
			timeout:       40 * time.Second,
			requireSettle: true,
			wantContains:  string(rune(0x2028)),
		},
		{
			name:          "tool",
			batches:       [][]string{{promptCmd("req-prompt", "tool")}},
			settles:       1,
			timeout:       60 * time.Second,
			requireSettle: true,
		},
		{
			name:          "empty",
			batches:       [][]string{{promptCmd("req-prompt", "empty")}},
			settles:       1,
			timeout:       40 * time.Second,
			requireSettle: true,
		},
		{
			// provider 返回 500：观察 Pi 的重试与错误上报路径。
			// 该场景可能不进入 idle，因此不强制要求 settled。
			name:          "provider-error",
			batches:       [][]string{{promptCmd("req-prompt", "error500")}},
			settles:       1,
			timeout:       75 * time.Second,
			requireSettle: false,
			acceptFailure: true,
		},
		{
			// 单轮超长输出。实测 fixture 上报的 usage 很小，Pi 不会自动压缩，
			// 因此该场景只用于记录"超长响应"本身，不作为 compaction 证据。
			name:          "long",
			batches:       [][]string{{promptCmd("req-prompt", "long")}},
			settles:       1,
			timeout:       90 * time.Second,
			requireSettle: true,
		},
		{
			// 验证 Pi 能加载本项目 fixture MCP，且工具真的进入模型可见的工具数组。
			name:              "mcp",
			batches:           [][]string{{promptCmd("req-prompt", "text")}},
			settles:           1,
			timeout:           60 * time.Second,
			requireSettle:     true,
			withMCP:           true,
			wantToolInRequest: "mcp__fixture__fixture_echo",
		},
		{
			// 验证真实 Pi 能加载本项目薄 extension：用 -e 显式加载，
			// 再通过 get_commands 确认扩展注册的命令确实出现。
			name:      "extension",
			batches:   [][]string{{`{"id":"req-cmds","type":"get_commands"}`, promptCmd("req-prompt", "text")}},
			settles:   1,
			timeout:   60 * time.Second,
			extraArgs: []string{"-e", filepath.Join(root, "tests", "fixtures", "extension", "fixture.ts")},
			// get_commands 的响应里必须出现扩展注册的命令。
			wantContains: "fixture-ext",
		},
		{
			// 上下文压缩：连续多轮上报大 usage，让 Pi 的 auto compaction 有机会触发；
			// 最后再显式发 compact 作为补充。压缩不产生新的 agent_settled，
			// 因此达标后进入固定 linger 窗口继续收记录。
			name: "compaction",
			batches: [][]string{
				{promptCmd("req-prompt-1", "bulk")},
				{promptCmd("req-prompt-2", "bulk")},
				{promptCmd("req-prompt-3", "bulk")},
				{compactCmd},
			},
			settles:       3,
			linger:        15 * time.Second,
			timeout:       180 * time.Second,
			requireSettle: true,
			wantContains:  "compaction",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			agentDir := filepath.Join(work, "agent")
			// 每个场景独立 server + 独立记录目录：请求落盘后可核对 Pi 真正发出了什么。
			recDir := filepath.Join(work, "requests")
			srv := httptest.NewServer(openaisse.NewHandler(recDir))
			defer srv.Close()
			writeFixtureModels(t, agentDir, srv.URL)
			if tc.withMCP {
				writeFixtureMCP(t, root, agentDir)
			}

			recs, stderr, code := runPiRPC(t, nodePath, cliPath, work, agentDir,
				tc.batches, tc.settles, tc.withMCP, tc.extraArgs, tc.linger, tc.timeout)

			t.Logf("场景 %s：%d 条记录，退出码 %d", tc.name, len(recs), code)
			if stderr != "" {
				t.Logf("stderr（前 600 字节）：\n%s", truncate(stderr, 600))
			}

			var buf strings.Builder
			for _, rec := range recs {
				buf.WriteString(normalizeLine(rec.Line, root))
				buf.WriteString("\n")
			}
			content := buf.String()
			if err := os.WriteFile(filepath.Join(outDir, tc.name+".jsonl"), []byte(content), 0o644); err != nil {
				t.Fatalf("写捕获记录失败: %v", err)
			}

			// 最低要求：必须真的发生了进程间往返，而不是空跑。
			var sawDispatch bool
			for _, rec := range recs {
				if rec.Type != "response" {
					continue
				}
				if rec.Success != nil && *rec.Success {
					sawDispatch = true
				} else if tc.acceptFailure && rec.Success != nil {
					sawDispatch = true
				}
			}
			if !sawDispatch {
				t.Errorf("没有收到任何 response，链路未打通")
			}
			if tc.requireSettle {
				var n int
				for _, rec := range recs {
					if rec.Type == "agent_settled" {
						n++
					}
				}
				if n < tc.settles {
					t.Errorf("期望 %d 次 agent_settled，实际 %d", tc.settles, n)
				}
			}
			if tc.wantContains != "" && !strings.Contains(content, tc.wantContains) {
				t.Errorf("记录中缺少预期内容 %q，该场景可能未被真实诱发", tc.wantContains)
			}
			if tc.wantToolInRequest != "" && !requestHasTool(recDir, tc.wantToolInRequest) {
				t.Errorf("provider 请求的 tools 数组里没有 %q；实际工具：%v\n请求摘要：\n%s", tc.wantToolInRequest, toolNames(recDir), summarizeRequests(recDir))
				if stderr != "" {
					t.Logf("stderr: %s", truncate(stderr, 1500))
				}
			}
		})
	}
}

// toolNames 收集所有请求里声明的工具名，用于诊断。
func toolNames(recDir string) []string {
	seen := map[string]bool{}
	var out []string
	entries, err := os.ReadDir(recDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(recDir, e.Name()))
		if err != nil {
			continue
		}
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.Unmarshal(b, &req) != nil {
			continue
		}
		for _, t := range req.Tools {
			if !seen[t.Function.Name] {
				seen[t.Function.Name] = true
				out = append(out, t.Function.Name)
			}
		}
	}
	return out
}

// requestHasTool 检查 fixture provider 收到的任一请求里，tools 数组是否声明了指定工具。
// MCP 工具不会出现在 stdout 事件里，只能从请求侧核对它是否真的暴露给了模型。
func requestHasTool(recDir, toolName string) bool {
	entries, err := os.ReadDir(recDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(recDir, e.Name()))
		if err != nil {
			continue
		}
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.Unmarshal(b, &req) != nil {
			continue
		}
		for _, t := range req.Tools {
			if t.Function.Name == toolName {
				return true
			}
		}
	}
	return false
}

// summarizeRequests 列出每个落盘请求的顶层字段，用于诊断 Pi 实际发送的 payload 形状。
func summarizeRequests(recDir string) string {
	entries, err := os.ReadDir(recDir)
	if err != nil {
		return "(无请求记录: " + err.Error() + ")"
	}
	var sb strings.Builder
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(recDir, e.Name()))
		if err != nil {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&sb, "%s bytes=%d keys=%v\n", e.Name(), len(b), keys)
	}
	if sb.Len() == 0 {
		return "(请求目录为空)"
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(共 %d 字节)", len(s))
}
