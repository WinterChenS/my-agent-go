package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeDesktop scripts agent-desktop steps without touching a real desktop.
type fakeDesktop struct {
	responses map[string]fakeStepResponse
	logs      map[string]string
	calls     []string
}

type fakeStepResponse struct {
	failure string
	exit    int
}

func (f *fakeDesktop) run(step StepSpec) (Command, Artifact, string) {
	f.calls = append(f.calls, step.Name)
	resp := f.responses[step.Name]
	exit := resp.exit
	if resp.failure != "" && exit == 0 {
		exit = 1
	}
	return Command{Command: step.Name, WorkDir: "fake", ExitCode: exit}, Artifact{}, resp.failure
}

func (f *fakeDesktop) read(stepName string) ([]byte, error) {
	return []byte(f.logs[stepName]), nil
}

func newFakeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if vol := strings.ToUpper(filepath.VolumeName(repo)); vol != "D:" {
		t.Fatalf("fake repo %q is on %q, want D:", repo, vol)
	}
	for _, dir := range []string{
		filepath.Join(repo, ".tmp", "bin"),
		filepath.Join(repo, "frontend", "dist"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".tmp", "bin", "anvil.exe"), []byte("fake exe"), 0o644); err != nil {
		t.Fatalf("write fake exe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "frontend", "dist", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write fake index.html: %v", err)
	}
	return repo
}

func fakeNativeMatrix(t *testing.T, dir string) string {
	t.Helper()
	doc := map[string]any{
		"schemaVersion": 1,
		"gates": []any{map[string]any{
			"id":            "G0",
			"targets":       []string{"win-x64"},
			"requiredTasks": []string{"R0-00"},
			"cases": []any{map[string]any{
				"id": "R0-00-native-window", "task": "R0-00",
				"evidenceKind": "native-desktop", "required": true,
			}},
		}},
	}
	path := filepath.Join(dir, "rebuild.json")
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal matrix: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write matrix: %v", err)
	}
	return path
}

func healthyDesktop() *fakeDesktop {
	return &fakeDesktop{
		responses: map[string]fakeStepResponse{},
		logs: map[string]string{
			"agent-desktop-status": `{"version":"2.5","ok":true,"command":"status","data":{"platform":"windows","permissions":{"accessibility":{"state":"granted"}}}}`,
			"list-windows":         `{"version":"2.5","ok":true,"command":"list-windows","data":[{"id":"w-1","title":"Anvil","visible":true,"accessible":true,"minimized":false,"app_name":"anvil.exe"}]}`,
		},
	}
}

func runFakeNativeCase(t *testing.T, desktop *fakeDesktop, hooks *nativeHooks) (VerifyOutcome, string) {
	t.Helper()
	repo := newFakeRepo(t)
	evidence := t.TempDir()
	matrix := fakeNativeMatrix(t, evidence)
	if hooks == nil {
		hooks = &nativeHooks{}
	}
	hooks.desktop = desktop
	if hooks.prepareHome == nil {
		hooks.prepareHome = func(desktopStepRunner, string, string) ([]Command, []Artifact, string) {
			return nil, nil, ""
		}
	}
	out := RunCase(VerifyOptions{
		RepoRoot:     repo,
		MatrixPath:   matrix,
		EvidenceRoot: evidence,
		CaseID:       "R0-00-native-window",
		Target:       "win-x64",
		GitSHA:       strings.Repeat("a", 40),
		native:       hooks,
	})
	caseDir := filepath.Join(evidence, strings.Repeat("a", 40), "win-x64", "R0-00-native-window")
	return out, filepath.Join(caseDir, "result.json")
}

func TestClassifyDesktopFailure(t *testing.T) {
	cases := []struct {
		name    string
		failure string
		body    string
		blocked bool
	}{
		{
			name:    "internal-dacl",
			failure: "snapshot failed",
			body:    `{"ok":false,"error":{"code":"INTERNAL","message":"private file DACL grants an untrusted principal"}}`,
			blocked: true,
		},
		{
			name:    "perm-denied",
			failure: "snapshot failed",
			body:    `{"ok":false,"error":{"code":"PERM_DENIED","message":"access denied"}}`,
			blocked: true,
		},
		{
			name:    "policy-denied",
			failure: "wait failed",
			body:    `{"ok":false,"error":{"code":"POLICY_DENIED","message":"headed input is not allowed"}}`,
			blocked: true,
		},
		{
			name:    "executable-missing",
			failure: `step "agent-desktop-status": look up "agent-desktop": executable file not found in %PATH%`,
			body:    "",
			blocked: true,
		},
		{
			name:    "window-not-found",
			failure: "snapshot failed",
			body:    `{"ok":false,"error":{"code":"WINDOW_NOT_FOUND","message":"No window with id w-1"}}`,
			blocked: false,
		},
		{
			name:    "timeout",
			failure: "wait failed",
			body:    `{"ok":false,"error":{"code":"TIMEOUT","message":"window did not appear"}}`,
			blocked: false,
		},
		{
			name:    "app-not-found",
			failure: "launch failed",
			body:    `{"ok":false,"error":{"code":"APP_NOT_FOUND","message":"no such application"}}`,
			blocked: false,
		},
		{
			name:    "marker-missing-is-not-blocked",
			failure: `step "find-build-marker-01" output does not contain "anvil-rebuild-shell"`,
			body:    `{"ok":true,"data":{"matches":[],"total_matches":0}}`,
			blocked: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocked, detail := classifyDesktopFailure(tc.failure, []byte(tc.body))
			if blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v (detail %q)", blocked, tc.blocked, detail)
			}
			if tc.blocked && detail == "" {
				t.Fatalf("blocked failure must keep a detail for limitations")
			}
		})
	}
}

// A DACL/INTERNAL error from agent-desktop is an environment problem: the case
// must be BLOCKED (exit 2), with the original error in limitations.
func TestNativeCaseBlocksWhenSnapshotReportsDACLFailure(t *testing.T) {
	desktop := healthyDesktop()
	const original = `{"version":"2.5","ok":false,"command":"snapshot","error":{"code":"INTERNAL","message":"private file DACL grants an untrusted principal"}}`
	desktop.responses["snapshot-01"] = fakeStepResponse{failure: `step "snapshot-01" exited 1, want 0`}
	desktop.logs["snapshot-01"] = original

	out, resultPath := runFakeNativeCase(t, desktop, nil)
	if out.Code != ExitIncomplete {
		t.Fatalf("exit = %d, want %d (status %s, problems %v)", out.Code, ExitIncomplete, out.Status, out.Problems)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want BLOCKED", out.Status)
	}
	problems := strings.Join(out.Problems, "\n")
	if !strings.Contains(problems, "private file DACL grants an untrusted principal") {
		t.Fatalf("limitations do not keep the original error:\n%s", problems)
	}

	var result Result
	body, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != StatusBlocked {
		t.Fatalf("result.json status = %q, want BLOCKED", result.Status)
	}
	if len(result.Limitations) == 0 || !strings.Contains(strings.Join(result.Limitations, "\n"), "untrusted principal") {
		t.Fatalf("result.json limitations = %v, want the DACL error", result.Limitations)
	}
}

// Failing to normalise the agent-desktop state directory is BLOCKED and stops
// before any desktop command runs.
func TestNativeCaseBlocksWhenStateDirectoryACLNormalisationFails(t *testing.T) {
	desktop := healthyDesktop()
	hooks := &nativeHooks{
		prepareHome: func(desktopStepRunner, string, string) ([]Command, []Artifact, string) {
			return nil, nil, "agent-desktop home ACL normalisation failed: icacls exited 5"
		},
	}
	out, _ := runFakeNativeCase(t, desktop, hooks)
	if out.Code != ExitIncomplete || out.Status != StatusBlocked {
		t.Fatalf("exit/status = %d/%s, want %d/BLOCKED (problems %v)", out.Code, out.Status, ExitIncomplete, out.Problems)
	}
	if !strings.Contains(strings.Join(out.Problems, "\n"), "ACL normalisation failed") {
		t.Fatalf("limitations do not explain the ACL failure: %v", out.Problems)
	}
	for _, call := range desktop.calls {
		if call == "agent-desktop-status" || call == "snapshot" {
			t.Fatalf("desktop step %q ran despite the ACL failure; calls=%v", call, desktop.calls)
		}
	}
}

// A missing window / failed marker stays a product assertion: FAIL (exit 1).
func TestNativeCaseKeepsProductAssertionAsFail(t *testing.T) {
	desktop := healthyDesktop()
	const original = `{"version":"2.5","ok":false,"command":"snapshot","error":{"code":"TIMEOUT","message":"window tree did not settle"}}`
	desktop.responses["snapshot-01"] = fakeStepResponse{failure: `step "snapshot-01" exited 1, want 0`}
	desktop.logs["snapshot-01"] = original

	out, _ := runFakeNativeCase(t, desktop, nil)
	if out.Code != ExitAssertion {
		t.Fatalf("exit = %d, want %d (status %s, problems %v)", out.Code, ExitAssertion, out.Status, out.Problems)
	}
	if out.Status != StatusFail {
		t.Fatalf("status = %q, want FAIL", out.Status)
	}
}

// The state-directory preparation must validate the D: path first and then
// run a locale-independent icacls step granting only user/SYSTEM/Administrators.
func TestAgentDesktopHomePreparationSteps(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("agent-desktop private-file DACL handling is Windows-specific")
	}
	home := filepath.Join(newFakeRepo(t), ".tmp", "tooling", "agent-desktop-home")
	steps, err := agentDesktopHomePreparationSteps(home)
	if err != nil {
		t.Fatalf("agentDesktopHomePreparationSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("steps = %v, want preflight + icacls", steps)
	}
	if steps[0].Command != "self" || !strings.Contains(strings.Join(steps[0].Args, " "), "--path="+home) {
		t.Fatalf("first step = %v, want self preflight for %q", steps[0], home)
	}
	acl := steps[1]
	if acl.Command != "icacls" {
		t.Fatalf("second step command = %q, want icacls", acl.Command)
	}
	joined := strings.Join(acl.Args, " ")
	for _, want := range []string{"/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("icacls args %q do not contain %q", joined, want)
		}
	}
	if strings.Contains(joined, "/T") {
		t.Fatalf("icacls args %q must not use /T: container-only ACEs on files break existing state", joined)
	}
}

// The real icacls step must leave exactly three explicit ACEs and no inherited
// ACEs, and must be safe to repeat.
func TestAgentDesktopHomeACLTightensIdempotently(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("agent-desktop private-file DACL handling is Windows-specific")
	}
	repo := newFakeRepo(t)
	caseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseDir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	home := filepath.Join(repo, ".tmp", "tooling", "agent-desktop-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	steps, err := agentDesktopHomePreparationSteps(home)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	aclStep := steps[len(steps)-1]
	if aclStep.Command != "icacls" {
		t.Fatalf("last step = %q, want icacls", aclStep.Command)
	}
	runner := automationRunner{repo: repo, caseDir: caseDir, env: os.Environ()}
	if _, _, failure := runner.runStep(aclStep); failure != "" {
		t.Fatalf("icacls step failed: %s", failure)
	}
	if _, _, failure := runner.runStep(aclStep); failure != "" {
		t.Fatalf("icacls step is not idempotent: %s", failure)
	}

	out, err := exec.Command("icacls", home).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %q: %v (%s)", home, err, out)
	}
	text := string(out)
	if count := strings.Count(text, ":("); count != 3 {
		t.Fatalf("state directory has %d ACEs, want 3 (user, SYSTEM, Administrators); icacls:\n%s", count, text)
	}
	if strings.Contains(text, "(I)(") {
		t.Fatalf("inherited ACEs remain on %q; icacls:\n%s", home, text)
	}
}

// Normalising the state directory must keep files left by a previous run
// usable. `/T` applied container-only (OI)(CI) ACEs to files, leaving them
// with no effective access, so the second native run failed with PERM_DENIED.
func TestAgentDesktopHomeACLKeepsExistingFilesUsable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("agent-desktop private-file DACL handling is Windows-specific")
	}
	home := filepath.Join(t.TempDir(), "agent-desktop-home")
	snapshots := filepath.Join(home, "snapshots")
	if err := os.MkdirAll(snapshots, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	lockPath := filepath.Join(home, "refstore.lock")
	if err := os.WriteFile(lockPath, []byte("previous run\n"), 0o644); err != nil {
		t.Fatalf("write refstore.lock: %v", err)
	}
	snapshotPath := filepath.Join(snapshots, "s1.json")
	if err := os.WriteFile(snapshotPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	caseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseDir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	steps, err := agentDesktopHomePreparationSteps(home)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	aclStep := steps[len(steps)-1]
	if aclStep.Command != "icacls" {
		t.Fatalf("last step = %q, want icacls", aclStep.Command)
	}
	runner := automationRunner{repo: t.TempDir(), caseDir: caseDir, env: os.Environ()}
	if _, _, failure := runner.runStep(aclStep); failure != "" {
		t.Fatalf("icacls step failed: %s", failure)
	}

	// Existing state must remain readable and appendable.
	body, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("existing state file became unreadable after ACL normalization: %v\nicacls:\n%s", err, icaclsListing(t, lockPath))
	}
	if !strings.Contains(string(body), "previous run") {
		t.Fatalf("existing state file content changed: %q", body)
	}
	f, err := os.OpenFile(lockPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("existing state file is not appendable after ACL normalization: %v\nicacls:\n%s", err, icaclsListing(t, lockPath))
	}
	if _, err := f.WriteString("append\n"); err != nil {
		_ = f.Close()
		t.Fatalf("append to existing state file failed: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close appended state file: %v", err)
	}
	if _, err := os.ReadFile(snapshotPath); err != nil {
		t.Fatalf("nested state file became unreadable: %v\nicacls:\n%s", err, icaclsListing(t, snapshotPath))
	}

	// New files (what agent-desktop writes next) must inherit a usable ACL.
	newFile := filepath.Join(home, "next-run.lock")
	if err := os.WriteFile(newFile, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("create new state file: %v", err)
	}
	newNested := filepath.Join(snapshots, "s2.json")
	if err := os.WriteFile(newNested, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("create new nested state file: %v", err)
	}

	// The root must have exactly the three trusted explicit ACEs and no
	// inherited ones; children must only carry the three trusted inherited
	// ACEs (no Authenticated Users / Users).
	rootACL := icaclsListing(t, home)
	if strings.Contains(rootACL, "(I)(") {
		t.Fatalf("root directory still has inherited ACEs:\n%s", rootACL)
	}
	if count := strings.Count(rootACL, ":("); count != 3 {
		t.Fatalf("root directory has %d ACEs, want 3:\n%s", count, rootACL)
	}
	for _, path := range []string{lockPath, snapshotPath, newFile, newNested} {
		listing := icaclsListing(t, path)
		if count := strings.Count(listing, ":("); count != 3 {
			t.Fatalf("%q has %d ACEs, want the 3 trusted inherited ACEs:\n%s", path, count, listing)
		}
		if strings.Contains(listing, "(M)") || strings.Contains(listing, "(RX)") {
			t.Fatalf("%q still grants untrusted principals:\n%s", path, listing)
		}
	}

	// agent-desktop must still be able to read its state directory.
	if agentDesktop, err := exec.LookPath("agent-desktop"); err == nil {
		cmd := exec.Command(agentDesktop, "status")
		cmd.Env = append(os.Environ(), "AGENT_DESKTOP_HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("agent-desktop status failed after ACL normalization: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), `"ok":true`) {
			t.Fatalf("agent-desktop status did not report ok:\n%s", out)
		}
	}
}

func icaclsListing(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("icacls", path).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %q: %v (%s)", path, err, out)
	}
	return string(out)
}
