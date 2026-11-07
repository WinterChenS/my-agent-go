package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// ---- 测试辅助 ----

func testIndexG0(t *testing.T) *CasesIndex {
	t.Helper()
	return &CasesIndex{
		SchemaVersion: 1,
		Cases: []CaseEntry{
			// scope=HOST：任一已知目标的有效证据即可。
			// 目标维度本身由 TestGate*Scope* 系列单独覆盖。
			{ID: "GOV01", Owner: "F01", Required: true, Scope: "HOST", Gates: []string{"G0"}},
			{ID: "GOV02", Owner: "F01", Required: true, Scope: "HOST", Gates: []string{"G0"}},
			{ID: "GOV03", Owner: "F01", Required: true, Scope: "HOST", Gates: []string{"G0"}},
			{ID: "GOV04", Owner: "F01", Required: true, Scope: "HOST", Gates: []string{"G0"}},
		},
	}
}

func testBaseline() GateBaseline {
	return GateBaseline{SourceRevision: "rev1", VersionsLockHash: "lock1"}
}

// writeEvidence 写出一个“否则有效”的证据，并可选地对 GateEvidence 做破坏性修改
// （用于模拟陈旧 revision / 未知 target / 缺审查人 / 空或全 skip / 伪造哈希）。
func writeEvidence(t *testing.T, root, rev, gate, target, caseID, lockHash string, mutate func(*contracts.GateEvidence)) string {
	t.Helper()
	return writeEvidenceAt(t, filepath.Join(root, rev, gate, target, caseID), rev, gate, target, caseID, lockHash, mutate)
}

// platformMinimumEnvironment 返回某目标「恰好等于最低要求」的环境身份（GOV05）。
//
// 测试默认就用最低线：这样任何拒绝都来自明确的 GOV05 判定，而不是
// 「默认值刚好不满足环境检查」。
func platformMinimumEnvironment(t *testing.T, target string) contracts.Environment {
	t.Helper()
	plats, err := LoadPlatformsFromRepo()
	if err != nil {
		t.Fatalf("加载 build/platforms.json: %v", err)
	}
	tm, ok := plats.Target(target)
	if !ok {
		// 未知 target 的负例（例如 "mars"）只需要证据能被写出来，
		// 环境身份由 validateEnvironment 另行判死；这里不能 Fatalf。
		return contracts.Environment{}
	}
	display := ""
	if !strings.HasPrefix(target, "linux-") {
		display = DisplayDesktop
	}
	return contracts.Environment{
		OS:        tm.GOOS,
		OSVersion: tm.MinOSVersion,
		OSBuild:   strconv.FormatInt(int64(tm.MinOSBuild), 10),
		Arch:      tm.GOARCH,
		Display:   display,
	}
}

// writeEvidenceAt 在指定目录写证据。重复证据测试需要写到非规范路径。
func writeEvidenceAt(t *testing.T, dir, rev, gate, target, caseID, lockHash string, mutate func(*contracts.GateEvidence)) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	minimum := platformMinimumEnvironment(t, target)
	ev := contracts.GateEvidence{
		SchemaVersion:    1,
		GateID:           gate,
		TaskID:           "F01",
		SourceRevision:   rev,
		VersionsLockHash: lockHash,
		TargetID:         target,
		Environment:      minimum,
		StartedAt:        "2026-11-03T00:00:00Z",
		EndedAt:          "2026-11-03T00:01:00Z",
		Command:          "echo ok",
		ExitCode:         0,
		CaseResults: []contracts.CaseResult{
			{ID: caseID, Status: contracts.StatusPass, Expected: "0", Actual: "0", OraclePath: "oracle.json", StartedAt: "s", EndedAt: "e"},
		},
		Reviewer: contracts.Reviewer{Name: "ci", Machine: "host-x", ReviewedAt: "2026-11-03T00:02:00Z"},
	}
	if mutate != nil {
		mutate(&ev)
	}
	// 环境身份未被子测试改写时，按证据自己的 display 归一化补全：
	// 否则「Linux x11 证据」会因为在 environment.display 上不一致而被额外拒绝，
	// 掩盖掉本测试真正要断言的那条判定。
	if ev.Environment == minimum {
		ev.Environment.Display = normalizeDisplay(target, ev.Display)
	}
	b, _ := json.MarshalIndent(ev, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644); err != nil {
		t.Fatalf("write result.json: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "command.txt"), []byte(ev.Command), 0o644)
	stdout := []byte("ok")
	stderr := []byte("")
	_ = os.WriteFile(filepath.Join(dir, "stdout.log"), stdout, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stderr.log"), stderr, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "environment.json"), []byte("{}"), 0o644)
	if err := writeProcessOracle(dir, ev.Command, ev.ExitCode, stdout, stderr); err != nil {
		t.Fatalf("write oracle: %v", err)
	}
	// 重新计算并写入报告哈希（伪造哈希测试在返回后另行覆盖字段）。
	ev.ReportHash = ComputeEvidenceHash(dir)
	b2, _ := json.MarshalIndent(ev, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "result.json"), b2, 0o644); err != nil {
		t.Fatalf("rewrite result.json: %v", err)
	}
	return dir
}

// ---- 门禁必需负例 ----

func TestGateRejectsMissingRequiredCase(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	// 写出除 GOV04 外的所有必需 case（否则有效）。
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		if id == "GOV04" {
			continue
		}
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", id, base.VersionsLockHash, nil)
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("期望判死缺 case，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
}

func TestGateRejectsStaleRevision(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	// 证据使用了旧 revision "old"，与基线 "rev1" 不一致。
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		writeEvidence(t, root, "old", "G0", "win-x64", id, base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.SourceRevision = "old"
		})
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("期望判死旧 revision，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
}

func TestGateRejectsEmptyOrSkippedNativeSuite(t *testing.T) {
	idx := testIndexG0(t)
	base := testBaseline()

	// 空结果列表
	rootEmpty := t.TempDir()
	writeEvidence(t, rootEmpty, base.SourceRevision, "G0", "win-x64", "GOV01", base.VersionsLockHash, func(e *contracts.GateEvidence) {
		e.CaseResults = []contracts.CaseResult{}
	})
	if rep, err := EvaluateGate(idx, "G0", rootEmpty, base); err != nil || rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("空 suite 应判死，实际 passed=%v exitCode=%d err=%v", rep.Passed, rep.ExitCode, err)
	}

	// 全部 SKIP
	rootSkip := t.TempDir()
	writeEvidence(t, rootSkip, base.SourceRevision, "G0", "win-x64", "GOV01", base.VersionsLockHash, func(e *contracts.GateEvidence) {
		e.CaseResults = []contracts.CaseResult{
			{ID: "GOV01", Status: contracts.StatusSkip, Expected: "-", Actual: "-", StartedAt: "s", EndedAt: "e"},
		}
	})
	if rep, err := EvaluateGate(idx, "G0", rootSkip, base); err != nil || rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("全 SKIP suite 应判死，实际 passed=%v exitCode=%d err=%v", rep.Passed, rep.ExitCode, err)
	}
}

func TestGateRejectsUnknownGate(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	rep, err := EvaluateGate(idx, "G9", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.ExitCode != 2 {
		t.Fatalf("未知 gate 应返回 2，实际 %d (reasons=%v)", rep.ExitCode, rep.Reasons)
	}
}

func TestGateRejectsUnknownTarget(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		writeEvidence(t, root, base.SourceRevision, "G0", "mars", id, base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.TargetID = "mars"
		})
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.ExitCode != 2 {
		t.Fatalf("未知 target 应返回 2，实际 %d (reasons=%v)", rep.ExitCode, rep.Reasons)
	}
}

func TestGateRejectsFakeReportHash(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	// 写出证据后篡改 reportHash 字段。
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		dir := writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", id, base.VersionsLockHash, nil)
		// 覆盖 result.json 的 reportHash 为伪造值。
		raw, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		var ev contracts.GateEvidence
		_ = json.Unmarshal(raw, &ev)
		ev.ReportHash = "sha256:FAKENOTMATCH"
		b, _ := json.MarshalIndent(ev, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644)
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("伪造报告哈希应判死，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
}

func TestGateRejectsMissingReviewer(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", id, base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.Reviewer = contracts.Reviewer{}
		})
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("缺审查人/机器信息应判死，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
}

// ---- 命令编排负例 ----

func TestCommandFailureReturnsAssertionCode(t *testing.T) {
	// 命令故意退出 1，必须原样返回 1（断言失败），不得被日志成功覆盖。
	// 用各平台自带的 shell 构造：写死 cmd 会让 Unix runner 变成"二进制缺失 → 2"的假失败。
	name, args := "cmd", []string{"/c", "exit 1"}
	if runtime.GOOS != "windows" {
		name, args = "sh", []string{"-c", "exit 1"}
	}
	code, _, _, _ := RunExternal(name, args...)
	if code != 1 {
		t.Fatalf("命令失败应返回 1，实际 %d", code)
	}
}

func TestMissingBinaryReturnsConfigCode(t *testing.T) {
	// 不存在的二进制：视为 runner/配置缺失，返回 2。
	code, _, _, _ := RunExternal("this-binary-must-not-exist-xyz")
	if code != 2 {
		t.Fatalf("缺失 runner 应返回 2，实际 %d", code)
	}
}

func TestVerifyRejectsUnknownSuite(t *testing.T) {
	if code := RunVerify("bogus", "", "", ""); code != 2 {
		t.Fatalf("未知 suite 应返回 2，实际 %d", code)
	}
}

func TestVerifyRejectsMissingSuite(t *testing.T) {
	if code := RunVerify("", "", "", ""); code != 2 {
		t.Fatalf("缺 suite 应返回 2，实际 %d", code)
	}
}

func TestFrontendTestRunnerMissingReturnsConfigCode(t *testing.T) {
	// 用显式 package.json 路径，而不是依赖 "frontend/package.json" 这个相对路径：
	// go test 的 cwd 是包目录，相对路径读不到会返回 2——那是「路径错了」，
	// 不是「runner 缺失」，两者都会让这个断言假绿（修复前实测就是假绿）。
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package.json")
	if err := os.WriteFile(pkg, []byte(`{"scripts":{"build":"vite build"}}`), 0o644); err != nil {
		t.Fatalf("写 fixture package.json: %v", err)
	}
	if code, msg, _ := frontendScript(pkg, "test"); code != 2 {
		t.Fatalf("缺 test 脚本应返回 2，实际 %d；msg=%s", code, msg)
	}
	if code, _, _ := frontendScript(pkg, "typecheck"); code != 2 {
		t.Fatalf("缺 typecheck 脚本应返回 2，实际 %d", code)
	}

	// 反向对照：脚本存在时不能返回 2，否则上面那条可能只是「永远返回 2」。
	if err := os.WriteFile(pkg, []byte(`{"scripts":{"test":"vitest run"}}`), 0o644); err != nil {
		t.Fatalf("写 fixture package.json: %v", err)
	}
	if code, msg, _ := frontendScript(pkg, "test"); code != 0 {
		t.Fatalf("有 test 脚本时不应返回 2，实际 %d；msg=%s", code, msg)
	}
}

// TestFrontendPackageJSONReachableFromPackageDir 钉住 repoFile 的仓库根解析：
// 测试运行在包目录里，仍必须能找到真实的 frontend/package.json。
// 这条测试失败意味着 verify --suite=static/unit 在子目录调用时也会读错文件。
func TestFrontendPackageJSONReachableFromPackageDir(t *testing.T) {
	pkg, err := repoFile("frontend/package.json")
	if err != nil {
		t.Fatalf("repoFile: %v", err)
	}
	for _, name := range []string{"typecheck", "test"} {
		if code, msg, _ := frontendScript(pkg, name); code != 0 {
			t.Fatalf("真实 frontend/package.json 缺 %s 脚本: %s", name, msg)
		}
	}
}

// ---- 目标 × 显示维度（门禁聚合回归） ----

func testIndexOne(t *testing.T, c CaseEntry) *CasesIndex {
	t.Helper()
	return &CasesIndex{SchemaVersion: 1, Cases: []CaseEntry{c}}
}

// rehashEvidence 在测试里修改证据文件后重算 reportHash，模拟“篡改后再伪造自洽”。
func rehashEvidence(t *testing.T, dir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("读 result.json: %v", err)
	}
	var ev contracts.GateEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("解析 result.json: %v", err)
	}
	ev.ReportHash = ""
	_ = os.WriteFile(filepath.Join(dir, "result.json"), mustJSON(t, ev), 0o644)
	ev.ReportHash = ComputeEvidenceHash(dir)
	_ = os.WriteFile(filepath.Join(dir, "result.json"), mustJSON(t, ev), 0o644)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func writeExternalOracle(t *testing.T, dir, source string, observation any, artifacts []string) {
	t.Helper()
	oracle := externalOracle{
		SchemaVersion: 1,
		Kind:          "external-observation",
		Source:        source,
		Observation:   mustJSON(t, observation),
		Artifacts:     artifacts,
	}
	if err := os.WriteFile(filepath.Join(dir, "oracle.json"), mustJSON(t, oracle), 0o644); err != nil {
		t.Fatalf("写 external oracle: %v", err)
	}
}

// TestGateRequiresExternalOracleForSideEffectCases：副作用 case 不能用自己的输出自证。
func TestGateRequiresExternalOracleForSideEffectCases(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{
		ID: "CONN02", Owner: "C08", Required: true, Scope: "HOST",
		Gates: []string{"G2"}, OracleKind: "external",
	})
	base := testBaseline()

	t.Run("test-process-rejected", func(t *testing.T) {
		root := t.TempDir()
		writeEvidence(t, root, base.SourceRevision, "G2", "win-x64", "CONN02", base.VersionsLockHash, nil)
		rep, err := EvaluateGate(idx, "G2", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "实现边界之外") {
			t.Fatalf("副作用 case 用 test-process 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("missing-artifact-rejected", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G2", "win-x64", "CONN02", base.VersionsLockHash, nil)
		writeExternalOracle(t, dir, "fixture-counter", map[string]int{"sends": 1}, []string{"counter.json"})
		rehashEvidence(t, dir)
		rep, err := EvaluateGate(idx, "G2", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "artifact") {
			t.Fatalf("external oracle 缺 artifact 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("unsupported-source-rejected", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G2", "win-x64", "CONN02", base.VersionsLockHash, nil)
		if err := os.WriteFile(filepath.Join(dir, "counter.json"), []byte(`{"sends":1}`), 0o644); err != nil {
			t.Fatalf("写 counter: %v", err)
		}
		writeExternalOracle(t, dir, "vibes", map[string]int{"sends": 1}, []string{"counter.json"})
		rehashEvidence(t, dir)
		rep, err := EvaluateGate(idx, "G2", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "source") {
			t.Fatalf("非法 source 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("external-ok", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G2", "win-x64", "CONN02", base.VersionsLockHash, nil)
		if err := os.WriteFile(filepath.Join(dir, "counter.json"), []byte(`{"sends":1}`), 0o644); err != nil {
			t.Fatalf("写 counter: %v", err)
		}
		writeExternalOracle(t, dir, "fixture-counter", map[string]int{"sends": 1}, []string{"counter.json"})
		rehashEvidence(t, dir)
		rep, err := EvaluateGate(idx, "G2", root, base)
		if err != nil || !rep.Passed || rep.ExitCode != 0 {
			t.Fatalf("合法的 external oracle 应通过（否则上面的判死可能只是永远判死），passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})
}

func TestGateAcceptsAllRequiredEvidence(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "HOST", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep.Passed || rep.ExitCode != 0 {
		t.Fatalf("完整证据应通过，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
}

// TestGateAcceptsAllScopeOnActiveTargets：scope=ALL 展开为本轮**验收目标**（ADR 0005 下是 win-x64）。
//
// 停用目标不能既不算缺失也不算 deferred：它们必须出现在报告里（见
// TestGateReportsDeferredTargetsInsteadOfDroppingThem），否则停用就悄悄缩小了验收面。
func TestGateAcceptsAllScopeOnActiveTargets(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "SEC06", Owner: "F04", Required: true, Scope: "ALL", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	for _, target := range activeTargets() {
		writeEvidence(t, root, base.SourceRevision, "G0", target, "SEC06", base.VersionsLockHash, nil)
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep.Passed || rep.ExitCode != 0 {
		t.Fatalf("验收目标齐全应通过，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
	if len(rep.PerRequirement) != len(activeTargets()) {
		t.Fatalf("scope=ALL 应展开为 %d 条必需证据位（active=%v），实际 %d",
			len(activeTargets()), activeTargets(), len(rep.PerRequirement))
	}
	if len(rep.Deferred) == 0 {
		t.Fatalf("scope=ALL 里停用目标必须记进 Deferred（ADR 0005），实际为空")
	}
}

// TestGateRejectsAllScopeMissingOneTarget 钉住交接文档指出的聚合缺陷：
// 同一 case 在某个目标上的 PASS 不能代表整个 ALL。
func TestGateRejectsAllScopeMissingOneTarget(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "SEC06", Owner: "F04", Required: true, Scope: "ALL", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	active := activeTargets()
	if len(active) == 0 {
		t.Fatal("没有验收目标，测试前提不成立")
	}
	missing := active[0]
	for _, target := range active {
		if target == missing {
			continue
		}
		writeEvidence(t, root, base.SourceRevision, "G0", target, "SEC06", base.VersionsLockHash, nil)
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("缺 %s 应判死，实际 passed=%v exitCode=%d", missing, rep.Passed, rep.ExitCode)
	}
	if !reasonMentions(rep, missing) {
		t.Fatalf("报告应明确指出缺 %s，实际 reasons=%v", missing, rep.Reasons)
	}
}

// TestDeferredLinuxScopeIsReported：Linux 目标在 ADR 0005 下停用，LNX scope 的 case
// 不能因此"凭空通过"——它必须出现在 Deferred 里，且不能产生必需证据位。
//
// 保留原来的 X11/Wayland 判定逻辑（双会话不能合并成一份）作为**将来恢复 Linux 验收时**
// 仍然有效的测试：这里直接按 X11+Wayland 写入两份证据，然后断言在当前矩阵下它们
// 不被当成必需位（否则本机会一直要求 linux 证据）。
func TestDeferredLinuxScopeIsReported(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "BR03", Owner: "B03", Required: true, Scope: "LNX", Gates: []string{"G1"}})
	root := t.TempDir()
	base := testBaseline()
	// 显示维度写在路径里（<rev>/<gate>/<target>/<display>/<case>）：
	// 同一 target 的 X11 与 Wayland 必须能在证据树里共存两份，不能互相覆盖。
	writeDisplayed := func(target, display string) {
		t.Helper()
		dir := filepath.Join(root, base.SourceRevision, "G1", target, display, "BR03")
		writeEvidenceAt(t, dir, base.SourceRevision, "G1", target, "BR03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.Display = display
		})
	}
	for _, target := range []string{"linux-x64", "linux-arm64"} {
		writeDisplayed(target, "x11")
	}
	for _, target := range []string{"linux-x64", "linux-arm64"} {
		writeDisplayed(target, "wayland")
	}
	rep, err := EvaluateGate(idx, "G1", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if len(activeTargets()) != 0 && contains(activeTargets(), "linux-x64") {
		// 将来恢复 Linux 验收时，这条分支才是主断言。
		if !rep.Passed || rep.ExitCode != 0 {
			t.Fatalf("X11+Wayland 齐全应通过，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
		}
		return
	}
	// 全部必需位都停用时 gate 不能判通过：那会成为"停用即绿"。
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("全停用的 gate 必须判非通过（不能把停用当通过），实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
	if !strings.Contains(strings.Join(rep.Reasons, " "), "停用目标") {
		t.Fatalf("拒绝原因必须说明是停用目标导致的，实际 reasons=%v", rep.Reasons)
	}
	if len(rep.PerRequirement) != 0 {
		t.Fatalf("停用目标不应产生必需证据位，实际 %d 条", len(rep.PerRequirement))
	}
	found := false
	for _, d := range rep.Deferred {
		if strings.HasPrefix(d, "BR03@linux-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Linux 必需位必须出现在 Deferred 里，实际 %v", rep.Deferred)
	}
}

// TestGateRejectsSameCaseOneTargetFails：两个目标一成一败不能互相覆盖。
func TestGateRejectsSameCaseOneTargetFails(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "BR01", Owner: "B01", Required: true, Scope: "Windows两架构", Gates: []string{"G1"}})
	root := t.TempDir()
	base := testBaseline()
	writeEvidence(t, root, base.SourceRevision, "G1", "win-x64", "BR01", base.VersionsLockHash, nil)
	writeEvidence(t, root, base.SourceRevision, "G1", "win-arm64", "BR01", base.VersionsLockHash, func(e *contracts.GateEvidence) {
		e.CaseResults = []contracts.CaseResult{
			{ID: "BR01", Status: contracts.StatusFail, Expected: "x", Actual: "y", StartedAt: "s", EndedAt: "e"},
		}
	})
	rep, err := EvaluateGate(idx, "G1", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("win-arm64 失败必须判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
}

// TestGateRejectsMissingOrTamperedOracle：PASS 必须绑定目录内且被哈希覆盖的外部判据。
func TestGateRejectsMissingOrTamperedOracle(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "HOST", Gates: []string{"G0"}})
	base := testBaseline()

	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.CaseResults[0].OraclePath = "missing-oracle.json"
		})
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "oraclePath") {
			t.Fatalf("缺 oracle 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("traversal", func(t *testing.T) {
		root := t.TempDir()
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.CaseResults[0].OraclePath = "../outside.json"
		})
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "相对路径") {
			t.Fatalf("越界 oracle 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("self-report", func(t *testing.T) {
		root := t.TempDir()
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.CaseResults[0].OraclePath = "result.json"
		})
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "自报结果") {
			t.Fatalf("result.json 不能作为 oracle，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
		if err := os.WriteFile(filepath.Join(dir, "oracle.json"), nil, 0o644); err != nil {
			t.Fatalf("清空 oracle: %v", err)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		var ev contracts.GateEvidence
		_ = json.Unmarshal(raw, &ev)
		ev.ReportHash = ComputeEvidenceHash(dir)
		updated, _ := json.MarshalIndent(ev, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "result.json"), updated, 0o644)
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "空文件") {
			t.Fatalf("空 oracle 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{"outside":true}`), 0o644); err != nil {
			t.Fatalf("写外部 oracle: %v", err)
		}
		link := filepath.Join(dir, "linked-oracle.json")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("当前环境不能创建 symlink: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
		if err != nil {
			t.Fatalf("读 result: %v", err)
		}
		var ev contracts.GateEvidence
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("解析 result: %v", err)
		}
		ev.CaseResults[0].OraclePath = "linked-oracle.json"
		ev.ReportHash = ""
		updated, _ := json.MarshalIndent(ev, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "result.json"), updated, 0o644); err != nil {
			t.Fatalf("改 result: %v", err)
		}
		ev.ReportHash = ComputeEvidenceHash(dir)
		updated, _ = json.MarshalIndent(ev, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "result.json"), updated, 0o644); err != nil {
			t.Fatalf("回填 hash: %v", err)
		}
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "符号链接") {
			t.Fatalf("symlink oracle 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("forged-process", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
		forged := processOracle{SchemaVersion: 1, Kind: "test-process", Command: "echo ok", ExitCode: 0, StdoutSHA256: bytesSHA256([]byte("forged")), StderrSHA256: bytesSHA256(nil)}
		oracleRaw, _ := json.Marshal(forged)
		if err := os.WriteFile(filepath.Join(dir, "oracle.json"), oracleRaw, 0o644); err != nil {
			t.Fatalf("伪造 oracle: %v", err)
		}
		resultRaw, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		var ev contracts.GateEvidence
		_ = json.Unmarshal(resultRaw, &ev)
		ev.ReportHash = ComputeEvidenceHash(dir)
		updated, _ := json.MarshalIndent(ev, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "result.json"), updated, 0o644)
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "输出哈希") {
			t.Fatalf("伪造进程 oracle 应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})

	t.Run("tampered", func(t *testing.T) {
		root := t.TempDir()
		dir := writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
		if err := os.WriteFile(filepath.Join(dir, "oracle.json"), []byte(`{"tampered":true}`), 0o644); err != nil {
			t.Fatalf("篡改 oracle: %v", err)
		}
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil || rep.Passed || !reasonMentions(rep, "哈希") {
			t.Fatalf("oracle 篡改应判死，passed=%v reasons=%v err=%v", rep.Passed, rep.Reasons, err)
		}
	})
}

// TestGateRejectsDuplicateEvidence：同一必需位出现两份，结论不确定，不能挑一份绿的。
func TestGateRejectsDuplicateEvidence(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "HOST", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
	// 第二份放到另一个证据树里，目录名仍是 case id（否则会被当成另一个 case）。
	dup := filepath.Join(root, "dup-tree", "win-x64", "ENV03")
	if err := os.MkdirAll(dup, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := filepath.Join(root, base.SourceRevision, "G0", "win-x64", "ENV03")
	for _, name := range []string{"result.json", "command.txt", "stdout.log", "stderr.log"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("读 %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dup, name), b, 0o644); err != nil {
			t.Fatalf("写 %s: %v", name, err)
		}
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("重复证据应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
}

// TestGateRejectsDirtyWorktreeEvidence：dirty 工作区的代码不是该 revision 那一份。
func TestGateRejectsDirtyWorktreeEvidence(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "HOST", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
		e.WorktreeDirty = true
	})
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("dirty 证据应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
	if !reasonMentions(rep, "dirty") {
		t.Fatalf("报告应指出 dirty 工作区，实际 reasons=%v", rep.Reasons)
	}
}

// TestGateRejectsEvidenceForWrongGate：把 G1 的报告放进 G0 证据树不能顶用。
func TestGateRejectsEvidenceForWrongGate(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "HOST", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
		e.GateID = "G1"
	})
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("gateId 不匹配应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
}

// TestGateRejectsUnknownScope：新增 case 写了 typo 的 scope 不能静默变成「不需要证据」。
func TestGateRejectsUnknownScope(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "火星", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("未知 scope 应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
}

// TestGateRejectsAtomicCaseWithoutScope：原子 case 必须声明目标范围。
func TestGateRejectsAtomicCaseWithoutScope(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("缺 scope 应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
}

// ---- GOV05：证据环境身份与目标最低 OS ----

// writeMinimumEvidenceForAllTargets 给六个目标各写一份「恰好等于最低要求」的证据，
// 只对 mutateTarget 施加破坏。这样一条判死只能来自被破坏的那个目标。
func writeMinimumEvidenceForAllTargets(t *testing.T, root string, base GateBaseline, caseID, gate, mutateTarget string, mutate func(*contracts.GateEvidence)) {
	t.Helper()
	for _, target := range KnownTargets {
		var m func(*contracts.GateEvidence)
		if target == mutateTarget {
			m = mutate
		}
		writeEvidence(t, root, base.SourceRevision, gate, target, caseID, base.VersionsLockHash, m)
	}
}

func TestGateRejectsEvidenceMissingEnvironment(t *testing.T) {
	idx := testIndexG0(t)
	root := t.TempDir()
	base := testBaseline()
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", id, base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.Environment = contracts.Environment{}
		})
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("缺环境身份应判死，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
	if !reasonMentions(rep, "环境") {
		t.Fatalf("拒绝原因必须点名环境身份，实际 reasons=%v", rep.Reasons)
	}
}

func TestGateRejectsEnvironmentOSTargetMismatch(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "ALL", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeMinimumEvidenceForAllTargets(t, root, base, "ENV03", "G0", "win-x64", func(e *contracts.GateEvidence) {
		e.Environment.OS = "linux"
	})
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("环境 OS 与目标不符应判死，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
	if !reasonMentions(rep, "environment.os") {
		t.Fatalf("拒绝原因应点名 environment.os，实际 reasons=%v", rep.Reasons)
	}

	// 反向对照：改回正确 OS 后必须通过（否则上面的判死可能只是「永远判死」）。
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, nil)
	rep2, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep2.Passed || rep2.ExitCode != 0 {
		t.Fatalf("环境与目标一致时不应判死，实际 passed=%v reasons=%v", rep2.Passed, rep2.Reasons)
	}
}

func TestGateRejectsBelowMinimumOSVersion(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "ALL", Gates: []string{"G0"}})

	t.Run("windows-below-build", func(t *testing.T) {
		root := t.TempDir()
		base := testBaseline()
		// ADR 0005：win-x64 的最低线是 Windows 10 22H2 build 19045（本机即最低线）。
		// 这里写 19044（最低线减一），证明 GOV05 仍在拦"低于最低"而不是被整体调松。
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "ENV03", base.VersionsLockHash, func(e *contracts.GateEvidence) {
			e.Environment.OSVersion = "10"
			e.Environment.OSBuild = "19044"
		})
		for _, target := range activeTargets() {
			if target == "win-x64" {
				continue
			}
			writeEvidence(t, root, base.SourceRevision, "G0", target, "ENV03", base.VersionsLockHash, nil)
		}
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil {
			t.Fatalf("EvaluateGate error: %v", err)
		}
		if rep.Passed || rep.ExitCode != 2 {
			t.Fatalf("低于最低 build 的证据应判死，实际 passed=%v reasons=%v", rep.Passed, rep.Reasons)
		}
	})

	t.Run("macos-below-version", func(t *testing.T) {
		root := t.TempDir()
		base := testBaseline()
		writeMinimumEvidenceForAllTargets(t, root, base, "ENV03", "G0", "mac-x64", func(e *contracts.GateEvidence) {
			e.Environment.OSVersion = "13.6"
			e.Environment.OSBuild = "0"
		})
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil {
			t.Fatalf("EvaluateGate error: %v", err)
		}
		if rep.Passed || rep.ExitCode != 2 {
			t.Fatalf("macOS 13.6 证据应判死，实际 passed=%v reasons=%v", rep.Passed, rep.Reasons)
		}
	})

	t.Run("unparsable-version", func(t *testing.T) {
		root := t.TempDir()
		base := testBaseline()
		writeMinimumEvidenceForAllTargets(t, root, base, "ENV03", "G0", "linux-x64", func(e *contracts.GateEvidence) {
			e.Environment.OSVersion = "twenty-four"
		})
		rep, err := EvaluateGate(idx, "G0", root, base)
		if err != nil {
			t.Fatalf("EvaluateGate error: %v", err)
		}
		if rep.Passed || rep.ExitCode != 2 {
			t.Fatalf("无法解析的 OS 版本应判死，实际 passed=%v reasons=%v", rep.Passed, rep.Reasons)
		}
	})
}

// TestGateAcceptsEvidenceMatchingPlatformMinimums 是正向对照：
// 恰好等于最低要求的证据必须通过，证明 GOV05 不是「一律判死」。
func TestGateAcceptsEvidenceMatchingPlatformMinimums(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "ENV03", Owner: "F00", Required: true, Scope: "ALL", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	writeMinimumEvidenceForAllTargets(t, root, base, "ENV03", "G0", "", nil)
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep.Passed || rep.ExitCode != 0 {
		t.Fatalf("恰好等于最低要求的证据应通过，实际 passed=%v reasons=%v", rep.Passed, rep.Reasons)
	}
}

// TestPlatformMinimumsCoverEveryTarget 钉住 build/platforms.json 的最低要求：
// 缺字段或被人调低都会被这里拦住（GOV05 的核心口径）。
func TestPlatformMinimumsCoverEveryTarget(t *testing.T) {
	plats, err := LoadPlatformsFromRepo()
	if err != nil {
		t.Fatalf("加载 build/platforms.json: %v", err)
	}
	// ADR 0005：win-x64 是**本轮唯一验收目标**，最低线收敛为 Windows 10 22H2
	// （build 19045，即本机）。其余五个目标必须显式声明 active=false 且写上
	// 停用依据，避免"忘了写 active"被当成"继续要求六目标"或"悄悄不要求"。
	want := map[string][2]string{
		"win-x64": {"10", "19045"}, "win-arm64": {"11", "26100"},
		"mac-x64": {"14", "0"}, "mac-arm64": {"14", "0"},
		"linux-x64": {"24.04", "0"}, "linux-arm64": {"24.04", "0"},
	}
	active := []string{}
	for target, expected := range want {
		tm, ok := plats.Target(target)
		if !ok {
			t.Fatalf("build/platforms.json 缺 target %s", target)
		}
		if tm.MinOSVersion != expected[0] {
			t.Fatalf("%s minOSVersion=%q，期望 %q", target, tm.MinOSVersion, expected[0])
		}
		if strconv.FormatInt(int64(tm.MinOSBuild), 10) != expected[1] {
			t.Fatalf("%s minOSBuild=%d，期望 %s", target, tm.MinOSBuild, expected[1])
		}
		if tm.IsActive() {
			active = append(active, target)
		} else if tm.DeferredBy == "" {
			t.Fatalf("%s 被停用但没有写 deferredBy（停用必须有依据可查）", target)
		}
	}
	if len(active) != 1 || active[0] != "win-x64" {
		t.Fatalf("ADR 0005 下验收目标必须恰好是 win-x64，实际 %v", active)
	}
}

// TestHostEnvironmentReportsRealValues 断言宿主探测读的是真实环境，不是常量。
func TestHostEnvironmentReportsRealValues(t *testing.T) {
	env, err := HostEnvironment(DisplayDesktop)
	if err != nil {
		t.Fatalf("HostEnvironment: %v", err)
	}
	if env.OS != runtime.GOOS || env.Arch != runtime.GOARCH {
		t.Fatalf("environment.os/arch 必须来自 runtime，实际 %q/%q", env.OS, env.Arch)
	}
	if strings.TrimSpace(env.OSVersion) == "" {
		t.Fatalf("environment.osVersion 不能为空: %+v", env)
	}
	if env.Display != DisplayDesktop {
		t.Fatalf("environment.display 应原样保留调用方归一化后的值，实际 %q", env.Display)
	}
	if runtime.GOOS == "windows" {
		// 不硬编码本机的 19045：按 Windows 自身的版本号规则核对一致性，
		// 同时钉住「Windows 10 不会自称 11」。
		build, err := strconv.Atoi(env.OSBuild)
		if err != nil || build <= 0 {
			t.Fatalf("environment.osBuild 必须是真实十进制 build 号，实际 %q", env.OSBuild)
		}
		wantVersion := "10"
		if build >= 22000 {
			wantVersion = "11"
		}
		if env.OSVersion != wantVersion {
			t.Fatalf("build %d 应报告 OSVersion %q，实际 %q", build, wantVersion, env.OSVersion)
		}
	}
}

// TestGateEvidenceSchemaRequiresEnvironment 钉住 schema 与 Go 结构一致（GOV05）：
// 证据 JSON 必须带 environment，五个字段全部必填。
func TestGateEvidenceSchemaRequiresEnvironment(t *testing.T) {
	path, err := repoFile("schemas/gate-evidence.schema.json")
	if err != nil {
		t.Fatalf("repoFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 schema: %v", err)
	}
	type propSchema struct {
		Type                 string                `json:"type"`
		AdditionalProperties *bool                 `json:"additionalProperties"`
		Required             []string              `json:"required"`
		Properties           map[string]propSchema `json:"properties"`
	}
	var schema propSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("解析 schema: %v", err)
	}
	if !contains(schema.Required, "environment") {
		t.Fatalf("schema 必须把 environment 列为 required，实际 %v", schema.Required)
	}
	envSchema, ok := schema.Properties["environment"]
	if !ok {
		t.Fatalf("schema 缺 environment 属性定义")
	}
	if envSchema.Type != "object" {
		t.Fatalf("environment 必须是 object，实际 %q", envSchema.Type)
	}
	if envSchema.AdditionalProperties == nil || *envSchema.AdditionalProperties {
		t.Fatalf("environment 必须 additionalProperties:false")
	}
	for _, field := range []string{"os", "osVersion", "osBuild", "arch", "display"} {
		if !contains(envSchema.Required, field) {
			t.Fatalf("environment 必须要求 %s，实际 %v", field, envSchema.Required)
		}
		ps, ok := envSchema.Properties[field]
		if !ok || ps.Type != "string" {
			t.Fatalf("environment.%s 必须是 string，实际 %+v", field, ps)
		}
	}

	// 结构侧一致性：GateEvidence 序列化后必须真的带这五个字段（无 omitempty）。
	b, err := json.Marshal(contracts.GateEvidence{})
	if err != nil {
		t.Fatalf("marshal GateEvidence: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal GateEvidence: %v", err)
	}
	rawEnv, ok := decoded["environment"]
	if !ok {
		t.Fatalf("GateEvidence 序列化后缺 environment 字段")
	}
	var decodedEnv map[string]json.RawMessage
	if err := json.Unmarshal(rawEnv, &decodedEnv); err != nil {
		t.Fatalf("environment 不是对象: %v", err)
	}
	for _, field := range []string{"os", "osVersion", "osBuild", "arch", "display"} {
		if _, ok := decodedEnv[field]; !ok {
			t.Fatalf("GateEvidence.environment 序列化后缺 %s（不能 omitempty）", field)
		}
	}
}

func reasonMentions(rep *GateReport, sub string) bool {
	for _, r := range rep.Reasons {
		if strings.Contains(strings.ToLower(r), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// TestIntegrationRejectsUnknownGate：gate 参数拼错不能变成"没有必需 case 也算通过"。
func TestIntegrationRejectsUnknownGate(t *testing.T) {
	if code := RunIntegrationForGate("win-x64", "G9"); code != 2 {
		t.Fatalf("未知 gate 应返回 2，实际 %d", code)
	}
}
