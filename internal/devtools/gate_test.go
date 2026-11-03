package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"my-agent-go/internal/contracts"
)

// ---- 测试辅助 ----

func testIndexG0(t *testing.T) *CasesIndex {
	t.Helper()
	return &CasesIndex{
		SchemaVersion: 1,
		Cases: []CaseEntry{
			{ID: "GOV01", Owner: "F01", Required: true, Gates: []string{"G0"}},
			{ID: "GOV02", Owner: "F01", Required: true, Gates: []string{"G0"}},
			{ID: "GOV03", Owner: "F01", Required: true, Gates: []string{"G0"}},
			{ID: "GOV04", Owner: "F01", Required: true, Gates: []string{"G0"}},
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
	dir := filepath.Join(root, rev, gate, target, caseID)
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
			{ID: caseID, Status: contracts.StatusPass, Expected: "0", Actual: "0", StartedAt: "s", EndedAt: "e"},
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
	_ = os.WriteFile(filepath.Join(dir, "stdout.log"), []byte("ok"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "environment.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "oracle.json"), []byte("{}"), 0o644)
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
	// 当前 frontend 无 test 脚本，必须明确报 runner 不存在并以 2 退出。
	code, _, out, _ := runFrontendTest()
	if code != 2 {
		t.Fatalf("前端 test runner 缺失应返回 2，实际 %d；输出: %s", code, out)
	}
}
