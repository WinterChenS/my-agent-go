package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/preflight"
)

// StepSpec is one command a verification case executes.
type StepSpec struct {
	Name                 string
	Command              string
	Args                 []string
	Dir                  string
	Timeout              time.Duration
	ExpectExit           int
	ExpectOutputContains string
	Mkdir                []string
}

// CasePlan is the executable form of a registered case.
type CasePlan struct {
	ID              string
	EvidenceKind    string
	Steps           []StepSpec
	Reset           []string
	NativeWindow    bool
	NativeShell     bool
	NativeTheme     bool
	NativeI18n      bool
	NativeWorkbench bool
	NativeChat      bool
	NativeAgents    bool
	NativeSkills    bool
	// CollectArtifacts lists case-directory globs whose files are hashed into
	// result.json after the steps (e.g. screenshots written by a helper).
	CollectArtifacts []string
}

// VerifyOptions selects the case to execute and where to record evidence.
type VerifyOptions struct {
	RepoRoot     string
	MatrixPath   string
	EvidenceRoot string
	CaseID       string
	Target       string
	GitSHA       string

	// native is a test seam for the desktop case; production leaves it nil.
	native *nativeHooks
}

// VerifyOutcome reports the result of a verification run.
type VerifyOutcome struct {
	Code     int
	Status   string
	CaseDir  string
	Problems []string
}

const defaultStepTimeout = 20 * time.Minute

// acceptanceStubEnv guards re-exec of the acceptance test binary: a child that
// receives it exits immediately instead of entering the test main function.
const acceptanceStubEnv = "ANVIL_ACCEPTANCE_STUB"

// LookupPlan returns the runner for a registered case.
func LookupPlan(caseID string) (CasePlan, bool) {
	for _, plan := range casePlans() {
		if plan.ID == caseID {
			return plan, true
		}
	}
	return CasePlan{}, false
}

func casePlans() []CasePlan {
	return []CasePlan{
		{
			ID:           "R0-00-build-gate",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "go-vet", Command: "go", Args: []string{"vet", "./..."}},
				{Name: "go-build", Command: "go", Args: []string{"build", "./..."}},
				{Name: "go-test", Command: "go", Args: []string{"test", "-count=1", "./..."}, Timeout: 30 * time.Minute},
				{Name: "node-version", Command: "node", Args: []string{"-v"}, ExpectOutputContains: "v24.16.0"},
				{Name: "npm-version", Command: "npm", Args: []string{"-v"}},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
			},
		},
		{
			ID:           "R0-00-d-drive-preflight",
			EvidenceKind: EvidenceAutomation,
			Reset:        []string{".tmp/test/junction-fixture"},
			Steps: []StepSpec{
				{Name: "preflight-tests", Command: "go", Args: []string{"test", "./internal/preflight/", "-v"}, Timeout: 10 * time.Minute},
				{Name: "path-worktree", Command: "self", Args: []string{"preflight", "--path={repo}", "--create"}},
				{Name: "path-temp", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/tooling/tmp", "--create"}},
				{Name: "path-go-build", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/tooling/go-build", "--create"}},
				{Name: "path-go-mod", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/tooling/go-mod", "--create"}},
				{Name: "path-gopath", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/tooling/gopath", "--create"}},
				{Name: "path-npm-cache", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/tooling/npm-cache", "--create"}},
				{Name: "path-agent-desktop", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/tooling/agent-desktop-home", "--create"}},
				{Name: "path-test-data", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/test", "--create"}},
				{Name: "path-evidence", Command: "self", Args: []string{"preflight", "--path={repo}/.tmp/evidence", "--create"}},
				{Name: "path-evidence-root", Command: "self", Args: []string{"preflight", "--path={evidence}", "--create"}},
				{Name: "reject-other-drive", Command: "self", Args: []string{"preflight", "--path=C:\\Users\\Administrator\\AppData\\Local\\anvil-r0-00-must-not-exist"}, ExpectExit: 2, ExpectOutputContains: "D:"},
				{Name: "junction-create", Command: "cmd", Args: []string{"/c", "mklink", "/J", `{repo}\.tmp\test\junction-fixture\to-c`, `C:\Windows`}, Mkdir: []string{".tmp/test/junction-fixture"}},
				{Name: "reject-junction", Command: "self", Args: []string{"preflight", `--path={repo}\.tmp\test\junction-fixture\to-c\anvil-r0-00-must-not-exist`}, ExpectExit: 2, ExpectOutputContains: "D:"},
				{Name: "junction-remove", Command: "cmd", Args: []string{"/c", "rmdir", `{repo}\.tmp\test\junction-fixture\to-c`}},
				{Name: "junction-dir-remove", Command: "cmd", Args: []string{"/c", "rmdir", `{repo}\.tmp\test\junction-fixture`}},
			},
		},
		{
			ID:           "R0-00-native-window",
			EvidenceKind: EvidenceNativeDesktop,
			NativeWindow: true,
		},
		{
			ID:           "R0-01-shell",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
				{Name: "capture-prototype", Command: "node", Args: []string{"scripts/capture-prototype.mjs", "--out", "{caseDir}/prototype"}, Dir: "frontend", Timeout: 5 * time.Minute},
			},
			CollectArtifacts: []string{"prototype/*.png"},
		},
		{
			ID:           "R0-01-native-shell",
			EvidenceKind: EvidenceNativeDesktop,
			NativeShell:  true,
		},
		{
			ID:           "R0-02-theme",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
			},
		},
		{
			ID:           "R0-02-native-theme",
			EvidenceKind: EvidenceNativeDesktop,
			NativeTheme:  true,
		},
		{
			ID:           "R0-03-i18n",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
			},
		},
		{
			ID:           "R0-03-native-i18n",
			EvidenceKind: EvidenceNativeDesktop,
			NativeI18n:   true,
		},
		{
			ID:           "R0-04-workbench",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
				{Name: "capture-prototype", Command: "node", Args: []string{"scripts/capture-prototype.mjs", "--out", "{caseDir}/prototype"}, Dir: "frontend", Timeout: 5 * time.Minute},
			},
			CollectArtifacts: []string{"prototype/*.png"},
		},
		{
			ID:              "R0-04-native-workbench",
			EvidenceKind:    EvidenceNativeDesktop,
			NativeWorkbench: true,
		},
		{
			ID:           "R0-05-chat",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "go-build", Command: "go", Args: []string{"build", "./..."}},
				{Name: "go-vet", Command: "go", Args: []string{"vet", "./..."}},
				{Name: "go-test", Command: "go", Args: []string{"test", "-count=1", "./..."}, Timeout: 30 * time.Minute},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
				{Name: "capture-prototype", Command: "node", Args: []string{"scripts/capture-prototype.mjs", "--out", "{caseDir}/prototype"}, Dir: "frontend", Timeout: 5 * time.Minute},
			},
			CollectArtifacts: []string{"prototype/*.png"},
		},
		{
			ID:           "R0-05-native-chat",
			EvidenceKind: EvidenceNativeDesktop,
			NativeChat:   true,
		},
		{
			ID:           "R0-06-agents",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "go-build", Command: "go", Args: []string{"build", "./..."}},
				{Name: "go-vet", Command: "go", Args: []string{"vet", "./..."}},
				{Name: "go-test", Command: "go", Args: []string{"test", "-count=1", "./..."}, Timeout: 30 * time.Minute},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
				{Name: "capture-prototype", Command: "node", Args: []string{"scripts/capture-prototype.mjs", "--out", "{caseDir}/prototype"}, Dir: "frontend", Timeout: 5 * time.Minute},
			},
			CollectArtifacts: []string{"prototype/*.png"},
		},
		{
			ID:           "R0-06-native-agents",
			EvidenceKind: EvidenceNativeDesktop,
			NativeAgents: true,
		},
		{
			ID:           "R0-07-skills",
			EvidenceKind: EvidenceAutomation,
			Steps: []StepSpec{
				{Name: "go-version", Command: "go", Args: []string{"version"}, ExpectOutputContains: "go1.27.3"},
				{Name: "go-build", Command: "go", Args: []string{"build", "./..."}},
				{Name: "go-vet", Command: "go", Args: []string{"vet", "./..."}},
				{Name: "go-test", Command: "go", Args: []string{"test", "-count=1", "./..."}, Timeout: 30 * time.Minute},
				{Name: "npm-ci", Command: "npm", Args: []string{"ci"}, Dir: "frontend", Timeout: 20 * time.Minute},
				{Name: "npm-test", Command: "npm", Args: []string{"test"}, Dir: "frontend", Timeout: 15 * time.Minute},
				{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
				{Name: "check-dist", Command: "node", Args: []string{"scripts/check-dist.mjs"}, Dir: "frontend"},
				{Name: "capture-prototype", Command: "node", Args: []string{"scripts/capture-prototype.mjs", "--out", "{caseDir}/prototype"}, Dir: "frontend", Timeout: 5 * time.Minute},
			},
			CollectArtifacts: []string{"prototype/*.png"},
		},
		{
			ID:           "R0-07-native-skills",
			EvidenceKind: EvidenceNativeDesktop,
			NativeSkills: true,
		},
	}
}

// RunCase executes a registered case and writes
// <evidence>/<gitSHA>/<target>/<caseId>/result.json.
func RunCase(opts VerifyOptions) VerifyOutcome {
	fail := func(format string, args ...any) VerifyOutcome {
		return VerifyOutcome{Code: ExitIncomplete, Status: StatusBlocked, Problems: []string{fmt.Sprintf(format, args...)}}
	}
	if strings.TrimSpace(opts.RepoRoot) == "" || strings.TrimSpace(opts.CaseID) == "" || strings.TrimSpace(opts.Target) == "" {
		return fail("repo root, case id and target are required")
	}
	sha := normalizeSHA(opts.GitSHA)
	if !isHexSHA(sha) {
		return fail("git SHA %q is not a full 40-character commit", opts.GitSHA)
	}
	if err := validateIdentifier(opts.Target, "target"); err != nil {
		return fail("%v", err)
	}

	matrix, err := LoadMatrix(opts.MatrixPath)
	if err != nil {
		return fail("%v", err)
	}
	var registered *Case
	var gate *Gate
	for gi := range matrix.Gates {
		for ci := range matrix.Gates[gi].Cases {
			if matrix.Gates[gi].Cases[ci].ID == opts.CaseID {
				registered = &matrix.Gates[gi].Cases[ci]
				gate = &matrix.Gates[gi]
			}
		}
	}
	if registered == nil {
		return fail("case %q is not registered in %s", opts.CaseID, opts.MatrixPath)
	}
	if gate == nil || !containsString(gate.Targets, opts.Target) {
		return fail("target %q is not declared for case %q", opts.Target, opts.CaseID)
	}
	plan, ok := LookupPlan(opts.CaseID)
	if !ok {
		return fail("case %q has no automated runner; record it through its documented procedure", opts.CaseID)
	}
	if plan.EvidenceKind != registered.EvidenceKind {
		return fail("case %q runner produces %q evidence, matrix declares %q", opts.CaseID, plan.EvidenceKind, registered.EvidenceKind)
	}

	root, err := preflight.EnsureDir(opts.EvidenceRoot, preflight.OSSystem{})
	if err != nil {
		return fail("evidence root: %v", err)
	}
	caseDir := filepath.Join(root.Resolved, sha, opts.Target, opts.CaseID)
	if err := ensureWithin(root.Resolved, caseDir); err != nil {
		return fail("case directory escapes the evidence root: %v", err)
	}
	// A rerun replaces the previous attempt entirely so stale logs cannot be
	// mistaken for this run's evidence.
	if err := os.RemoveAll(caseDir); err != nil {
		return fail("reset case directory: %v", err)
	}
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		return fail("create case directory: %v", err)
	}
	logDir := filepath.Join(caseDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fail("create log directory: %v", err)
	}
	for _, rel := range plan.Reset {
		if err := os.RemoveAll(filepath.Join(opts.RepoRoot, filepath.FromSlash(rel))); err != nil {
			return fail("reset %q: %v", rel, err)
		}
	}

	env, envSettings := caseEnv(opts.RepoRoot, root.Resolved)
	envArtifact, err := writeJSONArtifact(caseDir, "env.json", envSettings)
	if err != nil {
		return fail("record environment: %v", err)
	}

	runner := automationRunner{
		repo:     opts.RepoRoot,
		caseDir:  caseDir,
		env:      env,
		evidence: root.Resolved,
	}
	var (
		status      string
		commands    []Command
		artifacts   []Artifact
		limitations []string
	)
	artifacts = append(artifacts, envArtifact)

	if plan.NativeWindow || plan.NativeShell || plan.NativeTheme || plan.NativeI18n || plan.NativeWorkbench || plan.NativeChat || plan.NativeAgents || plan.NativeSkills {
		var native nativeOutcome
		switch {
		case plan.NativeSkills:
			native = runNativeSkillsCase(runner, sha, opts.Target, opts.native)
		case plan.NativeAgents:
			native = runNativeAgentsCase(runner, sha, opts.Target, opts.native)
		case plan.NativeChat:
			native = runNativeChatCase(runner, sha, opts.Target, opts.native)
		case plan.NativeWorkbench:
			native = runNativeWorkbenchCase(runner, sha, opts.Target, opts.native)
		case plan.NativeI18n:
			native = runNativeI18nCase(runner, sha, opts.Target, opts.native)
		case plan.NativeTheme:
			native = runNativeThemeCase(runner, sha, opts.Target, opts.native)
		case plan.NativeShell:
			native = runNativeShellCase(runner, sha, opts.Target, opts.native)
		default:
			native = runNativeWindowCase(runner, sha, opts.Target, opts.native)
		}
		status = native.Status
		commands = append(commands, native.Commands...)
		artifacts = append(artifacts, native.Artifacts...)
		limitations = append(limitations, native.Limitations...)
		if status != StatusPass && status != StatusFail {
			status = StatusBlocked
		}
	} else {
		ran, status2, runArtifacts, runLimitations := runner.runSteps(plan.Steps)
		commands = append(commands, ran...)
		artifacts = append(artifacts, runArtifacts...)
		limitations = append(limitations, runLimitations...)
		status = status2
	}

	for _, pattern := range plan.CollectArtifacts {
		matches, globErr := filepath.Glob(filepath.Join(caseDir, filepath.FromSlash(pattern)))
		if globErr != nil {
			return fail("collect artifacts %q: %v", pattern, globErr)
		}
		if len(matches) == 0 {
			return fail("collect artifacts %q: no files matched", pattern)
		}
		for _, match := range matches {
			rel, relErr := filepath.Rel(caseDir, match)
			if relErr != nil {
				return fail("collect artifacts %q: %v", pattern, relErr)
			}
			artifact, hashErr := hashArtifact(caseDir, filepath.ToSlash(rel))
			if hashErr != nil {
				return fail("collect artifact %q: %v", rel, hashErr)
			}
			artifacts = append(artifacts, artifact)
		}
	}

	// The launched client inherits the launch command's stdout and keeps
	// writing while it starts up. Re-hash every artifact after the case has
	// finished (and the client is closed) so result.json matches final bytes.
	for i := range artifacts {
		sum, hashErr := fileSHA256(filepath.Join(caseDir, filepath.FromSlash(artifacts[i].Path)))
		if hashErr != nil {
			return fail("rehash artifact %q: %v", artifacts[i].Path, hashErr)
		}
		artifacts[i].SHA256 = sum
	}

	versions := collectToolVersions(runner)
	runtimeVersion := "n/a (no agent runtime in this case)"
	if plan.NativeWindow || plan.NativeShell || plan.NativeTheme || plan.NativeI18n || plan.NativeWorkbench || plan.NativeChat || plan.NativeAgents || plan.NativeSkills {
		runtimeVersion = "wails " + moduleVersion(runner, "github.com/wailsapp/wails/v3")
	}

	result := Result{
		SchemaVersion:  ResultSchemaVersion,
		CaseID:         opts.CaseID,
		GitSHA:         sha,
		Target:         opts.Target,
		Status:         status,
		EvidenceKind:   plan.EvidenceKind,
		OS:             currentOSInfo(),
		ToolVersions:   versions,
		RuntimeVersion: runtimeVersion,
		Commands:       commands,
		Artifacts:      artifacts,
		Limitations:    limitations,
	}
	if result.Commands == nil {
		result.Commands = []Command{}
	}
	if result.Artifacts == nil {
		result.Artifacts = []Artifact{}
	}
	if result.Limitations == nil {
		result.Limitations = []string{}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fail("encode result.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "result.json"), data, 0o644); err != nil {
		return fail("write result.json: %v", err)
	}

	switch status {
	case StatusPass:
		return VerifyOutcome{Code: ExitPass, Status: status, CaseDir: caseDir}
	case StatusFail:
		return VerifyOutcome{Code: ExitAssertion, Status: status, CaseDir: caseDir, Problems: limitations}
	default:
		return VerifyOutcome{Code: ExitIncomplete, Status: status, CaseDir: caseDir, Problems: limitations}
	}
}

type automationRunner struct {
	repo     string
	caseDir  string
	env      []string
	evidence string
}

func (r automationRunner) runSteps(steps []StepSpec) ([]Command, string, []Artifact, []string) {
	var (
		commands  []Command
		artifacts []Artifact
	)
	for _, step := range steps {
		cmd, artifact, failure := r.runStep(step)
		commands = append(commands, cmd)
		artifacts = append(artifacts, artifact)
		if failure != "" {
			return commands, StatusFail, artifacts, []string{failure}
		}
	}
	return commands, StatusPass, artifacts, nil
}

func (r automationRunner) logPath(stepName string) string {
	return filepath.Join(r.caseDir, "logs", stepName+".log")
}

func (r automationRunner) runStep(step StepSpec) (Command, Artifact, string) {
	command := expandPlaceholders(step.Command, r.repo, r.evidence, r.caseDir)
	args := make([]string, len(step.Args))
	for i, arg := range step.Args {
		args[i] = expandPlaceholders(arg, r.repo, r.evidence, r.caseDir)
	}
	dir := r.repo
	if step.Dir != "" {
		dir = filepath.Join(r.repo, filepath.FromSlash(step.Dir))
	}
	for _, mk := range step.Mkdir {
		_ = os.MkdirAll(filepath.Join(r.repo, filepath.FromSlash(expandPlaceholders(mk, r.repo, r.evidence, r.caseDir))), 0o755)
	}

	logRel := filepath.ToSlash(filepath.Join("logs", step.Name+".log"))
	logPath := r.logPath(step.Name)
	logFile, err := os.Create(logPath)
	record := Command{Command: formatCommand(command, args), WorkDir: dir, ExitCode: -1}
	if err != nil {
		record.Command = fmt.Sprintf("log open failed: %v", err)
		return record, Artifact{}, fmt.Sprintf("step %q: create log: %v", step.Name, err)
	}

	timeout := step.Timeout
	if timeout == 0 {
		timeout = defaultStepTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	prog, progArgs, resolveErr := r.resolveCommand(command, args)
	if resolveErr != nil {
		fmt.Fprintf(logFile, "$ %s\n$ workdir: %s\n\nresolve: %v\n", record.Command, dir, resolveErr)
		_ = logFile.Close()
		artifact, _ := hashArtifact(r.caseDir, logRel)
		return record, artifact, fmt.Sprintf("step %q: %v", step.Name, resolveErr)
	}
	record.Command = formatCommand(prog, progArgs)
	fmt.Fprintf(logFile, "$ %s\n$ workdir: %s\n$ timeout: %s\n\n", record.Command, dir, timeout)
	proc := exec.CommandContext(ctx, prog, progArgs...)
	proc.Dir = dir
	proc.Env = r.env
	if command == "self" {
		proc.Env = setEnv(r.env, acceptanceStubEnv, "1")
	}
	proc.Stdout = logFile
	proc.Stderr = logFile
	runErr := proc.Run()
	_ = logFile.Close()

	exitCode := 0
	failure := ""
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		if ctx.Err() == context.DeadlineExceeded {
			failure = fmt.Sprintf("step %q timed out after %s", step.Name, timeout)
		}
	} else if ctx.Err() == context.DeadlineExceeded {
		exitCode = -1
		failure = fmt.Sprintf("step %q timed out after %s", step.Name, timeout)
	}
	record.ExitCode = exitCode

	expected := step.ExpectExit
	if failure == "" && exitCode != expected {
		failure = fmt.Sprintf("step %q exited %d, want %d", step.Name, exitCode, expected)
	}
	if failure == "" && step.ExpectOutputContains != "" {
		body, readErr := os.ReadFile(logPath)
		if readErr != nil {
			failure = fmt.Sprintf("step %q: read log: %v", step.Name, readErr)
		} else if !strings.Contains(string(body), step.ExpectOutputContains) {
			failure = fmt.Sprintf("step %q output does not contain %q", step.Name, step.ExpectOutputContains)
		}
	}
	if failure != "" {
		appendNote(logPath, failure)
	}
	artifact, hashErr := hashArtifact(r.caseDir, logRel)
	if hashErr != nil {
		failure = fmt.Sprintf("step %q: hash log: %v", step.Name, hashErr)
	}
	return record, artifact, failure
}

func (r automationRunner) resolveCommand(command string, args []string) (string, []string, error) {
	switch command {
	case "self":
		exe, err := os.Executable()
		if err != nil {
			return "", nil, err
		}
		if strings.HasSuffix(strings.ToLower(exe), ".test.exe") {
			return "", nil, errors.New("self command is unavailable inside the acceptance test binary")
		}
		return exe, args, nil
	case "node":
		node, _ := nodeExecutables()
		if node == "" {
			return "", nil, fmt.Errorf("node executable not found (set ANVIL_NODE or install Node 24.16.0)")
		}
		return node, args, nil
	case "npm":
		_, npm := nodeExecutables()
		if npm == "" {
			return "", nil, fmt.Errorf("npm.cmd not found (set ANVIL_NPM or install Node 24.16.0)")
		}
		return "cmd", append([]string{"/c", npm}, args...), nil
	default:
		path, err := exec.LookPath(command)
		if err != nil {
			return "", nil, fmt.Errorf("look up %q: %w", command, err)
		}
		lower := strings.ToLower(path)
		if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
			return "cmd", append([]string{"/c", "call", path}, args...), nil
		}
		return path, args, nil
	}
}

func expandPlaceholders(value, repo, evidence, caseDir string) string {
	replacer := strings.NewReplacer(
		"{repo}", repo,
		"{evidence}", evidence,
		"{caseDir}", caseDir,
	)
	return replacer.Replace(value)
}

func formatCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, command)
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t") {
			parts = append(parts, `"`+arg+`"`)
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}

func appendNote(logPath, note string) {
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "\n[verify] %s\n", note)
		_ = f.Close()
	}
}

func hashArtifact(caseDir, rel string) (Artifact, error) {
	sum, err := fileSHA256(filepath.Join(caseDir, filepath.FromSlash(rel)))
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: rel, SHA256: sum}, nil
}

func writeJSONArtifact(caseDir, rel string, value any) (Artifact, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(caseDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Artifact{}, err
	}
	return hashArtifact(caseDir, rel)
}

func caseEnv(repo, evidenceRoot string) ([]string, map[string]string) {
	tmp := filepath.Join(repo, ".tmp", "tooling", "tmp")
	settings := map[string]string{
		"TEMP":                tmp,
		"TMP":                 tmp,
		"TMPDIR":              tmp,
		"GOTMPDIR":            tmp,
		"GOCACHE":             filepath.Join(repo, ".tmp", "tooling", "go-build"),
		"GOMODCACHE":          filepath.Join(repo, ".tmp", "tooling", "go-mod"),
		"GOPATH":              filepath.Join(repo, ".tmp", "tooling", "gopath"),
		"npm_config_cache":    filepath.Join(repo, ".tmp", "tooling", "npm-cache"),
		"AGENT_DESKTOP_HOME":  filepath.Join(repo, ".tmp", "tooling", "agent-desktop-home"),
		"ANVIL_EVIDENCE_ROOT": evidenceRoot,
	}
	env := os.Environ()
	for key, value := range settings {
		env = setEnv(env, key, value)
	}
	if dir, _, err := nodeExecutablesWithDir(); err == nil && dir != "" {
		env = setEnv(env, "PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return env, settings
}

func setEnv(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	for i, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), prefix) {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}

func nodeExecutablesWithDir() (dir, node string, err error) {
	if override := strings.TrimSpace(os.Getenv("ANVIL_NODE_DIR")); override != "" {
		dir = override
	} else {
		dir = `D:\Program Files\nodejs`
		if _, statErr := os.Stat(filepath.Join(dir, "node.exe")); statErr != nil {
			dir = ""
		}
	}
	if dir != "" {
		return dir, filepath.Join(dir, "node.exe"), nil
	}
	found, lookErr := exec.LookPath("node")
	if lookErr != nil {
		return "", "", lookErr
	}
	return filepath.Dir(found), found, nil
}

func nodeExecutables() (node, npm string) {
	dir, node, err := nodeExecutablesWithDir()
	if err != nil || dir == "" {
		return "", ""
	}
	npm = os.Getenv("ANVIL_NPM")
	if npm == "" {
		npm = filepath.Join(dir, "npm.cmd")
	}
	if _, statErr := os.Stat(npm); statErr != nil {
		npm = ""
	}
	return node, npm
}

// Standard tool probes do not change within one process. Cache them so a
// multi-case test run does not spawn the same binaries repeatedly; production
// verify executes one case per process.
var (
	standardVersionsOnce sync.Once
	standardVersions     map[string]string
)

func standardToolVersions(env []string, dir string) map[string]string {
	standardVersionsOnce.Do(func() {
		versions := map[string]string{
			"os": runtime.GOOS + "/" + runtime.GOARCH,
		}
		for _, probe := range []struct {
			key  string
			cmd  string
			args []string
		}{
			{"go", "go", []string{"version"}},
			{"git", "git", []string{"--version"}},
			{"gcc", "gcc", []string{"--version"}},
		} {
			if value := captureVersion(probe.cmd, probe.args, env, dir); value != "" {
				versions[probe.key] = value
			}
		}
		if node, npm := nodeExecutables(); node != "" {
			versions["node"] = strings.TrimSpace(captureVersion(node, []string{"-v"}, env, dir)) + " (" + node + ")"
			versions["npm"] = strings.TrimSpace(captureVersion("cmd", []string{"/c", npm, "-v"}, env, dir)) + " (" + npm + ")"
		}
		if value := captureVersion("agent-desktop", []string{"version"}, env, dir); value != "" {
			versions["agent-desktop"] = value
		}
		standardVersions = versions
	})
	out := make(map[string]string, len(standardVersions)+1)
	for key, value := range standardVersions {
		out[key] = value
	}
	return out
}

func collectToolVersions(r automationRunner) map[string]string {
	versions := standardToolVersions(r.env, r.repo)
	if value := moduleVersion(r, "github.com/wailsapp/wails/v3"); value != "" {
		versions["wails"] = value
	}
	return versions
}

func captureVersion(command string, args []string, env []string, dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc := exec.CommandContext(ctx, command, args...)
	proc.Dir = dir
	proc.Env = env
	out, err := proc.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func moduleVersion(r automationRunner, module string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proc := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Version}}", module)
	proc.Dir = r.repo
	proc.Env = r.env
	out, err := proc.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
