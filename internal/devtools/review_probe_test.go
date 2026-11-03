package devtools

import (
	"testing"

	"my-agent-go/internal/contracts"
)

// 复核阶段新增的对抗性探针（reviewer probe）。
// 目的：验证门禁不会把「命令退出码为 0 但用例状态不是 PASS」的证据当成通过。
// 这类漏洞最危险——它让 FAIL/BLOCKED/NOT_RUN 的报告凭 exitCode=0 混过门禁。

// writeAllValidG0 写出某 evidence root 下 G0 的全部必需 case，均为有效 PASS 证据。
func writeAllValidG0(t *testing.T, root string, base GateBaseline) {
	t.Helper()
	idx := testIndexG0(t)
	for _, id := range ResolveRequiredAtomicCases(idx, "G0") {
		writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", id, base.VersionsLockHash, nil)
	}
}

func TestReviewGateRejectsFailedCaseStatus(t *testing.T) {
	idx := testIndexG0(t)
	base := testBaseline()

	cases := []struct {
		name   string
		status string
	}{
		{"FAIL", contracts.StatusFail},
		{"BLOCKED", contracts.StatusBlocked},
		{"NOT_RUN", contracts.StatusNotRun},
		{"未知状态值", "MAYBE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeAllValidG0(t, root, base)
			// 把 GOV01 改写为「exitCode=0，但状态不是 PASS」。
			writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "GOV01", base.VersionsLockHash,
				func(e *contracts.GateEvidence) {
					e.CaseResults = []contracts.CaseResult{{
						ID: "GOV01", Status: c.status,
						Expected: "PASS", Actual: c.status, StartedAt: "s", EndedAt: "e",
					}}
				})
			rep, err := EvaluateGate(idx, "G0", root, base)
			if err != nil {
				t.Fatalf("EvaluateGate error: %v", err)
			}
			if rep.Passed || rep.ExitCode == 0 {
				t.Fatalf("状态 %q 且 exitCode=0 不得通过门禁，实际 passed=%v exitCode=%d",
					c.status, rep.Passed, rep.ExitCode)
			}
		})
	}
}

// 证据目录名是 GOV01，但报告里的 caseResults 指向别处：不得把 A 的通过算到 B 头上。
func TestReviewGateRejectsCaseResultIDMismatch(t *testing.T) {
	idx := testIndexG0(t)
	base := testBaseline()
	root := t.TempDir()
	writeAllValidG0(t, root, base)
	writeEvidence(t, root, base.SourceRevision, "G0", "win-x64", "GOV01", base.VersionsLockHash,
		func(e *contracts.GateEvidence) {
			e.CaseResults = []contracts.CaseResult{{
				ID: "GOV02", Status: contracts.StatusPass,
				Expected: "0", Actual: "0", StartedAt: "s", EndedAt: "e",
			}}
		})

	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if rep.Passed || rep.ExitCode == 0 {
		t.Fatalf("caseResults 的 id 与目录 case 不一致时不得通过，实际 passed=%v exitCode=%d",
			rep.Passed, rep.ExitCode)
	}
}

// 反向对照：全部有效 PASS 证据时，门禁必须真的能通过（避免"永远判死"的假严格）。
func TestReviewGateAcceptsFullyValidEvidence(t *testing.T) {
	idx := testIndexG0(t)
	base := testBaseline()
	root := t.TempDir()
	writeAllValidG0(t, root, base)

	rep, err := EvaluateGate(idx, "G0", root, base)
	if err != nil {
		t.Fatalf("EvaluateGate error: %v", err)
	}
	if !rep.Passed || rep.ExitCode != 0 {
		t.Fatalf("全部有效 PASS 证据应通过门禁，实际 passed=%v exitCode=%d reasons=%v",
			rep.Passed, rep.ExitCode, rep.Reasons)
	}
}
