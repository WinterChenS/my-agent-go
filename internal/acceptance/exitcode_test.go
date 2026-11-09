package acceptance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot walks up from the test package to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod above %q", dir)
		}
		dir = parent
	}
}

func wrapperPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "devtools.ps1")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scripts/devtools.ps1 is missing: %v", err)
	}
	return path
}

func runDevtoolsWrapper(t *testing.T, args ...string) (int, string) {
	t.Helper()
	script := wrapperPath(t)
	cmdArgs := append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}, args...)
	cmd := exec.Command("powershell", cmdArgs...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run wrapper: %v (output: %s)", err, out)
		}
		code = exitErr.ExitCode()
	}
	return code, string(out)
}

// The wrapper must return the child process exit code unchanged: 0 pass,
// 1 valid FAIL evidence, 2 incomplete input. `go run` would collapse 1 and 2.
func TestDevtoolsWrapperPreservesExitCodes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell wrapper is Windows-only")
	}
	f := newFixture(t)
	m := f.matrix([]string{"R0-00"}, caseDecl("R0-00-build-gate", "R0-00", "automation"))
	const caseID = "R0-00-build-gate"
	f.writeResult(caseID, f.passResult(caseID, "automation"))

	base := []string{
		"gate",
		"--id=G0",
		"--target=win-x64",
		"--evidence=" + f.root,
		"--matrix=" + m,
		"--sha=" + f.sha,
	}

	if code, out := runDevtoolsWrapper(t, base...); code != 0 {
		t.Fatalf("complete PASS evidence: exit = %d, want 0\n%s", code, out)
	}

	fail := f.passResult(caseID, "automation")
	fail["status"] = "FAIL"
	f.writeResult(caseID, fail)
	if code, out := runDevtoolsWrapper(t, base...); code != 1 {
		t.Fatalf("valid FAIL evidence: exit = %d, want 1\n%s", code, out)
	}

	if err := os.Remove(filepath.Join(f.caseDir(caseID), "result.json")); err != nil {
		t.Fatalf("remove result.json: %v", err)
	}
	if code, out := runDevtoolsWrapper(t, base...); code != 2 {
		t.Fatalf("missing evidence: exit = %d, want 2\n%s", code, out)
	}

	if code, out := runDevtoolsWrapper(t, "gate", "--id=G0", "--target=linux-x64", "--evidence="+f.root, "--matrix="+m, "--sha="+f.sha); code != 2 {
		t.Fatalf("unknown target: exit = %d, want 2\n%s", code, out)
	}

	if code, out := runDevtoolsWrapper(t, "gate", "--id=G9", "--target=win-x64", "--evidence="+f.root, "--matrix="+m, "--sha="+f.sha); code != 2 {
		t.Fatalf("unknown gate: exit = %d, want 2\n%s", code, out)
	}
}

func TestDevtoolsWrapperRejectsUnknownCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell wrapper is Windows-only")
	}
	if code, out := runDevtoolsWrapper(t, "frobnicate"); code != 2 {
		t.Fatalf("unknown command: exit = %d, want 2\n%s", code, out)
	}
}
