package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"my-agent-go/internal/preflight"
)

// fixture builds an isolated evidence tree under the D: drive temp directory.
type fixture struct {
	t      *testing.T
	root   string
	sha    string
	target string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, root: t.TempDir(), sha: strings.Repeat("a", 40), target: "win-x64"}
}

func (f *fixture) caseDir(caseID string) string {
	return filepath.Join(f.root, f.sha, f.target, caseID)
}

func (f *fixture) writeJSON(path string, v any) string {
	f.t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		f.t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		f.t.Fatalf("write fixture %s: %v", path, err)
	}
	return path
}

func caseDecl(id, task, kind string) map[string]any {
	return map[string]any{"id": id, "task": task, "evidenceKind": kind, "required": true}
}

func (f *fixture) matrix(tasks []string, cases ...map[string]any) string {
	f.t.Helper()
	caseList := make([]any, 0, len(cases))
	for _, c := range cases {
		caseList = append(caseList, c)
	}
	return f.writeJSON(filepath.Join(f.root, "rebuild.json"), map[string]any{
		"schemaVersion": 1,
		"gates": []any{map[string]any{
			"id":            "G0",
			"targets":       []string{f.target},
			"requiredTasks": tasks,
			"cases":         caseList,
		}},
	})
}

func (f *fixture) artifact(caseID, name, content string) (string, string) {
	f.t.Helper()
	dir := f.caseDir(caseID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("mkdir case dir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write artifact: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	return filepath.ToSlash(name), hex.EncodeToString(sum[:])
}

// passResult builds a fully valid PASS evidence document.
func (f *fixture) passResult(caseID, kind string) map[string]any {
	f.t.Helper()
	rel, sum := f.artifact(caseID, "run.log", "real command output\n")
	return map[string]any{
		"schemaVersion": 1,
		"caseId":        caseID,
		"gitSHA":        f.sha,
		"target":        f.target,
		"status":        "PASS",
		"evidenceKind":  kind,
		"os": map[string]any{
			"name":    "Windows 10 Pro",
			"version": "10.0.19045",
			"build":   "19045.4529",
			"arch":    "amd64",
		},
		"toolVersions":   map[string]string{"go": "go1.27.3 windows/amd64"},
		"runtimeVersion": "n/a",
		"commands": []any{
			map[string]any{"command": "go build ./...", "workDir": "D:\\project\\anvil-wt\\anvil-rebuild", "exitCode": 0},
		},
		"artifacts":   []any{map[string]any{"path": rel, "sha256": sum}},
		"limitations": []any{},
	}
}

func (f *fixture) writeResult(caseID string, result map[string]any) {
	f.t.Helper()
	dir := f.caseDir(caseID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("mkdir case dir: %v", err)
	}
	f.writeJSON(filepath.Join(dir, "result.json"), result)
}

func (f *fixture) evaluate(matrix string) GateOutcome {
	f.t.Helper()
	return EvaluateGate(GateOptions{
		MatrixPath:   matrix,
		EvidenceRoot: f.root,
		GateID:       "G0",
		Target:       f.target,
		GitSHA:       f.sha,
	})
}

func assertOutcome(t *testing.T, out GateOutcome, wantCode int, wantProblem string) {
	t.Helper()
	if out.Code != wantCode {
		t.Fatalf("exit code = %d, want %d; problems:\n%s", out.Code, wantCode, strings.Join(out.Problems, "\n"))
	}
	if wantProblem != "" {
		joined := strings.Join(out.Problems, "\n")
		if !strings.Contains(joined, wantProblem) {
			t.Fatalf("problems do not mention %q; got:\n%s", wantProblem, joined)
		}
	}
}

// fakeResolver reports a fixed volume for every path. It is used for reject
// cases that must not touch another drive.
type fakeResolver struct{ volume string }

func (f fakeResolver) Resolve(p string) (preflight.Resolution, error) {
	return preflight.Resolution{Requested: p, Existing: p, Resolved: p, Volume: f.volume}, nil
}
func (f fakeResolver) MkdirAll(string) error      { return nil }
func (f fakeResolver) ProbeWritable(string) error { return nil }

func TestGatePassesCompleteEvidence(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	f.writeResult("R0-00-build-gate", f.passResult("R0-00-build-gate", "automation"))

	out := f.evaluate(m)
	assertOutcome(t, out, ExitPass, "")
	if len(out.Passed) != 1 || out.Passed[0] != "R0-00-build-gate" {
		t.Fatalf("passed = %v, want [R0-00-build-gate]", out.Passed)
	}
}

func TestGateRejectsEmptyMatrix(t *testing.T) {
	f := newFixture(t)
	m := f.writeJSON(filepath.Join(f.root, "rebuild.json"), map[string]any{"schemaVersion": 1, "gates": []any{}})
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "matrix declares no gates")
}

func TestGateRejectsInvalidMatrixSchema(t *testing.T) {
	f := newFixture(t)
	m := f.writeJSON(filepath.Join(f.root, "rebuild.json"), map[string]any{"schemaVersion": 99, "gates": []any{}})
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "schemaVersion")
}

func TestGateRejectsMalformedMatrix(t *testing.T) {
	f := newFixture(t)
	m := filepath.Join(f.root, "rebuild.json")
	if err := os.WriteFile(m, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write matrix: %v", err)
	}
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "matrix")
}

func TestGateRejectsUnknownGate(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	out := EvaluateGate(GateOptions{
		MatrixPath: m, EvidenceRoot: f.root, GateID: "G9", Target: f.target, GitSHA: f.sha,
	})
	assertOutcome(t, out, ExitIncomplete, "G9")
}

func TestGateRejectsUnknownTarget(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	f.writeResult("R0-00-build-gate", f.passResult("R0-00-build-gate", "automation"))
	out := EvaluateGate(GateOptions{
		MatrixPath: m, EvidenceRoot: f.root, GateID: "G0", Target: "linux-x64", GitSHA: f.sha,
	})
	assertOutcome(t, out, ExitIncomplete, "linux-x64")
}

func TestGateRejectsMissingEvidence(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "missing")
}

func TestGateRejectsWrongGitSHA(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["gitSHA"] = strings.Repeat("b", 40)
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "gitSHA")
}

func TestGateRejectsWrongTarget(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["target"] = "linux-x64"
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "target")
}

func TestGateRejectsWrongCaseID(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["caseId"] = "R0-00-other"
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "caseId")
}

func TestGateRejectsArtifactHashMismatch(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	f.writeResult("R0-00-build-gate", res)
	if err := os.WriteFile(filepath.Join(f.caseDir("R0-00-build-gate"), "run.log"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "sha256")
}

func TestGateRejectsMissingArtifactFile(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	f.writeResult("R0-00-build-gate", res)
	if err := os.Remove(filepath.Join(f.caseDir("R0-00-build-gate"), "run.log")); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "missing")
}

func TestGateRejectsEvidencePathEscapes(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"parent", "../outside.log"},
		{"nested-parent", "logs/../../outside.log"},
		{"backslash-parent", `..\outside.log`},
		{"absolute", `C:\Windows\win.ini`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
			res := f.passResult("R0-00-build-gate", "automation")
			res["artifacts"] = []any{map[string]any{"path": tc.path, "sha256": strings.Repeat("0", 64)}}
			f.writeResult("R0-00-build-gate", res)
			assertOutcome(t, f.evaluate(m), ExitIncomplete, "artifact")
		})
	}
}

func TestGateRejectsUnsafeCaseID(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("../../escape", "R0-00", "automation"))
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "case id")
}

func TestGateRejectsDuplicateCaseID(t *testing.T) {
	f := newFixture(t)
	decl := caseDecl("R0-00-build-gate", "R0-00", "automation")
	m := f.matrix([]string{"R0-00"}, decl, decl)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "duplicate")
}

func TestGateRejectsUnknownEvidenceKind(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "screenshot"))
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "evidenceKind")
}

func TestGateRejectsEvidenceKindMismatch(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "native-desktop"))
	f.writeResult("R0-00-build-gate", f.passResult("R0-00-build-gate", "automation"))
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "evidenceKind")
}

func TestGateReturnsOneForValidFailEvidence(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["status"] = "FAIL"
	f.writeResult("R0-00-build-gate", res)

	out := f.evaluate(m)
	assertOutcome(t, out, ExitAssertion, "")
	if len(out.Problems) != 0 {
		t.Fatalf("valid FAIL evidence should not report problems, got %v", out.Problems)
	}
	if out.Statuses["R0-00-build-gate"] != "FAIL" {
		t.Fatalf("status = %q, want FAIL", out.Statuses["R0-00-build-gate"])
	}
}

func TestGateReturnsTwoForBlockedEvidence(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["status"] = "BLOCKED"
	res["limitations"] = []any{"no interactive desktop session"}
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "BLOCKED")
}

func TestGateReturnsTwoForNotRunEvidence(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["status"] = "NOT_RUN"
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "NOT_RUN")
}

func TestGateRejectsInvalidStatus(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["status"] = "GREEN"
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "status")
}

func TestGateRejectsUnregisteredRequiredTask(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00", "R0-01"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	f.writeResult("R0-00-build-gate", f.passResult("R0-00-build-gate", "automation"))
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "R0-01")
}

func TestGateRejectsMissingLimitationsField(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	delete(res, "limitations")
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "limitations")
}

func TestGateRejectsEmptyCommands(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["commands"] = []any{}
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "commands")
}

func TestGateRejectsInvalidArtifactSHA(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	res := f.passResult("R0-00-build-gate", "automation")
	res["artifacts"] = []any{map[string]any{"path": "run.log", "sha256": "not-a-hash"}}
	f.writeResult("R0-00-build-gate", res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "sha256")
}

func TestGateRejectsEvidenceRootOutsideD(t *testing.T) {
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	f.writeResult("R0-00-build-gate", f.passResult("R0-00-build-gate", "automation"))
	out := EvaluateGate(GateOptions{
		MatrixPath: m, EvidenceRoot: f.root, GateID: "G0", Target: f.target, GitSHA: f.sha,
		Resolver: fakeResolver{volume: "C:"},
	})
	assertOutcome(t, out, ExitIncomplete, "D:")
}

func TestGateRejectsArtifactJunctionEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows feature")
	}
	f := newFixture(t)
	const caseID = "R0-00-build-gate"
	m := f.matrix([]string{"R0-00"}, caseDecl(caseID, "R0-00", "automation"))
	res := f.passResult(caseID, "automation")
	dir := f.caseDir(caseID)
	link := filepath.Join(dir, "escape")
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, `C:\Windows`).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink: %v (%s)", err, out)
	}
	res["artifacts"] = []any{map[string]any{"path": `escape\win.ini`, "sha256": strings.Repeat("0", 64)}}
	f.writeResult(caseID, res)
	assertOutcome(t, f.evaluate(m), ExitIncomplete, "artifact")
}
