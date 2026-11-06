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

// observationSpec 声明一个 case 必须产出的 external-observation oracle。
//
// 为什么需要它：RUN* 在用例索引里声明 oracleKind=external，门禁会对这类 case 调用
// validateOraclePath(..., wantExternal=true)，test-process 自证一律被拒；而 oracle
// 欠账 allowlist 是只减不增的棘轮，新 case 不能往里加。所以 runner 必须能把
// "case 自己写出的外部观察值" 变成合格的 external-observation oracle。
//
// 声明位置：case 级（c.Observation）作用于整条 case，观察目录就是该 case 的证据目录；
// step 级（integrationStep.Observation）只允许一个 step 声明，用于"这条 case 的
// 副作用只在某一个命令里产生"的写法。两者同时声明时必须完全一致，否则判配置错误——
// 宁可判错也不能猜哪一份才是真的。
//
// 声明之后没有退路：case 不产出 observation.json 就是 FAIL，runner 不会替它
// 编观察值，也不会退回 test-process oracle。
type observationSpec struct {
	// Source 必须是 evidence.go 里 externalOracleSources 的合法值
	// （fixture-counter/http-server/database/filesystem/process-tree/os-signature/manual-review）。
	Source string
	// Artifacts 是相对 case 证据目录的原始文件，必须由 case 自己写出来且非空。
	Artifacts []string
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
	// Steps 是"一条 case 需要跨多个包/命令验证"时的写法；非空时忽略 Package/External。
	//
	// 为什么需要：一个验收条件常常一半在资源层（落库/校验）、一半在绑定层
	// （UI 能看到的路径）。只跑其中一半，证据就只覆盖一半，但 case 状态是二元 PASS。
	Steps []integrationStep
	// Blocked 非空表示当前环境无法执行，值为可复核的原因。
	Blocked string
	// Observation 非空表示该 case 必须产出 external-observation oracle。
	Observation *observationSpec
}

// integrationStep 是一次 go test / 外部命令。
type integrationStep struct {
	Package  string
	Run      string
	External []string
	Env      []string
	// Observation 见 observationSpec 的说明；只影响 oracle 生成，不影响命令本身。
	Observation *observationSpec
}

// integrationCases 是 G0 需要的 case 到真实命令的映射。
//
// 只列当前仓库**真的能跑**的东西。做不到的写 Blocked 并说明缺什么，
// 不允许把"没实现"写成"跳过"然后当作通过。
func integrationCases() []integrationCase {
	gateTests := "internal/devtools"
	piTests := "internal/runtime/pi"
	policyTests := "internal/policy"
	runTests := "internal/run"
	historyTests := "internal/history"

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
		// GOV02：陈旧 revision/锁 hash/伪造报告/缺审查人/oracle 缺失或篡改/串用阶段/重复证据/dirty 工作区均拒绝。
		{ID: "GOV02", Package: gateTests, Run: "TestGateRejectsStaleRevision|TestGateRejectsFakeReportHash|TestGateRejectsMissingReviewer|TestGateRejectsMissingOrTamperedOracle|TestGateRequiresExternalOracleForSideEffectCases|TestGateRejectsDirtyWorktreeEvidence|TestGateRejectsEvidenceForWrongGate|TestGateRejectsDuplicateEvidence"},
		{ID: "GOV03", Package: gateTests, Run: "TestCommandFailureReturnsAssertionCode|TestMissingBinaryReturnsConfigCode|TestVerifyRejectsUnknownSuite|TestVerifyRejectsMissingSuite|TestNativeRunnerRejectsUnknownTarget|TestNativeRunnerRejectsMissingTarget|TestNativeRunnerRejectsWrongHost|TestNativeRunnerRejectsInvalidDisplay|TestLookupNativeCaseDisplayCannotBeSubstituted|TestIntegrationRejectsUnknownGate"},
		// GOV04：未实现/缺失的 runner 必须非零；前端 runner 的「缺失」不能用
		// 「路径错了读不到」冒充（修复前实测就是假绿）。
		{ID: "GOV04", Package: gateTests, Run: "TestFrontendTestRunnerMissingReturnsConfigCode|TestFrontendPackageJSONReachableFromPackageDir|TestIndexCoversEveryMatrixCase|TestIndexEntriesAreActionable|TestOracleKindIsDeclaredOrGrandfathered|TestPlanIndexIsConsistent|TestNativeCaseIDsExistInIndex|TestEvidenceRootLayout|TestIntegrationSelectsCasesByGate|TestGofmtScopeExcludesScratchDirs|TestReviewGate"},

		// ── Pi 定位与探测 ──
		{ID: "PI01", Package: piTests, Run: "TestLocate"},
		{ID: "PI02", Package: piTests, Run: "TestProbeHasNoUserConfigSideEffects|TestProbePs1NotDirectlyExecutable"},
		{ID: "PI03", Package: piTests, Run: "TestCaptureRealPiProtocol|TestProtocolGolden|TestWorkerWithRealPi"},
		{ID: "PI04", Package: piTests, Run: "TestClassifyVersion|TestStandaloneNodeFreeProbe"},
		// F08：发现/兼容状态。两条 case 的观察值都由测试自己写进 ANVIL_OBSERVATION_DIR
		// （PI_DISC01 → candidates.json，PI_DISC02 → rejected.json）。
		{ID: "PI_DISC01", Package: piTests, Run: "TestDiscoverReturnsAllCandidatesAndSelectsDeterministically|TestDiscoverMapsVersionToCompatibilityState|TestDiscoverSkipsBrokenSavedPath",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"candidates.json"}}},
		{ID: "PI_DISC02", Package: piTests, Run: "TestDiscoverRejectsWechatNodeShim|TestDiscoverNeverModifiesExternalFilesOrPath|TestSelectRejectsUnknownOrUnusableCandidate",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"rejected.json"}}},
		// F08-B：契约探测（rpc/settled/mcp/extension/abort/cleanup）与兼容报告。
		// 四条 case 的证据都由测试自己写进 ANVIL_OBSERVATION_DIR：
		//   PI_COMP01/02/04 → contract-report.json（探测结论）；
		//   PI_COMP03 → failure-report.json（具名失败项）。
		// 全部跑在 fakeworker + 临时 agentDir 上：本机没有 1.5.0，不能拿它冒充真实包证据。
		{ID: "PI_COMP01", Package: piTests, Run: "TestContractProbePassesOnCompliantWorker|TestContractProbeRejectsVersionRangeWithoutProbe",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"contract-report.json"}}},
		{ID: "PI_COMP02", Package: piTests, Run: "TestDiscoverUsesContractProbeForReadyCompatible|TestUnknownPatchBecomesCompatibleOnlyAfterProbe",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"contract-report.json"}}},
		{ID: "PI_COMP03", Package: piTests, Run: "TestContractProbeFailsOnMalformedSettled|TestContractProbeFailsOnHungAbort|TestContractProbeFailsOnLeakedGrandchild",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"failure-report.json"}}},
		{ID: "PI_COMP04", Package: piTests, Run: "TestVersionRangeMapsWithoutProbing|TestDiscoverMapsVersionToCompatibilityState",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"contract-report.json"}}},

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

		// ── 记忆（C05，G2） ──
		// MEM01/02/04 是 HOST（纯逻辑 + 真实 SQLite），MEM03/05 是 ALL，跨资源层与绑定层。
		// 观察值来自数据库文件本身（source:"database"）：memory-dump.json 由测试用
		// **第二个 storage.DB 句柄**从磁盘读出来，不是问被测 Service"你记对了吗"。
		{ID: "MEM01", Steps: []integrationStep{{Package: "internal/memory", Run: "TestChineseTrigramScopeAndExpiry|TestTrigramProbeReportsRealFacts"}},
			Observation: &observationSpec{Source: "database", Artifacts: []string{"memory-dump.json"}}},
		{ID: "MEM02", Steps: []integrationStep{{Package: "internal/memory", Run: "TestChineseTrigramScopeAndExpiry|TestScopeDisabledNotInjected|TestDeleteNotInjectedNextRun"}},
			Observation: &observationSpec{Source: "database", Artifacts: []string{"memory-dump.json"}}},
		{ID: "MEM03", Steps: []integrationStep{
			{Package: "internal/app", Run: "TestMemoryBindingsRoundTrip"},
			{Package: "internal/memory", Run: "TestNoImplicitModelOrNetworkCall"},
		},
			// 两个 artifact 缺一不可：库里的真实行 + fixture provider 自己的请求日志。
			Observation: &observationSpec{Source: "database", Artifacts: []string{"memory-dump.json", "http-requests.json"}}},
		{ID: "MEM04", Steps: []integrationStep{{Package: "internal/memory", Run: "TestMemoryContextBounded"}},
			Observation: &observationSpec{Source: "database", Artifacts: []string{"memory-dump.json"}}},
		{ID: "MEM05", Steps: []integrationStep{
			{Package: "internal/memory", Run: "TestDeleteNotInjectedNextRun|TestExportIsReadableJSON"},
			{Package: "internal/app", Run: "TestMemoryDeleteNotInjectedNextRun|TestMemoryScopeToggleAndWorkspaceIsolation"},
		},
			// export.json 是产品导出的真实文档（从磁盘读回），memory-dump.json 是库里的真实行。
			Observation: &observationSpec{Source: "database", Artifacts: []string{"memory-dump.json", "export.json"}}},

		// ── 技能与扩展（C03，G2；RES02 需要真实 Pi） ──
		{ID: "RES01", Steps: []integrationStep{{Package: "internal/resources", Run: "TestImportCopiesWithoutExecutingSource|TestImportRejectsInvalidSkills|TestImportRejectsBrokenUTF8|TestImportRejectsSymlinkEscape|TestImportRejectsDuplicateWithoutOverwrite|TestOverwriteSkillReplacesContentAndKeepsEnabled|TestDeleteSkillRemovesDirAndAllowsReimport|TestSkillCASConflictIsRejected|TestSaveSkillContentGuardsIdentity"}},
			// 观察值来自磁盘上的托管技能目录（source: filesystem）：导入/删除/CAS
			// 到底在磁盘上留下了什么，用 filepath.Walk 从盘上读，不问服务。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"skills-tree.json"}}},
		{ID: "RES02", Steps: []integrationStep{{Package: "internal/resources", Run: "TestDisabledManagedSkillAbsentInNextRun", Env: append([]string{"MYAGENT_PI_WORKER_INTEGRATION=1"}, baselinePathEnv()...)}},
			// 缺真实 Pi 包/Node 时不是 FAIL 也不是 PASS：这条 case 的观察装置就是
			// 真实 Pi，缺件只能 BLOCKED，并保留修复步骤（不静默降级成假的观察值）。
			Blocked: realPiBlockedReason(),
			// 观察值来自 fixture provider（source: http-server）自己收到的请求体，
			// 加真实交给 Pi 的 argv；两者都从磁盘上的 artifact 读回。
			Observation: &observationSpec{Source: "http-server", Artifacts: []string{"provider-request.json"}}},
		{ID: "RES03", Steps: []integrationStep{{Package: "internal/resources", Run: "TestMissingDependencyMarksUnavailable|TestImportRejectsInvalidSkills|TestImportRejectsBrokenUTF8|TestImportRejectsSymlinkEscape"}}},
		{ID: "RES04", Steps: []integrationStep{
			{Package: "internal/resources", Run: "TestSystemExtensionCannotBeDisabled|TestThirdPartyExtensionDefaultsDisabled|TestEnableThirdPartyExtensionAffectsArgs|TestUnknownExtensionRejected"},
			{Package: "internal/app", Run: "TestSkillToggleChangesRuntimeArgs"},
		}},

		// ── 配置隔离 ──
		{ID: "CFG03.isolation", Package: piTests, Run: "TestIsolationArgsCoverEveryProjectChannel|TestHarnessMCPRequiresExplicitEnable|TestIsolationRequiresExistingAbsoluteExtension"},

		// ── 运行语义（F05，G2）──
		//
		// RUN* 在用例索引里都是 oracleKind=external，所以每条都必须声明 Observation，
		// 由 internal/run 的测试把观察值和原始 artifact 写进 $ANVIL_OBSERVATION_DIR。
		// 注意 artifact 的诚实性：events.jsonl 是**产品自己的事件日志**（filesystem），
		// 不是实现边界之外的副作用；真正的外部副作用只有 RUN05 的 counter.txt
		// （由子进程里的 fixture 工具写出）。
		//
		// 复合 case（RUN01/RUN04/RUN06）没有自己的证据，不能列在这里：它们由子 case 各自取证。
		{ID: "RUN01.service", Package: runTests, Run: "TestSettledCompletesRun|TestRetryExhaustedSettlesAsFailedProviderError|TestSettledDuringCompactionDoesNotComplete|TestSettledDuringAutoRetryDoesNotComplete|TestUnknownSettledPayloadIsCompatibilityError|TestSettledWhileWaitingApprovalDoesNotHang",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"events.jsonl"}}},
		{ID: "RUN02", Package: runTests, Run: "TestReplayReturnsOnlyNewEvents|TestDeltaCoalescingDoesNotDropToolResults",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"events.jsonl"}}},
		{ID: "RUN03", Package: runTests, Run: "TestThirdRunQueuesAndQueuedCancelNeverStarts",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"events.jsonl"}}},
		{ID: "RUN04.service", Package: runTests, Run: "TestStopVisibleImmediatelyAndNoEscalationOnCleanClose|TestStopEscalatesForceKillAtFiveSeconds|TestStopCleanupFailedAtTenSecondsBlocksSession|TestToolCallsDeniedAfterStopRequested|TestSubscriberDisconnectDeniesPendingApproval",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"events.jsonl"}}},
		// RUN05：唯一的真外部副作用——计数文件由 fakeworker 子进程追加，不在测试进程里。
		{ID: "RUN05", Package: runTests, Run: "TestInterruptedRunDoesNotReplayUnknownWrite",
			Observation: &observationSpec{Source: "fixture-counter", Artifacts: []string{"counter.txt"}}},
		{ID: "RUN06.service", Package: runTests, Run: "TestBudgetDenies101stToolCall|TestBudgetGateBlocksToolCallThroughPolicyBridge|TestTimeBudgetStopsRun|TestUsageUnavailableIsNotZero|TestUsageReportedFromPiEvents|TestUsageDedupesRepeatedAssistantMessage|TestUsageUnknownPriceOmitsCost",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"events.jsonl"}}},
		// RUN07：历史索引与 transcript 定位。观察值来自数据库文件本身
		// （source:"database"）：index-dump.json 由测试用第二个 storage.DB 句柄
		// 从磁盘读出，不是问被测服务"你记对了吗"。
		{ID: "RUN07", Package: historyTests, Run: "TestRunIndexSurvivesRestart|TestTranscriptReadableAfterRestart|TestIndexStoresLocatorsNotContent|TestSingleWriterPerSession|TestNoPersistentHistoryWorker",
			Observation: &observationSpec{Source: "database", Artifacts: []string{"index-dump.json"}}},
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

// realPiBlockedReason 判断 RES02 的观察装置（真实 Pi 包 + 能跑它的 Node）在不在。
//
// 为什么不是 FAIL：RES02 要观察的就是“真实 Pi 读到了什么”。缺 Pi 包时能测的只剩
// 测试自己的拼装逻辑，那不是这条 case 的范围；按 AGENTS.md 写 BLOCKED 并保留修复步骤。
//
// 只做文件探测，不启动任何进程，也不改 .tools/pi（只读参考包）。
func realPiBlockedReason() string {
	root, err := repoFile("go.mod")
	if err != nil {
		return "无法定位仓库根，无法探测 .tools/pi：" + err.Error()
	}
	base := filepath.Join(filepath.Dir(root), ".tools", "pi")
	matches, _ := filepath.Glob(filepath.Join(base, "*", "node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js"))
	if len(matches) == 0 {
		return "本机没有 .tools/pi 安装件（node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js）；" +
			"修复：按 docs 用应用托管安装流程把 Pi 装到 .tools/pi（不要覆盖外部 Pi），然后重跑 " +
			"scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=RES02"
	}
	if _, err := exec.LookPath("node"); err != nil {
		return "PATH 上找不到 node（真实 Pi bundle 需要 Node ≥ 22.8）；修复：把基线 Node 放到 PATH 首位后重跑 RES02"
	}
	return ""
}

// RunIntegration 是 `verify --suite=integration` 的入口。
// selectedIntegrationCases 决定某 gate 要跑哪些必需 case。
//
// 单独抽出来是因为这里出过一次真 bug：`--gate` 只改了证据目录，case 列表仍写死
// "G0"——命令看起来接受参数，实际跑的却是 G0 的用例。跑一遍才能发现。
// 这条函数被 TestIntegrationSelectsCasesByGate 直接断言，不依赖真实测试执行。
func selectedIntegrationCases(idx *CasesIndex, gate string) ([]string, error) {
	if !contains(KnownGates, gate) {
		return nil, fmt.Errorf("未知 gate: %s（已知: %s）", gate, strings.Join(KnownGates, ","))
	}
	required := ResolveRequiredAtomicCases(idx, gate)
	if len(required) == 0 {
		return nil, fmt.Errorf("gate %s 没有必需 case（索引缺失）", gate)
	}
	return required, nil
}

// evidenceRoot 返回某 (rev, gate, target, display) 的证据目录。
//
// 布局见 v1-gates.md §4：docs/evidence/runs/<rev>/<gate>/<target>/<display?>/<case>（gitignore）。
// 历史证据文档与交接里的复现命令都指向这个根，改根会让它们全部失效。
// gate 与 display 都不能省：同一个 case 可能同时属于两个 gate（SEC05），
// Linux 的 X11/Wayland 也必须各存一份，省掉任一层都会互相覆盖。
func evidenceRoot(rev, gate, target, display string) string {
	parts := []string{"docs", "evidence", "runs", rev, gate, target}
	if display != "" {
		parts = append(parts, display)
	}
	return filepath.Join(parts...)
}

// filterIntegrationCases 把 --case 过滤应用到某 gate 的必需 case 列表。
//
// 只允许缩小到"该 gate 的必需 case"。未知 ID 或别的 gate 的 case 一律判配置
// 错误（退出 2），因为那会写出一份"目录在 G2、内容证明的是 G0"的证据，看起来
// 像通过，实际证明的不是这个 gate。
//
// 纪律：这个过滤只决定本次**产出哪些 case 的证据**，不改变 gate 判定——
// EvaluateGate 永远按完整矩阵评估，所以 --case 不能用来宣称某个 gate 通过。
// 它是开发期的单 case 取证加速器，不是验收口径。
func filterIntegrationCases(selected []string, caseFilter []string) ([]string, error) {
	if len(caseFilter) == 0 {
		return selected, nil
	}
	allowed := make(map[string]bool, len(selected))
	for _, id := range selected {
		allowed[id] = true
	}
	seen := make(map[string]bool, len(caseFilter))
	out := make([]string, 0, len(caseFilter))
	for _, raw := range caseFilter {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("--case 含空的 case id（逗号分隔，例如 --case=MEM01,MEM02）")
		}
		if !allowed[id] {
			return nil, fmt.Errorf("--case %s 不是该 gate 的必需 case（本 gate 可用: %s）",
				id, strings.Join(selected, ","))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// RunIntegration 是 G0 的集成入口（兼容旧调用）。
func RunIntegration(target string) int {
	return RunIntegrationForGate(target, "G0")
}

// RunIntegrationForGate 跑某个 gate 的**全部**必需 case。
//
// 为什么需要 gate 参数：同一个 case 可能同时属于 G0 与 G2（例如 SEC05），
// 而证据里的 gateId 必须与判定它的 gate 一致。证据目录按
// docs/evidence/runs/<rev>/<gate>/<target>/<case> 分（见 v1-gates.md §4），
// 两个 gate 的同名 case 不会互相覆盖、也不会串用报告。
func RunIntegrationForGate(target, gate string) int {
	return RunIntegrationForGateWithCases(target, gate, nil)
}

// RunIntegrationForGateWithCases 只跑指定 case（开发期取证用）。
//
// caseFilter 为空等价于 RunIntegrationForGate。非空时每个 ID 必须是该 gate 的
// 必需 case，否则在执行任何命令之前就返回 2——不允许"跑了一半才发现参数错"。
// 过滤后的退出码只描述这几条 case 自己，不构成 gate 结论。
func RunIntegrationForGateWithCases(target, gate string, caseFilter []string) int {
	return runIntegrationForGate(target, gate, caseFilter, "")
}

// runIntegrationForGate 是集成 runner 的实现。
//
// evidenceDir 为空表示按 (revision, gate, target) 计算规范证据根（生产路径）；
// 非空只给测试注入临时根用，避免测试往仓库的 docs/evidence/runs 里写东西。
func runIntegrationForGate(target, gate string, caseFilter []string, evidenceDir string) int {
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

	// GOV05：证据必须带真实环境身份。探测失败就退出 2，不能写猜测值。
	env, envErr := HostEnvironment(integrationEnvironmentDisplay(target))
	if envErr != nil {
		fmt.Fprintf(os.Stderr, "宿主环境探测失败，不能写证据: %v\n", envErr)
		return 2
	}

	idx, err := LoadCasesIndex("tests/acceptance/cases.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	required, selErr := selectedIntegrationCases(idx, gate)
	if selErr != nil {
		fmt.Fprintln(os.Stderr, selErr.Error())
		return 2
	}
	// --case 只缩小"本次产出哪些 case 的证据"；校验必须在跑任何命令之前完成。
	required, filterErr := filterIntegrationCases(required, caseFilter)
	if filterErr != nil {
		fmt.Fprintln(os.Stderr, filterErr.Error())
		return 2
	}

	table := map[string]integrationCase{}
	for _, c := range integrationCases() {
		table[c.ID] = c
	}
	// 缺 runner 属于配置缺失，但**不阻止**跑已接好的那部分：
	// G2 有 50 条 case，接入是渐进的；否则每加一条都被「其余未接」卡住，
	// 证据永远产不出来。缺失清单照打，最终退出码仍是 2。
	var runnable []string
	var missing []string
	for _, id := range required {
		if _, ok := table[id]; ok {
			runnable = append(runnable, id)
			continue
		}
		missing = append(missing, id)
	}
	if len(runnable) == 0 {
		fmt.Fprintf(os.Stderr, "以下必需 case 没有集成 runner：%s\n", strings.Join(missing, ", "))
		return 2
	}

	rev, dirty := SourceIdentity()
	root := evidenceDir
	if root == "" {
		root = evidenceRoot(rev, gate, target, "")
	}
	fmt.Printf("== verify --suite=integration --target=%s --gate=%s ==\n", target, gate)
	if len(caseFilter) > 0 {
		// 明确提示这不是 gate 结论：过滤运行只产出这几条 case 的证据。
		fmt.Printf("单 case 取证模式（开发用）：只跑 %s；这不是 gate 通过结论\n", strings.Join(required, ","))
	}
	fmt.Printf("证据根: %s\n", root)
	fmt.Printf("宿主环境: os=%s version=%s build=%s arch=%s display=%s\n",
		env.OS, env.OSVersion, orNone(env.OSBuild), env.Arch, orNone(env.Display))
	warnIfBelowMinimum(env, target)
	if dirty {
		// 证据照写（可诊断），但必须带 dirty 标记：门禁会拒绝它。
		// 不做「把改动藏起来当作干净 revision」这种假归属。
		fmt.Println("[警告] 工作区不干净：本次证据带 worktreeDirty=true，gate 会判死，不能用作正式结论。")
	}
	blocks := 0
	fails := 0
	for _, id := range runnable {
		c := table[id]
		res := runIntegrationCase(c, gate, target, root, env)
		if res.blocked {
			blocks++
		} else if !res.passed {
			fails++
		}
	}

	fmt.Printf("汇总: 可跑 %d / 必需 %d 条，失败 %d，阻塞 %d\n", len(runnable), len(required), fails, blocks)
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "以下必需 case 没有集成 runner：%s\n", strings.Join(missing, ", "))
		fmt.Fprintln(os.Stderr, "已跑的部分不等于该 gate 通过。")
	}

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
	if blocks > 0 || len(missing) > 0 {
		// 阻塞/缺 runner 不等于通过：按配置类返回 2。
		return 2
	}
	return 0
}

type caseOutcome struct {
	passed  bool
	blocked bool
}

// integrationEnvironmentDisplay 返回集成证据要记录的真实显示会话。
//
// 集成证据的 display 必需位是「不区分会话」（Windows/macOS 归一化为 desktop；
// Linux 为空），但 environment.display 记录的是机器真实会话：Linux 优先读
// XDG_SESSION_TYPE（x11/wayland），读不到就留空。两者用途不同，不违背归一化。
func integrationEnvironmentDisplay(target string) string {
	if !strings.HasPrefix(target, "linux-") {
		return DisplayDesktop
	}
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE"))); v {
	case DisplayX11, DisplayWayland:
		return v
	}
	return ""
}

// runIntegrationCase 执行一条 case 并写证据。
func runIntegrationCase(c integrationCase, gate, target, root string, environment contracts.Environment) caseOutcome {
	dir := filepath.Join(root, c.ID)
	_ = os.MkdirAll(dir, 0o755)
	// 子进程的 cwd 不是仓库根，而是它自己的包目录（go test 会把 cwd 设成包目录）。
	// 所以交给 case 的观察目录必须是绝对路径：给相对路径时测试会写进
	// internal/run/docs/...，既找不到证据，又把仓库弄脏（runner 自己会因此判配置失败）。
	absDir, absErr := filepath.Abs(dir)
	if absErr != nil {
		absDir = dir
	}

	started := time.Now().UTC()
	ev := contracts.GateEvidence{
		SchemaVersion:    1,
		GateID:           gate,
		TaskID:           taskForCase(c.ID),
		SourceRevision:   GetSourceRevision(),
		WorktreeDirty:    worktreeDirty(),
		VersionsLockHash: versionsHash(),
		TargetID:         target,
		Environment:      environment,
		StartedAt:        started.Format(time.RFC3339),
		Reviewer: contracts.Reviewer{
			Name:       "devtools-integration-runner",
			Machine:    hostname(),
			ReviewedAt: started.Format(time.RFC3339),
			Note:       "机器执行证据；结构完整不等于人工审查通过",
		},
	}

	cr := contracts.CaseResult{
		ID:         c.ID,
		Expected:   "命令退出码 0，且输出不含失败断言",
		OraclePath: "oracle.json",
		StartedAt:  started.Format(time.RFC3339),
	}

	var cmdline, stdout, stderr string
	exit := 0
	// external-observation 声明：source 不合法或 case/step 互相矛盾时直接判配置错误，不执行命令。
	obs, obsErr := resolveObservation(c)
	if obs != nil {
		// 观察文件必须由本次执行重新产出：先清掉上一轮的残留，
		// 否则「这次什么都没写」会被旧文件掩盖成 PASS。
		clearObservationArtifacts(dir, obs)
	}
	caseEnv := append(append([]string{}, c.Env...), observationEnv(obs, absDir)...)

	switch {
	case obsErr != nil:
		cr.Status = contracts.StatusFail
		cr.Actual = obsErr.Error()
		cmdline = "(未执行：observation 声明冲突)"
		exit = 2
	case c.Blocked != "":
		cr.Status = contracts.StatusBlocked
		cr.Actual = c.Blocked
		ev.Limitations = c.Blocked
		cmdline = "(未执行：环境或实现缺失)"
	case len(c.External) > 0:
		cmdline = strings.Join(c.External, " ")
		exit, stdout, stderr, _ = runWithEnv(caseEnv, c.External[0], c.External[1:]...)
	case c.Package != "":
		args := []string{"test", "./" + c.Package, "-run", c.Run, "-count=1", "-v"}
		cmdline = "go " + strings.Join(args, " ")
		exit, stdout, stderr, _ = runWithEnv(caseEnv, "go", args...)
	case len(c.Steps) > 0:
		var cmdlines, stdouts, stderrs []string
		for _, step := range c.Steps {
			code, out, errOut := 0, "", ""
			stepEnv := append(append([]string{}, step.Env...), observationEnv(obs, absDir)...)
			if len(step.External) > 0 {
				cmdlines = append(cmdlines, strings.Join(step.External, " "))
				code, out, errOut, _ = runWithEnv(stepEnv, step.External[0], step.External[1:]...)
			} else {
				args := []string{"test", "./" + step.Package, "-run", step.Run, "-count=1", "-v"}
				cmdlines = append(cmdlines, "go "+strings.Join(args, " "))
				code, out, errOut, _ = runWithEnv(stepEnv, "go", args...)
			}
			exit = code
			stdouts = append(stdouts, "### $ "+cmdlines[len(cmdlines)-1]+"\n"+out)
			stderrs = append(stderrs, "### $ "+cmdlines[len(cmdlines)-1]+"\n"+errOut)
			if code != 0 {
				break
			}
		}
		cmdline = strings.Join(cmdlines, "\n")
		stdout = strings.Join(stdouts, "\n")
		stderr = strings.Join(stderrs, "\n")
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

	if cr.Status == contracts.StatusPass && obs != nil {
		// external-observation：观察值必须由 case 自己写出来，runner 只负责封装。
		// 失败时不写任何 oracle —— 绝不能退回 test-process 自证。
		if err := writeExternalObservationOracle(dir, obs); err != nil {
			cr.Status = contracts.StatusFail
			cr.Actual = err.Error()
		}
	}

	ev.Command = cmdline
	ev.ExitCode = exit
	ev.CaseResults = []contracts.CaseResult{cr}
	ev.EndedAt = cr.EndedAt

	// oracle 已由 external 路径写好时不能再覆盖成 test-process。
	var oracleFn func() error
	if obs == nil {
		oracleFn = func() error {
			return writeProcessOracle(dir, cmdline, ev.ExitCode, []byte(stdout), []byte(stderr))
		}
	}
	writeEvidenceDirWithOracle(dir, &ev, cmdline, stdout, stderr, oracleFn)

	fmt.Printf("  [%s] %s — %s\n", cr.Status, c.ID, firstLine(cr.Actual))
	return caseOutcome{passed: cr.Status == contracts.StatusPass, blocked: cr.Status == contracts.StatusBlocked}
}

// writeEvidence 落盘证据并回填报告哈希。
//
// 顺序不能换：哈希覆盖 result.json 自身（清空 reportHash 后）+ 三个原始文件，
// 所以必须先把原始文件写全、先写一版 result.json，再算哈希回填。
func writeEvidenceDir(dir string, ev *contracts.GateEvidence, cmdline, stdout, stderr string) {
	writeEvidenceDirWithOracle(dir, ev, cmdline, stdout, stderr, func() error {
		return writeProcessOracle(dir, cmdline, ev.ExitCode, []byte(stdout), []byte(stderr))
	})
}

// writeEvidenceDirWithOracle 同 writeEvidenceDir，但 oracle 由调用方决定。
//
// oracleFn 为 nil 表示 oracle 已经被写好了（external-observation 路径）：这里
// 不能再用 test-process 覆盖它，否则声明了 external 的 case 会被自己的 runner
// 悄悄降级成自证。
func writeEvidenceDirWithOracle(dir string, ev *contracts.GateEvidence, cmdline, stdout, stderr string, oracleFn func() error) {
	_ = os.WriteFile(filepath.Join(dir, "command.txt"), []byte(cmdline+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(stdout), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(stderr), 0o644)
	if oracleFn != nil {
		_ = oracleFn()
	}

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

// resolveObservation 解析 case 与 step 上的 observation 声明。
//
// 规则（宁可否决，不靠猜）：
//   - 只有 case 级：用它；
//   - 只有 step 级：最多一个 step 能声明，用它；
//   - 两级都有：必须完全一致，否则判配置错误（同一 case 两份观察口径会互相覆盖）；
//   - 都没有：返回 nil，走原有的 test-process oracle。
func resolveObservation(c integrationCase) (*observationSpec, error) {
	var fromStep *observationSpec
	for _, step := range c.Steps {
		if step.Observation == nil {
			continue
		}
		if fromStep != nil {
			return nil, fmt.Errorf("case %s 有多个 step 声明了 observation，无法确定唯一的观察口径", c.ID)
		}
		fromStep = step.Observation
	}
	switch {
	case c.Observation == nil:
		return fromStep, nil
	case fromStep == nil:
		return c.Observation, nil
	case sameObservation(c.Observation, fromStep):
		return c.Observation, nil
	}
	return nil, fmt.Errorf("case %s 在 case 级与 step 级都声明了 observation 且不一致（source %q/%q）",
		c.ID, c.Observation.Source, fromStep.Source)
}

func sameObservation(a, b *observationSpec) bool {
	if a.Source != b.Source || len(a.Artifacts) != len(b.Artifacts) {
		return false
	}
	for i := range a.Artifacts {
		if a.Artifacts[i] != b.Artifacts[i] {
			return false
		}
	}
	return true
}

// observationEnv 把观察目录交给子进程。声明了 observation 的 case 必须拿到它，
// 否则测试无处写观察值，case 会以“未产出 observation.json”失败——这正是想要的：
// 宁可判死，也不要 runner 替 case 编观察值。
func observationEnv(obs *observationSpec, dir string) []string {
	if obs == nil {
		return nil
	}
	return []string{"ANVIL_OBSERVATION_DIR=" + dir}
}

// clearObservationArtifacts 清掉上一轮残留的观察文件。
// 证据目录按 revision 复用，不清会让“这次什么都没写”被旧文件掩盖成 PASS。
func clearObservationArtifacts(dir string, obs *observationSpec) {
	if obs == nil {
		return
	}
	_ = os.Remove(filepath.Join(dir, "observation.json"))
	for _, artifact := range obs.Artifacts {
		clean := filepath.Clean(artifact)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, clean))
	}
}

// writeExternalObservationOracle 把 case 自己写出的观察值封装成 external-observation oracle。
//
// 为什么不让 runner 自己构造 observation：observation.json 由 case 的测试写出，
// 里面是它真正观察到的产物值（计数行数、状态、事件条数）。runner 只做封装和完整性
// 校验；一旦替 case 生成内容，“外部观察”就退化成了 runner 自证。
func writeExternalObservationOracle(dir string, obs *observationSpec) error {
	if !externalOracleSources[obs.Source] {
		return fmt.Errorf("observation source %q 不合法（允许值见 externalOracleSources）", obs.Source)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "observation.json"))
	if err != nil {
		return fmt.Errorf("case 未产出 observation.json（声明 source=%s）：外部观察值必须由 case 自己写出，不能用测试进程自证: %v", obs.Source, err)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("case 的 observation.json 不是合法 JSON: %v", err)
	}
	if len(obs.Artifacts) == 0 {
		return fmt.Errorf("observation 未声明任何 artifact，无法核对副作用是否真的发生")
	}
	for _, artifact := range obs.Artifacts {
		if err := validateEvidenceArtifact(dir, artifact); err != nil {
			return fmt.Errorf("observation artifact %q: %v", artifact, err)
		}
	}
	oracle := externalOracle{
		SchemaVersion: 1,
		Kind:          "external-observation",
		Source:        obs.Source,
		Observation:   json.RawMessage(raw),
		Artifacts:     obs.Artifacts,
	}
	data, err := json.MarshalIndent(oracle, "", "  ")
	if err != nil {
		return fmt.Errorf("封装 external oracle 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oracle.json"), data, 0o644); err != nil {
		return fmt.Errorf("写 external oracle 失败: %v", err)
	}
	return nil
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
	case strings.HasPrefix(id, "PI_DISC"), strings.HasPrefix(id, "PI_COMP"):
		return "F08"
	case strings.HasPrefix(id, "PI_INS"), strings.HasPrefix(id, "PI_UPG"):
		return "F06"
	case strings.HasPrefix(id, "PI_PKG"):
		return "F06/X03"
	case strings.HasPrefix(id, "PI"):
		return "F02"
	case strings.HasPrefix(id, "RPC"):
		return "F03"
	case strings.HasPrefix(id, "SEC"):
		return "F04"
	case strings.HasPrefix(id, "CFG"):
		return "F07"
	case strings.HasPrefix(id, "CONN"):
		return "C08"
	case strings.HasPrefix(id, "RUN"):
		// RUN01–RUN07 属于运行语义/F05；任务书里就是按 F05-A..F05-D 切的。
		return "F05"
	case strings.HasPrefix(id, "SCH"):
		return "C09"
	case strings.HasPrefix(id, "UPD"):
		return "F09/X04"
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
