package devtools

// integration runner：把"真实的集成检查"跑成可被门禁评估的证据树。
//
// 与 static/unit 的区别：这里每一条都是一次真实子进程执行（go test / 平台脚本），
// 并且每条 case 单独产出 result.json + 原始 stdout/stderr。门禁只认这些原始产物，
// 不认"跑过一遍觉得没问题"。
//
// 纪律：
//   - case 表里没有的必需 case 直接判配置缺失（退出 2），不静默跳过。
//   - 环境不具备（缺 Pi、缺独立包）记 BLOCKED 并给出原因，不改写成 PASS。
//   - 任何时候都不写"全绿"的假报告：退出码由真实子进程结果决定。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// targetPlatforms 是六个目标的 GOOS/GOARCH。
// 集成测试必须在目标本机跑：交叉编译出来的二进制跑不了，结论无效。
var targetPlatforms = map[string][2]string{
	"win-x64":     {"windows", "amd64"},
	"win-arm64":   {"windows", "arm64"},
	"mac-x64":     {"darwin", "amd64"},
	"mac-arm64":   {"darwin", "arm64"},
	"linux-x64":   {"linux", "amd64"},
	"linux-arm64": {"linux", "arm64"},
}

// integrationCase 是一条集成 case 的真实执行方式。
type integrationCase struct {
	// ID 必须与 tests/acceptance/cases.json 中的 case id 完全一致。
	ID string
	// Package 为空时表示用 External 命令而不是 go test。
	Package string
	// Run 是 go test -run 的正则。
	Run string
	// External 是直接执行的命令（Package 为空时使用）。
	External []string
	// Env 追加到子进程环境。
	Env []string
	// Blocked 非空表示当前环境无法执行，值为可复核的原因。
	Blocked string
}

// integrationCases 是 G0 需要的 case 到真实命令的映射。
//
// 只列当前仓库**真的能跑**的东西。做不到的写 Blocked 并说明缺什么，
// 不允许把"没实现"写成"跳过"然后当作通过。
func integrationCases() []integrationCase {
	gateTests := "internal/devtools"
	piTests := "internal/runtime/pi"
	policyTests := "internal/policy"

	cases := []integrationCase{
		// ── 环境 ──
		// PATH 必须用子进程环境构造，不能改宿主：本机交互式 shell 的 PATH 首个 node
		// 是 WorkBuddy 托管的 v22，而基线要求 v24.16.0。脚本本身仍然独立校验，
		// 这里只是让"被测环境"等于基线声明的环境，不是把断言绕过去。
		{ID: "ENV01", External: toolchainCommand(), Env: baselinePathEnv()},
		{ID: "ENV02", External: toolchainCommand(), Env: baselinePathEnv()},
		{ID: "ENV03", External: toolchainCommand(), Env: baselinePathEnv()},

		// ── 门禁自身 ──
		// GOV01：缺证据/未知 gate/未知 target 返回 2；目标×显示维度缺失也判死，
		// 而完整的六目标证据必须能通过（否则「全判死」也能让上面几条绿）。
		{ID: "GOV01", Package: gateTests, Run: "TestGateRejectsMissingRequiredCase|TestGateRejectsUnknownGate|TestGateRejectsUnknownTarget|TestGateRejectsAllScopeMissingOneTarget|TestGateRejectsSameCaseOneTargetFails|TestGateRejectsUnknownScope|TestGateRejectsAtomicCaseWithoutScope|TestGateAcceptsAllRequiredEvidence|TestGateAcceptsAllScopeOnSixTargets|TestGateRejectsLinuxMissingWayland"},
		// GOV02：陈旧 revision/锁 hash/伪造报告/缺审查人/串用阶段/重复证据/dirty 工作区均拒绝。
		{ID: "GOV02", Package: gateTests, Run: "TestGateRejectsStaleRevision|TestGateRejectsFakeReportHash|TestGateRejectsMissingReviewer|TestGateRejectsDirtyWorktreeEvidence|TestGateRejectsEvidenceForWrongGate|TestGateRejectsDuplicateEvidence"},
		{ID: "GOV03", Package: gateTests, Run: "TestCommandFailureReturnsAssertionCode|TestMissingBinaryReturnsConfigCode|TestVerifyRejectsUnknownSuite|TestVerifyRejectsMissingSuite"},
		// GOV04：未实现/缺失的 runner 必须非零；前端 runner 的「缺失」不能用
		// 「路径错了读不到」冒充（修复前实测就是假绿）。
		{ID: "GOV04", Package: gateTests, Run: "TestFrontendTestRunnerMissingReturnsConfigCode|TestFrontendPackageJSONReachableFromPackageDir|TestIndexCoversEveryMatrixCase|TestIndexEntriesAreActionable|TestReviewGate"},

		// ── Pi 定位与探测 ──
		{ID: "PI01", Package: piTests, Run: "TestLocate"},
		{ID: "PI02", Package: piTests, Run: "TestProbeHasNoUserConfigSideEffects|TestProbePs1NotDirectlyExecutable"},
		{ID: "PI03", Package: piTests, Run: "TestCaptureRealPiProtocol|TestProtocolGolden|TestWorkerWithRealPi"},
		{ID: "PI04", Package: piTests, Run: "TestClassifyVersion|TestStandaloneNodeFreeProbe"},

		// ── RPC ──
		{ID: "RPC01", Package: piTests, Run: "TestLFFramingUnicodeAndLargeRecord"},
		{ID: "RPC02", Package: piTests, Run: "TestOutOfOrderResponsesMatchByID|TestDuplicateResponseNotMisattributed|TestCancelledCallDoesNotBlockSubsequentCalls"},
		{ID: "RPC03", Package: piTests, Run: "TestStdoutBackpressureDoesNotDeadlock|TestStderrFloodDoesNotBlockStdout"},
		{ID: "RPC04", Package: piTests, Run: "TestUnknownAndMalformedRecordsHaveDiagnostics|TestCallRejectsNonObjectCommand|TestCloseRejectsNewCalls"},
		{ID: "RPC05", Package: piTests, Run: "TestProcessTree|TestWorkerTerminatesGrandchild"},

		// ── 权限 ──
		{ID: "SEC01", Package: policyTests, Run: "TestApprovalBinds|TestControlGenerationInvalidatesApproval|TestSessionScopeGrantAllowsSubsequentCalls|TestPermissionDeniesWithoutApproval"},
		// SEC02：三条内置写通道（bash/write/edit）逐条做拒绝+允许对照。
		{ID: "SEC02", Package: piTests, Run: "TestRealPiPolicyBlocksToolAndProjectResources|TestRealPiPolicyBlocksWriteAndEditChannels", Env: []string{"MYAGENT_PI_POLICY=1"}},
		// SEC03.runtime：六条通道的真实对照，各自"拒绝 → 副作用不发生 / 允许 → 发生"：
		// builtin(bash)、内置写(write/edit)、外部 MCP、自有 MCP、扩展工具、codemode 嵌套。
		// 自有 MCP 用到 internal/harnessmcp 的真实 server，判定同时看文件系统与 handler 计数。
		// 无未支持通道后不再有"禁用并验证不可调用"的豁免项（该验证仍保留在
		// TestRealPiPolicyBlocksToolAndProjectResources 里，防止未来回归）。
		{ID: "SEC03.runtime", Package: piTests, Run: "TestRealPiPolicyBlocksToolAndProjectResources|TestRealPiPolicyBlocksWriteAndEditChannels|TestRealPiPolicyBlocksExtensionChannel|TestRealPiPolicyBlocksHarnessMCPChannel|TestRealPiPolicyBlocksCodemodeNestedChannel", Env: []string{"MYAGENT_PI_POLICY=1"}},
		{ID: "SEC04.runtime", Package: "internal/policybridge", Run: "TestRejectsUnauthenticatedAndForeignRequests|TestOriginFromOwnEndpointIsAccepted|TestApprovedOnceSideEffectIsExactlyOne"},
		{ID: "SEC05", Package: policyTests, Run: "TestPathGuard|TestApprovalBindsRealPathNotSpelling"},
		{ID: "SEC06", Package: "internal/policybridge", Run: "TestAskTimeoutDeniesWithoutSideEffect|TestRevokeWhileQueuedDenies|TestCloseDeniesPendingAndRefusesNewWork"},
		{ID: "SEC08", Package: piTests, Run: "TestRealPiPolicyBlocksToolAndProjectResources|TestIsolationArgsCoverEveryProjectChannel|TestValidateArgsDetectsEveryMissingFlag|TestUntrustedProject", Env: []string{"MYAGENT_PI_POLICY=1"}},

		// ── 配置隔离 ──
		{ID: "CFG03.isolation", Package: piTests, Run: "TestIsolationArgsCoverEveryProjectChannel|TestHarnessMCPRequiresExplicitEnable|TestIsolationRequiresExistingAbsoluteExtension"},
	}
	return cases
}

// toolchainCommand 返回本平台的工具链检查命令。
func toolchainCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/check-toolchain.ps1"}
	}
	return []string{"bash", "scripts/check-toolchain.sh"}
}

// baselinePathEnv 构造"基线声明的那套工具链"的 PATH。
//
// 本机交互式 shell 的 PATH 被别的工具改过（首个 node 是托管 v22、wails3 不在 PATH），
// 直接用它会得到 ENV 检查的 FAIL——那测的是 shell 配置，不是产品环境。
// 这里只影响子进程，不动宿主 PATH（AGENTS.md 明确要求不要为测试破坏系统 PATH）。
func baselinePathEnv() []string {
	sep := string(os.PathListSeparator)
	parts := make([]string, 0, 4)
	if runtime.GOOS == "windows" {
		if p := `D:\Program Files\nodejs`; dirExists(p) {
			parts = append(parts, p)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if p := filepath.Join(wd, ".tools", "bin"); dirExists(p) {
			parts = append(parts, p)
		}
	}
	parts = append(parts, os.Getenv("PATH"))
	return []string{"PATH=" + strings.Join(parts, sep)}
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// RunIntegration 是 `verify --suite=integration` 的入口。
func RunIntegration(target string) int {
	if target == "" {
		fmt.Fprintln(os.Stderr, "integration 必须指定 --target")
		return 2
	}
	want, ok := targetPlatforms[target]
	if !ok {
		fmt.Fprintf(os.Stderr, "未知 target: %s\n", target)
		return 2
	}
	if got := runtime.GOOS + "/" + runtime.GOARCH; got != want[0]+"/"+want[1] {
		fmt.Fprintf(os.Stderr, "当前宿主 %s 与目标 %s(%s) 不一致；集成测试必须在目标本机执行\n",
			got, target, want[0]+"/"+want[1])
		return 2
	}

	idx, err := LoadCasesIndex("tests/acceptance/cases.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	required := ResolveRequiredAtomicCases(idx, "G0")
	if len(required) == 0 {
		fmt.Fprintln(os.Stderr, "G0 没有必需 case（索引缺失）")
		return 2
	}

	table := map[string]integrationCase{}
	for _, c := range integrationCases() {
		table[c.ID] = c
	}
	var missing []string
	for _, id := range required {
		if _, ok := table[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		// 缺 runner 属于配置缺失：说清楚缺哪些，而不是少跑几条然后报绿。
		fmt.Fprintf(os.Stderr, "以下必需 case 没有集成 runner：%s\n", strings.Join(missing, ", "))
		return 2
	}

	rev, dirty := SourceIdentity()
	root := filepath.Join("docs", "evidence", "runs", rev, target)
	fmt.Printf("== verify --suite=integration --target=%s ==\n", target)
	fmt.Printf("证据根: %s\n", root)
	if dirty {
		// 证据照写（可诊断），但必须带 dirty 标记：门禁会拒绝它。
		// 不做「把改动藏起来当作干净 revision」这种假归属。
		fmt.Println("[警告] 工作区不干净：本次证据带 worktreeDirty=true，gate 会判死，不能用作正式结论。")
	}
	blocks := 0
	fails := 0
	for _, id := range required {
		c := table[id]
		res := runIntegrationCase(c, target, root)
		if res.blocked {
			blocks++
		} else if !res.passed {
			fails++
		}
	}

	fmt.Printf("汇总: %d 条必需 case，失败 %d，阻塞 %d\n", len(required), fails, blocks)

	// 测试不许改仓库。真实事故（2026-11-04）：F04/F02 的集成测试直接写
	// docs/evidence/raw/*.json，于是跑一次 verify 就把已提交的历史运行记录
	// 改写成当前参数（连 -ns 都抹掉），还让工作区变脏——下一轮证据就带
	// worktreeDirty 被门禁判死，看上去像门禁自己坏了。
	//
	// 这里把「运行期间工作区从干净变脏」直接判配置类失败：证据不可变是硬约束，
	// 不是靠人记住的约定。
	if !dirty {
		if _, nowDirty := SourceIdentity(); nowDirty {
			fmt.Println("[FAIL] 测试运行期间工作区变脏：有测试在写仓库文件。")
			fmt.Println("       历史证据必须不可变；需要归档新证据请显式指定独立输出目录。")
			if out, err := exec.Command("git", "status", "--porcelain").Output(); err == nil {
				fmt.Printf("       git status --porcelain:\n%s", out)
			}
			return 2
		}
	}

	if fails > 0 {
		return 1
	}
	if blocks > 0 {
		// 阻塞不等于通过：环境/实现缺失按配置类返回 2。
		return 2
	}
	return 0
}

type caseOutcome struct {
	passed  bool
	blocked bool
}

// runIntegrationCase 执行一条 case 并写证据。
func runIntegrationCase(c integrationCase, target, root string) caseOutcome {
	dir := filepath.Join(root, c.ID)
	_ = os.MkdirAll(dir, 0o755)

	started := time.Now().UTC()
	ev := contracts.GateEvidence{
		SchemaVersion:    1,
		GateID:           "G0",
		TaskID:           taskForCase(c.ID),
		SourceRevision:   GetSourceRevision(),
		WorktreeDirty:    worktreeDirty(),
		VersionsLockHash: versionsHash(),
		TargetID:         target,
		StartedAt:        started.Format(time.RFC3339),
		Reviewer: contracts.Reviewer{
			Name:       "devtools-integration-runner",
			Machine:    hostname(),
			ReviewedAt: started.Format(time.RFC3339),
			Note:       "机器执行证据；结构完整不等于人工审查通过",
		},
	}

	cr := contracts.CaseResult{
		ID:        c.ID,
		Expected:  "命令退出码 0，且输出不含失败断言",
		StartedAt: started.Format(time.RFC3339),
	}

	var cmdline, stdout, stderr string
	exit := 0
	switch {
	case c.Blocked != "":
		cr.Status = contracts.StatusBlocked
		cr.Actual = c.Blocked
		ev.Limitations = c.Blocked
		cmdline = "(未执行：环境或实现缺失)"
	case len(c.External) > 0:
		cmdline = strings.Join(c.External, " ")
		exit, stdout, stderr, _ = runWithEnv(c.Env, c.External[0], c.External[1:]...)
	case c.Package != "":
		args := []string{"test", "./" + c.Package, "-run", c.Run, "-count=1", "-v"}
		cmdline = "go " + strings.Join(args, " ")
		exit, stdout, stderr, _ = runWithEnv(c.Env, "go", args...)
	default:
		cr.Status = contracts.StatusFail
		cr.Actual = "runner 定义错误：既没有命令也没有包"
		exit = 1
	}

	if cr.Status == "" {
		if exit == 0 {
			cr.Status = contracts.StatusPass
			cr.Actual = "命令退出 0"
		} else {
			cr.Status = contracts.StatusFail
			cr.Actual = fmt.Sprintf("命令退出 %d：%s", exit, tail(stderr+"\n"+stdout, 600))
		}
	}
	cr.EndedAt = time.Now().UTC().Format(time.RFC3339)

	ev.Command = cmdline
	ev.ExitCode = exit
	ev.CaseResults = []contracts.CaseResult{cr}
	ev.EndedAt = cr.EndedAt

	writeEvidenceDir(dir, &ev, cmdline, stdout, stderr)

	fmt.Printf("  [%s] %s — %s\n", cr.Status, c.ID, firstLine(cr.Actual))
	return caseOutcome{passed: cr.Status == contracts.StatusPass, blocked: cr.Status == contracts.StatusBlocked}
}

// writeEvidence 落盘证据并回填报告哈希。
//
// 顺序不能换：哈希覆盖 result.json 自身（清空 reportHash 后）+ 三个原始文件，
// 所以必须先把原始文件写全、先写一版 result.json，再算哈希回填。
func writeEvidenceDir(dir string, ev *contracts.GateEvidence, cmdline, stdout, stderr string) {
	_ = os.WriteFile(filepath.Join(dir, "command.txt"), []byte(cmdline+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(stdout), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(stderr), 0o644)

	ev.ReportHash = ""
	if b, err := json.MarshalIndent(ev, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644)
	}
	ev.ReportHash = ComputeEvidenceHash(dir)
	if b, err := json.MarshalIndent(ev, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644)
	}
}

func runWithEnv(env []string, name string, args ...string) (int, string, string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return 0, out.String(), errb.String(), nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String(), errb.String(), nil
	}
	return 2, out.String(), errb.String(), err
}

func versionsHash() string {
	h, err := ComputeVersionsLockHash("build/versions.lock.json")
	if err != nil {
		return ""
	}
	return h
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// worktreeDirty 标记证据是否在脏工作区生成。
// 与 SourceIdentity 共用同一条 git 判定，避免两处口径分叉。
func worktreeDirty() bool {
	_, dirty := SourceIdentity()
	return dirty
}

// taskForCase 把 case 归属到模块，便于证据回溯到计划任务。
func taskForCase(id string) string {
	switch {
	case strings.HasPrefix(id, "ENV"):
		return "F00"
	case strings.HasPrefix(id, "GOV"):
		return "F01"
	case strings.HasPrefix(id, "PI"):
		return "F02"
	case strings.HasPrefix(id, "RPC"):
		return "F03"
	case strings.HasPrefix(id, "SEC"):
		return "F04"
	case strings.HasPrefix(id, "CFG"):
		return "F07"
	}
	return "unknown"
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
