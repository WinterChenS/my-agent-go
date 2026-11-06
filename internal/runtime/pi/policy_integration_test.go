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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/fixtures/openaisse"
	"my-agent-go/internal/harnessmcp"
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
	// label 标识这一次运行（用于 tree.json 里的键），由场景与结论派生。
	label string
	// wire 是本次运行期间记录代理写下的请求日志切片（从磁盘读回）。
	wire []bridgeWireRecord
	// tree 是本次运行结束时对 work 目录的磁盘遍历结果。
	tree []treeEntry
	// asked 是按工具名统计的审批请求数。
	// 它回答一个单看总数回答不了的问题：嵌套调用（codemode → bash）究竟有没有
	// 单独触发一次策略询问。只有总数而不分工具，"嵌套被拦"和"外层被拦"分不开。
	asked map[string]int
}

// runScenario 描述一次真实 Pi 运行。
type runScenario struct {
	// scenario 是 fixture provider 的回放脚本名。
	scenario string
	// choice 是用例替用户给出的审批结论。
	choice policy.ApprovalChoice
	// harnessMCP 为 true 时用 -e builtin:mcp 重新启用 MCP（-ne 会连带关掉它）。
	harnessMCP bool
	// extraExtensions 是额外显式 -e 加载的扩展（绝对路径）。
	// 用于验证"托管扩展注册的工具同样要过策略"，与项目级扩展（永远不加载）不是一回事。
	extraExtensions []string
	// extraArgs 是追加给 Pi 的额外参数（如 --tools +codemode）。
	extraArgs []string
	// choices 按工具名指定审批结论，未列出的工具用 choice。
	//
	// 为什么需要按工具区分：codemode 的外层调用与嵌套调用是**两个**独立的审批请求。
	// 如果两层用同一个结论，"外层被拒"会让脚本根本没跑，嵌套通道等于没被测到——
	// 那就会得到一个"看起来通过、实际没覆盖"的假证据。
	choices map[string]policy.ApprovalChoice
}

// label 给这次运行一个稳定且唯一的名字：同一个场景用不同结论跑多次时，
// tree.json 里的键不能撞车。
func (sc runScenario) label() string {
	parts := []string{sc.scenario, string(sc.choice)}
	if sc.harnessMCP {
		parts = append(parts, "mcp")
	}
	if len(sc.extraExtensions) > 0 {
		parts = append(parts, "ext"+strconv.Itoa(len(sc.extraExtensions)))
	}
	if len(sc.choices) > 0 {
		keys := make([]string, 0, len(sc.choices))
		for k := range sc.choices {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+"="+string(sc.choices[k]))
		}
	}
	return strings.Join(parts, "/")
}

// choiceFor 返回某个工具名对应的审批结论。
func (sc runScenario) choiceFor(toolName string) policy.ApprovalChoice {
	if c, ok := sc.choices[toolName]; ok {
		return c
	}
	return sc.choice
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
	var askedMu sync.Mutex
	asked := map[string]int{}
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
			askedMu.Lock()
			asked[p.ToolName]++
			askedMu.Unlock()
			// 由用例按工具名指定的结论；用独立 goroutine 提交，避免阻塞 Pi 的请求。
			go func() {
				time.Sleep(50 * time.Millisecond)
				_ = resolveViaBridge(srv.Endpoint(), srv.Env(), exec.SessionID, p.ID, sc.choiceFor(p.ToolName))
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
		ExtraExtensions: sc.extraExtensions,
	}
	extra := append(spec.Args(), sc.extraArgs...)

	// 外部观察：在端点前面加一层记录代理，Pi 只认识代理的 URL。
	// 未配置 ANVIL_OBSERVATION_DIR 时保持原样（直连端点），不影响断言。
	wirePath := policyWireLogPath()
	wireBase := 0
	if wirePath != "" {
		wireBase = bridgeWireLen(t, wirePath)
	}
	launch := nodeLaunch(captureNodePath(t), piCLIPath(t, root))
	launchEnv := srv.Env()
	if wirePath != "" {
		proxy := startBridgeWireProxy(t, srv.Endpoint().URL, wirePath, t.Name())
		launchEnv = envWithPolicyURL(launchEnv, proxy.URL)
	}
	launch.env = append(launch.env, launchEnv...)

	recs, stderr, code := runPiRPC(t, launch, work, agentDir,
		[][]string{{promptCmd("req-policy", sc.scenario)}}, 1, true, extra, 0, 120*time.Second)

	askedMu.Lock()
	askedCopy := make(map[string]int, len(asked))
	for k, v := range asked {
		askedCopy[k] = v
	}
	askedMu.Unlock()

	run := policyRun{
		records: recs, stderr: stderr, code: code,
		pending: atomic.LoadInt32(&pending), asked: askedCopy,
		label: sc.label(),
		tree:  walkTree(t, work),
	}
	if wirePath != "" {
		all := readBridgeWire(t, wirePath)
		if wireBase <= len(all) {
			run.wire = all[wireBase:]
		}
	}
	recordWorkTree(t, work, run)
	return run
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

// TestRealPiPolicyBlocksWriteAndEditChannels 覆盖 SEC02 里 bash 之外的写通道。
//
// 为什么必须单独测：bash 被拦住不代表 write/edit 也被拦住——它们是三条不同的
// 内置工具，参数结构不同（write: path/content；edit: path/edits[]），
// 策略扩展的取值与策略层的文件类工具判定都要各自成立。
//
// 判定同样落在文件系统上：write 产物是否存在、edit 目标内容是否被改。
func TestRealPiPolicyBlocksWriteAndEditChannels(t *testing.T) {
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
	srv := httptest.NewServer(openaisse.NewHandler(filepath.Join(work, "requests")))
	defer srv.Close()
	writeFixtureModels(t, agentDir, srv.URL)

	writeMarker := filepath.Join(work, "write-ran.txt")
	editTarget := filepath.Join(work, "edit-target.txt")

	// ── write：拒绝 ──
	_ = os.Remove(writeMarker)
	writeBaseline := sentinelBaseline(t, work, "write-ran.txt")
	writeDeny := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-write", choice: policy.ApprovalDeny})
	t.Logf("write 拒绝：%d 条记录，待审批 %d 次", len(writeDeny.records), writeDeny.pending)
	if writeDeny.pending == 0 {
		t.Fatal("write 调用没有触发审批——策略扩展可能没覆盖该通道，本用例不构成证据")
	}
	if fileExists(writeMarker) {
		t.Fatal("拒绝后 write 仍然写出了文件（write-ran.txt 存在）")
	}
	writeDenyCreated := fileExists(writeMarker)

	// ── write：允许（正向对照） ──
	_ = os.Remove(writeMarker)
	writeAllow := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-write", choice: policy.ApprovalOnce})
	t.Logf("write 允许：%d 条记录，待审批 %d 次", len(writeAllow.records), writeAllow.pending)
	if !fileExists(writeMarker) {
		t.Fatalf("允许后 write 应写出 write-ran.txt；前 1200 字节记录：\n%s", truncate(joinRecords(writeAllow.records), 1200))
	}
	writeAllowCreated := fileExists(writeMarker)

	// ── edit：拒绝 ──
	// edit 需要一个唯一的 oldText 才能改；先写入基线内容，拒绝后必须原样保留。
	if err := os.WriteFile(editTarget, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("写 edit 目标文件: %v", err)
	}
	editBaseline := sentinelBaseline(t, work, "edit-target.txt")
	editDeny := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-edit", choice: policy.ApprovalDeny})
	t.Logf("edit 拒绝：%d 条记录，待审批 %d 次", len(editDeny.records), editDeny.pending)
	if editDeny.pending == 0 {
		t.Fatal("edit 调用没有触发审批——该通道未被覆盖")
	}
	if got := readIfExists(editTarget); got != "before\n" {
		t.Fatalf("拒绝后 edit 仍然改了文件，实际内容 %q", got)
	}
	editDenyContent := readIfExists(editTarget)

	// ── edit：允许（正向对照） ──
	editAllow := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-edit", choice: policy.ApprovalOnce})
	t.Logf("edit 允许：%d 条记录，待审批 %d 次", len(editAllow.records), editAllow.pending)
	if got := readIfExists(editTarget); !strings.Contains(got, "after") {
		t.Fatalf("允许后 edit 应把 before 改成 after，实际内容 %q；前 1200 字节记录：\n%s",
			got, truncate(joinRecords(editAllow.records), 1200))
	}
	editAllowContent := readIfExists(editTarget)

	// ── 外部观察：write / edit 两条通道的磁盘证据 ──
	// blocked/allowed 由"运行前后的磁盘 sha256 + 请求日志里的结论条数"判定，
	// 不来自上面那些断言变量。
	recordChannelEvidence(t, channelEvidence{
		Channel: "write", ToolName: "write", Sentinel: "write-ran.txt", Baseline: writeBaseline,
		Deny:  channelRunFrom(writeDeny, "write", "write-ran.txt", treePresent(writeDeny.tree, "write-ran.txt"), "write-ran.txt 是否被写出"),
		Allow: channelRunFrom(writeAllow, "write", "write-ran.txt", treePresent(writeAllow.tree, "write-ran.txt"), "write-ran.txt 是否被写出"),
	})
	recordChannelEvidence(t, channelEvidence{
		Channel: "edit", ToolName: "edit", Sentinel: "edit-target.txt", Baseline: editBaseline,
		Deny:  channelRunFrom(editDeny, "edit", "edit-target.txt", true, "内容是否仍为 before"),
		Allow: channelRunFrom(editAllow, "edit", "edit-target.txt", true, "内容是否被改成 after"),
	})

	// 证据必须在每个场景结束时立刻取快照：跑完全部场景再读，读到的是最终状态，
	// 会让 deny 的记录看起来"也写了文件"——那不是事实，是采集时机错了。
	evidence := map[string]any{
		"write": map[string]any{
			"deny":  map[string]any{"pendingApprovals": writeDeny.pending, "fileCreated": writeDenyCreated},
			"allow": map[string]any{"pendingApprovals": writeAllow.pending, "fileCreated": writeAllowCreated},
		},
		"edit": map[string]any{
			"deny":          map[string]any{"pendingApprovals": editDeny.pending, "content": editDenyContent},
			"allow":         map[string]any{"pendingApprovals": editAllow.pending, "content": editAllowContent},
			"denyPreserved": editDenyContent == "before\n",
		},
	}
	if b, err := json.MarshalIndent(evidence, "", "  "); err == nil {
		t.Logf("写通道策略证据：\n%s", b)
		writeTestEvidence(t, "f04-policy-write-channels.json", b)
	}
}

// TestRealPiPolicyBlocksExtensionChannel 覆盖 SEC03 里的"扩展工具"通道。
//
// 为什么必须单独测：策略扩展在 tool_call 钩子上做拦截，而内置工具、MCP 工具、扩展工具
// 是三条不同的执行路径。内部工具被拦住，不能推出扩展工具也被拦住——扩展注册的工具
// 有自己的 execute 实现，参数结构也由扩展自定义。这条通道要么实测过，要么就是未知。
//
// 判定落在文件系统上：fixture_marker 被执行就写 extension-ran.txt，没被执行就不存在。
func TestRealPiPolicyBlocksExtensionChannel(t *testing.T) {
	if os.Getenv("MYAGENT_PI_POLICY") != "1" {
		t.Skip("设置 MYAGENT_PI_POLICY=1 才执行真实 Pi 的策略集成验证")
	}
	if runtime.GOOS != "windows" {
		t.Skip("当前集成脚本按 Windows 路径编写")
	}

	root := repoRoot(t)
	ext := filepath.Join(root, "tests", "fixtures", "extension", "fixture-tool.ts")
	if _, err := os.Stat(ext); err != nil {
		t.Fatalf("扩展工具 fixture 不存在: %v", err)
	}

	work := t.TempDir()
	agentDir := filepath.Join(work, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("建 agentDir: %v", err)
	}
	srv := httptest.NewServer(openaisse.NewHandler(filepath.Join(work, "requests")))
	defer srv.Close()
	writeFixtureModels(t, agentDir, srv.URL)

	// marker 由 fixture 工具按 Pi 进程的 cwd（即 work）解析。
	marker := filepath.Join(work, "extension-ran.txt")
	sc := func(choice policy.ApprovalChoice) runScenario {
		return runScenario{scenario: "tool-ext", choice: choice, extraExtensions: []string{ext}}
	}

	// ── 拒绝 ──
	_ = os.Remove(marker)
	extBaseline := sentinelBaseline(t, work, "extension-ran.txt")
	deny := runPolicyCase(t, root, work, agentDir, sc(policy.ApprovalDeny))
	t.Logf("扩展工具拒绝：%d 条记录，待审批 %d 次", len(deny.records), deny.pending)
	if deny.pending == 0 {
		t.Fatalf("扩展工具调用没有触发审批——该通道未被覆盖，本用例不构成证据；记录：\n%s",
			truncate(joinRecords(deny.records), 1200))
	}
	if fileExists(marker) {
		t.Fatal("拒绝后扩展工具仍然执行了（extension-ran.txt 存在）")
	}
	denyCreated := fileExists(marker)

	// ── 允许（正向对照） ──
	// 没有这一步，"拒绝后没写文件"也可能是因为工具根本没被注册/根本调不到，
	// 那样本用例证明的是"fixture 坏了"，不是"策略拦住了"。
	_ = os.Remove(marker)
	allow := runPolicyCase(t, root, work, agentDir, sc(policy.ApprovalOnce))
	t.Logf("扩展工具允许：%d 条记录，待审批 %d 次", len(allow.records), allow.pending)
	if !fileExists(marker) {
		t.Fatalf("允许后扩展工具应写出 extension-ran.txt；前 1200 字节记录：\n%s",
			truncate(joinRecords(allow.records), 1200))
	}
	allowContent := readIfExists(marker)

	recordChannelEvidence(t, channelEvidence{
		Channel: "extension", ToolName: "fixture_marker", Sentinel: "extension-ran.txt", Baseline: extBaseline,
		Deny:  channelRunFrom(deny, "fixture_marker", "extension-ran.txt", treePresent(deny.tree, "extension-ran.txt"), "extension-ran.txt 是否被写出"),
		Allow: channelRunFrom(allow, "fixture_marker", "extension-ran.txt", treePresent(allow.tree, "extension-ran.txt"), "extension-ran.txt 是否被写出"),
	})

	evidence := map[string]any{
		"channel":  "extension",
		"toolName": "fixture_marker",
		"deny":     map[string]any{"pendingApprovals": deny.pending, "fileCreated": denyCreated},
		"allow":    map[string]any{"pendingApprovals": allow.pending, "fileCreated": fileExists(marker), "content": allowContent},
	}
	if b, err := json.MarshalIndent(evidence, "", "  "); err == nil {
		t.Logf("扩展工具通道策略证据：\n%s", b)
		writeTestEvidence(t, "f04-policy-extension-channel.json", b)
	}
}

// TestRealPiPolicyBlocksCodemodeNestedChannel 覆盖 SEC03 的"codemode 嵌套调用"通道。
//
// 这是四条通道里最容易漏的一条：脚本在 QuickJS 沙箱里执行 `tools.bash(...)`，
// 没有经过模型、也不经过任何一层顶层的工具调用 UI。如果实现只拦"模型直接发起的
// 工具调用"，这条通道就会整体绕过策略层，而外面看起来一切正常。
//
// 因此本用例必须把外层与内层的结论分开给：
//   - 外层拒绝 → 脚本压根没跑（此步只证明外层被拦）；
//   - 外层允许 + 内层拒绝 → 脚本跑了，但嵌套的 bash 被单独拦下（关键一步）；
//   - 两层都允许 → marker 出现（正向对照，证明上面的"没出现"不是因为链路本来就不通）。
//
// TestRealPiPolicyBlocksHarnessMCPChannel 验证"自有 MCP"这条通道同样在执行前被拦。
//
// 与外部 MCP 的区别不是安全性，而是可观测性：server 是我们自己的，所以能直接问
// "handler 到底被调用了没有"，而不只能看 Pi 有没有报错、界面写了什么。
// 判定用两条独立证据，缺一不可：
//   - 文件系统：harness-ran.txt 是否出现；
//   - 服务端计数：harnessmcp 的 Calls() 是否记录到 harness_probe。
//
// 只看文件会把"写失败"误判成"被拦"；只看计数会把"策略拦了但客户端重试"漏掉。
func TestRealPiPolicyBlocksHarnessMCPChannel(t *testing.T) {
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
	prov := httptest.NewServer(openaisse.NewHandler(filepath.Join(work, "requests")))
	defer prov.Close()
	writeFixtureModels(t, agentDir, prov.URL)

	hs, err := harnessmcp.Start(context.Background(), harnessmcp.Options{
		Tools:   []harnessmcp.Tool{harnessmcp.ProbeTool(work)},
		Version: "test",
	})
	if err != nil {
		t.Fatalf("启动自有 MCP: %v", err)
	}
	defer func() { _ = hs.Close(context.Background()) }()

	// mcp.json 与生产同一份写法：令牌只写 ${...} 引用，由 worker 环境展开。
	cfg := map[string]any{"mcpServers": map[string]any{"harness": map[string]any{
		"url":     hs.Endpoint().URL,
		"headers": map[string]any{"Authorization": "Bearer ${MYAGENT_HARNESS_TOKEN}"},
		// 默认 exposure=codemode 时模型看不到 MCP 工具，直接调用会得到
		// "only reachable from codemode or tool_search"。生产配置同样用 direct。
		"exposure": "direct",
	}}}
	blob, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(agentDir, "mcp.json"), blob, 0o644); err != nil {
		t.Fatalf("写 mcp.json: %v", err)
	}
	// 端点与令牌只经环境变量交付；子进程按 os.Environ() 继承。
	for _, kv := range hs.Env() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			t.Setenv(k, v)
		}
	}

	marker := filepath.Join(work, "harness-ran.txt")
	sc := func(choice policy.ApprovalChoice) runScenario {
		return runScenario{scenario: "tool-harness", choice: choice, harnessMCP: true}
	}

	// ── 拒绝 ──
	_ = os.Remove(marker)
	harnessBaseline := sentinelBaseline(t, work, "harness-ran.txt")
	deny := runPolicyCase(t, root, work, agentDir, sc(policy.ApprovalDeny))
	callsAfterDeny := len(hs.Calls())
	t.Logf("自有 MCP 拒绝：%d 条记录，待审批 %d 次，handler 被调用 %d 次",
		len(deny.records), deny.pending, callsAfterDeny)
	if deny.pending == 0 {
		t.Fatalf("自有 MCP 调用没有触发审批——该通道未被覆盖，本用例不构成证据；记录：\n%s",
			truncate(joinRecords(deny.records), 1500))
	}
	denyCreated := fileExists(marker)
	if denyCreated {
		t.Fatal("拒绝后自有 MCP 工具仍然执行了（harness-ran.txt 存在）")
	}
	if callsAfterDeny != 0 {
		t.Fatalf("拒绝后 handler 仍被调用 %d 次：%v", callsAfterDeny, hs.Calls())
	}

	// ── 允许（正向对照） ──
	// 没有这一步，"拒绝后没写文件"也可能是因为 MCP 根本没连上、工具压根不在表里。
	_ = os.Remove(marker)
	allow := runPolicyCase(t, root, work, agentDir, sc(policy.ApprovalOnce))
	callsAfterAllow := len(hs.Calls())
	t.Logf("自有 MCP 允许：%d 条记录，待审批 %d 次，handler 被调用 %d 次",
		len(allow.records), allow.pending, callsAfterAllow)
	if !fileExists(marker) {
		t.Fatalf("允许后应写出 harness-ran.txt；handler 调用 %d 次，记录：\n%s",
			callsAfterAllow, truncate(joinRecords(allow.records), 1500))
	}
	if callsAfterAllow <= callsAfterDeny {
		t.Fatalf("允许后 handler 未被调用（%d → %d）——正向对照失败，拒绝场景的'没执行'说明不了问题",
			callsAfterDeny, callsAfterAllow)
	}
	allowContent := readIfExists(marker)

	recordChannelEvidence(t, channelEvidence{
		Channel: "harness-mcp", ToolName: "mcp__harness__harness_probe", Sentinel: "harness-ran.txt", Baseline: harnessBaseline,
		Deny:  channelRunFrom(deny, "mcp__harness__harness_probe", "harness-ran.txt", treePresent(deny.tree, "harness-ran.txt"), "harness-ran.txt 是否被写出"),
		Allow: channelRunFrom(allow, "mcp__harness__harness_probe", "harness-ran.txt", treePresent(allow.tree, "harness-ran.txt"), "harness-ran.txt 是否被写出"),
	})

	evidence := map[string]any{
		"channel":  "harness-mcp",
		"toolName": "mcp__harness__harness_probe",
		"deny": map[string]any{
			"pendingApprovals": deny.pending,
			"fileCreated":      denyCreated,
			"handlerCalls":     callsAfterDeny,
		},
		"allow": map[string]any{
			"pendingApprovals": allow.pending,
			"fileCreated":      fileExists(marker),
			"handlerCalls":     callsAfterAllow,
			"content":          allowContent,
		},
	}
	if b, err := json.MarshalIndent(evidence, "", "  "); err == nil {
		t.Logf("自有 MCP 通道策略证据：\n%s", b)
		writeTestEvidence(t, "f04-policy-harness-mcp-channel.json", b)
	}
}

func TestRealPiPolicyBlocksCodemodeNestedChannel(t *testing.T) {
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
	srv := httptest.NewServer(openaisse.NewHandler(filepath.Join(work, "requests")))
	defer srv.Close()
	writeFixtureModels(t, agentDir, srv.URL)

	marker := filepath.Join(work, "codemode-ran.txt")
	// codemode 是内置扩展：-ne 会把它一起关掉，必须显式 -e builtin:codemode 才加载；
	// 加载后仍是 defaultActive:false，还要 --tools +codemode 才进工具表。
	// 两句缺一不可——只写 --tools 会得到一个"工具不存在"的假失败。
	base := runScenario{
		scenario:  "tool-codemode",
		extraArgs: []string{"-e", "builtin:codemode", "--tools", "+codemode"},
	}

	codemodeBaseline := sentinelBaseline(t, work, "codemode-ran.txt")

	// ── 1. 外层拒绝：脚本不该跑 ──
	outerDeny := base
	outerDeny.choice = policy.ApprovalDeny
	_ = os.Remove(marker)
	r1 := runPolicyCase(t, root, work, agentDir, outerDeny)
	t.Logf("外层 codemode 拒绝：待审批 %d 次，按工具 %v", r1.pending, r1.asked)
	if r1.asked["codemode"] == 0 {
		t.Fatalf("外层 codemode 没有触发审批——该工具未激活或未被策略覆盖；按工具：%v\n前 2000 字节记录：\n%s",
			r1.asked, truncate(joinRecords(r1.records), 2000))
	}
	if fileExists(marker) {
		t.Fatal("外层 codemode 被拒绝后脚本仍然执行了（codemode-ran.txt 存在）")
	}

	// ── 2. 外层允许 + 内层 bash 拒绝：嵌套调用必须被单独拦下 ──
	innerDeny := base
	innerDeny.choice = policy.ApprovalOnce
	innerDeny.choices = map[string]policy.ApprovalChoice{"bash": policy.ApprovalDeny}
	_ = os.Remove(marker)
	r2 := runPolicyCase(t, root, work, agentDir, innerDeny)
	t.Logf("外层允许 / 嵌套 bash 拒绝：待审批 %d 次，按工具 %v", r2.pending, r2.asked)
	if r2.asked["codemode"] == 0 {
		t.Fatalf("外层 codemode 没有触发审批；按工具：%v", r2.asked)
	}
	if r2.asked["bash"] == 0 {
		t.Fatalf("嵌套 bash 没有单独触发审批——codemode 脚本内的工具调用绕过了策略层；按工具：%v", r2.asked)
	}
	if fileExists(marker) {
		t.Fatal("嵌套 bash 被拒绝后仍写出了 codemode-ran.txt")
	}
	innerDenyCreated := fileExists(marker)

	// ── 3. 两层都允许：正向对照 ──
	innerAllow := base
	innerAllow.choice = policy.ApprovalOnce
	innerAllow.choices = map[string]policy.ApprovalChoice{"bash": policy.ApprovalOnce}
	_ = os.Remove(marker)
	r3 := runPolicyCase(t, root, work, agentDir, innerAllow)
	t.Logf("两层都允许：待审批 %d 次，按工具 %v", r3.pending, r3.asked)
	if !fileExists(marker) {
		t.Fatalf("两层都允许后应写出 codemode-ran.txt；前 1200 字节记录：\n%s",
			truncate(joinRecords(r3.records), 1200))
	}
	allowContent := readIfExists(marker)

	// 关键一步是"外层允许 + 内层拒绝"：它才证明嵌套调用被单独拦下。
	// 所以 deny 侧取 r2（内层 bash 被拒），allow 侧取 r3（两层都允许）。
	recordChannelEvidence(t, channelEvidence{
		Channel: "codemode-nested", ToolName: "bash", Sentinel: "codemode-ran.txt", Baseline: codemodeBaseline,
		Deny:  channelRunFrom(r2, "bash", "codemode-ran.txt", treePresent(r2.tree, "codemode-ran.txt"), "外层允许、嵌套 bash 被拒时脚本副作用是否发生"),
		Allow: channelRunFrom(r3, "bash", "codemode-ran.txt", treePresent(r3.tree, "codemode-ran.txt"), "两层都允许时脚本副作用是否发生"),
	})

	evidence := map[string]any{
		"channel":  "codemode-nested",
		"scenario": "tool-codemode（脚本内调用 bash）",
		"outerDeny": map[string]any{
			"pendingApprovals": r1.pending, "askedByTool": r1.asked, "fileCreated": false,
		},
		"innerDeny": map[string]any{
			"pendingApprovals": r2.pending, "askedByTool": r2.asked, "fileCreated": innerDenyCreated,
		},
		"bothAllow": map[string]any{
			"pendingApprovals": r3.pending, "askedByTool": r3.asked, "fileCreated": true, "content": allowContent,
		},
		"nestedCallWasAsked": r2.asked["bash"] > 0,
	}
	if b, err := json.MarshalIndent(evidence, "", "  "); err == nil {
		t.Logf("codemode 嵌套通道策略证据：\n%s", b)
		writeTestEvidence(t, "f04-policy-codemode-nested.json", b)
	}
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
	toolBaseline := sentinelBaseline(t, work, "tool-ran.txt")
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
	mcpBaseline := sentinelBaseline(t, work, "mcp-requests.log")
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

	// ── 未支持通道：禁用之后必须"实际不能调用"，而不是"策略会说拒绝" ──
	//
	// 自有 MCP server（internal/harnessmcp）本期没有实现，属于"未支持的通道"。
	// 验收计划对这一类的要求不是"策略会拦住"，而是**从工具列表里禁用并验证不可调用**
	// （foundation-runtime 计划 §F04）。差别很实际：策略层拦截说明工具还在、
	// 有一条依赖"钩子实现正确"的安全边界；工具根本不在列表里，模型连发起的机会都没有。
	//
	// 判据落在两处：mcp.json 仍然存在（配置没被删掉，只是没启用），
	// 而 handler 日志里没有任何 tools/call、审批次数为 0（工具不存在 → 连询问都不会发生）。
	_ = os.Remove(mcpLog)
	mcpOff := runPolicyCase(t, root, work, agentDir, runScenario{scenario: "tool-mcp", choice: policy.ApprovalOnce, harnessMCP: false})
	offLog := readIfExists(mcpLog)
	t.Logf("MCP 禁用场景：%d 条记录，待审批 %d 次", len(mcpOff.records), mcpOff.pending)
	if strings.Contains(offLog, `"tools/call"`) {
		t.Fatalf("--no-mcp 下 MCP 调用仍到达 handler（通道未被真正禁用）；MCP 日志：\n%s", truncate(offLog, 600))
	}
	if mcpOff.pending != 0 {
		t.Fatalf("--no-mcp 下仍出现 %d 次审批请求，说明工具仍在工具表里（只是被拦）；按“禁用并不可调用”的要求应为 0", mcpOff.pending)
	}
	mcpDisabledHandlerReached := strings.Contains(offLog, `"tools/call"`)

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

	// ── 外部观察：bash / 外部 MCP 两条通道 + SEC08 的项目级资源 ──
	recordChannelEvidence(t, channelEvidence{
		Channel: "bash", ToolName: "bash", Sentinel: "tool-ran.txt", Baseline: toolBaseline,
		Deny:  channelRunFrom(denyRun, "bash", "tool-ran.txt", treePresent(denyRun.tree, "tool-ran.txt"), "tool-ran.txt 是否被写出"),
		Allow: channelRunFrom(allowRun, "bash", "tool-ran.txt", treePresent(allowRun.tree, "tool-ran.txt"), "tool-ran.txt 是否被写出"),
	})
	// 外部 MCP 的哨兵是"日志里出现 tools/call"，不是文件存在：
	// 日志本身一定有 initialize 等请求，只按文件判定会把"被拦"误读成"跑了"。
	recordChannelEvidence(t, channelEvidence{
		Channel: "external-mcp", ToolName: "mcp__fixture__fixture_echo", Sentinel: "mcp-requests.log", Baseline: mcpBaseline,
		Deny:  channelRunFrom(mcpDeny, "mcp__fixture__fixture_echo", "mcp-requests.log", strings.Contains(denyLog, `"tools/call"`), "handler 日志里是否出现 tools/call"),
		Allow: channelRunFrom(mcpAllow, "mcp__fixture__fixture_echo", "mcp-requests.log", strings.Contains(allowLog, `"tools/call"`), "handler 日志里是否出现 tools/call"),
	})

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
			"deny":     map[string]any{"pendingApprovals": mcpDeny.pending, "handlerReached": strings.Contains(denyLog, `"tools/call"`)},
			"allow":    map[string]any{"pendingApprovals": mcpAllow.pending, "handlerReached": strings.Contains(allowLog, `"tools/call"`)},
			"disabled": map[string]any{"pendingApprovals": mcpOff.pending, "handlerReached": mcpDisabledHandlerReached, "configStillPresent": fileExists(filepath.Join(agentDir, "mcp.json"))},
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
		writeTestEvidence(t, "f04-policy-integration.json", b)
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
