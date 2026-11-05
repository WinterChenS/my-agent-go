package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// writeEvidenceAt 在指定目录写证据。重复证据测试需要写到非规范路径。
func writeEvidenceAt(t *testing.T, dir, rev, gate, target, caseID, lockHash string, mutate func(*contracts.GateEvidence)) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ev := contracts.GateEvidence{
		SchemaVersion:    1,
		GateID:           gate,
		TaskID:           "F01",
		SourceRevision:   rev,
		VersionsLockHash: lockHash,
		TargetID:         target,
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

func TestGateAcceptsAllScopeOnSixTargets(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "SEC06", Owner: "F04", Required: true, Scope: "ALL", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	for _, target := range KnownTargets {
		writeEvidence(t, root, base.SourceRevision, "G0", target, "SEC06", base.VersionsLockHash, nil)
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep.Passed || rep.ExitCode != 0 {
		t.Fatalf("六目标齐全应通过，实际 passed=%v exitCode=%d reasons=%v", rep.Passed, rep.ExitCode, rep.Reasons)
	}
	if len(rep.PerRequirement) != 6 {
		t.Fatalf("scope=ALL 应展开为 6 条必需证据位，实际 %d", len(rep.PerRequirement))
	}
}

// TestGateRejectsAllScopeMissingOneTarget 钉住交接文档指出的聚合缺陷：
// 同一 case 在某个目标上的 PASS 不能代表整个 ALL。
func TestGateRejectsAllScopeMissingOneTarget(t *testing.T) {
	idx := testIndexOne(t, CaseEntry{ID: "SEC06", Owner: "F04", Required: true, Scope: "ALL", Gates: []string{"G0"}})
	root := t.TempDir()
	base := testBaseline()
	for _, target := range KnownTargets {
		if target == "mac-arm64" {
			continue
		}
		writeEvidence(t, root, base.SourceRevision, "G0", target, "SEC06", base.VersionsLockHash, nil)
	}
	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("缺 mac-arm64 应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
	if !reasonMentions(rep, "mac-arm64") {
		t.Fatalf("报告应明确指出缺 mac-arm64，实际 reasons=%v", rep.Reasons)
	}
}

// TestGateRejectsLinuxMissingWayland：Linux 只跑 X11 不能宣称 LNX 通过。
func TestGateRejectsLinuxMissingWayland(t *testing.T) {
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
	rep, err := EvaluateGate(idx, "G1", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode != 2 {
		t.Fatalf("缺 Wayland 应判死，实际 passed=%v exitCode=%d", rep.Passed, rep.ExitCode)
	}
	if !reasonMentions(rep, "wayland") {
		t.Fatalf("报告应指出缺 wayland，实际 reasons=%v", rep.Reasons)
	}

	// 反向对照：两个 display 都齐 → 通过（否则上面的判死可能只是「永远判死」）。
	for _, target := range []string{"linux-x64", "linux-arm64"} {
		writeDisplayed(target, "wayland")
	}
	rep2, err := EvaluateGate(idx, "G1", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep2.Passed || rep2.ExitCode != 0 {
		t.Fatalf("X11+Wayland 齐全应通过，实际 passed=%v exitCode=%d reasons=%v", rep2.Passed, rep2.ExitCode, rep2.Reasons)
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
